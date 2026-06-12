# kv

The storage abstraction at the base of the `searchcore` module. Package `kv`
defines the `Store` and `Batch` interfaces that every other `searchcore`
package (`invertedindex`, `documents`, `collection`, `idtable`) is written
against, so the search core never depends on a concrete key-value engine. The
bundled implementation lives in the sub-package `kv/pebblekv`, backed by
[Pebble](https://github.com/cockroachdb/pebble).

## Responsibility

- Define a small, engine-agnostic key-value contract (`Store`) plus an atomic
  write-batch contract (`Batch`).
- Let callers inject a single shared store into the whole `searchcore` stack
  while remaining free to supply an alternative backend.
- Ship one production-ready backend, `pebblekv`, that satisfies both
  interfaces.

## `kv.Store`

A flat, byte-keyed store. All operations return an error (rather than panic)
when the store has been closed.

| Method | Purpose |
|--------|---------|
| `Get(key) ([]byte, error)` | Point read. Returns `(nil, nil)` for a missing key. |
| `Put(key, value) error` | Point write. |
| `Delete(key) error` | Point delete. |
| `NewBatch(maxBatchSize int32) Batch` | Create a write batch (see below). |
| `Scan(prefix, cb) error` | Iterate every key with `prefix`, calling `cb`. |
| `ScanRange(begin, end, cb) error` | Iterate every key in `[begin, end)` (end exclusive). |
| `GetIncrementalId(key) (int, error)` | Monotonically increasing counter keyed by `key`. |
| `ScheduleCompact()` | Hint the backend to compact. |
| `Close() error` / `IsClosed() bool` | Lifecycle. |

### Iteration and slice lifetime

The `key` and `value` slices handed to a `Scan`/`ScanRange` callback are only
valid for the duration of that call. Callers that need to retain them past the
callback must copy them. Returning `false` from the callback stops the scan.

### Why `GetIncrementalId` and `ScheduleCompact` are on the interface

These two methods exist so `Store` is a drop-in replacement for the original
Pebble wrapper that `invertedindex` and `collection` were built against:

- `GetIncrementalId` backs id allocation. It returns the value currently stored
  at `key` and then writes back that value plus one, so the first call for a key
  returns `0` and stores `1`, and subsequent calls return `1, 2, 3, ...`.
  Alternative backends must preserve these monotonic semantics.
- `ScheduleCompact` lets the keyword merger trigger a compaction after large
  rewrites. A backend with no native compaction concept may implement it as a
  no-op.

## `kv.Batch`

A buffer of write operations applied atomically on `Commit`.

| Method | Purpose |
|--------|---------|
| `Put(key, value) error` / `Delete(key) error` | Buffer a write/delete. |
| `DeleteRange(start, end) error` | Buffer a range delete. |
| `DeletePrefix(prefix) error` | Buffer a delete of all keys under `prefix`. |
| `Commit() error` | Apply all buffered operations atomically. |
| `Reset()` | Discard buffered operations and reuse the batch. |
| `Close() error` | Release batch resources. |
| `Count() int32` | Number of buffered operations. |

`Close` must be called on a batch that is discarded without committing.
`Commit` performs its own cleanup, so calling `Close` after a successful
`Commit` is a safe no-op; failing to `Close` an uncommitted batch may leak
underlying resources.

## `kv/pebblekv` — the Pebble-backed implementation

`pebblekv` implements both interfaces:

- `PebbleDB` implements `kv.Store`.
- `PebbleBatch` implements `kv.Batch`.

### Opening a store

```go
store, err := pebblekv.Open("/path/to/data", 16<<20) // path, cache size in bytes
defer store.Close()
```

`Open` resolves the path to an absolute path and opens a Pebble database with a
fixed set of tuned options, including: a block cache sized by `cacheSize`, a WAL
min-sync interval of 500µs, `MaxOpenFiles` of 8192, a 4MB write buffer, L0
compaction thresholds, a bloom filter policy, and a cap of 2 concurrent
compactions. It returns a `kv.Store`.

### Behavioral details specific to pebblekv

- **Sync writes.** `Put` and `Delete` use Pebble's `Sync` write option, and
  `Commit` commits with `Sync`, so writes are durable on return.
- **Copying reads.** `Get` returns a copy of the value (the underlying Pebble
  slice may be invalidated after read) and maps `pebble.ErrNotFound` to
  `(nil, nil)`.
- **Non-copying scans.** As required by the interface, `Scan`/`ScanRange` pass
  the iterator's live key/value slices to the callback without copying, for
  performance. `Scan` bounds the iterator by `[prefix, prefix+0xff)` and also
  stops once a key no longer has the prefix.
- **`ScheduleCompact`** runs `Compact([]byte{0}, []byte{0xff})` in a background
  goroutine (and is a no-op if the store is already closed).
- **`GetIncrementalId`** stores the counter as the decimal-string form of the
  integer; it returns `-1` plus an error if the store is closed or the stored
  value cannot be parsed.

### `PebbleBatch` auto-commit

`NewBatch(maxBatchSize)` returns a batch that, when `maxBatchSize > 0`,
auto-commits and resets once its operation count reaches `maxBatchSize`. Pass
`0` to disable the limit and commit manually. `DeletePrefix` is implemented as a
single underlying range delete over `[prefix, prefix+0xFF)`, so it advances the
auto-commit counter by exactly one.

## Concurrency and lifecycle

`PebbleDB` tracks its closed state atomically; every method short-circuits with
an error once the store is closed. Within the `searchcore` stack a single shared
store is injected across all packages, and writes are serialized through the
shared `queue`; see the module-level `searchcore/README.md` for the recommended
open/compose/close ordering.
