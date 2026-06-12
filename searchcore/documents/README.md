# documents

Package `documents` is the per-collection document store for searchcore. It
persists document metadata, keywords, and path information in a `kv.Store`, and
optionally keeps a linked `invertedindex.Index` in sync so that **saving a
document also indexes its words** (no separate indexing call is required).

Writes are asynchronous: every mutation is serialized through a shared
`queue.Queue` (MPSC), so concurrent callers are safe and writes are applied in
order.

It is a standalone library package: it has no dependency on `internal/`.

## Place in the layering

```
collection      registry + lifecycle (composes this Store)
  documents       ← this package: per-collection document storage; Save auto-indexes
    invertedindex   low-level posting-list engine (composed and notified on every mutation)
```

`documents` is the middle layer. It is normally reached through a
`collection.Collection` handle, but can be used directly. The composed
`invertedindex.Index` is optional: pass `nil` for `idx` (e.g. in tests or
non-indexed configurations) and the index-update calls become no-ops.

## Key types

- **`Store`** (`storage.go`) — the instance-based store, constructed with
  `New(store, queue, idx, opts)`. Holds the `kv.Store`, the shared
  `queue.Queue`, the optional `*invertedindex.Index`, the resolved key-type
  prefix bytes, an in-memory per-collection document counter, and a cache of
  collection metadata. Collection lifecycle: `Create(collectionID, desc)`
  provisions a collection (allocating its inverted-index table via the index
  seam), `Delete(collectionID)` purges it. `CloseAndWait` flushes pending queue
  work and releases in-memory state.
- **`Document`** (`document.go`) — an indexed source file: caller-supplied `ID`
  (the key suffix; not persisted in the value, repopulated on read), `RelPath`,
  `Size`, `Hash`, `ModifiedTime`, `LastSyncTime`, and the `Words` / `PathWords`
  keyword slices. `Words` and `PathWords` are stored separately, not in the JSON
  metadata value.
- **`CollectionInfo`** (`storage.go`) — per-collection metadata persisted in the
  store and cached after first lookup: `CollectionID`, the `InvertedId` (its
  inverted-index table id), `Desc`, and `CreateAt`. The JSON tags are stable
  on-disk keys (note `workspace_id` / `inverted_id` for legacy compatibility)
  and must not change. Returned by `GetCollection`.
- **`Options`** (`storage.go`) — selects the four on-disk key-type prefix bytes;
  zero values select the defaults.

## Document operations

All write operations are submitted to the shared queue and run single-threaded:

- **`SaveNewDocuments(collectionID, docs)`** persists a batch of new documents
  (metadata, words, path) and indexes their words into the inverted index, then
  bumps the in-memory document counter. (`collection.Collection.Save` delegates
  here.)
- **`UpdateDocuments(collectionID, docs)`** re-saves existing documents and
  updates the inverted index with the diff between the old and new word sets.
- **`DeleteDocument(collectionID, docId)`** removes a document's metadata,
  words, and path entry and de-indexes its words.
- **`GetDocument(collectionID, docId, includeWords)`** reads a document;
  `includeWords` controls whether the `Words` slice is populated. Returns
  `(nil, nil)` if absent.
- **`GetDocumentPath` / `ScanFiles`** (`search.go`) — look up one document's
  relative path, or iterate over every `(docid, relPath)` in a collection.
- **`CountByCollection(collectionID)`** returns the document count in O(1) from
  the in-memory counter.

The index seam (`indexCreateTable` / `indexDeleteTable` / `indexDocument` in
`storage.go`) is a narrow internal boundary that isolates inverted-index
notification, so a second index type could be added additively without changing
the public `Save` API.

## Key-value schema

Keys are prefixed with a single key-type byte. The prefix bytes are configurable
via `Options`; a zero field selects the default. Byte 0 (NUL) is reserved as the
"use default" sentinel and cannot be selected as a custom prefix. Changing a
key-type byte after data has been written is a breaking on-disk change.

| Constant                      | Byte | Contents                  |
|-------------------------------|------|---------------------------|
| `DefaultKeyTypeDocCollection` | 10   | Collection metadata       |
| `DefaultKeyTypeDocWords`      | 11   | Document words            |
| `DefaultKeyTypeDocMeta`       | 12   | Document metadata         |
| `DefaultKeyTypeDocPath`       | 13   | Document path information  |

Key formats (see `codec.go`):

- Collection metadata: `{KeyTypeDocCollection}{collectionId}`
- Document metadata:   `{KeyTypeDocMeta}{collectionId}|{docId}`
- Document words:      `{KeyTypeDocWords}{collectionId}|{docId}`
- Document path:       `{KeyTypeDocPath}{collectionId}|{docId}`

## Values

- Document metadata is stored as JSON (`Document`).
- Document words are stored as a pipe-separated (`|`) string.
- Document path is stored as the raw relative-path string.
- Collection metadata is stored as JSON (`CollectionInfo`).
