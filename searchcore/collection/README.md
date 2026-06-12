# collection

Package `collection` is the top-level entry point of the searchcore stack: an
instance-based **catalog** that manages named collections of documents. A
"collection" is searchcore's unit of isolation — each one owns its own document
set and its own inverted-index table, so searches over one collection never see
another's data. Haystack uses one collection per indexed workspace, keyed by the
workspace's absolute path.

It composes a `documents.Store` (which in turn composes an
`invertedindex.Index`), so creating a collection also provisions its backing
document table and inverted-index table, and deleting a collection purges them.

This is a standalone library package: it has no dependency on `internal/`.

## Place in the layering

```
collection      ← this package: registry + lifecycle of named collections
  documents       per-collection document storage (composed via documents.Store)
    invertedindex   low-level posting-list engine (reached through documents)
```

`collection` is the layer most consumers talk to. It delegates all document
operations to the composed `documents.Store`, scoped to a collection's id.

## Key types

- **`Catalog`** (`catalog.go`) — the registry. Constructed with `New(store, docs, opts)`.
  It owns an in-memory index of every collection (`byID` and `byName` maps),
  populated once at construction by scanning persisted records. All lookups
  (`Get`, `GetByName`, `List`) are served from memory without disk reads;
  mutations (`Create`, `Save`, `Delete`) update both disk and the in-memory
  maps under a single `sync.RWMutex`.
- **`Collection`** (`catalog.go`) — a lightweight handle bound to one collection
  id, returned by `Create`/`Get`/`GetByName`. Its document methods (`Save`,
  `Update`, `DeleteDocument`, `GetDocument`, `Count`, `ScanFiles`) delegate to
  the catalog's `documents.Store` scoped to that id. `ID()` and `Meta()` expose
  the id and a snapshot copy of the record.
- **`Record`** (`catalog.go`) — the persisted, JSON-encoded metadata for one
  collection: numeric `ID`, unique `Name`, optional `Desc`, `CreatedAt` /
  `LastAccessed` / `LastFullSync` timestamps, and an opaque consumer-defined
  `Extra` byte slice (haystack stores filter configuration there). `List` and
  `Meta` always return independent deep copies, so callers cannot mutate the
  catalog's internal state.
- **`Options`** (`catalog.go`) — selects the catalog's two on-disk key-type
  prefix bytes; zero values select the defaults.

## Lifecycle operations

- **`Create(name)`** allocates a new id via `store.GetIncrementalId`, persists
  the `Record`, then calls `documents.Store.Create` to provision the backing
  document/inverted-index table. If the table creation fails it best-effort
  rolls back the persisted record. Returns an error if the name is already in use.
- **`Delete(id)`** removes the on-disk record and de-indexes it from the
  in-memory maps under the lock first, then purges document data via
  `documents.Store.Delete` outside the lock (that call blocks on the shared
  queue). Removing the record first guarantees a partial failure cannot leave a
  live record pointing at deleted data.
- **`Save(record)`** re-persists an existing record (e.g. updated `Extra`,
  timestamps, or `Name`), guarding against renaming into a name already owned by
  another collection.

## On-disk layout

The catalog uses two key-type prefix bytes for its own keys (the composed
`documents.Store` uses separate bytes, 10–13). Defaults are configurable via
`Options`; a zero field selects the default. Byte 0 (NUL) is reserved as the
"use default" sentinel and cannot be a custom prefix.

| Constant                | Byte | Contents                                        |
|-------------------------|------|-------------------------------------------------|
| `DefaultKeyTypeIncrId`  | 1    | Single key holding the auto-increment id counter |
| `DefaultKeyTypeRecord`  | 2    | One key per collection; value is JSON `Record`  |

These defaults (1 and 2) match the legacy haystack workspace registry's on-disk
layout, so existing data is readable without migration. Changing a key-type byte
after data has been written is a breaking on-disk change.

Record keys are encoded as `{KeyTypeRecord}{id}` (see `codec.go`); records that
fail to decode or carry an empty name are skipped during the initial load and
logged.
