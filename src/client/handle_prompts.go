package client

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/shared/running"
	"github.com/ai-microsoft/haystack/shared/types"
)

func handlePrompts(args []string) {
	// Create a new FlagSet for the prompts command
	promptsCmd := flag.NewFlagSet("prompts", flag.ExitOnError)

	// Define flags for prompts command
	maxResults := promptsCmd.Int("limit", conf.Get().Client.DefaultLimit.MaxResults, "Maximum number of results")
	maxResultsPerFile := promptsCmd.Int("limit-per-file", conf.Get().Client.DefaultLimit.MaxResultsPerFile, "Maximum number of results per file")
	path := promptsCmd.String("path", "", "Path to search in")
	include := promptsCmd.String("include", "", "File patterns to include")
	exclude := promptsCmd.String("exclude", "", "File patterns to exclude")
	workspace := promptsCmd.String("workspace", conf.Get().Client.DefaultWorkspace, "Workspace path to search in")
	caseSensitive := promptsCmd.Bool("case-sensitive", false, "Enable case-sensitive search")

	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Println("Usage: " + running.ExecutableName() + " prompts [options] <query>")
		fmt.Println("Options:")
		promptsCmd.PrintDefaults()
		fmt.Println("Description:")
		fmt.Println("  Search for prompts in the workspace using natural language queries.")
		fmt.Println("  This command searches through prompt files and returns relevant matches.")
		return
	}

	// Parse the remaining arguments
	promptsCmd.Parse(args)

	// Get the search query (all non-flag arguments)
	query := strings.Join(promptsCmd.Args(), " ")

	if query == "" {
		fmt.Println("Error: Search query cannot be empty")
		fmt.Println("Usage: " + running.ExecutableName() + " prompts [options] <query>")
		fmt.Println("Options:")
		promptsCmd.PrintDefaults()
		return
	}

	// Prepare the search request
	searchReq := types.SearchPromptRequest{
		Workspace:     *workspace,
		Query:         query,
		CaseSensitive: *caseSensitive,
		Limit: &types.SearchLimit{
			MaxResults:        *maxResults,
			MaxResultsPerFile: *maxResultsPerFile,
		},
	}

	// Add filters if specified
	if *path != "" || *include != "" || *exclude != "" {
		searchReq.Filters = &types.SearchFilters{
			Path:    *path,
			Include: *include,
			Exclude: *exclude,
		}
	}

	// Execute the search
	fmt.Printf("Searching prompts for: %s (limit: %d, limit-per-file: %d)\n", query, *maxResults, *maxResultsPerFile)
	results, err := sendSearchPromptsRequest(searchReq)
	if err != nil {
		fmt.Printf("Error searching prompts: %v\n", err)
		return
	}

	// Display results
	displayPromptSearchResults(results)
}

func sendSearchPromptsRequest(req types.SearchPromptRequest) (*[]string, error) {
	// Marshal request to JSON
	reqData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %v", err)
	}

	result, err := serverRequest("/search/prompts", reqData)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %v", err)
	}

	// Check if response body is nil
	if result.Body == nil || result.Body.Data == nil {
		return nil, fmt.Errorf("server returned empty response")
	}

	log.Printf("[HTTP] Process /api/v1/search/prompts: results: %s", string(*result.Body.Data))

	// Parse response
	var searchResp types.SearchPromptsResponse
	if err := json.Unmarshal(*result.Body.Data, &searchResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %v", err)
	}

	if searchResp.Code != 0 {
		return nil, fmt.Errorf("error code: %d, message: %s", searchResp.Code, searchResp.Message)
	}

	return &searchResp.Data, nil
}

func displayPromptSearchResults(results *[]string) {
	if results == nil || len(*results) == 0 {
		fmt.Println("No prompts found.")
		return
	}

	fmt.Printf("\nFound %d prompt(s):\n", len(*results))
	fmt.Println(strings.Repeat("=", 80))

	for i, promptContent := range *results {
		fmt.Printf("\n[Prompt %d]\n", i+1)
		fmt.Println(strings.Repeat("-", 40))

		// Display prompt content with some formatting
		lines := strings.Split(promptContent, "\n")
		for j, line := range lines {
			// Limit display to first 20 lines to avoid overwhelming output
			if j >= 20 {
				fmt.Println("... (content truncated)")
				break
			}
			fmt.Println(line)
		}

		if i < len(*results)-1 {
			fmt.Println()
		}
	}

	fmt.Println(strings.Repeat("=", 80))
}

// Test function to validate the prompts search functionality
func testSearchPrompts(workspace, query string) error {
	fmt.Printf("Testing prompts search with workspace: %s, query: %s\n", workspace, query)

	// Prepare test request
	testReq := types.SearchPromptRequest{
		Workspace: workspace,
		Query:     query,
		Limit: &types.SearchLimit{
			MaxResults:        10,
			MaxResultsPerFile: 5,
		},
	}

	// Execute search
	results, err := sendSearchPromptsRequest(testReq)
	if err != nil {
		return fmt.Errorf("test failed: %v", err)
	}

	// Display test results
	fmt.Printf("Test completed successfully. Found %d results.\n", len(*results))
	displayPromptSearchResults(results)

	return nil
}
