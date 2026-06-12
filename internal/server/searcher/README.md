# searcher

Package `searcher` is the **query side** of Haystack. It answers the three search
kinds the server exposes — content search, file-name (fuzzy) search, and symbol
search — over the data the indexer has written. It is the read counterpart to
`internal/server/indexer`.

The actual full-text query logic (query compilation, candidate collection, and
line matching) lives in the reusable `searchcore/engine` package; this package
orchestrates it: applies workspace/path filters, prioritizes results by editor
context, opens candidate files to confirm matches, and enforces limits and
timeouts.

## Responsibility / scope

- `SearchContent` — full-text content search with context lines, filtering,
  editor-aware ordering, streaming-friendly callbacks, and unsaved-file search.
- `SearchFiles` — fuzzy matching over indexed file paths, scored and ranked.
- `SearchSymbols` — symbol (function/method) search over the symbol index.

## Dependency injection

`Run(wg, idx, st)` (called by `internal/server`) injects and stores two
process-wide handles used by every query:

- `idxInst *invertedindex.Index` — the shared inverted index (content + symbols).
- `stInst *documents.Store` — the document store (paths, metadata, ids).

`Run` itself only registers a shutdown waiter goroutine; the search functions are
plain calls invoked by the HTTP handlers and MCP tools.

## Files

- `searcher.go` — `SearchContent`, `SearchFiles`, document sorting
  (`sortDocuments`), the per-file line matcher (`searchInContent`), and the
  fuzzy-path scorer (`fuzzyMatchWithScore`).
- `symbols_searcher.go` — `SearchSymbols` and its fuzzy implementation
  (`fuzzySearchSymbols`), plus the exact `searchSymbols` variant and the
  file-changed reconciliation helper (`isFileChanged`).
- `search.md` — a work-in-progress query-syntax reference for end users.

## Content search (`SearchContent`)

1. **Limits.** Start from `Server.Search.Limit`, tightened by any per-request
   limit.
2. **Filters.** Build path / include / exclude filters from
   `req.Filters` (path is resolved relative to the workspace root) into a
   `wantFile` predicate.
3. **Unsaved files.** If `req.UnsavedFiles` is provided, search their in-memory
   content first (so editor buffers win over on-disk copies); these paths are
   then skipped during the index pass. `req.UnsavedFilesOnly` returns after this
   step.
4. **Compile.** Construct a `searchcore/engine.Engine` via `engine.New(idxInst,
   stInst, workspace.Id, …)` with `MaxWildcardLength`, `MaxKeywordDistance`, and
   `WholeWord` options, then `Compile(query, caseSensitive)`. The engine parses
   the query into OR-of-AND clauses with prefix/phrase/wildcard handling.
5. **Collect candidates.** `engine.CollectDocuments()` returns candidate document
   ids from the inverted index.
6. **Order.** `sortDocuments` prioritizes results using `req.Editor`: the active
   file, then open files, then files in the same directories / parent directory,
   then wildcard-hit docs, then the remainder. It also drops candidates rejected
   by `wantFile`.
7. **Confirm.** For each candidate, `RefreshFileIfNeeded` (from `indexer`) prunes
   stale entries, then the file is opened and scanned line by line with
   `engine.IsLineMatch`; matches become `types.LineMatch` entries, optionally
   with `BeforeAfter` context lines (clamped to 0–5).
8. **Stop conditions.** Per-file (`MaxResultsPerFile`) and total (`MaxResults`)
   limits, plus the caller-supplied context/timeout, bound the work. The boolean
   return value reports truncation. The optional callback receives each file's
   result as it is produced (used for SSE streaming in the HTTP layer).

## File search (`SearchFiles`)

Scans all indexed paths for the workspace (`stInst.ScanFiles`), filters with
`fuzzy.Match`, and scores survivors with `fuzzyMatchWithScore` — a heuristic
combining text-coverage, consecutive runs, match density, a leading-position
bonus, and a filename-match bonus. Matches scoring above 50 are sorted (score
desc, then shorter path) and returned up to the request limit. Paths whose files
have since disappeared are filtered out and removed from the index
asynchronously.

## Symbol search (`SearchSymbols`)

Currently delegates to `fuzzySearchSymbols`. The query is split into words with
`wordsegmentation` against an English corpus, then matched against the
workspace's symbol words table via the shared inverted index
(`idxInst.Search` on `swt.InvertedId`). For each candidate document it confirms
the file is current (`isFileChanged`, which re-syncs or removes drifted files via
`indexer`), fetches its functions from `internal/core/symbols`, and keeps those
whose name contains the query words in order. The exact-match `searchSymbols`
path (querying `symbols.GetSymbolTable`) also exists but is not currently the
default entry point.

## Configuration (`internal/conf`)

- `Server.Search.Limit` — `MaxResults`, `MaxResultsPerFile`, `MaxFilesResults`.
- `Server.Search.MaxWildcardLength`, `Server.Search.MaxKeywordDistance` — engine
  query options.

## Relationships

- **Started/wired by** `internal/server` (`searcher.Run` with the inverted index
  and document store).
- **Called by** `internal/server/httpapi` (search handlers) and
  `internal/server/mcptools` (MCP search tools).
- **Uses** `searchcore/engine` (content query engine), `searchcore/invertedindex`
  and `searchcore/documents` (candidate lookup and document metadata),
  `internal/core/symbols` (symbol data), and `internal/server/indexer`
  (`RefreshFileIfNeeded`, `RemoveFile`, `AddOrSyncFile`) for read-time
  reconciliation.
