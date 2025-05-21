package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/server/core/workspace"
	"github.com/ai-microsoft/haystack/server/searcher"
	"github.com/ai-microsoft/haystack/shared/running"
	"github.com/ai-microsoft/haystack/shared/types"
	"github.com/ai-microsoft/haystack/utils"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type ToolName string

const (
	HaystackSearch ToolName = "HaystackSearch"
	HaystackFiles  ToolName = "HaystackFiles"
)

// mcpInit initializes and sets up the Model Context Protocol (MCP) server
func mcpInit() {
	hooks := &server.Hooks{}

	// Create a new MCP server instance
	mcpServer := server.NewMCPServer(
		"Haystack",
		running.Version(),
		server.WithResourceCapabilities(true, true),
		server.WithPromptCapabilities(true),
		server.WithLogging(),
		server.WithHooks(hooks),
	)

	// Register MCP tools (framework only, implementations will be added later)
	registerMCPTools(mcpServer)

	sse := server.NewSSEServer(mcpServer,
		server.WithBaseURL(fmt.Sprintf("http://localhost:%d", conf.Get().Global.Port)),
		server.WithStaticBasePath("/mcp"),
		server.WithKeepAlive(true),
		server.WithKeepAliveInterval(20*time.Second),
	)

	http.HandleFunc("/mcp/", func(w http.ResponseWriter, r *http.Request) {
		sse.ServeHTTP(w, r)
		log.Printf("[MCP] Request: %s %s", r.Method, r.URL.Path)
	})
	log.Println("[MCP] Server initialized at /mcp endpoint")
}

// registerMCPTools registers all the MCP tools with the server
func registerMCPTools(mcpServer *server.MCPServer) {
	// Register search tool
	config := conf.Get()

	mcpServer.AddTool(mcp.NewTool(string(HaystackSearch),
		mcp.WithDescription("Search for code in current project, supports prefix matching and "+
			"logical operators to help you find exactly what you're looking for in your codebase."),
		mcp.WithString("query",
			mcp.Description("The search query. Supports the following syntax features:\n"+
				"- Basic terms: single words like 'function'\n"+
				"- Prefix matching: 'func*' matches 'function', 'functional', etc. (wildcard only at end of term)\n"+
				"- Exact matching for quoted phrases: '\"Second third\"' matches 'preSecond thirdSuf' but not 'preSecond and thirdSuf'\n"+
				"- Logical operators: 'AND' (or space) for conjunction, '|' for OR operator\n"+
				"- Examples: 'error AND handle', 'create | update', 'init*'"),
			mcp.Required(),
		),
		mcp.WithString("workspace",
			mcp.Description("The workspace to search in, normally it's the absolute path to the project directory, "+
				"e.g. /home/user/projects/project1. Please always passing current workspace path."),
			mcp.Required(),
		),
		mcp.WithString("path",
			mcp.Description("The path to search in, related to workspace, e.g. src/core"),
		),
		mcp.WithString("filter", mcp.Description("Filter the search results by file path. The filter supports "+
			"glob patterns, separated by comma(','), e.g. 'src/**/*.go,*.cc' to search only in Go files in the src directory "+
			"or *.cc files in all directory.")),
		mcp.WithString("exclude", mcp.Description("Exclude files from the search. The exclude filter supports glob "+
			"patterns, separated by comma, e.g. 'test/**/*.go' to exclude all Go test files.")),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results to return. The search will stop once this limit is reached, "+
				"which can improve performance for large codebases.\n"+
				fmt.Sprintf("Currently, the default limit is %d, and the maximum limit is %d.\n",
					config.Client.DefaultLimit.MaxResults, config.Server.Search.Limit.MaxResults))),
	), handleSearch)

	mcpServer.AddTool(mcp.NewTool(string(HaystackFiles),
		mcp.WithDescription("Search for files in current project, supports fuzzy matching "+
			"on filenames and attempts to return a list of the most relevant files"),
		mcp.WithString("query",
			mcp.Description("The search query which is case-insensitive. Fuzzy match\n"+
				"e.g. query 'savedtabgroup' will match 'saved_tab_group', 'src/**/saved/tabgroup'"),
			mcp.Required(),
		),
		mcp.WithString("workspace",
			mcp.Description("The workspace to search in, normally it's the absolute path to the project directory, "+
				"e.g. /home/user/projects/project1. Please always passing current workspace path."),
			mcp.Required(),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results to return. \n"+
				fmt.Sprintf("Currently, the default limit is %d.\n", config.Client.DefaultLimit.MaxFilesResults))),
	), searchFilesToolHandler)

	log.Println("[MCP] Tools registered")
}

// Tool handler function stubs - implementations will be added later

// searchHandler handles search requests from MCP
func handleSearch(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	arguments := request.GetArguments()
	query, ok1 := arguments["query"].(string)
	workspacePath, ok2 := arguments["workspace"].(string)
	limit, _ := arguments["limit"].(float64)
	path, _ := arguments["path"].(string)
	filter, _ := arguments["filter"].(string)
	exclude, _ := arguments["exclude"].(string)
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("invalid arguments")
	}

	workspacePath = utils.NormalizePath(workspacePath)
	if !filepath.IsAbs(workspacePath) {
		return nil, fmt.Errorf("workspace is not absolute")
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
			MaxResults:        int(limit),
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
	results, truncate := searcher.SearchContent(workspace, &req, nil, context.Background(), 10*time.Second)
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

func searchFilesToolHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	arguments := request.GetArguments()
	query, ok1 := arguments["query"].(string)
	workspacePath, ok2 := arguments["workspace"].(string)
	limitCount, ok3 := arguments["limit"].(float64)
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("invalid arguments")
	}

	workspacePath = utils.NormalizePath(workspacePath)
	if !filepath.IsAbs(workspacePath) {
		return nil, fmt.Errorf("workspace is not absolute")
	}

	workspace, err := workspace.GetByPath(workspacePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get workspace: %v", err)
	}

	limit := conf.Get().Client.DefaultLimit.MaxFilesResults
	if ok3 {
		limit = int(limitCount)
	}

	if limit > conf.Get().Server.Search.Limit.MaxFilesResults {
		limit = conf.Get().Server.Search.Limit.MaxFilesResults
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
