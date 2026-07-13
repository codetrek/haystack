# Storage — records, segments, ids

> Subsystem page under [architecture.md](architecture.md). Owns the vectors at
> rest: the head, sealed segments, the on-disk layout, the two-level id model, and
> the metric-natural storage form. State authority for vector/payload data and the
> per-segment `docId↔slot` mapping.

## The head and sealed segments

A record lives in exactly one of:

- the **head** — a single mutable in-memory segment. It is brute-searched, has no
  graph, and absorbs all writes. It is backed for durability by the head WAL
  (see [durability.md](durability.md)).
- a **sealed segment** — immutable and mmap-backed. It owns its vectors and, per
  named index, an HNSW graph. The **only** mutable part of a sealed segment is its
  tombstone bitmap.

When the head reaches `maxSegSize` rows (default 50 000, adaptive to a latency
budget) or on an explicit `Seal()`, it is frozen into a sealed segment and a fresh
head starts. A record is therefore in the head, or in one sealed segment — never
duplicated.

## On-disk layout

One store is one directory:

```
<dir>/
  manifest                    structural source of truth (see durability.md)
  records.wal                 head write-ahead log (see durability.md)
  seg-<id>-<gen>/             one dir per sealed segment; gen = compaction generation
    vectors.dat               slot → stored-form vector + norm (metric-natural, below)
    slotdoc.dat               slot → docId  (on-disk source of truth for the id mapping)
    tomb.dat                  tombstone bitmap (the only mutable file; msync'd on delete)
    payload.dat               slot → structured payload
    attr.dat                  declared attribute indexes (see indexing.md)
    graph-<name>.dat          one HNSW graph per named index (see indexing.md)
```

Each `.dat` file opens with a page-aligned header (magic + dims + counts); the body
is page-aligned so a vector can be read by zero-copy mmap aliasing (see
[indexing.md](indexing.md#performance)). The directory name encodes `(segId, gen)`
— paths are derived from it, never stored in the manifest, so a sealed segment is
self-describing: given its directory you can reconstruct everything except the global
cross-segment maps.

## The two-level id model

A record is named three ways, at decreasing stability:

```
caller id (string) ──idtable──▶ docId (int64) ──docToSeg──▶ segId ──slotdoc──▶ slot
   user identity              stable logical id          which segment        physical row
                            (survives compaction/merge)                      (changes on rewrite)
```

- **`docId`** (from `core/idtable`) is the stable internal id; every internal
  reference uses it and it never changes across compaction or merge.
- **`segId`** names the owning segment. The global `docId → segId` map is **derived**
  in-memory state (rebuilt at recovery by scanning each segment's `slotdoc.dat` plus
  the WAL); the head uses a sentinel `headSegID`.
- **`slot`** is the row within a segment; each segment owns its `docId↔slot` mapping,
  whose authority is its on-disk `slotdoc.dat`.

The two-map split (global `docId→segId` + per-segment `docId↔slot`, rather than one
flat `docId→(segId,slot)`) is what **isolates a segment's slot reordering**. A
same-segment compaction keeps `segId` fixed and rewrites only that segment's
`slot→docId` and graph — the global map is untouched. Only a **merge** (which fuses
inputs into a new `segId`) edits the global map, and only for the docs it moved. See
[reclamation.md](reclamation.md).

`Search` returns results in **docId space**; mapping a docId back to the caller's
string id is the caller's responsibility (idtable keeps no reverse map). The in-memory
`string→docId` map (`idToDoc`, rebuilt from WAL replay) lets reads resolve a string id
without allocating.

## Metric-natural storage form: one near-full-precision copy

The vector is stored **once**, in the records segment, in the **primary metric's
natural form**. Indexes never store a second copy; they hold only topology and
reconstruct any other representation on demand.

- **Cosine** stores the **unit vector + its norm `|v|`**. Distance is then
  `1 − dot(unit_a, unit_b)` — no per-distance division, components scaled to O(1).
- **DotProduct / Euclidean** store the **raw vector** (their natural form).
- **Raw on demand**: a non-primary-metric index, `Get`, or a future IVF-PQ
  reconstructs raw as `unit · norm`. The measured L2 relative error is ~1e-7 (float32
  noise), so `unit + norm` is itself a near-full-precision copy. Storing raw under
  cosine was measured ~11% slower and underflows tiny vectors with no consumer needing
  the extra bit-exactness, so it is deliberately not done.

`Metric` (the stable type) is `Cosine | DotProduct | Euclidean`. `Metric.prepare`
converts an input to stored form; `Metric.restore` reconstructs raw from
`(stored, norm)`; `Metric.distance` compares two stored-form vectors. The store's
primary metric is fixed at `Open` and recorded in the manifest; an index may declare a
different metric (see [indexing.md](indexing.md#per-index-metric)).
