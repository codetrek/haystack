package mcptools

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/server/core/workspace"
	"github.com/ai-microsoft/haystack/server/searcher"
	"github.com/ai-microsoft/haystack/shared/types"
	"github.com/ai-microsoft/haystack/utils"
	"github.com/mark3labs/mcp-go/mcp"
)

func SearchPromtToolHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	arguments := request.GetArguments()
	query, ok1 := arguments["query"].(string)
	workspacePath, ok2 := arguments["workspace"].(string)
	limitFloat, _ := arguments["limit"].(float64)
	path, _ := arguments["path"].(string)
	filter, _ := arguments["filter"].(string)
	exclude, _ := arguments["exclude"].(string)

	if !ok1 || query == "" {
		return nil, fmt.Errorf("invalid arguments: 'query' is required and cannot be empty")
	}
	if !ok2 || workspacePath == "" {
		return nil, fmt.Errorf("invalid arguments: 'workspace' is required and cannot be empty")
	}

	workspacePath = utils.NormalizePath(workspacePath)
	if !filepath.IsAbs(workspacePath) {
		return nil, fmt.Errorf("workspace path '%s' is not absolute", workspacePath)
	}

	ws, err := workspace.GetByPath(workspacePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get workspace '%s': %v", workspacePath, err)
	}

	// Default and max limits
	defaultLimit := conf.Get().Client.DefaultLimit.MaxResults
	maxLimit := conf.Get().Server.Search.Limit.MaxResults
	limit := defaultLimit
	if limitFloat > 0 {
		limit = int(limitFloat)
	}
	if limit > maxLimit {
		log.Printf("[MCP] searchPromtToolHandler: Requested limit %d exceeds maximum %d. Capping at maximum.", limit, maxLimit)
		limit = maxLimit
	}
	if limit <= 0 { // Ensure limit is positive
		limit = defaultLimit
	}

	req := types.SearchPromptRequest{
		Query:         query,
		Workspace:     workspacePath,
		CaseSensitive: false, // TODO: Make this configurable if needed for prompt search. Assuming false for now.
		Limit: &types.SearchLimit{
			MaxResults:        limit,
			MaxResultsPerFile: conf.Get().Server.Search.Limit.MaxResultsPerFile, // TODO: Re-evaluate if this specific limit is best for prompts.
		},
		Filters: &types.SearchFilters{
			Path:    path,
			Include: filter,
			Exclude: exclude,
		},
		Editor: nil, // TODO: Populate if editor context is relevant for prompt search
	}
	// Call HaystackPromptSearch (actual implementation is a TODO in searcher.go)
	// The signature of HaystackPromptSearch should be updated to accept *types.SearchPromptRequest
	results, err := searcher.PromptSearch(ws, &req)
	if err != nil {
		return nil, fmt.Errorf("failed to search prompts: %v", err)
	}

	tr := &mcp.CallToolResult{
		IsError: false,
	}

	if len(results) == 0 {
		tr.Content = append(tr.Content, mcp.TextContent{
			Type: "text",
			Text: "No results found.",
		})
		return tr, nil
	}

	for _, result := range results {
		tr.Content = append(tr.Content, mcp.TextContent{
			Type: "text",
			Text: result, // Access the Text field of SearchPromptResult
		})
	}
	return tr, nil
}