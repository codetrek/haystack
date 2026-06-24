# documents

Package `documents` is the document store for core. It persists document
metadata, keywords, and path information in a `kv.Store`, and optionally keeps a
linked `invertedindex.Index` in sync for full-text search.

It is a standalone library package: it has no dependency on `internal/`.

## Components

- **`storage.go`** — `Store` (the instance-based store) and `New`. Holds the
  `kv.Store`, optional `*invertedindex.Index`, and the resolved key-type prefix
  bytes. `Workspace` holds per-collection metadata (workspace id, inverted-index
  table id, description).
- **`codec.go`** — key/value encoding and decoding. Key encoders are methods on
  `*Store` so they use the store's resolved key-type bytes; value encoders are
  package functions.
- **`document.go`** — the `Document` type and document read/write operations
  (`GetDocument`, save/update/delete).
- **`document_internal.go`** — internal persistence helpers (`saveDocument`).
- **`search.go`** — `GetDocumentPath` and `ScanFiles`.
- **`batch_write.go`** — `MaxBatchSize` and the batch constructor.

## Key-value schema

Keys are prefixed with a single key-type byte. The prefix bytes are configurable
via `Options`; a zero field selects the default. The defaults are:

| Constant                      | Byte | Contents                       |
|-------------------------------|------|--------------------------------|
| `DefaultKeyTypeDocCollection` | 10   | Workspace metadata             |
| _(retired)_                   | 11   | Was document words (see below) |
| `DefaultKeyTypeDocMeta`       | 12   | Document metadata              |
| `DefaultKeyTypeDocPath`       | 13   | Document path information      |

Byte 0 (NUL) is reserved as the "use default" sentinel and cannot be selected as
a custom prefix. Changing a key-type byte after data has been written is a
breaking on-disk change.

Byte 11 (formerly `DefaultKeyTypeDocWords`) is retired in this (data) db:
per-document keywords are now owned by the inverted index's forward map (in the
separate `index` db), not duplicated here. Do not reuse byte 11 in this db
without updating the key-space canary.

Key formats:

- Workspace metadata: `{KeyTypeDocCollection}{workspaceId}`
- Document metadata:   `{KeyTypeDocMeta}{workspaceId}|{docId}`
- Document path:       `{KeyTypeDocPath}{workspaceId}|{docId}`

## Values

- Document metadata is stored as JSON (`Document`).
- Workspace metadata is stored as JSON (`Workspace`).
