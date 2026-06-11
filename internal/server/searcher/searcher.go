package searcher

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/internal/core/documents"
	"github.com/codetrek/haystack/internal/core/workspace"
	"github.com/codetrek/haystack/internal/server/indexer"
	"github.com/codetrek/haystack/internal/shared/running"
	"github.com/codetrek/haystack/internal/shared/types"
	"github.com/codetrek/haystack/internal/utils"
	"github.com/codetrek/haystack/searchcore/invertedindex"

	"github.com/lithammer/fuzzysearch/fuzzy"
)

// idxInst is the inverted index instance injected via Run. It backs the
// content and symbol search lookups.
var idxInst *invertedindex.Index

// stInst is the documents.Store instance injected via Run.
var stInst *documents.Store

func Run(wg *sync.WaitGroup, idx *invertedindex.Index, st *documents.Store) {
	log.Println("[Searcher] Starting...")

	idxInst = idx
	stInst = st

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer log.Println("[Searcher] Shutdown")
		running.WaitingForShutdown()
	}()
}

type QueryFilters struct {
	Path    string
	Include *utils.SimpleFilter
	Exclude *utils.SimpleFilter
}

func sortDocuments(workspaceId int, editor *types.Editor, sr *invertedindex.SearchResult,
	filter func(path string) bool) []string {
	start := time.Now()

	docs := map[string]string{}
	if len(sr.DocIds) > 10000 {
		stInst.ScanFiles(workspaceId, func(docid, relPath string) bool {
			if _, ok := sr.DocIds[docid]; ok {
				docs[relPath] = docid
			}
			return true
		})
	} else {
		for docid := range sr.DocIds {
			relPath := stInst.GetDocumentPath(workspaceId, docid)
			if relPath != "" {
				docs[relPath] = docid
			}
		}
	}

	for relPath, id := range docs {
		if _, ok := sr.DocIds[id]; ok {
			if !filter(relPath) {
				delete(docs, relPath)
				delete(sr.WildDocIds, id)
			}
		}
	}

	sorted := make([]string, 0, len(sr.DocIds))
	if editor != nil {
		var add = func(dir string) {
			if dir != "" {
				for relPath, docid := range docs {
					if strings.HasPrefix(relPath, dir) {
						sorted = append(sorted, docid)
						delete(docs, relPath)
						delete(sr.WildDocIds, docid)
					}
				}
			}
		}

		if editor.ActiveFile != "" {
			// Add the active file to the beginning of the list
			file := filepath.ToSlash(filepath.Clean(editor.ActiveFile))
			if docid, ok := docs[file]; ok {
				sorted = append(sorted, docid)
				delete(docs, file)
				delete(sr.WildDocIds, docid)
			}
		}

		// Add the files in the editor to the list
		for _, f := range editor.OpenFiles {
			file := filepath.ToSlash(filepath.Clean(f))
			if docid, ok := docs[file]; ok {
				sorted = append(sorted, docid)
				delete(docs, file)
				delete(sr.WildDocIds, docid)
			}
		}

		// The same directory with active file
		if editor.ActiveFile != "" {
			dir := filepath.ToSlash(filepath.Dir(editor.ActiveFile))
			add(dir)
		}

		// The same directory with open files
		for _, f := range editor.OpenFiles {
			dir := filepath.ToSlash(filepath.Dir(f))
			add(dir)
		}

		// Parent directory of the active file
		if editor.ActiveFile != "" {
			dir := filepath.ToSlash(filepath.Dir(filepath.Dir(editor.ActiveFile)))
			add(dir)
		}
	}

	for docid := range sr.WildDocIds {
		sorted = append(sorted, docid)
	}

	// Append the rest of the files
	for _, docid := range docs {
		if _, ok := sr.WildDocIds[docid]; ok {
			continue // Skip wildcards, they are already added
		}
		sorted = append(sorted, docid)
	}

	log.Printf("[Searcher] SortDocuments took %s", time.Since(start))

	return sorted
}

// SearchContent searches the content of the workspace
// query is a list of words to search for
// returns a list of results
func SearchContent(workspace *workspace.Workspace, req *types.SearchContentRequest,
	callback func(types.SearchContentResult), ctx context.Context, timeout time.Duration) ([]types.SearchContentResult, bool) {
	startTime := time.Now()
	var isTimeout = func() bool {
		select {
		case <-ctx.Done():
			return true
		default:
			return time.Since(startTime) > timeout
		}
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

	/*
		var globalInclude *utils.SimpleFilter
		if len(workspace.GetFilters().Include) > 0 {
			globalInclude = utils.NewSimpleFilter(workspace.GetFilters().Include, workspace.Path)
		}
	*/

	var includeFilter *utils.SimpleFilter
	var excludeFilter *utils.SimpleFilter
	var pathFilter = ""
	if req.Filters != nil {
		pathFilter = strings.ToLower(
			filepath.FromSlash(filepath.Clean(filepath.Join(workspace.Path, req.Filters.Path))))

		if req.Filters.Include != "" {
			includeFilter = utils.NewSimpleFilter(strings.Split(req.Filters.Include, ","))
		}

		if req.Filters.Exclude != "" {
			excludeFilter = utils.NewSimpleFilter(strings.Split(req.Filters.Exclude, ","))
		}
	}

	// Check if the file should be included in the search
	var wantFile = func(relPath string) bool {
		fullPath := filepath.Join(workspace.Path, relPath)
		if len(pathFilter) > 0 && !strings.HasPrefix(strings.ToLower(fullPath), pathFilter) {
			return false
		}

		/*
			// File not included by workspace filters
			if globalInclude != nil && !globalInclude.Match(fullPath, false) {
				return false
			}
		*/

		// Excluded by filter
		if excludeFilter != nil && excludeFilter.Match(relPath, false) {
			return false
		}

		// Not included by include filter
		if includeFilter != nil && !includeFilter.Match(relPath, false) {
			return false
		}

		return true
	}

	// Compile the query
	engine := NewSimpleContentSearchEngine(workspace,
		conf.Get().Server.Search.MaxWildcardLength,
		conf.Get().Server.Search.MaxKeywordDistance,
		req.WholeWord)

	err := engine.Compile(req.Query, req.CaseSensitive)
	if err != nil {
		log.Println("[Searcher] Failed to compile query:", err)
		return []types.SearchContentResult{}, false
	}

	finalResults := []types.SearchContentResult{}
	totalHits := 0

	// Keep track of unsaved files to avoid searching them twice
	unsavedFilePaths := make(map[string]bool)

	beforeAfter := req.BeforeAfter
	if beforeAfter < 0 {
		beforeAfter = 0
	} else if beforeAfter > 5 {
		beforeAfter = 5
	}

	// Search in unsaved files
	if len(req.UnsavedFiles) > 0 {
		for _, unsavedFile := range req.UnsavedFiles {
			if isTimeout() {
				break
			}

			// Check if the unsaved file should be included in the search
			if !wantFile(unsavedFile.Path) {
				continue
			}

			// Mark this file as processed
			normalizedPath := filepath.ToSlash(unsavedFile.Path)
			unsavedFilePaths[normalizedPath] = true

			// Search in unsaved file content
			unsavedResult, err := searchInContent(unsavedFile.Path, strings.NewReader(unsavedFile.Content), engine, beforeAfter, req.Limit, &totalHits)
			if err != nil {
				log.Printf("[Searcher] Failed to search in unsaved file %s: %v", unsavedFile.Path, err)
				continue
			}
			if len(unsavedResult.Lines) > 0 {
				if callback != nil {
					callback(unsavedResult)
				}
				finalResults = append(finalResults, unsavedResult)
			}

			if totalHits >= limit.MaxResults {
				return finalResults, true
			}
		}
	}

	// If UnsavedFilesOnly is true, skip index search and return early for better performance
	if req.UnsavedFilesOnly {
		log.Printf("[Searcher] UnsavedFilesOnly mode: skipping index search, returning %d results", len(finalResults))
		return finalResults, totalHits >= limit.MaxResults
	}

	// Collect the all related documents
	results, err := engine.CollectDocuments()
	if err != nil {
		return []types.SearchContentResult{}, false
	}
	log.Printf("[Searcher] CollectDocuments took %s", time.Since(startTime))

	// Sort documents based on prioritization logic:
	// - Documents associated with the editor's active file are prioritized first.
	// - Documents from open files in the editor are prioritized next.
	// - Remaining documents are sorted based on relevance or other criteria.
	for _, docid := range sortDocuments(workspace.Id, req.Editor, results, wantFile) {
		if isTimeout() {
			break
		}

		doc, err := stInst.GetDocument(workspace.Id, docid, false)
		if err != nil || doc == nil {
			continue
		}

		// If the file was already searched from the unsaved list, skip it.
		normalizedPath := filepath.ToSlash(doc.RelPath)
		if _, ok := unsavedFilePaths[normalizedPath]; ok {
			continue
		}

		// File has been removed, skip it
		removed, err := indexer.RefreshFileIfNeeded(workspace, doc)
		if err != nil || removed {
			continue
		}

		fullPath := filepath.Join(workspace.Path, doc.RelPath)
		file, err := os.Open(fullPath)
		if err != nil {
			log.Printf("[Searcher] Failed to open file:`%s`, error:%s", fullPath, err)
			continue
		}

		fileMatch, err := searchInContent(doc.RelPath, file, engine, beforeAfter, req.Limit, &totalHits)
		file.Close()
		if err != nil {
			log.Printf("[Searcher] Failed to search in file %s: %v", doc.RelPath, err)
			continue
		}

		if len(fileMatch.Lines) > 0 {
			if callback != nil {
				callback(fileMatch)
			}
			finalResults = append(finalResults, fileMatch)
		}

		if totalHits >= limit.MaxResults {
			break
		}
	}

	return finalResults, totalHits >= limit.MaxResults
}

func searchInContent(relPath string, reader io.Reader, engine *SimpleContentSearchEngine, beforeAfter int, limit *types.SearchLimit, totalHits *int) (types.SearchContentResult, error) {
	fileMatch := types.SearchContentResult{
		File:  filepath.Clean(relPath),
		Lines: []types.LineMatch{},
	}

	scanner := bufio.NewScanner(reader)

	lines := []string{}
	lineNumber := 1
	fileHits := 0

	maxResultsPerFile := conf.Get().Server.Search.Limit.MaxResultsPerFile
	if limit != nil && limit.MaxResultsPerFile > 0 {
		maxResultsPerFile = limit.MaxResultsPerFile
	}

	maxResults := conf.Get().Server.Search.Limit.MaxResults
	if limit != nil && limit.MaxResults > 0 {
		maxResults = limit.MaxResults
	}

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

				(*totalHits)++
				fileHits++
				if fileHits >= maxResultsPerFile {
					fileMatch.Truncate = true
					break
				}
			}
			if fileHits >= maxResultsPerFile || *totalHits >= maxResults {
				break
			}
		}
		lineNumber++
	}

	if err := scanner.Err(); err != nil {
		return fileMatch, fmt.Errorf("error scanning content for %s: %w", relPath, err)
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
	stInst.ScanFiles(workspace.Id, func(_, relPath string) bool {
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
			log.Printf("[Searcher] Warning: file `%s` has been removed or is a directory", match.RelPath)
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
