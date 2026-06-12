# `internal/core/vectorindex`

A self-contained approximate-nearest-neighbor (ANN) subsystem: an HNSW
(Hierarchical Navigable Small World) graph plus a memory-mapped persistence layer
(`MmapStore`).

> **Not on the live query path.** Per the
> [architecture overview](../../../docs/architecture.md), this subsystem is
> **not** wired into the running server's index/search path. Nothing under
> `internal/server`, `internal/client`, or the rest of `internal/core` imports
> it. Within the repository it is exercised only by the `cmd/insert-analysis`
> and `cmd/gen-testdata` tools. It is documented as a present, self-contained
> subsystem rather than as a stage of the live data flow.

## Responsibility

Provide an embeddable vector index that can insert document vectors, search for
the *k* nearest neighbors of a query vector, and delete documents, with durable
on-disk persistence and crash recovery. The graph algorithm and the storage
backend are cleanly separated by the `NodeStore` interface.

## Components

### HNSW graph (`hnsw.go`)

`HNSWIndex` implements Algorithms 1–5 from arXiv:1603.09320 over a pluggable
`NodeStore`. It is constructed with `NewHNSWIndex(store, distance, opts...)` and
configured via functional options: `WithM`, `WithEfConstruction`, `WithEfSearch`,
`WithRand`, and `WithCosineDistance` (which enables precomputed-norm
optimizations). Defaults: `DefaultM = 16`, `DefaultMmax0 = 32`,
`DefaultEfConstruction = 200`, `DefaultEfSearch = 64`.

Operations (serialized by an internal `sync.RWMutex`):

- `Insert(docId, vector)` — upserts (deletes any existing node for `docId`
  first), allocates a node id, assigns a random level, and links the node into
  the graph using the neighbor-selection heuristic (Algorithm 4). When the store
  implements `BatchableStore`, the insert is wrapped in a batch.
- `InsertBatch([]InsertItem)` — inserts many items under a single outer batch.
- `Search(query, k)` — greedy descent through the upper layers, then a beam
  search at layer 0 with `ef = max(efSearch, k)`, returning up to `k`
  `SearchResult` values sorted by distance.
- `Delete(docId)` — removes a node, repairs its neighbors' adjacency, and
  reassigns the entry point if needed.

### Vector types and distances (`types.go`, `distance.go`)

- `Node`, `Vector` (`[]float32`), and `SearchResult{ID, DocID, Distance}`.
  Note that `Search` currently populates `ID` and `Distance`; `DocID` is part of
  the result struct but is not filled in by the graph itself.
- `DistanceFunc` and built-ins `CosineDistance`, `CosineDistanceWithNorms`,
  `EuclideanDistance`, `DotProductDistance`, all using SIMD-accelerated
  primitives from `github.com/viterin/vek/vek32`.

### Node store interface (`store.go`)

`NodeStore` is the persistence contract for the graph: vector get/put
(`GetVector` returns a copy, `GetVectorRef` may return a no-copy reference),
neighbor lists per layer, entry point, node level, doc↔node id mapping, node-id
allocation, and precomputed L2 norms. `BatchableStore` is an optional interface
adding `BeginBatch` / `CommitBatch` / `DiscardBatch` / `BatchDepth`. The file
also contains little-endian encode/decode helpers.

### `MmapStore` — production store (`mmap_store*.go`, `mmap_format.go`, `mmap_wal.go`, `mmap*.go`)

`MmapStore` is the production `NodeStore`, backed by several memory-mapped flat
files plus a write-ahead log. Opened with
`OpenMmapStore(dir, MmapStoreOptions{Dim, M, CheckpointInterval})`.

- **Files (`mmap_format.go`):** `meta.bin` (64-byte `MetaHeader`), `vectors.dat`,
  `nodes.dat` (16-byte slots: level, flags, norm, upper-slot pointer),
  `graph_l0.dat` (layer-0 neighbor lists), `graph_upper.dat` (upper-layer
  neighbor lists), `idmap.dat` (doc↔node id mappings, CRC-protected), and
  `wal.bin`. Headers are page-aligned (4 KiB) and the package asserts a
  little-endian platform.
- **Write-ahead log (`mmap_wal.go`):** an append-only, CRC32-checked `WAL` with
  five record types (`WalInsert`, `WalDelete`, `WalSetNeighbors`, `WalSetEntry`,
  `WalSetNorm`). All mutations write the WAL first, then the mmap regions.
- **Crash recovery:** on open, `MmapStore` loads `idmap.dat`, replays WAL records
  after the checkpoint LSN to reconstruct mmap data and meta, then checkpoints.
- **Checkpointing:** a checkpoint msyncs the mmap regions, atomically rewrites
  `meta.bin` (recording the WAL LSN), truncates the WAL, and compacts
  `idmap.dat`. Checkpoints fire automatically every `CheckpointInterval` ops
  (default 1000) and on `Close`.
- **Sync modes:** `SyncImmediate` (default; WAL fsync + msync on commit) and
  `SyncDeferred` (WAL flush only — the caller must call `Sync()` later), set via
  `SetSyncMode`. Deferred mode speeds up bulk inserts.
- **Capacity growth (`mmap_store_grow.go`):** data files grow and are re-mmapped
  on demand as node ids exceed current capacity.
- **Concurrency:** all writes are serialized by `muWrite`; reads use
  fine-grained `RWMutex`es per region (`muVec`, `muGraph`, `muNodes`, `muDoc`).
- **Platform mmap (`mmap_unix.go`, `mmap_windows.go`, `mmap.go`):** thin
  per-platform `mmap`/`munmap`/`msync` wrappers behind a common interface.

### `MemNodeStore` — in-memory store (`mem_store.go`)

A simple map-backed `NodeStore` used for tests and benchmarks. It does not
implement `BatchableStore`.

## Relationships

- **Self-contained:** depends only on the standard library and
  `github.com/viterin/vek/vek32`. It imports nothing under `internal/` and is
  imported by nothing in the live server.
- The earlier `PebbleNodeStore` implementation has been removed; `MmapStore` is
  its replacement. This vector index is entirely separate from the
  Pebble-backed full-text/inverted-index path (`internal/core/storage` +
  `searchcore`).
