# Indexing & search

> Subsystem page under [architecture.md](architecture.md). Owns per-segment HNSW
> graphs, the N named-index model and its `pending|indexed` state, per-index metric,
> attribute filtering, and the N-way merge search. State authority for graph files;
> the records they index are owned by [storage](storage.md).

## One graph per (index, segment)

A **named vector index** is `name → vindex{cfg, metric, graphs}`, where `graphs` maps
`segId → builtIndex`. All indexes share the **one** set of records-segments — records
are sealed and merged exactly once, never copied per index. Each index owns one HNSW
graph per sealed segment, stored as `graph-<name>.dat` in the shared segment dir.

The `(index, segment)` state is encoded **structurally** by presence in `graphs`:

- **absent key ⇒ PENDING** — no graph yet; served by the same brute fallback the head
  uses.
- **present key ⇒ INDEXED** — served by the segment's HNSW graph.

This one mechanism unifies three cases that would otherwise each need bespoke logic:
the un-graphed head, a freshly sealed segment whose build is still running, and a
brand-new index with no graphs. A new index is **born pending for every segment**
(immediately queryable via brute); a background builder fills its graphs, flipping
each pending→indexed. No per-vector watermark is needed.

A store is born with a single index named **`"default"`** carrying the store's primary
metric; with only `"default"` present, every path below reduces to a single-index
engine byte-identically.

| operation | cost |
|---|---|
| seal the head | build N graphs (one per index) for the new segment, background |
| compact / merge a segment | rebuild that segment's N graphs |
| `CreateVectorIndex` | mark the index pending for all segments → queryable at once → builder converges it |
| `DropVectorIndex` | delete only that index's `graph-<name>.dat` files; records and other indexes untouched |
| `RebuildVectorIndex` | clear + delete + respawn that index's builds (param/metric change or torn-graph repair from records) |

## Per-index metric

An index whose `Metric` differs from the store's primary metric does **not** get a
second copy of the vectors. Both when **building** its graph and at **search** time it
reconstructs raw from the segment's stored form + norm (`primaryMetric.restore`,
~1e-7) and re-prepares under its own metric. The build wraps the segment's node store
in a reconstruction seam so a node and its neighbors are compared in one consistent
space; every brute distance leg at search does the same reconstruction. The
same-metric path skips reconstruction. (Storage form: [storage.md](storage.md#metric-natural-storage-form-one-near-full-precision-copy).)

## Attribute indexes (filtering)

Each segment stores, in `attr.dat`, a `value → in-segment-slot bitmap` for every
**declared** indexed property (`CreateAttrIndex(property, kind)`, `kind ∈ {Keyword,
Numeric}`). The declared set is mirrored in the manifest and is the runtime authority;
every seal/merge rebuilds `attr.dat` for it, so a property's bitmap is rewritten with
its segment and reclamation cleans it up for free. v1 uses a dense per-segment bitset
(not roaring): within a segment of ≤ `maxSegSize` slots, a flat membership test beats
roaring on the per-candidate traversal gate and adds no dependency.

## Search: the N-way merge

`Search(index, q, k, filter)` runs under `s.mu.RLock()` (see
[durability.md](durability.md#concurrency-and-locking)). There is **no key-range
pruning** — vectors have no query-relevant order, so every k-NN query consults every
segment; segment count is what's bounded ([reclamation.md](reclamation.md)), giving
bounded read amplification.

```
vx = indexes[index]
for each sealed segment seg:
    vx.graphs[seg] present (INDEXED):  HNSW search → post-filter by seg's tombstone bitmap
    else (PENDING):                    brute over seg's live, filter-matching slots
head:                                  brute over live, filter-matching slots
merge every leg → one shared top-k heap in docId space → attach payload
```

Every leg produces **exact distances under the index's metric**, so all legs feed one
comparable size-k heap. The graph is immutable and can return a tombstoned node, so an
indexed leg **post-filters** by the segment's tombstone bitmap and over-fetches by the
tombstone count so deletes don't collapse recall.

**Filtering is per-segment and adaptive.** With a `filter`, each segment evaluates the
predicate to a member set `S_seg`:

- `|S_seg| ≤ attrSearchT` (default 512) → **brute-S**: exact scan of just the matching
  slots;
- `|S_seg| > attrSearchT` → **graph ∩ S**: HNSW filter-*during*-traversal — the member
  predicate gates which neighbors are admitted to the result heap while traversal still
  expands *through* non-members so the graph stays connected (a post-filter would sever
  connectivity and under-return for a selective filter).

The member set is always AND-ed with the segment's live bits, so deletes never leak. v1
predicates are conjunctions of `Eq`/range over declared attributes; OR/NOT/nested are
reserved.

## Public API

```go
type VectorIndexConfig struct {
    Type           string // "hnsw" (v1); "ivfpq" reserved
    Metric         Metric // Cosine | DotProduct | Euclidean
    M, EfConstruction, EfSearch int // default 16 / 200 / 64 when zero
}
func (s *Store) CreateVectorIndex(name string, cfg VectorIndexConfig) error // born pending; brute-queryable at once
func (s *Store) DropVectorIndex(name string) error
func (s *Store) RebuildVectorIndex(name string) error
func (s *Store) ListVectorIndexes() []VectorIndexInfo
func (s *Store) IndexLag(name string) IndexLagInfo                          // segments/vectors still pending
func (s *Store) Search(index string, q []float32, k int, filter Predicate) ([]SearchResult, error)
func (s *Store) CreateAttrIndex(property string, kind AttrKind) error       // kind ∈ {Keyword, Numeric}
func (s *Store) DropAttrIndex(property string) error
func (s *Store) WaitForIndex() error                                        // block until all builds quiesce (tests/strong consistency)
```

`Search` returns `[]SearchResult{DocID int64, Distance float32}` in docId space.
`WaitForIndex` drains all in-flight builds (it is not per-index — a reserved gap).

## Performance

Measured on random vectors, cosine, M=16, efC=200, single-thread, AMD EPYC 7763:

- **Build is inherently expensive** — even optimized C++ single-threaded is ~0.3–1
  ms/vec; parallelism buys only ~2.5–4.6× (graph contention). Hence the head is brute
  and graphs build in the background at seal time.
- **`maxSegSize` is adaptive** to a head-brute budget: brute ≈ `N·dim·0.3ns`, so a ~3
  ms budget gives `maxSegSize ≈ 10M/dim` (~78k @128, ~39k @256, ~26k @384), clamped to
  `[~16k, ~128k]`.
- **The hot path is distance computation**, partly memory-latency-bound (traversal
  loads vectors from scattered mmap locations). Vectors are read **zero-copy** by
  aliasing the segment mmap (`getVectorRef`), which is safe because `Search` holds
  `s.mu.RLock()` for its whole traversal and the merge swap that unmaps a segment holds
  the write lock — so a search can never alias a freed mmap. The amd64 dot kernel is an
  8-accumulator AVX2+FMA routine. The remaining gap to a C++ library (~2–3.7×, narrowing
  with dimension) is inherent Go-vs-C++ plus that memory latency, not redundant work.
