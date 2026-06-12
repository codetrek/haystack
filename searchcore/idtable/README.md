# idtable

A document-id allocator. Package `idtable` maps arbitrary byte keys (typically
file paths) to **stable, compact ids** and persists those mappings, so the same
key always resolves to the same id across runs. The inverted-index codec packs
document ids as fixed-width 8-byte strings, and `idtable` is the component that
mints them.

## Responsibility

- Allocate a stable id for each distinct key, reusing the existing id whenever a
  key is seen again.
- Persist the key→id mappings and the monotonic `nextId` counter to a
  `kv.Store`.
- Keep lookups cheap with an in-memory LRU cache and batch persistence on a
  background commit loop.

## Key type: `Allocator`

```go
alloc, err := idtable.New(store, idtable.Options{})
defer alloc.Close()

id, err := alloc.GetId([]byte("main.go")) // path → stable id
```

- **`New(store, opts)`** reads the persisted `nextId` counter from the store
  (defaulting to `1` if none exists), initializes the LRU cache and write batch,
  and starts the background commit goroutine. A negative persisted counter is
  rejected as a malformed database.
- **`GetId(key)`** returns the id for `key` as an **8-byte big-endian string**,
  allocating a new one if the key is unknown. It is safe for concurrent use
  (guarded by an internal mutex). Lookup order: LRU cache → store → allocate new.
  On a new allocation it caches the mapping, increments `nextId`, and buffers
  both the updated counter and the new key→id mapping into the pending batch.
- **`Close()`** stops the background goroutine, flushes any pending batch
  writes, and clears the cache. It is idempotent.

## `Options`

Zero-value fields fall back to defaults:

| Field | Default | Meaning |
|-------|---------|---------|
| `KeyTypeNextId` | `28` | Single-byte KV key prefix for the persisted `nextId` counter. |
| `KeyTypeKey` | `29` | Single-byte KV key prefix for each key→id mapping. |
| `LRUCacheSize` | `200000` | Capacity of the in-memory id cache. |
| `BatchSize` | `100` | Write-batch size handed to `store.NewBatch`. |
| `CommitInterval` | `5s` | How often the background loop flushes the batch. |

The two prefix bytes namespace the allocator's keys within a shared store.
**Byte `0` is reserved** to mean "use the default", so it cannot be chosen as an
explicit prefix. Changing a prefix after data has been written is a breaking
on-disk change: previously allocated ids become unreachable. The defaults match
the historical on-disk layout for compatibility.

## Persistence and durability

Writes are buffered into a `kv.Batch` and flushed in two ways:

1. **Periodically** by a background goroutine every `CommitInterval`.
2. **On `Close`**, which performs a final flush.

This means a freshly allocated id is held in memory (and the LRU cache) and is
durable only after the next batch commit. The mutex is released while `Close`
waits for the background goroutine to exit, because that goroutine's
periodic-commit branch also acquires the mutex.

## Internal LRU cache

`lruCache` is an unexported, fixed-capacity, thread-safe LRU mapping string keys
to `int64` ids, used to avoid redundant store lookups for recently allocated
ids. A capacity of `0` disables caching entirely. Although it has its own lock,
every `Allocator` access to it already happens under the allocator's mutex, so
its lock is effectively uncontended within this package.

## Id encoding note

Internally ids are `int64` values produced from the monotonic `nextId` counter;
`GetId` returns each as an 8-byte big-endian string. This fixed-width string form
is exactly what the `invertedindex` codec expects when it packs document ids
into posting lists.
