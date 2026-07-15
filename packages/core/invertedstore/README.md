# invertedstore — status, measurements & findings

The segment-based (LSM-like) inverted index: the **go-forward** replacement for the pebble-backed
`core/invertedindex`. Full as-built architecture: [`docs/design/invertedstore-design.md`](../../docs/design/invertedstore-design.md).
This README is the durable record of **measured data + key conclusions** (so the next iteration does
not re-derive them); the forward-looking redesign lives in
[`docs/design/invertedstore-luceneization-exploration.md`](../../docs/design/invertedstore-luceneization-exploration.md)
and [`...-implementation-plan.md`](../../docs/design/invertedstore-luceneization-implementation-plan.md).

## Status (2026-06)

**Built and component-complete, but NOT integrated.** The production server runs on the pebble-backed
`invertedindex`; `invertedstore` is a **standalone, deliberately unwired** component. It is held back
because it is **not yet mature at scale** (see "Known scale gaps" below), and — since `core/invertedindex`
is a published package other projects depend on — **no swap seam / interface is added to it** for an
unproven replacement. The integration approach will be designed only once invertedstore is stable. The
component is exercised by its own tests + the `core/cmd/idxbench` A/B harness (a dev-only, uncommitted
tool). Form: single mpsc-worker-owned head buffer → atomically-published immutable sealed segments +
a MANIFEST; size-tiered background merge; single-mutator invariant; lock-free refcounted reader
snapshots.

## Measurements (lx corpus: 94,559 docs; `/workspace` xfs; default config CapBytes 16 MiB / Fanout 4 / L0 snappy, merged zstd)

### Build / steady state (current tree, post C.4 fix)
| metric | value |
|---|---|
| build (AutoMerge on) | **42.6 s** |
| disk (settled) | **234.9 MiB** |
| peak build RSS | **393 MiB** |
| search | ~9.0 ms/q over 198 queries (hits 2,414,505) |
| final live segments | 3 (1×L1 + 2×L2) |

### Store vs pebble `invertedindex` (A/B, same corpus; store re-confirmed this session, pebble from the prior A/B)
| | store | pebble | |
|---|---|---|---|
| build | ~42.6 s | ~64 s | store **~1.5× faster** |
| disk | 234.9 MiB | ~643 MiB | store **~2.7× smaller** |
| peak RSS | 393 MiB | ~610 MiB | store lower |
| search | — | — | store **~4× SLOWER** (the known weak axis: read-amp = scan every live segment) |

### Spill / merge cadence
- A spill fires when the head's byte estimate reaches **CapBytes (16 MiB)** → an L0 segment ≈ **4.6 MiB on
  disk** (snappy, ~3.4× compression). The lx corpus produces **~56–69** L0 spills.
- With AutoMerge on (Fanout 4): **~23 tiered merge passes** collapse the 56 spills to **3** segments — a
  merge roughly every **2–3 spills / ~1.7 s**, run concurrently (off-worker) with ingest. **No covering
  merge fires on a pure-add build** (dead-fraction ≈ 0; covering only triggers at ≥ 0.25 or DeleteTable).

### Fanout / write-amplification sweep (`idxbench -fanout`, write_bytes = real disk writes)
| config | build | disk written (amp) | final segs |
|---|---|---|---|
| AutoMerge **off** (pure build) | 30.1 s | 322 MiB (**~1×**) | 69 (all L0) |
| Fanout **4** (default) | 40.8 s | 725 MiB (**2.25×**) | 3 |
| Fanout **8** | 50.8 s *(run-to-run outlier)* | 766 MiB (2.4×) | 5 |
| Fanout **16** | 35.2 s *(reproduced)* | 540 MiB (**1.7×**) | 8 |

→ Merges are **overlapped** with ingest, so AutoMerge adds modest wall-time (+~10 s vs pure build) but
**~2.25× write amplification**; larger Fanout trades fewer/cheaper merges (less write-amp) for more
residual segments (worse search). The pure-build vs merged disk gap (322→234.9 MiB) is the merge's
recompress (L0 snappy → merged zstd) + dedup.

## Merge strategy (and where it sits vs Lucene / RocksDB)

**Size-tiered** (Cassandra STCS-like), NOT leveled (LevelDB/pebble): a level with ≥ Fanout (4) segments
is k-way merged into ONE next-level segment; segments within a level are full-keyspace overlapping sorted
runs, so a query scans **every** live segment newest→oldest (read-amp = segment count — the source of the
~4× search gap vs pebble's leveled 1-file-per-level). A **covering** merge (all live segments → one,
triggered at dead-fraction ≥ 0.25 or DeleteTable) is the escape hatch that reclaims tombstones/dead-table
keys and collapses read-amp — the analog of Lucene `forceMerge(1)`.

This is the **same family as Lucene's `TieredMergePolicy`** (immutable segments, size-tiered, forceMerge):
deliberately write-optimized (low write-amp, cheap build) trading read-amp — aligned with the priority
order **build ≫ mem > search**. Differences from Lucene: (1) reconciliation is **per-(keyword,docid)
newest-wins** because our docid is a reused-with-new-content external id, not Lucene's append-only
segment-local docid + liveDocs bitset; (2) selection is the crude "whole level ≥ Fanout" (no Lucene
score-based sizing); (3) **no max-segment-size cap** (Lucene's `maxMergedSegmentBytes`); (4) no concurrent
merges; (5) FST term index was measured **slower** than the sorted-keyword dict and is rejected.

## Key findings (corrected ground-truth — respect these in any redesign)

- **docid is a monotonic sequential int64 from idtable (`nextId++`), STABLE-PER-KEY, never recycled.** The
  MD5 is the content/path *key* that maps into the id. Re-indexing the same file reuses the same id with a
  NEW keyword set — so a plain Lucene deleted-docid bitset is insufficient (the live id can't just be
  marked dead).
- **`[I]` keys sort before `[F]`** in a segment, so during a merge a posting is emitted before its doc's
  forward record (its version) is seen → any merge-time version filter needs a **resident** version table,
  not inline resolution.
- **Search resolves deletes INLINE** via the inverted value's `dels` half (newest-wins) and **never reads
  the forward map**. The term-id ordinal coupling (forward stores ordinals into the segment's sorted
  inverted dict) is purely a forward concern and is the source of ~120 lines of merge remap/ordSentinel
  complexity + the "tiered merge can't drop a key" constraint.
- **Merge is streaming across blocks/segments (one decompressed block per cursor) but NOT within a single
  keyword:** a hot keyword's whole posting list is materialized (the reconciliation map + `readExternal`
  reads the whole blob) — in BOTH merge and search. This is the dominant **scale OOM vector**.

## The C.4 regression (a recorded footgun)

`clear()` on a Go map does **not** release its bucket capacity. C.4 (commit 581383a) hoisted the
per-keyword `adds`/`dels` reconciliation maps out of the merge loop and `clear()`+reused them; once a
high-cardinality keyword grew a map, every later small key's `for d := range adds` scanned the retained
(mostly empty) buckets → **O(numKeys × peakBuckets)**, regressing the lx build **6.5× (46→277 s)**. Fix
(977fa05): revert to a **fresh map per key** → build 42.6 s, RSS −19% (the giant reused map no longer
stays resident). Lesson: never `clear()`+reuse a Go map across keys of wildly varying size.

## Known scale gaps (lx is a TEST corpus; real targets are orders of magnitude larger)

The above numbers are on a 234 MiB test corpus; none of these gaps shows there. For a general engine they
are correctness/scalability floors — see the [exploration](../../docs/design/invertedstore-luceneization-exploration.md)
+ [implementation plan](../../docs/design/invertedstore-luceneization-implementation-plan.md):

1. **Hot-keyword OOM** — merge AND search materialize a whole keyword's posting list. Fixes: streaming
   per-keyword reconciliation (bounds the cross-source union, no reindex) → then **chunked/block postings**
   (fully df-independent, in the one reindex).
2. **No max-segment-size cap** — merge grows unbounded; needs `MaxMergedSegmentBytes` with
   newest-contiguous-by-id subset selection (an OLD subset would invert newest-wins).
3. **Deletion is a trade** — per-keyword del-postings, reclaimed only by a full-index covering rewrite at
   25% garbage. A per-doc forward-version tombstone makes deletes O(1) on the write side BUT adds a
   search-time liveness filter; the two cannot both be free.
4. **O(segments) MANIFEST** rewritten on every install + **O(docs) `recomputeLive`** on Open — worsen as
   #2 multiplies segment count.

Roadmap (no large corpus to validate yet): D0 synthetic-stress harness → streaming merge → streaming
search → max-seg cap → ONE StorageVersion reindex (forward/inverted split + per-doc delete + chunked
postings + keyword-range skip).
