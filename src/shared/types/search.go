package types

type SearchLimit struct {
	MaxResults        int `yaml:"max_results" json:"max_results,omitempty"`
	MaxResultsPerFile int `yaml:"max_results_per_file" json:"max_results_per_file,omitempty"`
	MaxFilesResults   int `yaml:"max_files_results" json:"max_files_results,omitempty"`
}

type SearchFilters struct {
	Path    string `json:"path,omitempty"`
	Include string `json:"include,omitempty"`
	Exclude string `json:"exclude,omitempty"`
}

// @param OpenFiles: is the list of files that open in the editor
// @param ActiveFile: is the active file in the editor
type Editor struct {
	OpenFiles  []string `json:"open_files,omitempty"`
	ActiveFile string   `json:"active_file,omitempty"`
}

// SearchContentRequest is the request for searching the content of a workspace
// @param Workspace: is the path to the workspace
// @param Query: is the query to search for, refer to the search query syntax in the server/server/search.md
// @param Filters: is the filters to apply to the search
// @param Limit: is the limit to apply to the search
// @param Filters.Path: is the path to the workspace
// @param Filters.Include: is the include to apply to the search
// @param Filters.Exclude: is the exclude to apply to the search
// @param Limit.MaxLines: is the max lines to apply to the search
// @param Limit.MaxFiles: is the max files to apply to the search
// @param Limit.MaxLinesPerFile: is the max lines per file to apply to the search
type SearchContentRequest struct {
	Workspace     string         `json:"workspace,omitempty"`
	Query         string         `json:"query,omitempty"`
	Editor        *Editor        `json:"editor,omitempty"`
	CaseSensitive bool           `json:"case_sensitive,omitempty"`
	Filters       *SearchFilters `json:"filters,omitempty"`
	Limit         *SearchLimit   `json:"limit,omitempty"`
	BeforeAfter   int            `json:"before_after,omitempty"`
}

// SearchPromptRequest is the request for searching prompts in a workspace.
// It's structurally similar to SearchContentRequest but tailored for prompt-specific searches.
type SearchPromptRequest struct {
	Workspace     string         `json:"workspace,omitempty"`
	Query         string         `json:"query,omitempty"`
	Editor        *Editor        `json:"editor,omitempty"`         // Optional: editor context might be useful
	CaseSensitive bool           `json:"case_sensitive,omitempty"` // Optional: for query matching
	Filters       *SearchFilters `json:"filters,omitempty"`        // Optional: to narrow down search by path/type
	Limit         *SearchLimit   `json:"limit,omitempty"`          // Optional: to limit number of results
	// Add any prompt-specific fields here, e.g.:
	// PromptType string `json:"prompt_type,omitempty"`
	// MinQuality float64 `json:"min_quality,omitempty"`
}

type SearchFilesRequest struct {
	Workspace string `json:"workspace,omitempty"`
	Query     string `json:"query,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type LineMatch struct {
	Before []SearchContentLine `json:"before,omitempty"`
	Line   SearchContentLine   `json:"line"`
	After  []SearchContentLine `json:"after,omitempty"`
}

type SearchContentLine struct {
	LineNumber int    `json:"line_number"`
	Content    string `json:"content"`
	Match      []int  `json:"match,omitempty"`
}

type SearchContentResult struct {
	File     string      `json:"file"`
	Lines    []LineMatch `json:"lines,omitempty"`
	Truncate bool        `json:"truncate,omitempty"`
}

type SearchContentResults struct {
	Results  []SearchContentResult `json:"results,omitempty"`
	Truncate bool                  `json:"truncate,omitempty"`
}

type SearchContentResponse struct {
	Code    int                  `json:"code"`
	Message string               `json:"message"`
	Data    SearchContentResults `json:"data,omitempty"`
}

type SearchFilesResult struct {
	Query string   `json:"query"`
	Files []string `json:"results,omitempty"`
}

type SearchFilesResponse struct {
	Code    int               `json:"code"`
	Message string            `json:"message"`
	Data    SearchFilesResult `json:"data,omitempty"`
}

type SearchSymbolsRequest struct {
	Workspace string       `json:"workspace,omitempty"`
	Query     string       `json:"query,omitempty"`
	Fuzzy     bool         `json:"fuzzy,omitempty"`
	Limit     *SearchLimit `json:"limit,omitempty"`
}

type SearchPromptsResponse struct {
	Code    int      `json:"code"`
	Message string   `json:"message"`
	Data    []string `json:"data,omitempty"`
}

type SymbolsFileMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

type SymbolContent struct {
	Name  string             `json:"name"`
	Files []SymbolsFileMatch `json:"files"`
}

type SymbolsContentResults struct {
	Query   string          `json:"query"`
	Symbols []SymbolContent `json:"symbols,omitempty"`
}

type SearchSymbolsResponse struct {
	Code    int                   `json:"code"`
	Message string                `json:"message"`
	Data    SymbolsContentResults `json:"data,omitempty"`
}
