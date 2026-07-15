# Reclamation — turning tombstones back into space

> Subsystem page under [architecture.md](architecture.md). Owns the merge/compaction
> machine and its two policy drivers. It rewrites live records from
> [storage](storage.md) into new segments and rebuilds their [indexes](indexing.md);
> the manifest swap that commits a reclamation is described in
> [durability.md](durability.md).

## One machine, two drivers

Deletes are O(1) tombstone bits; space is reclaimed asynchronously. There is **one
mechanism** — write new segment files, build their N graphs, atomically swap the
manifest, delete the old dirs — fed by two policy drivers (`mergeConfig`):

| driver | trigger | which segments | purpose |
|---|---|---|---|
| **delete-driven** | a segment's live ratio `live/count` < `MergeFloor` (~50%) | the shrunken segments, bin-packed into ~`maxSegSize` buckets | reclaim tombstones, refill segments, drop the count |
| **growth** (size-tiered) | a size tier accumulates ≥ `K` (~8–10) segments | same-tier segments, merged up a tier | bound total segment count as the store grows |

**Single-segment compaction is the degenerate "merge of one"** — used for a
high-tombstone segment with no merge partner. Graphs cannot be merged trivially, so a
merge **rebuilds the HNSW (×N indexes)** over the surviving vectors; a shrunken segment
is cheap to merge precisely because it has few live docs. `maxMergedSize` (~1M) caps
top-tier growth, keeping the live segment count to a few dozen so the N-way search
([indexing.md](indexing.md#search-the-n-way-merge)) stays cheap.

## Why size-tiered, not leveled

Leveled compaction (RocksDB-style) earns its keep from key-range pruning, which a
vector query — which must search *all* segments — cannot use. So the store bounds the
segment **count** rather than maintaining a leveled key order. This is the same reason
search has no range pruning.

## Effect on the id model

A merge fuses inputs into a **new** `segId`, so it edits the global `docId→segId` map,
but only for the docs it moved. A single-segment compaction keeps `segId` fixed (it
bumps `gen`) and rewrites only that segment's `slot→docId` and graphs — the global map
is untouched. This is the payoff of the two-level id model
([storage.md](storage.md#the-two-level-id-model)): slot churn is isolated to a segment
unless docs actually cross a segment boundary.

## Crash safety and concurrency

A reclamation is "write new + atomic manifest swap + delete old," so a crash before
the swap leaves the new dirs unreferenced (swept at recovery) and a crash after leaves
the old dirs unreferenced (swept) — see
[durability.md](durability.md#recovery-and-crash-windows). The swap holds
`buildMu + s.mu` and re-validates that every input is indexed in **every** index before
unmapping it, so it never frees an mmap a still-pending builder is reading
([durability.md](durability.md#concurrency-and-locking)). A merge that finds an input
re-pended mid-flight aborts cleanly and is re-planned later.

## Tunables

Measured, not asserted (`mergeConfig`): `maxSegSize` (adaptive, see
[indexing.md](indexing.md#performance)), fanout `K` ~8–10, `maxMergedSize` ~1M,
`MergeFloor` ~50%, target live segment count ~dozens.

## Scale ceiling (reserved)

Beyond ~tens of millions of vectors, both "search all segments" and "merge huge
segments" become painful at once; that is the regime for IVF candidate-segment pruning
or sharding, reserved and not built in v1.
