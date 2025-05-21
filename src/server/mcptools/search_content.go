package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/server/core/workspace"
	"github.com/ai-microsoft/haystack/server/searcher"
	"github.com/ai-microsoft/haystack/shared/types"
	"github.com/ai-microsoft/haystack/utils"
	"github.com/mark3labs/mcp-go/mcp"
)

// SearchContent handles search requests from MCP
func SearchContent(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	arguments := request.GetArguments()
	path, _ := arguments["path"].(string)
	filter, _ := arguments["filter"].(string)
	exclude, _ := arguments["exclude"].(string)

	query, workspacePath, limit, err := parseAndValidateSearchArgs(arguments)
	if err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}

	workspace, err := workspace.GetByPath(workspacePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get workspace: %v", err)
	}

	if path != "" {
		path = utils.NormalizePath(path)
		if filepath.IsAbs(path) {
			return nil, fmt.Errorf("path could not be absolute")
		}
	}

	req := types.SearchContentRequest{
		Query:     query,
		Workspace: workspacePath,
		Limit: &types.SearchLimit{
			MaxResults:        limit,
			MaxResultsPerFile: conf.Get().Server.Search.Limit.MaxResultsPerFile,
		},
		Filters: &types.SearchFilters{
			Path:    path,
			Include: filter,
			Exclude: exclude,
		},
		BeforeAfter: 1,
	}

	start := time.Now()
	results, truncate := searcher.SearchContent(workspace, &req, nil, ctx, 10*time.Second)
	defer func() {
		totalHits := 0
		for _, result := range results {
			totalHits += len(result.Lines)
		}
		req, _ := json.Marshal(request)
		log.Printf("[MCP] HaystackSearch `%s`: took %s, found %d results in %d files, truncate: %t",
			string(req), time.Since(start), totalHits, len(results), truncate)
	}()

	resultCount := 0
	for _, result := range results {
		resultCount += len(result.Lines)
	}

	var toTruncated = func(truncated bool) string {
		if truncated {
			return " (truncated)"
		}
		return ""
	}

	var printLine = func(tr *mcp.CallToolResult, line string) {
		tr.Content = append(tr.Content, mcp.TextContent{
			Type: "text",
			Text: line,
		})
	}

	tr := &mcp.CallToolResult{}
	printLine(tr, fmt.Sprintf("Found %d results in %d files%s", resultCount, len(results), toTruncated(truncate)))

	if len(results) == 0 {
		printLine(tr, "No results found.")
		return tr, nil
	}

	for _, result := range results {
		printLine(tr, "")
		printLine(tr, strings.Repeat("=", 20))
		printLine(tr, fmt.Sprintf("File: %s, %d result%s", result.File, len(result.Lines), toTruncated(result.Truncate)))
		for _, line := range result.Lines {
			printLine(tr, strings.Repeat("-", 20))
			for _, before := range line.Before {
				printLine(tr, fmt.Sprintf("Line %d: %s", before.LineNumber, before.Content))
			}
			printLine(tr, fmt.Sprintf("Line %d: %s", line.Line.LineNumber, line.Line.Content))
			for _, after := range line.After {
				printLine(tr, fmt.Sprintf("Line %d: %s", after.LineNumber, after.Content))
			}
		}
	}

	return tr, nil
}
