package server

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/server/mcptools"
	"github.com/ai-microsoft/haystack/shared/running"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type ToolName string

const (
	HaystackSearch ToolName = "HaystackSearch"
	HaystackFiles  ToolName = "HaystackFiles"
)

var (
	WithDesc = mcp.WithDescription
	WithStr  = mcp.WithString
	WithNum  = mcp.WithNumber

	Desc     = mcp.Description
	Required = mcp.Required
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
		WithDesc("Search for code in current project, supports prefix matching and "+
			"logical operators to help you find exactly what you're looking for in your codebase."),
		WithStr("query",
			Desc("The search query. Supports the following syntax features:\n"+
				"- Basic terms: single words like 'function'\n"+
				"- Prefix matching: 'func*' matches 'function', 'functional', etc. (wildcard only at end of term)\n"+
				"- Exact matching for quoted phrases: '\"Second third\"' matches 'preSecond thirdSuf' but not 'preSecond and thirdSuf'\n"+
				"- Logical operators: 'AND' (or space) for conjunction, '|' for OR operator\n"+
				"- Examples: 'error AND handle', 'create | update', 'init*'"),
			Required(),
		),
		WithStr("workspace",
			Desc("The workspace to search in, normally it's the absolute path to the project directory, "+
				"e.g. /home/user/projects/project1. Please always passing current workspace path."),
			Required(),
		),
		WithStr("path",
			Desc("The path to search in, related to workspace, e.g. src/core"),
		),
		WithStr("filter", Desc("Filter the search results by file path. The filter supports "+
			"glob patterns, separated by comma(','), e.g. 'src/**/*.go,*.cc' to search only in Go files in the src directory "+
			"or *.cc files in all directory.")),
		WithStr("exclude", Desc("Exclude files from the search. The exclude filter supports glob "+
			"patterns, separated by comma, e.g. 'test/**/*.go' to exclude all Go test files.")),
		WithNum("limit",
			Desc("Maximum number of results to return. The search will stop once this limit is reached, "+
				"which can improve performance for large codebases.\n"+
				fmt.Sprintf("Currently, the default limit is %d, and the maximum limit is %d.\n",
					config.Client.DefaultLimit.MaxResults, config.Server.Search.Limit.MaxResults))),
	), mcptools.SearchContent)

	mcpServer.AddTool(mcp.NewTool(string(HaystackFiles),
		WithDesc("Search for files in current project, supports fuzzy matching "+
			"on filenames and attempts to return a list of the most relevant files"),
		WithStr("query",
			Desc("The search query which is case-insensitive. Fuzzy match\n"+
				"e.g. query 'savedtabgroup' will match 'saved_tab_group', 'src/**/saved/tabgroup'"),
			Required(),
		),
		WithStr("workspace",
			Desc("The workspace to search in, normally it's the absolute path to the project directory, "+
				"e.g. /home/user/projects/project1. Please always passing current workspace path."),
			Required(),
		),
		WithNum("limit",
			Desc("Maximum number of results to return. \n"+
				fmt.Sprintf("Currently, the default limit is %d.\n", config.Client.DefaultLimit.MaxFilesResults))),
	), mcptools.SearchFiles)

	log.Println("[MCP] Tools registered")
}
