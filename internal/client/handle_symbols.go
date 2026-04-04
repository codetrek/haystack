package client

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/internal/shared/running"
	"github.com/codetrek/haystack/internal/shared/types"
)

func sendSearchSymbolsRequest(req types.SearchSymbolsRequest) (*types.SymbolsContentResults, error) {
	// Marshal request to JSON
	reqData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %v", err)
	}

	result, err := serverRequest("/search/symbols", reqData)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %v", err)
	}

	// Parse response
	var searchResp types.SymbolsContentResults
	if err := json.Unmarshal(*result.Body.Data, &searchResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %v", err)
	}

	return &searchResp, nil
}

func handleSymbols(args []string) {
	// Create a new FlagSet for the search command
	searchCmd := flag.NewFlagSet("files", flag.ExitOnError)

	// Define flags for search command
	maxResults := searchCmd.Int("limit", conf.Get().Server.Search.Limit.MaxResults, "Maximum number of results")
	maxResultsPerFile := searchCmd.Int("limit-per-file", conf.Get().Server.Search.Limit.MaxResultsPerFile, "Maximum number of results per file")
	workspace := searchCmd.String("workspace", conf.Get().Client.DefaultWorkspace, "Workspace path to search in")
	fuzzy := searchCmd.Bool("fuzzy", false, "Use fuzzy search")

	if wantsHelp(args) {
		fmt.Println("Usage: " + running.ExecutableName() + " symbols [options] <query>")
		fmt.Println("Options:")
		searchCmd.PrintDefaults()
		return
	}

	// Parse the remaining arguments
	searchCmd.Parse(args)

	// Get the search query (all non-flag arguments)
	query := strings.Join(searchCmd.Args(), " ")

	if query == "" {
		fmt.Println("Error: Search query cannot be empty")
		fmt.Println("Usage: " + running.ExecutableName() + " symbols [options] <query>")
		fmt.Println("Options:")
		searchCmd.PrintDefaults()
		return
	}

	// Prepare the search request
	searchReq := types.SearchSymbolsRequest{
		Workspace: *workspace,
		Query:     query,
		Fuzzy:     *fuzzy,
		Limit: &types.SearchLimit{
			MaxResults:        *maxResults,
			MaxResultsPerFile: *maxResultsPerFile,
		},
	}

	// Execute the search
	fmt.Printf("Searching for: %s (limit: %d) (fuzzy: %t)\n", query, *maxResults, *fuzzy)
	result, err := sendSearchSymbolsRequest(searchReq)
	if err != nil {
		fmt.Printf("Error searching: %v\n", err)
		return
	}

	// Display results
	for _, symbol := range result.Symbols {
		fmt.Printf("🎯 %s\n", symbol.Name)
		for _, file := range symbol.Files {
			fmt.Printf("  %s:%d\n", file.Path, file.Line)
		}
	}
}
