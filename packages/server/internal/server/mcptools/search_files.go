package mcptools

import (
	"context"
	"fmt"

	"github.com/codetrek/haystack/server/internal/core/workspace"
	"github.com/codetrek/haystack/server/internal/server/searcher"
	"github.com/codetrek/haystack/server/internal/shared/types"
	"github.com/mark3labs/mcp-go/mcp"
)

func SearchFiles(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	arguments := request.GetArguments()
	query, workspacePath, limit, err := parseAndValidateSearchArgs(arguments)
	if err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}

	workspace, err := workspace.GetByPath(workspacePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get workspace: %v", err)
	}

	req := types.SearchFilesRequest{
		Query:     query,
		Workspace: workspacePath,
		Limit:     limit,
	}

	result, err := searcher.SearchFiles(workspace, &req)
	if err != nil {
		return nil, fmt.Errorf("failed to search files: %v", err)
	}

	tr := &mcp.CallToolResult{}
	tr.Content = append(tr.Content, mcp.TextContent{
		Type: "text",
		Text: fmt.Sprintf("Found %d files.", len(result.Files)),
	})

	if len(result.Files) == 0 {
		tr.Content = append(tr.Content, mcp.TextContent{
			Type: "text",
			Text: "No results found.",
		})
		return tr, nil
	}

	for _, file := range result.Files {
		tr.Content = append(tr.Content, mcp.TextContent{
			Type: "text",
			Text: file,
		})
	}
	return tr, nil
}
