package searcher

import (
	"bufio"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/server/core/fulltext"
	"github.com/ai-microsoft/haystack/server/core/workspace"
	"github.com/ai-microsoft/haystack/server/indexer"
	"github.com/ai-microsoft/haystack/shared/running"
	"github.com/ai-microsoft/haystack/shared/types"
	"github.com/ai-microsoft/haystack/utils"

	"github.com/lithammer/fuzzysearch/fuzzy"
)

func Run(wg *sync.WaitGroup) {
	log.Println("Starting searcher...")

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer log.Println("Searcher shutdown")
		running.WaitingForShutdown()
	}()
}

type QueryFilters struct {
	Path    string
	Include *utils.SimpleFilter
	Exclude *utils.SimpleFilter
}

// SearchContent searches the content of the workspace
// query is a list of words to search for
// returns a list of results
func SearchContent(workspace *workspace.Workspace, req *types.SearchContentRequest) ([]types.SearchContentResult, bool) {
	startTime := time.Now()
	var isTimeout = func() bool {
		return time.Since(startTime) > 10*time.Second
	}

	limit := conf.Get().Server.Search.Limit
	if req.Limit != nil {
		if req.Limit.MaxResults > 0 && req.Limit.MaxResults < limit.MaxResults {
			limit.MaxResults = req.Limit.MaxResults
		}

		if req.Limit.MaxResultsPerFile > 0 && req.Limit.MaxResultsPerFile < limit.MaxResultsPerFile {
			limit.MaxResultsPerFile = req.Limit.MaxResultsPerFile
		}
	}

	var globalInclude *utils.SimpleFilter
	if len(workspace.GetFilters().Include) > 0 {
		globalInclude = utils.NewSimpleFilter(workspace.GetFilters().Include, workspace.Path)
	}

	var includeFilter *utils.SimpleFilter
	var excludeFilter *utils.SimpleFilter
	var pathFilter = ""
	if req.Filters != nil {
		pathFilter = strings.ToLower(
			filepath.FromSlash(filepath.Clean(filepath.Join(workspace.Path, req.Filters.Path)) + "/"))

		if req.Filters.Include != "" {
			includeFilter = utils.NewSimpleFilter(strings.Split(req.Filters.Include, ","), workspace.Path)
		}

		if req.Filters.Exclude != "" {
			excludeFilter = utils.NewSimpleFilter(strings.Split(req.Filters.Exclude, ","), workspace.Path)
		}
	}

	// Check if the file should be included in the search
	var wantFile = func(doc *fulltext.Document) bool {
		fullPath := filepath.Join(workspace.Path, doc.RelPath)
		if len(pathFilter) > 0 && !strings.HasPrefix(strings.ToLower(fullPath), pathFilter) {
			return false
		}

		// File not included by workspace filters
		if globalInclude != nil && !globalInclude.Match(fullPath, false) {
			return false
		}

		// Excluded by filter
		if excludeFilter != nil && excludeFilter.Match(fullPath, false) {
			return false
		}

		// Not included by include filter
		if includeFilter != nil && !includeFilter.Match(fullPath, false) {
			return false
		}

		return true
	}

	// Compile the query
	engine := NewSimpleContentSearchEngine(workspace)
	err := engine.Compile(req.Query, req.CaseSensitive)
	if err != nil {
		log.Println("Failed to compile query:", err)
		return []types.SearchContentResult{}, false
	}

	finalResults := []types.SearchContentResult{}
	totalHits := 0

	beforeAfter := req.BeforeAfter
	if beforeAfter < 0 {
		beforeAfter = 0
	} else if beforeAfter > 5 {
		beforeAfter = 5
	}

	// Match the content of the file line by line
	var matchFileContent = func(doc *fulltext.Document) (types.SearchContentResult, error) {
		fullPath := filepath.Join(workspace.Path, doc.RelPath)
		fileMatch := types.SearchContentResult{
			File:  filepath.Clean(doc.RelPath),
			Lines: []types.LineMatch{},
		}

		// Read file and match line by line
		file, err := os.Open(fullPath)
		if err != nil {
			log.Printf("Failed to open file:`%s`, error:%s", fullPath, err)
			return fileMatch, err
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)

		lines := []string{}
		lineNumber := 1
		fileHits := 0
		for scanner.Scan() {
			line := scanner.Text()
			if beforeAfter > 0 {
				lines = append(lines, line)
			}
			matches := engine.IsLineMatch(line)
			if len(matches) > 0 {
				for _, match := range matches {
					fileMatch.Lines = append(fileMatch.Lines, types.LineMatch{
						Line: types.SearchContentLine{
							LineNumber: lineNumber,
							Content:    line,
							Match:      match,
						},
					})

					totalHits++
					fileHits++
					if fileHits >= limit.MaxResultsPerFile {
						fileMatch.Truncate = true
						break
					}
				}
				if fileHits >= limit.MaxResultsPerFile || totalHits >= limit.MaxResults {
					break
				}
			}
			lineNumber++
		}

		// Populate before and after context lines
		if beforeAfter > 0 {
			for i := 0; i < len(fileMatch.Lines); i++ {
				line := &fileMatch.Lines[i]
				lineNum := line.Line.LineNumber

				// Add before context lines
				for j := lineNum - beforeAfter; j < lineNum; j++ {
					if j > 0 && j <= len(lines) {
						line.Before = append(line.Before, types.SearchContentLine{
							LineNumber: j,
							Content:    lines[j-1], // -1 because line numbers are 1-based, but array is 0-based
						})
					}
				}

				// Add after context lines
				for j := lineNum + 1; j <= lineNum+beforeAfter; j++ {
					if j <= len(lines) {
						line.After = append(line.After, types.SearchContentLine{
							LineNumber: j,
							Content:    lines[j-1], // -1 because line numbers are 1-based, but array is 0-based
						})
					}
				}
			}
		}

		return fileMatch, nil
	}

	// Collect the all related documents
	results, err := engine.CollectDocuments()
	if err != nil {
		return []types.SearchContentResult{}, false
	}

	for docid := range results.DocIds {
		if isTimeout() {
			break
		}

		doc, err := fulltext.GetDocument(workspace.ID, docid, false)
		if err != nil || doc == nil {
			continue
		}

		// Check if the file should be included in the search
		if !wantFile(doc) {
			continue
		}

		// File has been removed, skip it
		removed, err := indexer.RefreshFileIfNeeded(workspace, doc)
		if err != nil || removed {
			continue
		}

		fileMatch, err := matchFileContent(doc)
		if err != nil {
			continue
		}

		if len(fileMatch.Lines) > 0 {
			finalResults = append(finalResults, fileMatch)
		}

		if totalHits >= limit.MaxResults {
			break
		}
	}

	return finalResults, totalHits >= limit.MaxResults
}

// fuzzyMatchWithScore checks if pattern matches text and returns a score (0-100)
// Higher score means better match
func fuzzyMatchWithScore(pattern, text string) (bool, int) {
	// For exact matches, return perfect score
	if strings.Contains(strings.ToLower(text), strings.ToLower(pattern)) {
		return true, 100
	}

	// Check if the text is a path and extract filename (part after last '/')
	isFilePath := false
	filename := text
	if strings.Contains(text, "/") || strings.Contains(text, "\\") {
		parts := strings.Split(strings.ReplaceAll(text, "\\", "/"), "/")
		filename = parts[len(parts)-1]
		isFilePath = true
	}

	// For fuzzy matches, calculate a score
	patLower := strings.ToLower(pattern)
	textLower := strings.ToLower(text)

	patLen := len(patLower)
	textLen := len(textLower)

	// Find pattern character positions in text
	positions := make([]int, 0, patLen)
	lastPos := 0

	for _, pc := range patLower {
		found := false
		for i := lastPos; i < textLen; i++ {
			if rune(textLower[i]) == pc {
				positions = append(positions, i)
				lastPos = i + 1
				found = true
				break
			}
		}
		if !found {
			// Should never happen as we confirmed Match already
			return true, 50
		}
	}

	// Calculate consecutive matches
	consecutive := 0
	for i := 0; i < len(positions)-1; i++ {
		if positions[i+1] == positions[i]+1 {
			consecutive++
		}
	}

	// Calculate gaps between matches
	totalGap := 0
	if len(positions) > 1 {
		for i := 0; i < len(positions)-1; i++ {
			totalGap += positions[i+1] - positions[i] - 1
		}
	}

	// Calculate match density (how close are the matches to each other)
	matchSpan := positions[len(positions)-1] - positions[0] + 1
	density := float64(patLen) / float64(matchSpan)

	// Calculate the final score based on several factors
	// 1. How much of the pattern is matched (always 100% for fuzzy.Match)
	// 2. How much of the text is matched (ratio of pattern to text)
	// 3. How many consecutive characters are matched
	// 4. How dense the matches are (fewer gaps is better)

	textRatio := float64(patLen) / float64(textLen) * 25              // Max 25 points
	consecutiveRatio := float64(consecutive) / float64(patLen-1) * 25 // Max 25 points
	densityScore := density * 30                                      // Max 30 points

	// Position bonus - matches at start of text or after delimiters are better
	positionBonus := 0
	if positions[0] <= 2 { // Match near the start
		positionBonus = 25
	} else {
		// Check if match starts after a common delimiter
		delimiters := []rune{'_', '-', ' ', '.', '/', '\\'}
		for _, d := range delimiters {
			if positions[0] > 0 && rune(textLower[positions[0]-1]) == d {
				positionBonus = 15
				break
			}
		}
	}

	// Calculate filename match bonus
	filenameBonus := 0
	if isFilePath && fuzzy.Match(pattern, filename) {
		// If the pattern matches the filename, add a significant bonus
		_, filenameScore := fuzzyMatchWithScore(pattern, filename)
		// Scale the filename score to give it more weight
		filenameBonus = filenameScore * 4 / 5
	}

	// Calculate final score (max 100)
	score := int(textRatio+consecutiveRatio+densityScore) + positionBonus + filenameBonus
	if score > 100 {
		score = 100
	}

	return true, score
}

func SearchFiles(workspace *workspace.Workspace, req *types.SearchFilesRequest) (types.SearchFilesResult, error) {
	type MatchResult struct {
		RelPath string
		Score   int
	}

	startTime := time.Now()
	var isTimeout = func() bool {
		return time.Since(startTime) > 10*time.Second
	}

	pattern := strings.ReplaceAll(req.Query, " ", "")
	matches := []MatchResult{}
	fulltext.ScanFiles(workspace.ID, func(_, relPath string) bool {
		if isTimeout() {
			return false
		}

		if !fuzzy.Match(pattern, relPath) {
			return true
		}

		matched, score := fuzzyMatchWithScore(pattern, relPath)
		if matched {
			matches = append(matches, MatchResult{
				RelPath: relPath,
				Score:   score,
			})
		}
		return true
	})

	// Sort matches by score (highest first)
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score == matches[j].Score {
			return len(matches[i].RelPath) < len(matches[j].RelPath)
		} else {
			return matches[i].Score > matches[j].Score
		}
	})

	result := types.SearchFilesResult{
		Query: req.Query,
		Files: []string{},
	}

	removedFiles := []string{}
	// Filter and display only matches with score > 50
	for _, match := range matches {
		if match.Score <= 50 {
			continue
		}
		stat, err := os.Stat(filepath.Join(workspace.Path, match.RelPath))
		if os.IsNotExist(err) || stat.IsDir() {
			log.Printf("Warning: file `%s` has been removed or is a directory", match.RelPath)
			removedFiles = append(removedFiles, match.RelPath)
			continue
		}
		if err != nil {
			continue
		}

		result.Files = append(result.Files, match.RelPath)
		if len(result.Files) >= req.Limit {
			break
		}
	}

	if len(removedFiles) > 0 {
		go func() {
			for _, relPath := range removedFiles {
				// Remove the file from the index
				indexer.RemoveFile(workspace, relPath)
			}
		}() // Remove the files from the workspace
	}

	return result, nil
}
