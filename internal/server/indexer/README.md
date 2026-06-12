# indexer

Package `indexer` **builds and maintains the search index** for each workspace.
It is a multi-stage pipeline of long-running goroutines — scanner → parser →
writer, with a parallel symbol-parser stage — that walk a workspace's files,
tokenize their contents, and persist documents (and code symbols) into the
`searchcore` document/inverted-index stores.

The pipeline is the *write* side of Haystack; the matching *read* side is
`internal/server/searcher`.

## Responsibility / scope

- Run the indexing pipeline as background stages (`Run`).
- Full-workspace sync (`Sync`, `SyncIfNeeded`) and workspace creation
  (`CreateWorkspace`).
- Incremental per-file maintenance: add/update (`AddOrSyncFile`), delete
  (`RemoveFile`), and on-demand refresh (`RefreshFileIfNeeded`,
  `RefreshFilesIfNeeded`).
- Decide whether a path is indexable (`ShouldIndexFile`, `IsNotIndexiable`,
  `IsLikelyText`) and mint stable document ids from relative paths
  (`GetDocumentId`, backed by `searchcore/idtable`).

## Dependency injection

The package holds two process-wide dependencies injected by
`internal/server` before `Run`:

- `SetIdAllocator(*idtable.Allocator)` — used by `GetDocumentId`.
- `SetDocStore(*documents.Store)` — the `searchcore/documents` store all stages
  write through.

The four stage objects (`scanner`, `parser`, `writer`, `symbolParser`) are
package-level singletons guarded by a mutex; `Run(wg)` starts each one's
goroutine(s) and registers a shutdown goroutine that stops them when the
`running` shutdown signal fires.

## Pipeline stages

### Scanner (`scanner.go`)

Processes one workspace at a time from an internal FIFO queue (`Scanner.Add`,
fed by `Sync`). For each workspace it walks the tree with `fsutils.ListFiles`,
applying the workspace's filters: `.gitignore` rules (`GitIgnoreFilter`) when
`Exclude.UseGitIgnore` is set, otherwise customized exclude globs, plus an
include filter. Non-indexable extensions are skipped (`IsNotIndexiable`).
Surviving paths are handed to the parser via `parser.Add`. On success it records
`UpdateLastFullSync`; scanning aborts cleanly on shutdown or workspace deletion.

### Parser (`parser.go`)

A pool of `Server.IndexWorkers` goroutines reading file paths off a channel. For
each file (`parse`):

1. Stat the file; flag it oversize if it exceeds `Server.MaxFileSize`.
2. Skip if an existing document has the same modification time.
3. For non-oversize files: read content, skip non-text content
   (`IsLikelyText`), and skip if the content hash (`GetContentHash`) is unchanged.
4. Tokenize content and the relative path with
   `searchcore/tokenizer.TokenizeForIndex` to produce `Words` / `PathWords`.
5. Build a `documents.Document` (id, rel path, size, mod time, hash, words) and
   hand it to the writer (`writer.Add`), flagged as new or existing.

Oversize files are still recorded as documents (with empty words) so they appear
in file searches, but their content is not indexed and they are not sent to the
symbol parser. Non-oversize files are additionally queued to the symbol parser.

### Writer (`writer.go`)

A single goroutine that drains documents from a channel and **batches** them
(up to 8 per cycle) before persisting. It groups documents per workspace and
calls `documents.Store.SaveNewDocuments` / `UpdateDocuments`, which also update
the inverted-index posting lists (writes are serialized through the shared
`searchcore/queue`). Documents belonging to a deleted workspace are dropped. On
shutdown it flushes any remaining queued documents before exiting.

### Symbol parser (`symbol_parser.go`)

Active only when `Symbols.EnableFeature` is set and a `ctags` executable is
found (`getCtagsPath`). Files are cached per workspace and flushed in batches
(by `MaxBatchSize` or on a periodic timer). For each batch it groups files by
language (`GetLangFromFilename`), runs universal-ctags with JSON output
(`parseFunction`) to extract function/method symbols, and records them through
`internal/core/symbols.AddFunctions` — which indexes symbol keywords into the
symbol inverted-index tables. Files with no extracted symbols are still recorded
with an empty function list so deletions/changes are tracked.

## Change detection & incremental updates

- `AddOrSyncFile` looks up the existing document: a new file is queued to the
  parser; an existing path that is now missing or a directory is removed; an
  existing file is re-parsed.
- `RemoveFile` deletes the document from both the documents store and the symbol
  store.
- `RefreshFileIfNeeded` is called from the searcher during query time: it removes
  documents whose files have disappeared or become directories, and re-queues
  files whose modification time changed.

## Configuration (`internal/conf`)

- `Server.IndexWorkers` — parser worker count.
- `Server.SymbolParserWorkers` — symbol-parser worker count.
- `Server.MaxFileSize` — content-indexing size cap.
- `Symbols.EnableFeature`, `BinPath.CTags` — symbol extraction.

## Relationships

- **Started/wired by** `internal/server` (`indexer.Run`, `SetDocStore`,
  `SetIdAllocator`).
- **Triggered by** `internal/server/httpapi` (workspace/document handlers) and,
  at query time, by `internal/server/searcher` (`RefreshFileIfNeeded`,
  `RemoveFile`, `AddOrSyncFile`).
- **Writes through** `searchcore/documents` (+ `searchcore/invertedindex` via the
  document store), `searchcore/tokenizer`, `searchcore/idtable`, and
  `internal/core/symbols`.
- **Reads from** `internal/core/workspace` for filters and indexing-progress
  state.
