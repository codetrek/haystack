# Design exploration — "Lucene-izing" invertedstore (forward/inverted split, per-doc deletion, max-seg cap, per-segment bloom)

Status: EXPLORATION (pre-spec). NOT a spec, NOT approved. Produced by a 15-agent ground-truth +
adversarial-review workflow; every current-system claim is cited to the real code, and the
adversarial pass corrected several errors (recorded inline). Purpose: lay out the design space,
the honest tradeoffs, and the OPEN DECISIONS for the maintainer to rule on before any spec.

## 0. The honest framing — read this first

**None of the four pillars improves the bulk-build benchmark (priority #1).** The lx/linux build
(94,559 docs / ~41.4M postings, write-once, NO deletes, NO edits) is exactly the path that exercises
none of these features. On that path:

- P1 (forward/inverted split): build-neutral; likely a small **disk regression** (#3 priority) if the
  forward stores keyword strings.
- P2 (per-doc version delete): in its aggressive form it **taxes the build** (+bytes on all 41.4M
  postings for a feature the build never uses) → a NEGATIVE on #1.
- P3 (max-seg cap): a 5 GiB cap **never fires** at 234 MiB → completely inert on the current bench.
- P4 (per-segment bloom): **adds** build CPU + resident RAM (#2) for ~0 search gain at today's
  single-digit segment count.

So this entire redesign is a **steady-state / incremental-update / large-index investment**, not a
build-speed win. The build (already 42.6s, beats pebble) is not what it improves. It targets the
**delete / re-index / many-segment** path — which **we have not measured yet** (idxbench has no
delete/re-index workload). That gap is the single most important thing to fix before committing.

The direction (become Lucene/RocksDB-like for steady state) is sound and well-precedented. But the
engineering discipline this repo demands (measure at the source, Principle 2) says: **quantify the
current model's actual incremental-update pain before rearchitecting for it.**

## 1. Ground-truth — corrected facts the design must respect

The adversarial pass corrected several beliefs (mine included). The accurate picture:

- **docid is a monotonic SEQUENTIAL int64 from idtable (`nextId++`, starts at 1), STABLE-PER-KEY —
  NOT MD5-derived and NOT recycled.** The MD5 is the *content/path key* that maps INTO the id; the id
  itself is a counter. Re-indexing the SAME file returns the SAME id with a NEW keyword set; ids are
  never freed and handed to a different key. So the correct property is **"stable per key, never
  recycled"**, not "reused." This matters: a plain Lucene deleted-docid bitset is insufficient not
  because ids recycle, but because **re-index reuses the same id with new content** (can't just mark
  the id dead). Dense + monotonic ⇒ a roaring bitmap / version table is feasible.
  (`core/idtable/idtable.go:69,92,186-187`)
- **`[I]` (0x01) sorts BEFORE `[F]` (0x02) within a segment.** Consequence for merge: every inverted
  posting is streamed and emitted BEFORE its doc's forward record (and thus its version) is seen in the
  same merge. So "derive currentVersion(docid) by streaming forwards first" is **FALSE** at merge time
  — a merge-time version filter needs a RESIDENT version table (rebuilt on Open), not inline
  resolution. (`keys.go:9-11`, `merge.go:226`)
- **Search NEVER reads the forward map.** Deletes are resolved entirely inline as tombstones in the
  INVERTED value's `dels` half, via newest-wins over the inverted postings. So a per-segment skip's
  "tombstone resurrection" risk depends on the **inverted** tombstone representation, not the forward.
  (`search.go:146-155`)
- **Skips ALREADY exist** (so "no skip today" is wrong): within a segment, `scanPrefix` binary-searches
  the per-segment block index and decompresses only blocks overlapping the `[I]` prefix; on the FORWARD
  read path, whole segments are skipped by the persisted `[MinDocid,MaxDocid]` range (`coversDocid`).
  What's missing is a per-segment skip for the KEYWORD/search path. (`segment.go:379-404`,
  `dictcache.go:214-218`)
- **Search is a PREFIX scan** (`strings.HasPrefix(kw, q)`), not exact match — and the index-side
  tokenizer deliberately prefix-dedups (drops a keyword that is a prefix of another in the same doc), so
  prefix semantics are REQUIRED, not incidental. A standard keyword bloom answers EXACT membership and
  therefore can only serve the exact path. An exact entry point already exists: `GetDocs(tableId,key)`.
  (`search.go:112,129,178-266`, `core/tokenizer/ascii_tokenizer.go:55-64`)
- **The term-id ordinal coupling** (forward stores ordinals into the segment's sorted inverted dict) is
  the source of merge.go's remap/`[][]uint32`/`ordSentinel`/self-heal (~120 lines) AND the "tiered
  merge cannot drop a key" constraint. Decoupling forward removes all of it from the inverted side.
  (`head.go:228-254`, `merge.go:166-336`)
- **The §3/spike numbers** (build ~22s, disk 241 MiB, search ~1180µs) are from the **sortbench spike**,
  not production (spike keys carry no tableId; deltas are int32 not int64). Don't quote them as prod.
- **Crash story:** today one atomic MANIFEST rename installs everything; `server.go` already opens
  THREE independent durable stores. Splitting forward/inverted must preserve the single-atomic-install
  property or accept a torn-state window.

## 2. Pillar P1 — split forward and inverted storage

**Current:** both `[I]` and `[F]` live in one segment keyspace, co-merged, coupled by term-id ordinals.
**Target:** two segment families. **Inverted** = `keyword → postings` only — no dict region, no
ordinals, no remap/ordSentinel; the inverted merge becomes a pure string-keyed newest-wins k-way merge
that **can drop a key** freely. **Forward** = `docid → keyword-set`, self-resolving.

**Open decisions:**
- **D1.1 — MANIFEST:** one shared MANIFEST (add a `Kind` field to segMeta) **[rec]** vs two manifests
  vs two independent stores. Shared keeps the single-atomic-install crash story (the strongest current
  property); two manifests open a torn-state window.
- **D1.2 — forward value encoding:** **B1 strings** (full decouple, no dict region, simplest; measured
  **+78 MiB disk, 319 vs 241** on the spike — a #3-priority regression) vs **B2 forward-owned term dict**
  (disk parity but relocates the ordinal complexity into the forward store — a trap) vs **B3 docid→version
  only** (smallest, but couples to P2). Rec: B1 as default IF P1 lands standalone; B3 if P2 lands first.
- **D1.3 — keep the full keyword SET in forward?** Needed TODAY by the delete fan-out + edit-diff +
  `recomputeLive`. Rec: keep it for a standalone P1 (correctness-neutral split); let P2 shrink it later.

**Tradeoff:** removes ~120 lines of merge complexity + unblocks free key-drop / continuous reclaim, at a
**disk cost** (B1) and **doubled per-spill fsync + live-handle bookkeeping** (two families) on the
slow-disk target the design optimizes for. **Priority impact:** build-neutral, **disk-negative** (#3),
maintainability-positive. NOT a current-bench win.

## 3. Pillar P2 — per-doc deletion (collapse the per-keyword fan-out)

**Current:** delete/re-index writes per-keyword del-postings (fan-out via the forward keyword set);
tombstones linger through every tiered merge and are physically reclaimed **only by covering** (auto at
dead-fraction ≥ 0.25 = a full-index rewrite). **Target:** record a delete/re-index ONCE per doc.

**The crux (corrected):** docid is stable-per-key but re-index reuses it with new content, and `[I]<[F]`
means a posting streams before its version is known in a merge. So two honest forms:

- **(a) full per-posting version tags** — live iff `posting.version == currentVersion(docid)`, every
  merge drops stale postings (continuous reclaim). REJECTED as the default: taxes all 41.4M postings on
  the write-once build (#1 NEGATIVE), breaks the pure sort+dedup delta-varint posting layout, and needs a
  **resident version table** (rebuilt on Open) because the version isn't known when a posting streams.
- **(b) delete-only collapse [rec]** — keep today's del-postings for re-index; add a per-doc
  **forward-version tombstone** for the DELETE path only. Collapses delete fan-out to **O(1)** without
  taxing every posting; minimal blast radius (forward-value extension + FormatVersion bump to 4, no
  posting re-encode, no reindex).

**Open decisions:**
- **D2.1 — form (a) full versioning vs (b) delete-only collapse vs (c) status quo.** Rec: **(b)** first.
- **D2.2 — where currentVersion(docid) lives** for any merge-time staleness check: a **resident per-table
  version table rebuilt on Open** (rec, reuses `recomputeLive`) vs a two-pass merge (breaks the
  one-block-per-cursor bound) vs search-time only (no continuous reclaim → defeats half the point).
- **D2.3 — version counter home + crash replay:** per-doc logical counter (read-before-write,
  replay-fragile) vs a store-wide monotonic seal-order sequence. Must survive the volatile-head replay.

**Missed-risk corrections:** GetDocs (exact path) also needs the liveness check, not just Search;
`liveByTable`/deadFraction accounting must stay consistent under version-staleness; crash/replay
double-bump must be prevented. **Priority impact:** the **delete fan-out collapse (b) is the one clean
near-win** here — small, build-neutral, real for the update path. Full versioning (a) is build-negative.

## 4. Pillar P3 — max merged-segment-size cap

**Current:** no cap; tiered collapses a whole level, covering collapses ALL live → trends to one
unbounded segment. **Target:** `Options.MaxMergedSegmentBytes` (default high, e.g. 5 GiB; 0 = uncapped);
tiered selection becomes a **size-bounded subset** instead of "whole level"; a level whose smallest
Fanout members already exceed the cap is **settled** (never re-merged).

**The central correctness constraint (corrected — was understated):** a merged output always gets a
**fresh highest seal id**, and Search/ForwardDocids resolve newest-wins by **global id descending**.
So a size-bounded subset MUST be the **NEWEST contiguous-by-id run** of a level — merging an OLD subset
would give old content the newest id and **invert newest-wins** (resurrect superseded postings). This
is the load-bearing rule the greedy-oldest-first recommendation got backwards.

**Open decisions:**
- **D3.1 — default cap value / on-by-default:** 5 GiB (never fires at current scale, floor=1 below it)
  **[rec]** vs a lower value to exercise the floor vs 0/opt-in. (Decision lacks in-repo evidence of real
  index sizes — a gap.)
- **D3.2 — covering also capped?** Keep covering **UNCAPPED [rec]** until P2 provides continuous reclaim
  (covering is the ONLY path that reclaims dels today; capping it before P2 splits dangling garbage
  across groups). Once P2 lands, covering's whole-index sweep largely disappears and this is moot.
- **D3.3 — subset selection:** greedy **newest-contiguous-by-id** (corrected; preserves newest-wins) +
  conservative `Sum(input Size)` output estimate (cheap, metadata-only, never surprises a >cap output;
  may under-pack harmlessly at high cap).
- **D3.4 — "settled level" definition + livelock guard:** a size-capped selector can leave a level
  permanently holding ≥ Fanout segments that individually sum over the cap → `pickLowestQualifyingLevel`
  must not re-select a settled level forever.

**Priority impact:** **inert at current scale** (5 GiB never fires on 234 MiB) — adds an option,
selection logic, and a livelock surface for ZERO current-bench movement. The win is purely at multi-GB
scale (bounded merge wall-time / write-amp / encode-RSS; stop re-merging settled bulk) and over long
update sessions. Honest: do not pitch as a build/mem win on the existing bench.

## 5. Pillar P4 — per-segment bloom (FST excluded per measured perf)

**Target:** a per-segment bloom over the segment's distinct keywords so Search can skip segments lacking
the term (and instantly answer absent/rare-term and AND-with-rare-term queries, avoiding the wasted
block-decompress on a miss). FST is EXCLUDED (measured slower than sorted-keyword). Build it for free at
spill/merge (both already iterate the sorted keyword set); ~10 bits/key, ~1% FPP; persist in a new
segment region (footer magic bump `SRSEG\x00\x01` + bloomOff/params; old magic ⇒ no bloom ⇒ scan).

**The load-bearing problem (corrected):** Search is a **PREFIX** scan; a keyword bloom answers **EXACT**
membership. So a plain bloom can ONLY serve the exact path (`GetDocs`), not prefix Search — the prefix
semantics are required (the tokenizer prefix-dedups). Options: an exact-membership bloom wired to a
`GetDocs`-style fast path (narrow benefit; needs the ENGINE to call the exact API — cross-module blast
radius) vs a prefix/gram bloom (10–30× bigger, blows the mem budget) vs **answer exact membership from
the already-persisted term-dict** (no new structure — the dict is already a sorted keyword set;
membership is a binary search). The last makes the bloom possibly **redundant**.

**Open decisions:** D4.1 exact-vs-prefix-vs-gram + which entry point; D4.2 in-segment region vs sidecar
(rec in-segment, self-describing); D4.3 resident vs mmap (rec resident, small); D4.4 **ship now vs gate
behind P3** (rec **defer** — at single-digit segments the bloom rejects ~nothing; it only pays off once
P3 creates many segments → **bloom + cap are a pair**); D4.5 `BloomBitsPerKey` knob, persisted per-seg.

**Tombstone-resurrection safety:** the bloom MUST include del-only (fully-tombstoned) keywords a tiered
segment keeps, or a skip could resurrect a deleted docid. **Priority impact:** does NOT help build (#1,
adds cost), ADDS resident RAM (#2), and is NOT the fix for today's 4×-vs-pebble search gap (which is
result-build/decompress, not segment-skip across 3 segments). A scale feature, paired with P3.

## 6. Sequencing & compat

**Dependency order:** P1 (split) is the enabler — it removes the term-id coupling so the inverted merge
can drop keys / reclaim continuously, which P2 needs; P3 (cap) deliberately creates many segments, which
P4 (bloom) exists to keep searchable; P2 changes covering's role, which P3's covering-cap decision waits
on. So: **P1 → P2(b) → P3 → P4**, with P4 gated on P3 actually producing many segments.

**Compat / migration:**
- Splitting one keyspace into two families is an **on-disk format change** (the `[I]<[F]` interleave
  disappears) → needs a StorageVersion decision; likely **not** no-reindex.
- **Bundle byte-format changes** (P2 forward extension, P4 bloom region) into a **single StorageVersion
  bump** so a user reindexes at most once (reindex re-tokenizes the whole corpus on the user's machine —
  disruptive; minimize the count). Push pure-metadata/merge-policy changes (P3 selection, any
  MANIFEST-resident skip) through **in-place FormatVersion upgrades** (precedent: `upgradeSegmentRanges`).
- **No mixed-format readers** (rec) — keep the clean reindex model; a derived cache doesn't need rolling
  upgrade.
- Open: does a reindex need to coordinate idtable docid allocation; remove vs document the dead
  `manifest.StorageVersion` field.

**What does NOT change (keep):** sorted-keyword term dict (FST rejected), snappy-L0/zstd-merged codecs,
the plan→compute→install pipeline, the single-mutator worker, the block index + `coversDocid` skips.

## 7. Recommendation & the decision the maintainer must make

**The honest bottom line:** this is a sound Lucene/RocksDB-ization of the **steady-state/update/scale**
path, but **none of it improves the build benchmark** we've been optimizing, and **we have not measured
the current model's actual incremental-update cost** at all (idxbench is build-only). Committing to a
4-pillar core rearchitecture on Lucene-analogy intuition, without measuring our own delete/re-index pain,
violates the repo's measure-at-the-source principle.

**Recommended path (in order):**
1. **MEASURE FIRST (spike).** Add a delete/re-index workload to idxbench (re-index N% of docs, delete M%)
   and measure the CURRENT model's real cost: del-posting write-amp, tombstone bloat on search, how often
   covering (full-index rewrite) fires and what it costs. This quantifies each pillar's actual payoff and
   replaces intuition with numbers — and it's harness-only (no product code, no SDD).
2. **The one near-pure win regardless: P2(b) — delete fan-out collapse** (per-doc forward-version
   tombstone for the DELETE path). Small, build-neutral, real for the update path, minimal blast radius.
3. **P1 (split)** if the measured maintenance/merge complexity + future-pillar enablement justify the
   disk/crash cost — it's the structural enabler but also the biggest change.
4. **P3 (cap) + P4 (bloom)** only once indexes are demonstrably large enough that the read-amp floor and
   unbounded merges actually bite — paired, scale-justified.

**Top open decisions for you (each blocks a spec):** D1.1 (shared vs split MANIFEST), D1.2 (forward
strings vs version-only — couples P1↔P2), D2.1 (delete-only collapse vs full versioning), D3.1 (cap
value / real target index size), D4.1 (exact bloom vs prefix/gram vs answer-from-dict).

**Biggest unresolved tension:** P1.B1 (forward strings) regresses disk (#3); P1.B3 (version-only) is
smallest but forces P2 first. The split's value and the deletion redesign are entangled — decide D1.2 and
D2.1 together.

## 8. SCALE REFRAME (supersedes §0's "current-bench" framing) + merge memory at scale

**Correction to §0's framing.** This is a GENERAL-PURPOSE index engine. `lx` (234 MiB) is a *test
corpus*, not the target — real deployments are **orders of magnitude larger**. So the earlier "inert at
current scale / not a current-bench win" framing is the WRONG lens: it is right only in the narrow sense
that *the toy bench cannot validate these features*, not that they are optional. **The scale pillars are
REQUIRED for the real target, not deferrable niceties.** The correct engineering statement is: *we lack a
representative-scale corpus to measure them, so the next measurement must be at real scale, not on lx.*
P3 (cap), bounded merge, and skip/bloom move from "deferred" to "mandatory for a general engine."

**Is merge done in memory? (OOM analysis.)** Merge is streaming on TWO axes — one decompressed block per
source cursor, and streamed output blocks — so **segment SIZE alone does not OOM**. But three terms are
NOT bounded by streaming and become OOM vectors at orders-of-magnitude scale:

1. **A single keyword's posting list is materialized WHOLE — the biggest risk.** A cursor reads one
   record's value via `readExternal` (the entire posting blob, `merge.go` cursor / `segment.go:117`), and
   the inverted reconciliation loads that keyword's docids across all K sources into the `adds`/`dels`
   maps (O(df)). A HOT keyword (df in the tens of millions) ⇒ hundreds of MB per source × K + the merged
   set ⇒ **GBs for one keyword**. This hits **both merge AND search** (search also decodes whole posting
   lists). Posting lists are stored as one delta-varint blob (externally chunked for storage but decoded
   whole), so the data model itself assumes a keyword's postings fit in RAM.
2. **`remap [][]uint32`** = Σ(per-source term counts); merging the whole index uncapped ⇒ O(all
   keyword-occurrences) ⇒ GB-scale.
3. The largest single external value, read whole.

**Fixes, by leverage (all REQUIRED-track for a general engine, not optional):**
- **P3 max-seg cap** bounds per-segment size ⇒ bounds `remap`, bounds the largest single merge, bounds
  blast radius. Mandatory at scale. (Does NOT bound a hot keyword's *total* df across segments — that's
  orthogonal, see next.)
- **Streaming per-keyword reconciliation** — replace the materialized `adds`/`dels` map with a k-way
  merge of the per-source SORTED docid streams (emit in sorted order, newest-wins, no whole-set
  materialization). Bounds merge memory to **O(K cursors)** regardless of df. This is "Option C" from the
  C.4 discussion, now strongly motivated by OOM-at-scale (not just alloc churn).
- **Block-based / chunked postings with skip data (Lucene-style)** — chunk the posting list itself so
  neither merge nor search ever materializes a whole hot keyword's list. The deep, correct fix for a
  general engine; larger change. Without it, a hot keyword's *unioned* df (across all live segments at
  search/merge) is unbounded even WITH a per-segment cap.

**Also scale-fragile (flagged for later):** the single-JSON MANIFEST rewritten on every install grows
with segment count; `recomputeLive`/`liveByTable` on Open scans all forwards O(docs); resident
dict-LRU/blooms/version-table scale with index size. A general engine must bound all of these.

**Revised priority read:** for the real (orders-of-magnitude-larger) target, the merge-memory bound
(streaming reconciliation + chunked postings) and P3 (cap) are **load-bearing correctness/scalability
requirements**, not optional perf. The "measure first" recommendation stands but must be done on a
**representative large corpus**, not lx.
