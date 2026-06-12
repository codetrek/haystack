# invertedindex

Package `invertedindex` is the low-level posting-list engine at the bottom of
the searchcore search stack. It maps **search terms (keywords) → document ids**
within isolated *tables*, with batched asynchronous writes and a background
keyword-merging compactor. It is intentionally low-level: it knows nothing about
documents, files, or query syntax — only keywords and 8-byte document ids.

It is a standalone library package: it has no dependency on `internal/`. All
mutating operations are expected to run on the shared `queue.Queue` (MPSC) so
the engine's pending-write state is mutated single-threaded.

## Place in the layering

```
documents       per-collection document storage (composes and notifies this Index)
  invertedindex   ← this package: term → doc-ids posting lists, per table
```

`invertedindex` is composed by `documents.Store`, which calls `Update` on every
document mutation, and is read by the query `engine` via `Search` / `GetDocs`.
Nothing in this package depends on the layers above it.

## Key types

- **`Index`** (`storage.go`) — the instance-based engine, constructed with
  `New(store, queue, opts)`. `New` also starts two background goroutines: a
  flush ticker that periodically drains pending writes/deletes to the store, and
  the keyword merger. `CloseAndWait` stops both, then performs a final
  synchronous flush.
- **`TableInfo`** (`table.go`) — metadata for one table (its id, creation time,
  description). Each table is an isolated keyword namespace. `CreateTable` mints
  a new id and persists the metadata; `DeleteTable` drops the table and all of
  its posting rows. `documents` allocates one table per collection.
- **`SearchResult`** (`invertedindex.go`) — the result of a query: `DocIds` (a
  set of 8-byte doc-id strings from exact keyword matches) plus an optional
  `WildDocIds` set that wildcard-aware callers populate from wildcard expansion.
- **`Options`** (`storage.go`) — the three on-disk key-type prefix bytes plus
  flush/merge tunables (flush ticker, batch-size and age thresholds for writes
  and deletes, flush cooldown, and the max doc-ids per row). Zero values select
  sensible defaults.

## Read and write operations

- **`Update(tableId, docid, newKeywords, oldKeywords)`** is the single mutation
  entry point. With no old keywords it adds, with no new keywords it removes, and
  otherwise it diffs the two sets and applies only the additions/removals. It
  must run on the shared queue. `documents.Store` calls it on every save/update/delete.
- **`Search(tableId, query, limit, filterKeyword)`** returns the union of doc
  ids whose keywords match `query` as a lower-cased keyword **prefix**. An
  optional `filterKeyword` callback can reject candidate keys; a positive `limit`
  caps the number of distinct doc ids.
- **`GetDocs(tableId, key)`** returns the union of doc ids under an exact keyword
  key (matched verbatim, no lower-casing, no limit).

## How posting lists are stored

A single keyword's documents are spread across one or more **rows** rather than
one giant value. Each row key encodes the table id, the keyword, the row's
doc-count, and a microsecond timestamp:

```
{KeyTypeRow}{tableId}|{keyword}|{docCount}|{tickMicros}
```

The row *value* is the concatenation of fixed-width **8-byte** document ids (so
ids must be exactly 8 bytes — see the `idtable` allocator). `MaxInvertedIndexSize`
(default 1000) caps the doc-ids per row. Splitting across rows keeps individual
writes small and lets concurrent updates append without rewriting a large value;
the merger later consolidates fragmented rows.

```
"hello" → row(count=3) [Doc1 Doc3 Doc5]
          row(count=2) [Doc7 Doc9]
"world" → row(count=4) [Doc2 Doc3 Doc4 Doc8]
```

## Batched async writes and the merger

- **Pending writes / deletes** (`pending_writes.go`) — `Update` does not touch
  the store directly. It buffers additions in `pendingWrites` and removals in
  `pendingDeletes`, keyed by table then keyword. The flush ticker enqueues a
  `flushPendingWritesTask`; a keyword is flushed once it exceeds the batch-size
  threshold or ages past the wait timeout (or unconditionally when closing).
  Deletes (`removeDocumentsFromInvertedIndex`) rewrite or drop affected rows.
- **Keyword merger** (`keywords_merger.go`) — a background goroutine that
  periodically scans the row space and consolidates the many small rows a busy
  keyword accumulates into fewer well-packed rows (`rewriteIndex` /
  `mergeKeywordsIndex`), bounded by time slices so it never blocks for long. It
  waits for pending writes to drain before merging, and after a productive full
  scan it calls `store.ScheduleCompact()` to reclaim space.

## On-disk key types

Configurable via `Options`; a zero field selects the default. Byte 0 (NUL) is
reserved as the "use default" sentinel. Changing a key-type byte after data has
been written is a breaking on-disk change.

| Constant               | Byte | Contents                              |
|------------------------|------|---------------------------------------|
| `DefaultKeyTypeRow`    | 20   | Posting-list rows (term → doc ids)    |
| `DefaultKeyTypeTable`  | 21   | Per-table metadata (`TableInfo`)      |
| `DefaultKeyTypeNextId` | 22   | Next-table-id auto-increment counter  |

## Source map

- **`invertedindex.go`** — the `Index.Search` / `GetDocs` / `Update` API and `SearchResult`.
- **`invertedindex_internal.go`** — pending-write/delete bookkeeping and row rewriting.
- **`storage.go`** — `Index`, `Options`, `New`, `CloseAndWait`, the flush goroutine.
- **`table.go`** — `TableInfo`, `CreateTable`, `DeleteTable`.
- **`pending_writes.go`** — the pending caches and flush logic.
- **`keywords_merger.go`** — the background compactor.
- **`codec.go`** — key/value encoding (the 8-byte doc-id packing lives here).
- **`batch_write.go`** — the batch constructor and `MaxBatchSize`.
