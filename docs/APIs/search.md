# Search APIs

These APIs provide comprehensive search functionality across code content, files, symbols, and prompts within indexed workspaces.

## Content Search

**Endpoint:** `POST /api/v1/search/content`

### Description

Performs full-text search across the content of all indexed files in a workspace. Supports both regular JSON responses and streaming responses for real-time results.

### Request

- **Method:** POST
- **Content-Type:** `application/json`
- **Accept:** `application/json` (default) or `text/event-stream` (for streaming)

**Request Body:**
```json
{
  "workspace": "/absolute/path/to/workspace",
  "query": "function AND search",
  "case_sensitive": false,
  "whole_word": false,
  "filters": {
    "path": "src/",
    "include": "*.go,*.js",
    "exclude": "*_test.go"
  },
  "limit": {
    "max_results": 100,
    "max_results_per_file": 10
  },
  "before_after": 3,
  "editor": {
    "open_files": ["/path/to/file1.go", "/path/to/file2.js"],
    "active_file": "/path/to/file1.go"
  },
  "unsaved_files": [
    {
      "path": "relative/path/to/file.go",
      "content": "modified content..."
    }
  ],
  "unsaved_files_only": false
}
```

### Request Fields

- `workspace` (required): Absolute path to the workspace
- `query` (required): Search query string
- `case_sensitive` (optional): Whether search is case-sensitive
- `whole_word` (optional): Whether to match whole words only
- `filters` (optional): Search filters
  - `path`: Limit search to specific path
  - `include`: File patterns to include (comma-separated)
  - `exclude`: File patterns to exclude (comma-separated)
- `limit` (optional): Result limits
  - `max_results`: Maximum total results
  - `max_results_per_file`: Maximum results per file
- `before_after` (optional): Number of context lines before/after matches
- `editor` (optional): Editor context information
- `unsaved_files` (optional): Files with unsaved changes
- `unsaved_files_only` (optional): Search only in unsaved files

### Response (JSON Mode)

```json
{
  "code": 0,
  "message": "Ok",
  "data": {
    "results": [
      {
        "file": "src/main.go",
        "lines": [
          {
            "before": [
              {
                "line_number": 10,
                "content": "package main",
                "match": []
              }
            ],
            "line": {
              "line_number": 11,
              "content": "func search() {",
              "match": [5, 11]
            },
            "after": [
              {
                "line_number": 12,
                "content": "  return nil",
                "match": []
              }
            ]
          }
        ]
      }
    ],
    "truncate": false
  }
}
```

### Response (Streaming Mode)

When `Accept: text/event-stream` header is set:

```
event:result
data:{"file":"src/main.go","lines":[...]}

event:result
data:{"file":"src/other.go","lines":[...]}

event:done
data:{"truncate":false}
```

### Search Query Syntax

The search supports various query patterns:
- **Basic terms**: `function` (matches "function")
- **Prefix matching**: `func*` (matches "function", "functional", etc.)
- **Exact phrases**: `"exact phrase"` (matches exact phrase)
- **Logical operators**:
  - `AND` or space: `error AND handle`
  - `OR`: `create | update`
- **Combined**: `func* AND (error | warning)`

---

## File Search

**Endpoint:** `POST /api/v1/search/files`

### Description

Searches for files by name/path using fuzzy matching algorithms. Returns a list of file paths that match the query.

### Request

- **Method:** POST
- **Content-Type:** `application/json`

**Request Body:**
```json
{
  "workspace": "/absolute/path/to/workspace",
  "query": "main.go",
  "limit": 50
}
```

### Request Fields

- `workspace` (required): Absolute path to the workspace
- `query` (required): File name or path pattern to search for
- `limit` (optional): Maximum number of results to return

### Response

```json
{
  "code": 0,
  "message": "Ok",
  "data": {
    "query": "main.go",
    "results": [
      "src/main.go",
      "cmd/main.go",
      "examples/main.go"
    ]
  }
}
```

### Notes

- Uses fuzzy matching (e.g., "maintgo" can match "main.go")
- Results are ranked by relevance
- Case-insensitive matching

---

## Symbol Search

**Endpoint:** `POST /api/v1/search/symbols`

### Description

Searches for code symbols (functions, classes, variables, etc.) across the workspace using ctags-based indexing.

### Request

- **Method:** POST
- **Content-Type:** `application/json`

**Request Body:**
```json
{
  "workspace": "/absolute/path/to/workspace",
  "query": "SearchContent",
  "fuzzy": true,
  "limit": {
    "max_results": 100,
    "max_results_per_file": 10
  }
}
```

### Request Fields

- `workspace` (required): Absolute path to the workspace
- `query` (required): Symbol name to search for
- `fuzzy` (optional): Enable fuzzy matching
- `limit` (optional): Result limits

### Response

```json
{
  "code": 0,
  "message": "Ok",
  "data": {
    "query": "SearchContent",
    "symbols": [
      {
        "name": "SearchContent",
        "files": [
          {
            "path": "src/searcher/searcher.go",
            "line": 45
          },
          {
            "path": "src/server/search.go",
            "line": 120
          }
        ]
      }
    ]
  }
}
```

### Symbol Types

The search includes various symbol types:
- Functions and methods
- Classes and structs
- Variables and constants
- Type definitions
- Interfaces
- Enums

---

## Prompt Search

**Endpoint:** `POST /api/v1/search/prompts`

### Description

Searches for AI prompts within the codebase, useful for finding prompt templates and AI-related content.

### Request

- **Method:** POST
- **Content-Type:** `application/json`

**Request Body:**
```json
{
  "workspace": "/absolute/path/to/workspace",
  "query": "code generation",
  "case_sensitive": false,
  "filters": {
    "path": "prompts/",
    "include": "*.md,*.txt",
    "exclude": "*.tmp"
  },
  "limit": {
    "max_results": 50,
    "max_results_per_file": 5
  }
}
```

### Request Fields

- `workspace` (required): Absolute path to the workspace
- `query` (required): Search query for prompts
- `case_sensitive` (optional): Case-sensitive search
- `filters` (optional): Path and file filters
- `limit` (optional): Result limits

### Response

```json
{
  "code": 0,
  "message": "Ok",
  "data": [
    "prompts/code-generation.md",
    "templates/ai-prompts.txt",
    "docs/prompt-examples.md"
  ]
}
```

### Error Responses

All search APIs return error responses in this format:

```json
{
  "code": 1,
  "message": "Error description"
}
```

### Common Error Codes

- `code: 1`: General error (workspace not found, invalid query, etc.)
- HTTP 400: Bad request (invalid JSON, missing required fields)
- HTTP 500: Internal server error

### Performance Notes

- Content search with streaming provides better user experience for large results
- File and symbol searches are typically very fast due to optimized indexing
- Use filters to improve performance and relevance
- Prompt search performance depends on workspace size and prompt density
