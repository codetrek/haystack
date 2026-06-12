# mcptools

Package `mcptools` implements the **MCP (Model Context Protocol) tool handlers**
that let AI clients search a workspace through Haystack's `/mcp` endpoint. Each
handler takes an `mcp.CallToolRequest`, runs a search via
`internal/server/searcher`, and returns a `mcp.CallToolResult` with
human-readable text content.

The *tool definitions* (names, argument schemas, descriptions) live in
`internal/server/httpapi` (`mcp.go`, `registerMCPTools`); this package supplies
the *handler functions* those definitions are bound to. The split keeps endpoint
wiring in `httpapi` and search-result formatting here.

## Responsibility / scope

- `SearchContent` — handler for the `HaystackSearch` tool (content search).
- `SearchFiles` — handler for the `HaystackFiles` tool (filename fuzzy search).
- Shared argument parsing/validation (`parseAndValidateSearchArgs`).

## Files

- `search_content.go` — `SearchContent`. Parses `query`, `workspace`, `limit`
  plus optional `path`, `filter`, and `exclude`; resolves the workspace; rejects
  absolute `path` values; builds a `types.SearchContentRequest` (with
  `BeforeAfter: 1` for context lines) and calls
  `searcher.SearchContent` with a 10 s timeout. Formats the matches into a text
  block (per-file headers, line numbers, before/after context, and a truncation
  notice).
- `search_files.go` — `SearchFiles`. Parses arguments, resolves the workspace,
  calls `searcher.SearchFiles`, and returns the matched file paths as text
  content (or a "No results found." message).
- `utils.go` — `parseAndValidateSearchArgs`: extracts `query` / `workspace` /
  `limit` from the raw argument map, normalizes the workspace path and requires
  it to be absolute, and clamps the limit to the configured maximum
  (`Server.Search.Limit.MaxFilesResults`, defaulting to
  `Client.DefaultLimit.MaxFilesResults`).

## Relationships

- **Registered by** `internal/server/httpapi` — `registerMCPTools` binds
  `mcptools.SearchContent`/`mcptools.SearchFiles` to the `HaystackSearch` and
  `HaystackFiles` tool definitions on the MCP server mounted at `/mcp`.
- **Delegates to** `internal/server/searcher` for the actual queries.
- **Consumes** `internal/core/workspace` (workspace lookup),
  `internal/shared/types` (request shapes), `internal/conf`, `internal/utils`
  (path normalization), and the `mark3labs/mcp-go` `mcp` package.

The full per-tool argument reference is owned by the API reference docs; this
README documents the package design.
