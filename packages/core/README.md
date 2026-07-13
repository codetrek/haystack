# core

A reusable, embeddable **search core** for Go — full-text inverted indexing, document storage,
multi-collection management, and content query matching. Extracted from
[haystack](https://github.com/codetrek/haystack) so it can be used standalone.

Zero dependency on haystack internals. Backed by [Pebble](https://github.com/cockroachdb/pebble)
out of the box, or any store implementing the small `kv.Store` interface.

## Layering

```
collection   registry + lifecycle (named collections of documents)   ← top-level entry
  documents  per-collection document storage (metadata/words/path); Save auto-indexes
    invertedindex   low-level posting-list engine (term → doc ids)
engine       content query engine (compile query → collect candidates → line matching)
─────────────────────────────────────────────────────────────────────────────
kv (+ pebblekv impl) · queue (MPSC) · tokenizer (CJK/camel/snake) · idtable (doc-id allocator)
```

Each layer is instance-based, composes the layer below, and can be used on its own.

## Install

```
go get github.com/codetrek/haystack/packages/core
```

Requires Go 1.23+.

## Usage

```go
import (
    "github.com/codetrek/haystack/packages/core/kv/pebblekv"
    "github.com/codetrek/haystack/packages/core/queue"
    "github.com/codetrek/haystack/packages/core/idtable"
    "github.com/codetrek/haystack/packages/core/invertedindex"
    "github.com/codetrek/haystack/packages/core/documents"
    "github.com/codetrek/haystack/packages/core/collection"
    "github.com/codetrek/haystack/packages/core/engine"
)

// 1. Open a store (or supply your own kv.Store implementation).
store, _ := pebblekv.Open("/path/to/data", 16<<20)
defer store.Close()

// 2. A shared async write queue.
q := queue.NewMpsc("writes")
q.Start()
defer q.Stop()

// 2b. Mint a stable 8-byte document id from idtable (required by the
//     inverted-index codec which packs ids as fixed-width 8-byte strings).
alloc, _ := idtable.New(store, idtable.Options{})
defer alloc.Close()
docID, _ := alloc.GetId([]byte("main.go")) // path → stable 8-byte id

// 3. Compose the stack (one shared inverted index instance).
idx, _ := invertedindex.New(store, q, invertedindex.Options{})
docs, _ := documents.New(store, q, idx, documents.Options{})
cat, _ := collection.New(store, docs, collection.Options{})

// 4. Create a collection and add documents (Save auto-indexes).
col, _ := cat.Create("my-project")
col.Save([]*documents.Document{
    {ID: docID, RelPath: "main.go", Words: []string{"hello", "world", "main"}},
})

// 5. Query its content.
eng := engine.New(idx, docs, col.ID(), engine.Options{
    MaxWildcardLength:  24,
    MaxKeywordDistance: 32,
})
_ = eng.Compile("hello world", false /* caseSensitive */)
result, _ := eng.CollectDocuments() // candidate doc ids from the inverted index
for docID := range result.DocIds {
    // fetch the file content yourself, then per line:
    //   matches := eng.IsLineMatch(line)  // [][]int match ranges for highlighting
    _ = docID
}
```

## Packages

| Package | Purpose |
|---------|---------|
| `kv` | `Store` / `Batch` key-value interfaces |
| `kv/pebblekv` | Pebble-backed `kv.Store` (`Open`) |
| `queue` | MPSC async task queue + `Queue` injection interface |
| `tokenizer` | tokenization (ASCII, CJK, camelCase/snake_case, stopwords) |
| `idtable` | key → stable compact int64 id allocator |
| `invertedindex` | low-level posting-list engine (`Index`) |
| `documents` | per-collection document storage (`Store`), composes an index |
| `collection` | collection registry + lifecycle (`Catalog`/`Collection`), composes documents |
| `engine` | content query engine (`Engine`: Compile / CollectDocuments / IsLineMatch) |

## Notes

- **Pluggable backend.** Anything implementing `kv.Store` works; `pebblekv` is the bundled default.
  Inject one shared store + one shared write queue across the stack.
- **On-disk key namespace.** Each package's `Options` exposes its key-type prefix byte(s)
  (defaults: collection 1-2, documents 10-13, inverted index 20-22, idtable 28-29). Override them
  to coexist with other data in a shared store; the defaults are stable for on-disk compatibility.
  Byte `0` is reserved as the "use default" sentinel.
- **Concurrency.** All types are safe for concurrent use; document/index writes are serialized
  through the injected queue. At shutdown call in order: `idx.CloseAndWait()` → `docs.CloseAndWait()` → `store.Close()` (and `q.Stop()`).
  Skipping `docs.CloseAndWait()` risks unflushed queue work.
- **Forward-compatible.** `documents` keeps the document→index update behind a narrow internal seam,
  so additional index types (e.g. a vector index) can be added without changing the `Save` API.
