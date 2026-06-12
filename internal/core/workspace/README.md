# `internal/core/workspace`

The haystack-side workspace model: the in-memory registry of indexed
repositories, their per-workspace filters and indexing-progress state, and the
lifecycle operations that create, move, and delete them. See the
[architecture overview](../../../docs/architecture.md) for the broader workspace
model and how it relates to the `searchcore/collection` catalog.

## Responsibility

A **workspace** is a local repository directory that haystack indexes and
searches. This package owns the haystack-specific concept of a workspace, while
**persistence is delegated to `searchcore/collection`**: each workspace is a
collection in the catalog, keyed by the workspace's absolute path. This package
keeps an in-memory overlay (the `*Workspace` maps) built from the catalog and
layers runtime-only state (indexing progress, filters) on top of the persisted
records.

## State and initialization (`init.go`)

The package holds process-global maps guarded by a `sync.RWMutex`:

- `workspaces map[int]*Workspace` — keyed by workspace id.
- `workspacePaths map[string]*Workspace` — keyed by normalized absolute path.
- `catalog *collection.Catalog` — the backing catalog injected by `Init`.

`Init(cat *collection.Catalog) error` wires the package to an
already-constructed catalog and rebuilds the in-memory maps from `cat.List()`.
Runtime indexing state always starts fresh; persisted filter config is decoded
from each record's `Extra` field.

## Key type: `Workspace` (`workspace.go`)

`Workspace` carries both persisted fields (`Id`, `Path`, `Desc`,
`UseGlobalFilters`, `Filters`, `CreatedAt`, `LastAccessed`, `LastFullSync`) and
unexported runtime fields (`deleted`, `indexingState`, `indexingProgress`)
guarded by a per-workspace mutex. Notable methods:

- Indexing lifecycle: `StartIndexing`, `AddIndexingFiles`,
  `AddSymbolParsedFiles`, `GetIndexingProgress`, `GetIndexingState`,
  `UpdateLastFullSync`, `SetIndexingFailed`, `ResetIndexingState`. The
  `IndexingState` enum is `IndexingIdle | IndexingScanning | IndexingDone |
  IndexingFailed`.
- `GetFilters()` resolves effective include/exclude filters, falling back to the
  global config in `internal/conf` when the workspace uses global filters or has
  none of its own.
- `GetTotalFiles()` returns the document count for the workspace via the injected
  `documents.Store` (`SetDocStore` injects it at startup).
- `Save()` snapshots the workspace into a `collection.Record` and persists it
  through the catalog.

## Lifecycle operations (`manage.go`)

Package-level functions operate against the registry and catalog under the
package mutex:

- `Create(path)` validates the path (absolute, existing directory), creates the
  catalog collection (which allocates the id, persists the record, and creates
  the document store), persists `UseGlobalFilters=true` into the record's
  `Extra`, publishes the workspace into the maps, and calls
  `symbols.Create` to set up the per-workspace symbol tables.
- `Delete(id)` deletes the catalog record (and its document data) first, then
  drops the workspace from the overlay and calls `symbols.Delete`.
- `Move(id, newPath)` validates and renames the workspace path, re-keying the
  path map and persisting via `Save()`.
- `Get`, `GetByPath`, `GetAll`, `GetAllPaths` are read accessors; `GetAll`
  returns `types.Workspace` snapshots (including total file count and indexing
  flag) sorted by id.

## Filter encoding and migration (`migrate.go`)

- Per-workspace filter config (`UseGlobalFilters` + `*types.Filters`) is
  serialized into the opaque `Extra` field of a `collection.Record` via
  `encodeExtra` / `decodeExtra`.
- `MigrateLegacyRecords(db, opts)` upgrades old on-disk workspace JSON (records
  with a `"path"` field but no `"name"`) into the new `collection.Record` format.
  It is idempotent, leaves the incr-id counter untouched, and is intended to run
  **before** `collection.New` so the catalog sees only new-format data.

## Relationships

- **Depends on** `searchcore/collection` (persistence/catalog),
  `searchcore/documents` (file counting), `searchcore/kv` (migration scan),
  `internal/core/symbols` (per-workspace symbol tables),
  `internal/conf` (global filters/config), `internal/shared/types`, and
  `internal/utils` (path normalization).
- **Consumed by** `internal/server` (`server.go` runs `MigrateLegacyRecords`,
  builds the catalog, and calls `workspace.Init`), and across
  `internal/server/indexer`, `internal/server/searcher`, `internal/server/httpapi`,
  and `internal/server/mcptools`, which look up and mutate workspaces.
- Each workspace owns its own inverted-index table(s) and document set, so search
  results are scoped to a single workspace.
