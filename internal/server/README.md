# server

Package `server` is the **process bootstrap and dependency-wiring layer** of the
Haystack daemon. When the `haystack` binary runs in daemon mode (see
`cmd/haystack/main.go`), it calls `server.Run()`; this package opens storage,
constructs the search/index subsystems, wires them into the indexer, searcher,
and HTTP/MCP server, and then blocks until shutdown.

It owns no request handling or query logic itself — those live in the
sub-packages (`httpapi`, `indexer`, `searcher`, `mcptools`). This package's job
is lifecycle and composition.

## Responsibility / scope

- Acquire the single-instance server lock and run the daemon (`Run`).
- Open the two on-disk Pebble stores and the shared async write queue.
- Construct the `searchcore` and `internal/core` subsystems in dependency order
  and inject them into the packages that need them.
- Start the indexer, searcher, and HTTP/MCP server goroutines.
- Coordinate graceful shutdown and close everything in the correct order.

## Files

- `server.go` — `Run` and the internal `run` function that perform all wiring
  (described below). Also holds test-overridable function variables
  (`invertedindexInit`, `documentsNew`, `workspaceInit`, `symbolsInit`).
- `log.go` — `initLog` configures the standard logger to write either to stdout
  (when `Server.LoggingStdout` is set) or to a rotating log file under
  `<data_path>/logs/server.log` (via `lumberjack`).

## Startup wiring (`run`)

`run` builds the system bottom-up; any failure triggers `running.Shutdown()` and
returns an error. In order:

1. **Storage.** Opens two Pebble-backed stores via `storage.Open`
   (`internal/core/storage`):
   - `<data_path>/data` — documents, collection catalog, the id allocator, and
     workspace/symbol records.
   - `<data_path>/index` — the inverted index (shared by content and symbol
     search).
   The data path and cache size come from `internal/conf`.
2. **Async write queue.** Creates and starts a `searchcore/queue.Mpsc`
   (`"DBQueue"`) that serializes batched writes for the index and document store.
3. **Id allocator.** Builds a `searchcore/idtable.Allocator` over the data store
   and injects it into the indexer (`indexer.SetIdAllocator`); it mints stable,
   compact document ids from file paths.
4. **Inverted index.** Constructs a `searchcore/invertedindex.Index` over the
   index store and the queue.
5. **Documents store.** Constructs a `searchcore/documents.Store` over the data
   store, queue, and inverted index, then injects it into the indexer and
   workspace packages (`indexer.SetDocStore`, `workspace.SetDocStore`).
6. **Workspace registry.** Migrates any legacy workspace records
   (`workspace.MigrateLegacyRecords`) **before** constructing the
   `searchcore/collection.Catalog`, then calls `workspace.Init(cat)` to build the
   in-memory registry from the catalog.
7. **Symbols.** Initializes `internal/core/symbols` over the data store, queue,
   and the shared inverted index.
8. **Indexer & searcher.** Starts `indexer.Run(wg)` (the scan/parse/write/symbol
   pipeline) and `searcher.Run(wg, idx, st)` (injecting the inverted index and
   documents store used by content/file/symbol search).
9. **HTTP/MCP server.** Calls `httpapi.StartServer(wg, tcpAddr, socketPath)`. A
   loopback TCP address is built only when `Global.Port > 0`; the Unix socket
   path comes from `Global.SocketPath`. The MCP endpoint is only enabled when a
   TCP address is present.

If `ForTest.Path` is configured, the workspace at that path is synced on startup
(`indexer.SyncIfNeeded`) to support test fixtures.

## Shutdown

`run` blocks on `wg.Wait()` until all started goroutines drain after a shutdown
signal (managed by `internal/shared/running`). It then closes the subsystems in
reverse dependency order: documents store, inverted index, symbols, the MPSC
queue, the id allocator, and finally the two Pebble stores.

## Relationships

- **Consumes** `internal/conf` (configuration), `internal/core/storage`,
  `internal/core/workspace`, `internal/core/symbols`,
  `internal/shared/running`, and the `searchcore` packages
  (`collection`, `documents`, `idtable`, `invertedindex`, `kv`, `queue`).
- **Owns / starts** the sub-packages `internal/server/indexer`,
  `internal/server/searcher`, and `internal/server/httpapi` (which in turn wires
  in `internal/server/mcptools`).
- **Is invoked by** `internal/client` (`server run` / daemon launch) and
  `cmd/haystack`.

See `docs/architecture.md` for the system-wide view and the `searchcore` module
boundary.
