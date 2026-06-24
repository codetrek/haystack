# invertedstore — Covering-Merge Trigger Fix (Spec)

Status: **proposal / for review**. Scope: a perf-correctness fix to ONE mechanism in the
already-built `core/invertedstore` — the covering-merge trigger. It does not change the
on-disk format's semantics, the merge/search logic, or the public API. It replaces an
O(spills × bottom-level-size) full-decompression scan with an O(#segments) metadata
computation, removing a build-time pathology measured below.

Related: [invertedstore-design.md](invertedstore-design.md) §6 (the merger) and §8
(term-id). This spec refines the §6 covering-merge **trigger** only.

---

## 1. Problem

On a cold bulk build of the linux corpus (94,559 docs / 41.4M postings), the store never
finished in a reasonable time — a 25,000-doc prefix already took **61.7s**, as slow as
pebble building the *entire* corpus (65s). The cost is super-linear in corpus size, so the
full build ran for **6+ minutes** without completing.

### 1.1 Root cause (measured, not inferred)

A CPU profile of the build (`idxbench -impl=store -maxdocs=25000 -buildprofile`) attributes
the time decisively:

| function | cum CPU | share |
|---|---|---|
| `(*Store).bottomDeadFraction` | 53.18s | **73%** |
| └ `maps.(*Iter).Next` / `mapIterStart` / `matchFull` | ~50s | (map build+iterate) |
| `(*Store).mergeSegments` (the actual useful merge) | 1.64s | 2% |
| `(*Store).applyBatch` (the actual indexing) | 6.07s | 8% |
| `(*Store).spill` | 2.72s | 4% |

`maybeMerge` runs after **every spill** (enqueued on the merge loop) and always finishes
with `maybeCoveringMerge → bottomDeadFraction()`. `bottomDeadFraction` **decompresses the
entire bottom level** and, per `[I]` keyword, builds a `map[int64]bool` + `map[int64]int`
and ranges them twice (tally + clear). As the bottom level grows via tiered merges, this
full scan is repeated over a larger and larger level — hence the super-linear cost.

### 1.2 Why it is pure waste on a clean build

On a cold build there are no deletes and no re-posts, so every `(keyword, docid)` pair is
written exactly once. In `bottomDeadFraction`'s tally that means `count[d] == 1` and
`latest[d] == add` for every pair, so `dead += count - survivors == 0`. The dead fraction
is **structurally 0** for the whole build — the covering merge it gates **never fires** —
yet the scan that proves "0" runs after every spill over the whole bottom level.

### 1.3 Confirmation

Short-circuiting `maybeCoveringMerge` to `return nil` (diagnostic only, reverted) dropped
the same 25,000-doc build from **61.7s → 8.2s (7.5×)** with **identical disk** (33.3 MiB) —
---

## 2. Goal & non-goals

**Goal.** Make the covering-merge *trigger* cheap: replace `bottomDeadFraction`'s
full-decompression scan with an O(#segments) metadata computation backed by two running
counters, so the trigger costs microseconds regardless of corpus size and is **0 on a clean
build**. Restore build to spike-level (~30s on linux), with disk/search/correctness
unchanged.

**Non-goals (explicitly out of scope for this spec).**
- The covering merge's *reclamation logic* (`coveringMerge` / `mergeSegments`) — unchanged.
- Search latency / the per-`(keyword)` map churn in `Search` — a separate axis, separate
  spec. This fix does not touch `search.go`.
- The per-`(keyword,docid)` map reconciliation inside `mergeSegments` (a deliberate
  correctness choice for add→del→add collapse; measured at 2% — not the bottleneck).
- The tiered-merge policy (`mergeOneLevel`), fanout, codecs, head cap.

## 3. Why the covering merge must stay (the trigger, not the feature, is the bug)

The covering merge is the store's **only** path that reclaims accumulated garbage, so it
cannot simply be removed:

- Segments are immutable; a delete writes a **tombstone** (a `del` posting), an update
  re-posts (new adds + tombstones for dropped keywords). These accumulate.
- A **tiered merge cannot drop a keyword key**: the term-id forward map references keywords
  by ordinal, and the merge's per-source `remap` append-index *is* the source ordinal (§8),
  so dropping a key would shift every later ordinal and corrupt the forward map. Tombstones
  are therefore carried through verbatim as del-only records — garbage only grows.
- The **covering merge** compacts the whole live set into one segment and **rebuilds the
  term dict from scratch** (ordinals reassigned), which is the only point at which
  fully-tombstoned keys, dangling tombstones, forward-tombstones, and dead-table keys can
  actually be physically dropped.

So this fix keeps the covering merge and its threshold semantics; it only changes **how the
"is there enough garbage to be worth it?" question is answered** — from an exact, expensive,
per-spill scan to a cheap metadata estimate.

---

## 4. Design

> **v2 (post-review).** Round-1 review found the v1 "global persisted `livePostings`
> scalar" to be crash-unsafe (a per-table spill persists a head-inclusive global count
> that is inconsistent with the per-segment `Σ Postings` it ships with → indexer replay
> double-counts → covering merge pinned off forever) and `DeleteTable`-leaky. v2 keeps the
> `1 − live/written` ratio (verified by review as the *exact* covering-merge reclaim
> fraction) but makes the two terms robust: `written` is **per-segment metadata** (exact,
> travels in the MANIFEST), and `live` is **never a persisted free-floating scalar** — it is
> recomputed on `Open` from the segments, maintained incrementally **per table** during a run
> (so `DeleteTable` drops its contribution in O(1)), and never touched by the merge path.

The dead fraction is `clamp₀(1 − live / written)` where:

### 4.1 `written` — exact per-segment metadata

Add `Postings int64` to `segMeta` (manifest.go): the number of **inverted posting entries
(adds + dels)** the segment stores. It is **caller-counted** where the counts are already in
hand and written into the `segMeta` at its existing construction site — NOT routed through
`segWriter`/`finish` (review: `addEntry` is key-type-blind and cannot recover an entry's
add/del count from its opaque value):

- **spill** (head.go, the `for _, t := range terms` loop, ~lines 121–126): accumulate
  `len(adds) + len(dels)` into a local `postings`, then set `sm.Postings = postings` at the
  `segMeta{…}` construction (~line 162).
- **mergeSegments** (merge.go, the inverted branch ~lines 287–289, which already holds
  `addList`/`delList`): accumulate `len(addList) + len(delList)` **only on the emitted (`keep`)
  path** — i.e. inside the `if keep {` block right where `w.addEntry` is called, so a key the
  covering merge drops (`keep == false`, fully-tombstoned / dead-table) contributes 0 — then set
  `res.sm.Postings` at the `segMeta{…}` construction (~line 314). (In a covering merge `delList`
  is empty, so this naturally yields adds-only — the segment's live count.)

Forward (`[F]`) records are not counted (the fraction is about inverted-posting reclamation).
`written := Σ segMeta.Postings` over **all live segments** (the covering merge's actual input
set) — an O(#segments) sum over MANIFEST metadata, no I/O. `Postings` is per-segment, so it
is **crash-consistent by construction**: it ships in the same MANIFEST as the segment it
describes; there is no global scalar that can outlive its segments.

### 4.2 `live` — distinct live pairs, per-table, segment-anchored (never a persisted scalar)

`live` = number of **live, distinct `(keyword, docid)` pairs** in the index = Σ over live docs
of their current distinct keyword count. It is tracked **per table** — `s.liveByTable
map[int]int64` — so a `DeleteTable` drops its whole contribution in O(1) with no rescan (this
is what makes `DeleteTable` correct; see below). The global `live` used by §4.3 is
`Σ_t liveByTable[t]`. It is **not stored in the MANIFEST**; it is anchored to the segments so
crash recovery cannot inflate it:

1. **On `Open` — recompute exactly, per table, via the SHARED newest-wins resolver.** The
   recompute MUST reuse `reconcile.go`'s existing newest-wins forward resolution rather than a
   parallel hand-rolled scan (round-2: a second scan would drift from what `Search`/
   `ForwardDocids` see on tombstones, ordering, and catalog gating). `ForwardDocids`'s `decided`
   map is **per-table** (docids are not globally unique across tables, reconcile.go:40), so the
   shared core is **per-table**: factor it into `forEachLiveForward(tableId int, includeHead
   bool, visit func(docid int64, ords []uint32, deleted bool) (keepGoing bool))` (round-3:
   surface `ords` — `decodeForward` already returns them, reconcile.go:90 just discards them —
   and thread the `keepGoing` bool so `ForwardDocids`'s early-stop contract / `TestForwardDocids_
   EarlyStop` is preserved). Then:
   - `ForwardDocids(tableId, fn)` = wrapper: `includeHead=true`, `visit` yields the docid when
     `!deleted` and forwards `fn`'s bool. (Signature unchanged; it has zero non-test callers, so
     the refactor risk is contained to the in-package tests, whose behavior body-extraction
     preserves.)
   - The Open recompute loops the catalog: `for tid := range s.man.Tables {
     forEachLiveForward(tid, false /*head empty on Open*/, …) }`, accumulating
     `liveByTable[tid] += distinct(ords)` for each `!deleted` record. **The catalog gate is
     realized by iterating `s.man.Tables`** — NOT a per-record `s.man.Tables` lookup in one
     global pass (that would share one `decided` map across tables and let table A's docid
     suppress table B's). A dropped-but-unmerged table's `[F]` records are simply never visited.
   - On `Open` the head is empty (`s.head` has no entries before any write, store.go:155), so
     `includeHead=false` is correct; the recompute runs **after** `publishSnapshotLocked()`
     (store.go:165) so the resolver acquires the published segment snapshot.
   - **Distinct ords:** `encodeForward` sorts but does NOT dedup (keys.go), and the forward
     stores the raw `op.keywords` ords (head.go `setForward`), so a doc indexed with duplicate
     keywords yields duplicate ords; dedup the sorted ords so the count matches the inverted
     index, which dedups via `addPosting`. (Within one segment `kw2ord` is a *bijection* over
     the distinct keyword set, and a merge remaps ords injectively, so `distinct(ords)` equals
     the doc's distinct-keyword count in any segment's ord-space — an implicit dependency on
     `kw2ord` being built from the distinct keyword set, which spill guarantees.)
   - **Cost:** this decompresses every segment's `[F]` data blocks (the forward region — one
     record per live doc), NOT the bulk `[I]` blocks or the dict region. It is bounded by
     forward-region size, **measured and reported in §9** (not asserted here as a fixed number).
2. **During a run — maintain incrementally.** In `applyBatch`, **inside the existing
   `s.mu.Lock()` window** (update.go ~122–156, so the §4.3 RLock read is race-free), using the
   distinct sets already in hand:
   - delete (`op.keywords` empty): `liveByTable[t] -= len(dedup(old))`
   - add / re-post: `liveByTable[t] += len(newSet) - len(dedup(old))`

   `newSet` is the dedup map `applyBatch` already builds (update.go ~140–143); `old` (from the
   forward read or in-batch state) may carry caller duplicates (the forward stores raw ords),
   so it MUST be deduped — `len(old)` is not the distinct count. A re-post of an unchanged set
   nets exactly 0 (head also nets 0 via `addPosting` dedup). In-batch repeats use the same
   `old` the head logic uses (update.go ~115), so multiple ops on one docid don't double count.
   `liveByTable` is a **plain arithmetic counter** (`map[int]int64`, missing key reads as 0,
   initialized `map[int]int64{}` in `Open` alongside `s.head`): it does NOT depend on
   `CreateTable` seeding a key or on the write path validating the catalog (`applyBatch` lazily
   heads any tableId — update.go:123). The Open recompute's catalog gate is the **authoritative**
   definition of the live set; the running counter is corrected to it at the next `Open`, so a
   write to an un-created / already-deleted table can at worst transiently mis-count and is
   reconciled on reopen.
3. **`DeleteTable(t)` — drop the partition.** Under the lock, `delete(s.liveByTable, t)`. The
   table's segments are then reclaimed by the covering merge `DeleteTable` force-schedules, so
   `live` and `written` lose the table's pairs together (the non-crash transient where `written`
   still has the table's segments only *raises* `deadFraction`, harmless — a covering merge is
   pending). The **crash-in-window** case (crash after the catalog/MANIFEST write but before that
   merge installs) is handled by §6 (catalog-gated recompute + Open re-scheduling the covering
   merge for any segment that covers an absent table). **No covering-merge reseat is needed**: a
   covering merge *preserves* live pairs **on a consistent index** (it only drops dead postings),
   so `liveByTable` is correct across one with no adjustment; the one exception is the merge's
   self-heal path (merge.go ~221–242), which can drop a forward ord ONLY for a pre-existing
   inverted/forward inconsistency in the input — that bounded delta is reconciled at the next
   `Open` recompute, not by the running counter. A tiered merge and a spill leave `liveByTable`
   unchanged. The merge path touches only `written` (via `segMeta.Postings`), never `liveByTable`.

> Rejected sub-alternative: a single global `livePostings` reseated from a covering merge's
> output. The covering merge compacts only *segments*, not the head, so its output count omits
> head-resident live pairs — reseating to it would under-count by a head's worth. The per-table
> incremental counter (head-inclusive, partitioned for `DeleteTable`) avoids that entirely.

### 4.3 `deadFraction()`

`bottomDeadFraction()` (its sole caller is `maybeCoveringMerge`, merge.go:534; no test
references it) is replaced by:

```go
func (s *Store) deadFraction() float64 {
    s.mu.RLock()
    var written int64
    for _, sm := range s.man.Segments {
        written += sm.Postings
    }
    var live int64
    for t, n := range s.liveByTable {
        if _, ok := s.man.Tables[t]; ok { // catalog-gate the running sum too (round-3 R3-1)
            live += n                      // so a stale post-DeleteTable partition can't bias the trigger
        }
    }
    s.mu.RUnlock()
    if written <= 0 {
        return 0
    }
    d := 1 - float64(live)/float64(written)
    if d < 0 {
        d = 0 // head-resident live pairs (≤ the 16 MiB cap) can exceed sealed `written`
    }
    return d
}
```

The running sum is **catalog-gated** to match the Open recompute exactly: a write to a table
after its `DeleteTable` (the only in-run divergence — every reader/writer of `liveByTable`
runs on the single worker, so `deadFraction` only ever observes whole-task states) re-creates
`liveByTable[t]` for a non-catalog `t`, which this gate excludes. Without the gate it would only
*lower* the fraction (additive to `live`, never reducing `written`) → a safe under-trigger that
the next Open discards anyway; the gate erases even that cosmetic transient for ~O(#tables) cost.

The trigger is otherwise unchanged: fire a covering merge when `deadFraction() >=
coveringDeadThreshold` and there are `>= 2` segments. The computation is now O(#segments)
integer work, so it stays on the every-spill path with **no throttling**.

**Scope note (intentional change, not semantics-preserving).** The old `bottomDeadFraction`
measured only the *bottom level*; the new `deadFraction` measures **all live segments** —
which is exactly what a covering merge compacts, so it is the more correct denominator. But
the `coveringDeadThreshold` (currently 0.25) was tuned against the bottom-only distribution;
it is **revalidated by measurement** (§8) against the new global ratio rather than asserted
unchanged. The constant is the single tuning knob.

---

## 5. Correctness of the estimate

`1 − live/written` is the **exact** fraction of written inverted postings a covering merge
would reclaim (round-1 review verified this against `mergeSegments(covering=true)`: the
covering output is exactly the live adds, so `written − live` = everything it drops). It
drives only *when* to compact; the covering merge itself stays exact, so an imprecise
estimate can only make one fire early or late, never corrupt data.

- **Cold build.** Every posting is live and sealed → `live ≈ written` → fraction ≈ 0 → never
  fires; the check is a metadata sum. Pathology removed.
- **Delete.** Writes a tombstone (`written += del`) and `live −= len(dedup(old))`; a
  double-delete reads an already-tombstoned forward → `old` empty → no double decrement.
- **Update / re-post.** Overlapping-keyword re-post leaves the *old* adds in their old
  segment (`written` keeps them) while `live` counts each pair once → the stale copies count
  as dead (the case a tombstone-only proxy misses). An *unchanged* re-post nets `live += 0`
  (matching the head's `addPosting` dedup no-op), provided `old` is deduped (it may carry
  caller duplicates — see §4.2).
- **DeleteTable.** Drops the table's catalog entry + head, **drops `liveByTable[t]` in O(1)**,
  and force-schedules a covering merge that reclaims the table's segments. `live` loses the
  table's pairs immediately and `written` loses them when the merge installs — no permanent
  over-count (the v1 blocker), no rescan.
- **Head-resident excess (the one residual bias).** `live` is global (includes pairs still in
  the head, ≤ the 16 MiB cap); `written` counts only sealed segments. During active writing
  `live` can slightly exceed sealed `written` → raw fraction negative → clamped to 0. The bias
  is always toward **under**-triggering by at most a head's worth of postings — negligible
  against the hundreds of MB at which a covering merge is worth running, and it never hides
  real garbage (when garbage is high, `live ≪ written`). A debug/test invariant asserts
  `live − written ≤ headCap`, so a *larger* excess (which would signal a counter bug, not head
  bias) is caught rather than silently clamped.

## 6. Persistence & crash recovery

**Only `segMeta.Postings` is persisted** — and it is per-segment, so it is automatically
consistent with the segment set in every MANIFEST. `written` is the on-demand sum of those.
**`live` is NOT persisted** — there is no global scalar in the MANIFEST to go stale, which is
what removes the v1 crash blocker entirely.

- **`Open`** recomputes `live` exactly from the opened segments' forward records, **gated by
  the live catalog** (§4.2.1). Because it is derived from the *segments actually on disk* and
  restricted to *catalog tables*, it is consistent with `written` and with what `Search` sees;
  a crash that drops unspilled head writes drops them from `live` too (they were never in a
  segment to be recomputed). No "persisted scalar vs segment set" divergence can occur, so the
  indexer replay that follows **adds only genuinely-missing docs** and cannot double-count.
- **Crash inside the `DeleteTable` window (round-2 BLOCKER).** `DeleteTable` removes the table
  from the catalog and durably rewrites the MANIFEST *before* its force-scheduled covering merge
  runs (store.go); a crash in between leaves the dropped table's segments on disk while the
  catalog no longer lists it, and the volatile force-merge trigger is lost. Two guards make this
  safe:
  - **(a) Counting — the catalog-gated recompute** does not resurrect the dropped table into
    `live` (the per-table recompute only iterates `s.man.Tables`, §4.2.1; the running sum is
    catalog-gated too, §4.3). So the trigger is never suppressed by orphan bytes. *Required for
    the trigger to stay correct.*
  - **(b) Bytes — synchronous orphan reclamation on `Open`, independent of AutoMerge.** Detect
    an orphan via segment metadata: a segment whose `[MinTable,MaxTable]` range (manifest.go)
    covers a tableId absent from `s.man.Tables`. **The reclamation MUST NOT route through
    `triggerMerge` — that early-returns when `AutoMerge` is off (concurrency.go), which is the
    default and the test default, so the bytes would leak (round-3 BLOCKER).** Instead, when an
    orphan is detected, `Open` runs `coveringMerge()` **synchronously on the worker**
    (`s.q.RunFunc`, after `startMergeLoop`), which is always available regardless of `AutoMerge`
    (store.go:29–30); its `liveTables` gate (merge.go ~561) drops the dead-table keys. The
    `[MinTable,MaxTable]`-vs-catalog test is a *range* check (a segment's range may span tables
    it doesn't actually contain), so it can only **over**-detect → at worst one extra covering
    merge that is a near-no-op on an already-clean index — never a miss.

  Both guards are required: (a) keeps the trigger correct, (b) actually reclaims the bytes. They
  are independent of `AutoMerge`.
- **Clean close.** `CloseAndWait` spills every head table then drains merges; the on-disk
  segments are the full state, so the next `Open`'s recompute is exact (zero drift).
- **Indexer-driven recovery** (no WAL) is unchanged; `live` needs nothing from it — it is
  rebuilt from segments on `Open` (per table, catalog-gated) and kept exact thereafter by the
  in-lock incremental counter.

`segMeta.Postings` is an additive field. invertedstore is **unreleased** (no production
MANIFEST exists), so the format is greenfield: we **bump `FormatVersion`** with this change; no
back-compat decode path is implemented (a pre-`Postings` segment is not expected to exist, and
the `written <= 0 → return 0` guard would in any case make an all-zero-`Postings` store a safe
no-op). No
`live` field is persisted, so there is **no** "absent field → `live = 0` → `deadFraction =
1.0` → spurious whole-index compaction on reopen" hazard (the v1 review finding) — `live` is
always recomputed, never read from disk.

## 7. What is NOT changed

- `mergeSegments`/`coveringMerge`/`mergeOneLevel` **reclamation logic and output bytes** —
  the only addition is that spill and merge set `segMeta.Postings` (`len(adds)+len(dels)`).
  The bytes written are identical; the merge does NOT touch `liveByTable`.
- `Search` / `GetDocs`, the term-id forward map, ord→ord remap, on-disk segment byte format.
- Public API (`Indexer` seam), head cap, codecs, fanout.

Newly added (small, contained): `segMeta.Postings` (a metadata int), a forward-only count
scan in `Open`, a per-table `liveByTable` counter updated in-lock in `applyBatch` /
`CreateTable` / `DeleteTable`. The merge path touches only `segMeta.Postings`, never
`liveByTable`. Existing covering-merge **correctness** tests stay valid unchanged — only the
*timing* of when one fires moves, covered by §8.

---

## 8. Test plan (TDD)

1. **`deadFraction` unit.** Build directly: all-add (cold) → `0`; delete half → ≈ `0.33`
   (`live = N/2·k`, `written = N·k + N/2·k`); delete all → `1`. Pure metadata math, no
   decompression.
2. **No false trigger (the regression guard).** Bulk-add to spill **N ≥ 3 segments, zero
   deletes**; assert (covering-merge counter hook) **no covering merge fires** and
   `deadFraction()` stays `< threshold` throughout. The test that would have caught the bug.
3. **Trigger still fires.** Clean build, then delete ≥ threshold of docs; assert exactly one
   covering merge fires and reclaims (segment count / disk drops). Extend the existing
   covering-merge test.
4. **`DeleteTable` / covering merge preserve correctness (the v1-blocker guards).**
   - Build two tables, `DeleteTable` one, force the covering merge, assert `deadFraction()` and
     `Σ liveByTable` match a store that never had the dropped table (no permanent over-count;
     `liveByTable[droppedTable]` is gone).
   - After a *garbage-reclaiming* covering merge on a **cleanly-built fixture** (no injected
     inverted/forward inconsistency), assert `Σ liveByTable` is unchanged across it (covering
     preserves live) while `written` drops. (On an inconsistent input the merge's self-heal may
     drop a forward term, §4.2.3 — that delta is reconciled at the next `Open`, so do not assert
     invariance there.)
5. **Crash recovery does not double-count — three shapes (round-2 BLOCKER guards).** Reuse
   `crashAndReopen` (differential_test.go). Assert in each case `deadFraction()` after recovery
   **equals** that of a clean store built from the same final source state (not merely "in
   [0,1]"):
   - **(a) head-only loss + over-replay:** build ≥ 2 tables, spill table A only, leave table B
     in the head, crash+reopen, indexer over-replays from cursor 0. (Convergence after replay.)
   - **(b) partially-durable table + over-replay:** spill *some* of table B's segments, lose the
     rest with the head; over-replay. This is the shape where a recompute/replay double-count
     would actually surface — the durable part is in the Open recompute AND re-touched by replay,
     so it verifies replay's `forwardKeywords` reads the durable forward (`old == new` → Δ0).
   - **(c) DeleteTable-window crash:** build 2 tables, `DeleteTable(B)` but **prevent B's
     covering merge from installing** — run `AutoMerge` ON with a test hook that blocks the merge
     before install (a `beforeCoveringInstall` gate, added with this change, since
     `beforeManifestFsync` is shared by spill and can't single out the merge) — then
     crash+reopen. Assert: (i) the recompute is catalog-gated → **no** `liveByTable[B]` (B absent
     from the catalog); (ii) `deadFraction()` matches a store that only ever had A; (iii) `Open`
     ran a **synchronous** covering merge (AutoMerge-independent, §6) for the orphaned B segments
     and B's bytes are reclaimed (segment count drops, no segMeta covers B). This test + the §6
     synchronous-reclaim fix + the `beforeCoveringInstall` hook land together.
6. **`Open` recompute == incremental, incl. dedup, PER TABLE.** After a clean build, assert the
   `Open`-recomputed `liveByTable[t]` equals the incremental counter's value **for each table
   `t`** (not only the global sum — the partition must be right, else a cross-table mis-credit
   passes while breaking `DeleteTable`'s O(1) drop). Add a doc whose forward stores **duplicate
   ords** (caller passed duplicate keywords): assert the Open recompute counts the **distinct**
   ord count (the path §8.7's incremental dedup does NOT cover).
7. **Incremental delta branches.** Re-post a doc with an identical (and a duplicate-containing)
   keyword set → `Σ liveByTable` unchanged. A **growing** re-post (`{a}`→`{a,b,c}`, the `+=
   len(newSet)-len(old)` positive branch) and a **shrinking** one (`{a,b,c}`→`{a}`) → assert the
   delta matches the distinct change. **Zero-delta** cases: delete an unknown docid, and
   double-delete a deleted docid → `Σ liveByTable` unchanged (no negative drift). An
   **add→del→add within ONE batch** (`{a,b,c}`→delete→`{a}`) → settles to the final distinct
   count and the Open recompute agrees (guards the in-batch `old` selection, §4.2.2).
8. **`segMeta.Postings` accuracy.** After a spill and after a merge, assert `Σ segMeta.Postings`
   equals the actual inverted entries written (decode cross-check, test-only). Include the
   **empty covering-merge output** case — drop a single-table store, reclaim it, assert the
   output segment has `Postings == 0` and `deadFraction()` returns 0 via the `written <= 0` guard
   (the terminal orphan-reclamation state).
9. **Threshold revalidation.** Measure `deadFraction()` at known delete/re-post ratios on the
   new global metric; confirm `coveringDeadThreshold` fires where intended (recalibrate the
   constant here if the measured distribution warrants — §4.3 scope note).
10. **Differential unchanged.** `differential_test.go` (vs invertedindex, identical search
    results) stays green — proves search/data semantics untouched.

## 9. Acceptance criteria

- `idxbench -impl=store` full linux build (94,559 docs, real disk) completes in **≈30s** (down
  from 6+ min), within ~1.5× of the spike, **faster than pebble** (≈65s).
- Disk, `hits` (vs pebble: 2,414,505), and `-race` cleanliness unchanged.
- `Open` recompute adds a bounded one-time cost (forward-region count scan, target < ~100 ms
  on the linux index); measured and reported, not assumed.
- Whole-workspace build + tests green (both modules); `go-cov` gate on `core` passes.
- Build CPU profile shows `deadFraction` at **< 1%** (was 73%).

## 10. Alternatives considered (rejected)

- **v1: global persisted `livePostings` scalar.** Persisting a head-inclusive global count at
  a per-table spill makes it inconsistent with the per-segment `Σ Postings` it ships with;
  after a crash the indexer replay *adds* the lost head's pairs on top of the already-counted
  persisted value → permanent over-count → `deadFraction` pinned at 0 → covering merge never
  fires → unbounded bloat. Also leaked on `DeleteTable`. **Rejected** (round-1 review BLOCKER);
  replaced by per-table, segment-anchored `live` (recompute-on-Open + in-lock incremental).
- **Single global `live` reseated from a covering merge's output.** The covering merge compacts
  only segments, not the head, so its output count omits head-resident live pairs → reseating
  would under-count by a head's worth. Rejected for the per-table incremental counter (§4.2).
- **Per-segment `LivePostings` summed on Open.** A segment's "live" count is not well-defined
  in isolation (a newer segment can supersede its adds), so Σ per-segment-live over-counts.
  Rejected as a standalone metric (the on-`Open` recompute does the newest-wins resolution
  once, globally, instead).
- **Assume-clean on Open (`live := written`, no scan).** Simpler (no forward scan) and safe
  (under-counts → never spurious), but it *forgets* pre-restart garbage until new activity
  re-crosses the threshold, so a restart of a dirty index delays reclamation indefinitely if the
  index then goes read-mostly. **Not for production default** — it defeats the priority that the
  covering merge actually reclaims. Documented only as an emergency knob if the Open recompute
  cost ever proves problematic on a measured workload; the catalog-gated forward recompute is the
  chosen design.
- **Count tombstones only** (`Σ Tombstones / written`). Misses overlapping-keyword re-post
  garbage (superseded adds, no tombstone) → update-heavy workloads never reclaim. The
  `live/written` form subsumes it at the same cost. Rejected.
- **Throttle the existing scan** (run `bottomDeadFraction` every K spills). Treats the symptom;
  the full-decompression scan still runs and still grows with the level; K is arbitrary.
  Rejected.
- **Exact cross-segment dead count at merge time.** Supersession is a global property; an
  exact count is the very scan we are removing. A trigger needs only a metadata heuristic.
  Rejected.


