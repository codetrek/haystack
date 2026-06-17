# `core/vectorstore` — Architecture

`vectorstore` is a persistent, crash-safe vector store. A record is
`(string id, []float32 vector, payload)`; the store answers metadata-filtered
top-k nearest-neighbor queries over one or more named indexes. It is a peer of
`core/invertedindex` — "the inverted index, for vectors."

This page is the **entrypoint and reading map**. It states the system boundary,
who owns which state, and how the main flows move; each subsystem's detail lives
in a focused page linked below. It describes the system **as built**; the package
doc comment (`doc.go`) is the one-paragraph form.

## System boundary and non-goals

In scope: durable storage of vectors + payload; background-built per-segment HNSW
indexes; N named indexes over one set of vectors (different params/metrics);
metadata-filtered search; O(1) deletes with background space reclamation;
crash recovery.

Reserved, **not built** in v1: IVF-PQ (the `Type` field is reserved); indexes that
cover only a subset of segments (every index covers every segment); OR/NOT/nested
boolean filters; per-index `WaitForIndex(name)`; pruning/sharding beyond ~tens of
millions of vectors; network, distribution, GPU.

The HNSW graph algorithm and its node-store seam are vectorstore's own (per
arXiv:1603.09320): the graph holds only topology and resolves vectors from the
owning segment.

## Shape: one mutable head + immutable sealed segments (LSM)

Records do not live in one growing structure. They live in **one mutable in-memory
`head` segment** (brute-searched, never graphed) plus an **ordered set of immutable
on-disk `sealed` segments** (each owns its vectors and a per-segment HNSW graph). A
write appends to the head and returns; when the head fills (or on `Seal()`) it is
frozen to disk as a sealed segment and a fresh head starts, and a background builder
graphs it. This LSM shape is what makes writes-don't-block, delete + reclamation,
crash-safety, and multi-index/rebuild fall out together — without any per-vector
"is it indexed yet" watermark. See [storage](storage.md) and [indexing](indexing.md).

## Subsystems and ownership

| subsystem | owns | detail |
|---|---|---|
| **Storage** | the vectors at rest: the head, sealed segments, on-disk file layout, the two-level id model, the metric-natural storage form | [storage.md](storage.md) |
| **Indexing & search** | per-segment HNSW graphs, the N named-index model and its `pending\|indexed` state, per-index metric, attribute filtering, and the N-way merge search | [indexing.md](indexing.md) |
| **Durability & recovery** | the head WAL, the atomic manifest, the seal commit order, crash windows, recovery, and the concurrency/locking model | [durability.md](durability.md) |
| **Reclamation** | turning tombstones back into space: the merge/compaction machine and its two policy drivers | [reclamation.md](reclamation.md) |

## State authority (source of truth vs derived)

| state | authority | derived / rebuilt from |
|---|---|---|
| a record's vector + payload | the owning sealed segment's `vectors.dat`/`payload.dat`, or the head | — |
| `string id → docId` | `core/idtable` (in the KV) | also mirrored in-memory (`idToDoc`) from WAL replay |
| `docId → slot` within a segment | the segment's on-disk `slotdoc.dat` | — |
| `docId → owning segId` (global) | **derived** | scanning every segment's `slotdoc.dat` + replaying the head WAL at recovery |
| which segments exist + each `(index, segment)` `pending\|indexed` | the **manifest** | — |
| tombstones (the only mutable segment state) | the segment's `tomb.dat` | — |
| HNSW graphs | `graph-<name>.dat` per (index, segment) | fully rebuildable from the segment's vectors |

The manifest is the structural source of truth; vectors/ids/tombstones are the data
source of truth; everything else (the global `docId→segId` map, the graphs) is
derived and rebuildable. This is why recovery and crash-safety are uniform — see
[durability.md](durability.md).

## Main flows at a glance

- **Write** (`Put`/`Delete`): append to the head WAL (fsync) → mutate the in-memory
  head; a delete sets a tombstone bit in the owning segment. Durable on return.
- **Seal**: dump the head to a sealed segment (fsync) → commit the idtable → atomic
  manifest swap (head → new sealed segment, marked `pending` for every index) → reset
  the WAL → background-build the N graphs. Detail: [durability.md](durability.md).
- **Search** (`Search(index, q, k, filter)`): an N-way merge under a read lock —
  each indexed segment via its HNSW (tombstone-post-filtered), each pending segment
  and the head by brute, all into one top-k heap in docId space. Detail:
  [indexing.md](indexing.md).
- **Reclaim** (`Compact`/background): rewrite live docs from shrunken or
  size-tiered segments into new segments, rebuild their graphs, atomic manifest swap,
  delete the old. Detail: [reclamation.md](reclamation.md).
- **Recover** (`Open`): load the manifest → mmap sealed segments → rebuild the global
  id map → reopen indexed graphs → replay the WAL → resume pending builds → sweep
  orphan dirs. Detail: [durability.md](durability.md).

## Public entrypoints (stable anchors)

`Open(Options{Dir, KV, Metric})` returns a `*Store`. Writes: `Put`, `Get`, `Delete`.
Lifecycle: `Seal`, `Compact`, `WaitForIndex`, `WaitForMerge`, `Close`. Vector
indexes: `CreateVectorIndex`, `DropVectorIndex`, `RebuildVectorIndex`,
`ListVectorIndexes`, `IndexLag`, `Search(index, q, k, filter)`. Attribute indexes:
`CreateAttrIndex`, `DropAttrIndex`. The full signatures live in
[indexing.md](indexing.md#public-api); `Search` returns
`[]SearchResult{DocID int64, Distance float32}` in docId space (mapping docId back to
the caller's string id is the caller's responsibility — idtable has no reverse map).

## Performance posture

Building an HNSW graph is inherently expensive (even optimized C++ is ~0.3–1 ms/vec),
so the head is brute and graphs are built only in the background at seal time;
`maxSegSize` is sized to a head-brute latency budget (~`10M/dim` for ~3 ms). The
build/search hot path is dominated by distance computation, which is partly
memory-latency-bound; vectors are read zero-copy by aliasing the segment mmap, and
the amd64 dot kernel is 8-accumulator AVX2+FMA. See
[indexing.md](indexing.md#performance) for the measured numbers and the
zero-copy/locking interaction.
