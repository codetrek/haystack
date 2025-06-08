package searcher

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-microsoft/haystack/server/core/prompts"
	"github.com/ai-microsoft/haystack/server/core/workspace"
	"github.com/ai-microsoft/haystack/shared/types"
)

// PromptSearch searches for prompts in the workspace based on the provided request.
// It scans prompt files, calculates similarity scores using embeddings, and returns matching prompts.
// The search can be filtered by path and limited to a maximum number of results.
func PromptSearch(workspace *workspace.Workspace, req *types.SearchPromptRequest) ([]string, error) {
	startTime := time.Now()
	var isTimeout = func() bool {
		return time.Since(startTime) > 10*time.Second
	}

	results := []string{}
	foundFiles := make(map[string]struct{}) // Tracks relPath to avoid duplicates by path
	var limitReachedOrTimedOut = false

	// Helper function to add a result
	addResult := func(relPath string, content string, score float32) {
		if limitReachedOrTimedOut {
			return
		}
		if isTimeout() {
			limitReachedOrTimedOut = true
			log.Println("[Searcher] PromptSearch: Timed out.")
			return
		}

		if _, exists := foundFiles[relPath]; exists {
			return
		}

		results = append(results, content)
		foundFiles[relPath] = struct{}{}

		if req.Limit != nil && req.Limit.MaxResults > 0 {
			limitReachedOrTimedOut = true
			log.Printf("[Searcher] PromptSearch: Max results limit (%d) reached.", req.Limit.MaxResults)
		}
	}

	prompts.ScanPromptFiles(workspace.Id, "", func(promptKey string, value []byte) bool {
		_, relPath := prompts.DecodePromptPathKey(promptKey)

		// Skip directories (though .prompt.md suffix makes this unlikely for a dir)
		fullPath := filepath.Join(workspace.Path, relPath)
		contentBytes, err := os.ReadFile(fullPath)
		if err != nil {
			return true
		}
		contentStr := string(contentBytes)

		if req.Filters != nil && req.Filters.Path != "" {
			// Ensure filter path is cleaned and has a trailing separator for directory matching
			if strings.HasPrefix(relPath, req.Filters.Path) {
				addResult(relPath, contentStr, 1)
				return !limitReachedOrTimedOut // Continue scanning if not timed out or limit reached
			}
		}

		queryToMatch := req.Query
		contentToSearch := contentStr

		if !req.CaseSensitive {
			queryToMatch = strings.ToLower(queryToMatch)
			contentToSearch = strings.ToLower(contentToSearch)
		}
		words := strings.Fields(queryToMatch)

		if workspace.EnablePromptSearch {
			queryVector, err := prompts.EmbeddingText(queryToMatch)
			if queryVector == nil || err != nil {
				log.Printf("[Searcher] PromptSearch: Failed to get embedding for query `%s`: %v", queryToMatch, err)
				return true
			}
			contentVector, err := prompts.DecodeToFloat32Vector(value)
			if err != nil {
				log.Printf("[Searcher] PromptSearch: Failed to decode vector for `%s`: %v", promptKey, err)
				return true
			}

			similarity := prompts.CosineSimilarity(queryVector, contentVector)
			log.Printf("[Searcher] PromptSearch: Similarity for `%s` is %.2f", relPath, similarity)
			if similarity > prompts.GetThresholdByQueryLength(len(words)) {
				addResult(relPath, contentStr, similarity)
			}
		} else {
			if len(words) == 0 { // If query is empty or only spaces, it's not a match or handle as per desired logic
				return !limitReachedOrTimedOut
			}

			allWordsMatch := false
			score := 0.0
			for _, word := range words {
				// Individual word already cased if !req.CaseSensitive from queryToMatch
				// contentToSearch is also already cased
				if strings.Contains(contentToSearch, word) {
					allWordsMatch = true
					score += 1.0
				}
			}

			if allWordsMatch {
				addResult(relPath, contentStr, float32(score))
			}
		}
		return !limitReachedOrTimedOut
	})

	return results, nil
}

// HandlePromptSearch performs a prompt search and prints the results.
func HandlePromptSearch(wsp *workspace.Workspace, req *types.SearchPromptRequest) {
	log.Printf("[Searcher] Handling PromptSearch request for query: \"%s\"", req.Query)
	if req.Filters != nil && req.Filters.Path != "" {
		log.Printf("[Searcher] Path filter: \"%s\"", req.Filters.Path)
	}

	results, err := PromptSearch(wsp, req)
	if err != nil {
		log.Printf("[Searcher] Error executing PromptSearch: %v", err)
		return
	}

	if len(results) == 0 {
		log.Println("[Searcher] PromptSearch returned no results.")
	} else {
		log.Printf("[Searcher] PromptSearch returned %d result(s):", len(results))
		for i, promptContent := range results {
			// The PromptSearch function returns the full content of the prompt.
			// Logging the full content might be verbose depending on prompt sizes.
			// For now, we'll log it as requested.
			log.Printf("[Searcher] Result %d:\n---\n%s\n---", i+1, promptContent)
		}
	}
}
