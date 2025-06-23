package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"time"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/server/core/workspace"
	"github.com/ai-microsoft/haystack/server/searcher"
	"github.com/ai-microsoft/haystack/shared/types"
	"github.com/ai-microsoft/haystack/utils"
)

// handleSearchContent handles the search content endpoint
// It will search the content of the server
func handleSearchContent(w http.ResponseWriter, r *http.Request) {
	// Check if the request is a stream
	stream := r.Header.Get("Accept") == "text/event-stream"

	var request types.SearchContentRequest
	err := json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var flusher http.Flusher
	timeout := time.Second * 10
	if stream {
		// If the request is a stream, set the header to text/event-stream
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		timeout = time.Second * 60 // streaming mode have longer timeout

		var ok bool
		flusher, ok = w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}
	} else {
		w.Header().Set("Content-Type", "application/json")
	}

	w.WriteHeader(http.StatusOK)
	if request.Workspace == "" {
		json.NewEncoder(w).Encode(types.SearchContentResponse{
			Code:    1,
			Message: "Workspace is required",
		})
		return
	}

	// Normalize the workspace path
	// If the path is not absolute, return an error
	workspacePath := utils.NormalizePath(request.Workspace)
	if !filepath.IsAbs(workspacePath) {
		json.NewEncoder(w).Encode(types.SearchContentResponse{
			Code:    1,
			Message: "Workspace is not absolute",
		})
		return
	}

	// Get the workspace by path
	// If the workspace is not found, return an error
	workspace, err := workspace.GetByPath(workspacePath)
	if err != nil {
		json.NewEncoder(w).Encode(types.SearchContentResponse{
			Code:    1,
			Message: err.Error(),
		})
		return
	}

	// If the query is empty, return an error
	if request.Query == "" {
		json.NewEncoder(w).Encode(types.SearchContentResponse{
			Code:    1,
			Message: "Query is required",
		})
		return
	}

	start := time.Now()
	// Search the content of the workspace
	results := make([]types.SearchContentResult, 0)
	_, truncate := searcher.SearchContent(workspace, &request, func(result types.SearchContentResult) {
		results = append(results, result)
		if stream {
			msg, _ := json.Marshal(result)
			fmt.Fprintf(w, "event:result\ndata:%s\n\n", string(msg))
			flusher.Flush()
		}
	}, r.Context(), timeout)
	defer func() {
		totalHits := 0
		for _, result := range results {
			totalHits += len(result.Lines)
		}
		r := request
		r.UnsavedFiles = nil
		req, _ := json.Marshal(r)
		log.Printf("[HTTP] Process /api/v1/search/content `%s`: took %s, found %d results in %d files, truncate: %t",
			string(req), time.Since(start), totalHits, len(results), truncate)
	}()

	if stream {
		// If the request is a stream, send the results as a stream
		msg, _ := json.Marshal(types.SearchContentResult{Truncate: truncate})
		fmt.Fprintf(w, "event:done\ndata:%s\n\n", string(msg))
		flusher.Flush()
	} else {
		json.NewEncoder(w).Encode(types.SearchContentResponse{
			Code:    0,
			Message: "Ok",
			Data: struct {
				Results  []types.SearchContentResult `json:"results,omitempty"`
				Truncate bool                        `json:"truncate,omitempty"`
			}{
				Results:  results,
				Truncate: truncate,
			},
		})
	}
}

// handleSearchContent handles the search content endpoint
// It will search the content of the server
func handleSearchFiles(w http.ResponseWriter, r *http.Request) {
	var request types.SearchFilesRequest
	err := json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if request.Workspace == "" {
		json.NewEncoder(w).Encode(types.SearchFilesResponse{
			Code:    1,
			Message: "Workspace is required",
		})
		return
	}

	// Normalize the workspace path
	// If the path is not absolute, return an error
	workspacePath := utils.NormalizePath(request.Workspace)
	if !filepath.IsAbs(workspacePath) {
		json.NewEncoder(w).Encode(types.SearchFilesResponse{
			Code:    1,
			Message: "Workspace is not absolute",
		})
		return
	}

	// Get the workspace by path
	// If the workspace is not found, return an error
	workspace, err := workspace.GetByPath(workspacePath)
	if err != nil {
		json.NewEncoder(w).Encode(types.SearchFilesResponse{
			Code:    1,
			Message: err.Error(),
		})
		return
	}

	// If the query is empty, return an error
	if request.Query == "" {
		json.NewEncoder(w).Encode(types.SearchFilesResponse{
			Code:    1,
			Message: "Query is required",
		})
		return
	}

	start := time.Now()
	// Search the content of the workspace
	result, err := searcher.SearchFiles(workspace, &request)
	defer func() {
		req, _ := json.Marshal(request)
		log.Printf("[HTTP] Process /api/v1/search/files `%s`: took %s, found %d results, err: %s",
			string(req), time.Since(start), len(result.Files), err)
	}()

	json.NewEncoder(w).Encode(types.SearchFilesResponse{
		Code:    0,
		Message: "Ok",
		Data:    result,
	})
}

func handleSearchSymbols(w http.ResponseWriter, r *http.Request) {
	var request types.SearchSymbolsRequest
	err := json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if request.Workspace == "" {
		json.NewEncoder(w).Encode(types.SearchSymbolsResponse{
			Code:    1,
			Message: "Workspace is required",
		})
		return
	}

	limit := conf.Get().Server.Search.Limit
	if request.Limit != nil {
		if request.Limit.MaxResults > 0 && request.Limit.MaxResults < limit.MaxResults {
			limit.MaxResults = request.Limit.MaxResults
		}

		if request.Limit.MaxResultsPerFile > 0 && request.Limit.MaxResultsPerFile < limit.MaxResultsPerFile {
			limit.MaxResultsPerFile = request.Limit.MaxResultsPerFile
		}
	}
	request.Limit = &limit

	// Normalize the workspace path
	// If the path is not absolute, return an error
	workspacePath := utils.NormalizePath(request.Workspace)
	if !filepath.IsAbs(workspacePath) {
		json.NewEncoder(w).Encode(types.SearchSymbolsResponse{
			Code:    1,
			Message: "Workspace is not absolute",
		})
		return
	}

	// Get the workspace by path
	// If the workspace is not found, return an error
	workspace, err := workspace.GetByPath(workspacePath)
	if err != nil {
		json.NewEncoder(w).Encode(types.SearchSymbolsResponse{
			Code:    1,
			Message: err.Error(),
		})
		return
	}

	// If the query is empty, return an error
	if request.Query == "" {
		json.NewEncoder(w).Encode(types.SearchSymbolsResponse{
			Code:    1,
			Message: "Query is required",
		})
		return
	}

	start := time.Now()
	result, err := searcher.SearchSymbols(workspace, &request)
	defer func() {
		req, _ := json.Marshal(request)
		log.Printf("[HTTP] Process /api/v1/search/symbols `%s`: took %s, found %d results, err: %s",
			string(req), time.Since(start), len(result.Symbols), err)
	}()

	json.NewEncoder(w).Encode(types.SearchSymbolsResponse{
		Code:    0,
		Message: "Ok",
		Data:    result,
	})
}

func handleSearchPrompts(w http.ResponseWriter, r *http.Request) {
	var request types.SearchPromptRequest
	err := json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if request.Workspace == "" {
		json.NewEncoder(w).Encode(types.SearchPromptsResponse{
			Code:    1,
			Message: "Workspace is required",
		})
		return
	}

	limit := conf.Get().Server.Search.Limit
	if request.Limit != nil {
		if request.Limit.MaxResults > 0 && request.Limit.MaxResults < limit.MaxResults {
			limit.MaxResults = request.Limit.MaxResults
		}

		if request.Limit.MaxResultsPerFile > 0 && request.Limit.MaxResultsPerFile < limit.MaxResultsPerFile {
			limit.MaxResultsPerFile = request.Limit.MaxResultsPerFile
		}
	}
	request.Limit = &limit

	// Normalize the workspace path
	// If the path is not absolute, return an error
	workspacePath := utils.NormalizePath(request.Workspace)
	if !filepath.IsAbs(workspacePath) {
		json.NewEncoder(w).Encode(types.SearchPromptsResponse{
			Code:    1,
			Message: "Workspace is not absolute",
		})
		return
	}

	// Get the workspace by path
	// If the workspace is not found, return an error
	workspace, err := workspace.GetByPath(workspacePath)
	if err != nil {
		json.NewEncoder(w).Encode(types.SearchPromptsResponse{
			Code:    1,
			Message: err.Error(),
		})
		return
	}

	// If the query is empty, return an error
	if request.Query == "" {
		json.NewEncoder(w).Encode(types.SearchPromptsResponse{
			Code:    1,
			Message: "Query is required",
		})
		return
	}

	start := time.Now()
	result, err := searcher.PromptSearch(workspace, &request)
	defer func() {
		req, _ := json.Marshal(request)
		log.Printf("[HTTP] Process /api/v1/search/prompts `%s`: took %s, found %d results, err: %s",
			string(req), time.Since(start), len(result), err)
	}()

	if err != nil {
		json.NewEncoder(w).Encode(types.SearchPromptsResponse{
			Code:    1,
			Message: err.Error(),
		})
		return
	}

	json.NewEncoder(w).Encode(types.SearchPromptsResponse{
		Code:    0,
		Message: "Ok",
		Data:    result,
	})
}
