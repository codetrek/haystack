# httpapi

Package `httpapi` is the daemon's **request surface**. It builds the HTTP route
table, implements every JSON handler, and sets up the `/mcp` endpoint. It is the
boundary where external requests (from the CLI client, editor integrations, or
MCP clients) enter the server and are dispatched to the indexer, searcher, and
workspace subsystems.

It contains no search, indexing, or storage logic of its own — handlers validate
input, look up the target workspace, delegate to the appropriate subsystem, and
encode the response.

## Responsibility / scope

- Register all `/api/v1/...` routes plus `/health` on a single `http.ServeMux`.
- Serve that mux over a loopback TCP listener and/or a Unix-socket listener.
- Mount the Model Context Protocol endpoint at `/mcp`.
- Decode request bodies into `internal/shared/types` request structs, validate
  them (workspace required, absolute paths, non-empty query), and encode the
  matching response struct.
- Handle graceful shutdown of both listeners.

## Files

- `server.go` — `StartServer(wg, addr, socketPath)`: creates the mux, registers
  every route, starts the TCP and/or Unix-socket `http.Server`s, calls
  `mcpInit` when a TCP address is present, and shuts both servers down (5 s
  timeout) on the `running` shutdown signal.
- `server_cntl.go` — server-control handlers: `/health`, `/api/v1/server/status`,
  `/api/v1/server/restart`, `/api/v1/server/stop`. These report state from
  `internal/shared/running` and `internal/conf`, or trigger restart/shutdown.
- `workspace.go` — workspace lifecycle handlers: `create`, `update`, `delete`,
  `list`, `get`, `sync`, `sync-all`, `move`. These call into
  `internal/core/workspace` and `internal/server/indexer`
  (`CreateWorkspace`, `Sync`).
- `document.go` — per-file index maintenance: `/api/v1/document/update` (calls
  `indexer.ShouldIndexFile` then `indexer.AddOrSyncFile`) and
  `/api/v1/document/delete` (calls `indexer.RemoveFile`).
- `search.go` — `/api/v1/search/content`, `/search/files`, `/search/symbols`.
  These resolve the workspace by path and delegate to
  `internal/server/searcher` (`SearchContent`, `SearchFiles`, `SearchSymbols`).
  Content search additionally supports a streaming mode: when the request's
  `Accept` header is `text/event-stream`, results are flushed as SSE
  `event:result` / `event:done` frames instead of a single JSON body.
- `mcp.go` — `mcpInit` and `registerMCPTools`: builds the MCP server, registers
  the tool definitions, and mounts the endpoint (see below).

## Route surface

| Group | Routes |
|-------|--------|
| Health/control | `/health`, `/api/v1/server/{status,restart,stop}` |
| Documents | `/api/v1/document/{update,delete}` |
| Workspace | `/api/v1/workspace/{create,update,delete,list,get,sync,sync-all,move}` |
| Search | `/api/v1/search/{content,files,symbols}` |
| MCP | `/mcp` (and `/mcp/sse`, `/mcp/message`) |

Responses generally use the `Code`/`Message`/`Data` envelope from
`internal/shared/types` (`Code == 0` means success). This README describes the
package design; the full endpoint/field reference is owned by the API reference
docs, not here.

## MCP endpoint

`mcpInit` constructs a `mark3labs/mcp-go` server named `"Haystack"`, registers
the tool schemas, and exposes them under `/mcp`:

- A streamable HTTP server handles `/mcp` requests by default.
- An SSE server handles the `/mcp/sse` and `/mcp/message` sub-paths.

`registerMCPTools` defines two tools — `HaystackSearch` (content search) and
`HaystackFiles` (filename fuzzy search) — including their argument schemas and
help text, then binds them to the handlers in `internal/server/mcptools`
(`mcptools.SearchContent`, `mcptools.SearchFiles`). The MCP server is only set up
when `StartServer` receives a non-empty TCP address.

## Relationships

- **Started by** `internal/server` (`httpapi.StartServer` in `server.go`).
- **Delegates to** `internal/server/searcher` (queries),
  `internal/server/indexer` (workspace/document indexing),
  `internal/core/workspace` (registry lookups), and
  `internal/server/mcptools` (MCP tool handlers).
- **Consumes** `internal/shared/types` (request/response shapes),
  `internal/conf`, `internal/shared/running`, and `internal/utils`
  (path normalization).
