# Spec — Fix the C.4 merge map-reuse build regression (`clear()` retained-capacity blow-up)

Status: APPROVED (stage 2 review converged — Round 2 zero Blocking/Major). Owner: ingestion-perf. Supersedes the C.4 portion of
`invertedstore-ingestion-perf-spec.md` §5.

## 1. Problem

The `invertedstore` full-corpus build regressed **6×** — from **46.5 s** (commit `a52da8d`,
F.7B) to **277 s / 4m37s** (commit `905888b`, current HEAD) — on the `lx` corpus (94 559 docs,
2 414 505 hits) on `/workspace` (xfs). The regression was misattributed to the container /
disk; that is disproven below. It is a **code** regression introduced by commit `581383a`
("perf(invertedstore): cut merge/encode alloc churn (C.2-4)"), specifically item **C.4**.

This spec defines the surgical fix and how it is verified.

## 2. Evidence — environment ruled out, code pinpointed

All measurements on the same idle 8-core / 125 GiB host, `/workspace` xfs, `idxbench` harness,
full `lx` corpus, `-batch=1`.

1. **Not fsync / disk.** Full build to a 2 GiB tmpfs (`/dev/shm`, fsync ~free) = **4m37s** —
   identical to the xfs build (4m30s). fsync latency was ~3.1 ms (1000×4 KiB write+sync = 3.09 s),
   but fsync-count × latency is only a few seconds, not minutes. Disk/fsync is NOT the bottleneck.
2. **Not CPU throttling.** A build capped to ~1/5 of the corpus = 14.3 s, CPU-bound at **157 %**
   (profile: 22.45 s samples / 14.26 s wall) — healthy per-core speed. The full build instead runs
   at loadavg ~0.7 (mostly single-threaded merge), i.e. the cost is **super-linear**, not a slower core.
3. **CPU profile of the full build** (`go tool pprof -top`, Duration 277.65 s, samples 354.63 s):
   ```
   132.82s 37.45%  internal/runtime/maps.(*Iter).Next        ← map iteration
    91.61s 25.83%  internal/runtime/maps.ctrlGroup.matchFull  ← scanning empty control words
     ...    cum 274.93s 77.53%  invertedstore.(*Store).mergeSegments
   ```
   ~224 s flat (≈ **63 % of all build CPU**) is Go-map iteration, entirely under `mergeSegments`.
4. **Bisection.** `a52da8d` (immediately before C.2-4) builds in **46.5 s**; `905888b` (current)
   in **277 s**. The 6× regression lands exactly on `581383a` (C.2-4).

## 3. Root cause — `clear()` does not release map bucket capacity

C.4 hoisted the per-keyword reconciliation maps out of the merge loop and `clear()`+reused them
for every key (`merge.go`):

```go
adds := map[int64]struct{}{}   // hoisted ABOVE the loop
dels := map[int64]struct{}{}
for { ... // INVERTED branch, per keyword:
    clear(adds); clear(dels)
    for _, i := range hit { /* fill adds/dels, newest-wins */ }
    for d := range adds { addList = append(addList, d) }   // ← the hot iteration
    for d := range dels { delList = append(delList, d) }
}
```

`clear(m)` empties a Go map but **retains its bucket array** (capacity never shrinks). A few very
high-frequency keywords grow `adds` to hundreds of thousands of buckets. After `clear()`, the map
keeps that capacity, so for **every subsequent keyword** — including the long tail of low-cardinality
ones with only 1–2 docids (illustrative: the `lx` shape is many tiny keywords plus a few very
high-cardinality terms; the exact counts are not load-bearing) — `for d := range adds` must scan the
entire retained bucket array (mostly empty buckets;
`matchFull` walks the empty control words) to find the handful of live elements. Total drain cost
becomes **O(numKeys × peakBucketCount)** instead of O(Σ key sizes) — the observed super-linearity
and the `maps.Iter.Next` / `matchFull` profile.

Pre-C.4 (`a52da8d`) declared `adds`/`dels` **fresh inside** the inverted branch, so each key's map
was sized to that key and iteration was O(key size) — hence 46 s.

C.2 (segWriter `blkFirst` copy) and C.3 (`encodeScratch` reuse) in the same commit are NOT
implicated by the profile (no `blkFirst`/encode hotspot) and are correctness fixes / real wins;
they are **kept**.

## 4. The fix (chosen: Option A — fresh map per key, revert the C.4 hoist)

Restore the pre-C.4 (`a52da8d`) structure for the reconciliation maps ONLY:

- DELETE the hoisted block above the merge loop — BOTH the two declarations AND the stale C.4
  comment that precedes them (in the current `merge.go` this is the comment block + the two
  `adds`/`dels` declarations; the comment claims the maps are "hoisted OUT of the merge loop and
  clear()+reused", which must not survive next to reverted code):
  ```go
  // C.4: per-keyword reconciliation maps hoisted OUT of the merge loop and clear()+reused ...  (DELETE)
  adds := map[int64]struct{}{}
  dels := map[int64]struct{}{}
  ```
- In the INVERTED branch, declare them **fresh per key** (back inside), and DELETE the two
  `clear(adds)`/`clear(dels)` calls **AND the second stale C.4 comment that sits immediately above
  them** (in the current `merge.go`, the "C.4: clear() the reused maps UNCONDITIONALLY here — before
  any keep/drop decision ..." block — once the `clear()` calls are gone it describes code that no
  longer exists, so it must go too; the new fresh-declaration guard comment below replaces it):
  ```go
  } else { // INVERTED
      // C.4: clear() the reused maps UNCONDITIONALLY here ...   (DELETE this comment block)
      adds := map[int64]struct{}{}
      dels := map[int64]struct{}{}
      // clear(adds); clear(dels)                                (DELETE these two calls)
      for _, i := range hit { /* unchanged */ }
      ...
  }
  ```
- KEEP `var enc encodeScratch` hoisted (C.3 — used by BOTH the forward and inverted branches via
  `enc.encodeForwardInto` / `enc.encodeInvertedValueInto`; unrelated to the regression).
- ADD a one-line code comment at the fresh declaration warning WHY they must NOT be hoisted +
  `clear()`-reused (`clear()` retains bucket capacity → O(numKeys × peak) drain), so the footgun is
  not re-introduced. This comment is the durable guard.

This is the entire change: a few lines in one function (`mergeSegments`, `core/invertedstore/merge.go`).
No other file changes; no public API, on-disk format, or MANIFEST change.

## 5. Alternatives considered

- **D — keep reuse, shed capacity after big keys** (`clear()` small keys, `make()` a fresh map once a
  key exceeded a threshold). Preserves C.4's alloc win but needs a tuned threshold and is subtler to
  review. Rejected: C.4's "win" is GC/alloc-churn time, which `a52da8d` proves is NOT the bottleneck
  (46 s build WITH the churn); the simpler revert wins on the priority axis (build ≫ mem).
- **C — drop the map entirely, sorted k-way merge of per-source docid streams.** The "ideal" form, but
  materially more code and risk for no measured build benefit over A. Deferred (could be a separate,
  later spec if a future profile shows the fresh-map alloc itself is the next bottleneck).
- **Chosen: A.** Behavior-identical to the well-tested `a52da8d`, smallest diff, lowest risk, directly
  removes the super-linear term. Matches the user-selected direction.

## 6. Correctness, compatibility, risk

- **Correctness is unchanged.** The fix only changes WHERE `adds`/`dels` are allocated (per-key vs
  hoisted+cleared), not HOW they are filled or drained. Per-key newest-wins reconciliation, the
  oldest→newest `hit` walk, the add-then-del-within-a-source ordering, the covering-merge drop rules,
  the remap append-index invariant, and the dropped-key sentinel path are all byte-for-byte identical.
  A fresh empty map per key is semantically identical to a `clear()`ed reused map. The `adds`/`dels`
  reconciliation reverts to **exactly `a52da8d`'s structure**, now combined with C.3's retained
  `encodeScratch` on the (separate) encode path — so the function is not byte-identical to `a52da8d`
  as a whole, but the map-drain hot path is. `a52da8d` (with that map structure) passed the full
  differential suite.
- **No durability / format / API impact.** No segment byte layout, MANIFEST, FormatVersion, option, or
  exported signature changes. No reindex. Reader path untouched.
- **Risk: per-key map allocation churn returns** (the 2.1 GB cumulative alloc C.4 removed). Mitigation:
  it is short-lived per-key garbage, collected promptly; `a52da8d` built in 46 s WITH it. Verified by
  measuring build time AND peak RSS post-fix (§7) — if RSS regresses materially vs the current 484 MiB,
  escalate to Option D (recorded, not pre-emptively built).
- **Risk: accidentally reverting C.2/C.3 too.** Mitigation: the diff MUST touch only the `adds`/`dels`
  declarations + the two `clear()` lines; `enc`/`encodeForwardInto`/`encodeInvertedValueInto`/segment.go
  `blkFirst` stay. The task breakdown calls this out and the review checks the diff scope.

## 7. Verification

1. **Existing suite is the correctness oracle.** `cd core && GOWORK=off go test ./invertedstore/`
   (incl. all `TestDifferential_*` — they cross-check merge output against a reference model: multi-source
   newest-wins through a forced merge, forward-tombstone survival, full int64 docid range through spill
   AND tiered merge, tableId isolation; plus `merge_test.go`/`merge_robustness_test.go` for covering-vs-
   tiered, dropped keys, dead-table keys, the ord→ord remap, and the sentinel self-heal) must stay green,
   and `-race` green. These already cover the reconciliation behavior this fix restores.
2. **New CORRECTNESS test (NOT a perf-regression guard) — fills a real coverage gap.** Add a focused test
   that drives `mergeSegments` through a "one very high-cardinality keyword (a single large posting list)
   followed by many tiny keywords" shape and asserts the merged output is correct (every key's adds/dels
   match the newest-wins reference). No existing test builds one giant posting list adjacent to a long
   tail of tiny ones — this is exactly the map-population shape the fix touches, so the case is worth
   adding for COVERAGE. **It does NOT guard the regression:** the bug is performance, not correctness, so
   this assertion passes byte-identically on both the buggy (`clear()`-reuse) and fixed (fresh-map) code.
   Run it under `-race`. Do not label it a regression guard.
3. **There is deliberately NO mechanical CI guard against re-introducing the hoist+`clear()` footgun.**
   A correctness test cannot detect a perf-only regression (§7.2). The only non-flaky mechanical guard
   would be an iteration/work-count property assertion (drain work scales with Σ key sizes, not
   numKeys×peak), which requires instrumenting the merge HOT PATH with a counter hook — we reject that:
   it bloats production code for a test, and an allocation-count (`AllocsPerRun`) guard is actively wrong
   here because Option A *increases* allocations (the buggy reuse allocated less). A wall-clock timing
   test is forbidden by the no-CPU-burn-measurement-tests principle. **The durable guard is therefore
   social, and the spec says so plainly:** (a) the code comment at the fresh declaration (§4) explaining
   why the maps must not be hoisted+`clear()`-reused, and (b) the build A/B numbers recorded in the PR and
   memory. Neither fails CI; both stop a human/agent from re-attempting the "optimization".
4. **Build A/B (manual, recorded — not a CI test).** Re-run the `idxbench` full-`lx` build on `/workspace`
   pre/post fix: expect build to drop from ~277 s back to ~46–50 s, with identical `disk=` and `hits=`.
   Measure peak RSS on BOTH the cold build AND a covering-merge pass (covering builds the largest `adds`
   maps, so it is where the per-key fresh-map churn risk from §6 would show) — expect RSS ≈ 484 MiB (±);
   if it regresses materially, escalate to Option D. Also confirm search latency + `hits=` are unchanged
   (the reader path is untouched, so this is a parity check). Record all numbers in the PR and memory.
5. **Coverage** `go-cov` ≥ 90 % for `invertedstore` must hold. (Note: the reverted lines are already
   executed by every merge test, so coverage will not move and does not itself guard the regression —
   the gate is kept for the package, not claimed as a perf guard.)

## 8. Out of scope

- Option C (sorted k-way merge) — deferred.
- The H `+8`/op spill-cadence tweak (`+8` → `+4`) — a separate, independent follow-up; not bundled here.
- Any further build-time work beyond removing this regression.

## 9. Review log

### Round 1 (3 independent agents: correctness / scope / verification lenses)

- **Correctness lens — VERDICT clean.** Verified the root cause in the Go 1.24/1.25 toolchain source
  (`table.Clear` retains the group array, `Iter.Next` walks the retained capacity, `matchFull` scans
  empty groups — exact match for the profile). Confirmed fresh-per-key is output-identical and `enc`
  must stay hoisted. Findings: [Minor] §6 "exactly the a52da8d code" overstated → **fixed** (now
  "reverts to exactly a52da8d's map structure, combined with C.3's encode path"); [Nit] §3 counts are
  illustrative → **fixed** (softened); [impl note] the stale C.4 comment block must be deleted too →
  **fixed** (§4 now names it).
- **Scope lens — VERDICT clean.** Confirmed C.4 is cleanly separable from C.2/C.3, no entangled files
  (codec.go/keys.go/segment.go/block_index_test.go untouched), no test asserts the maps are hoisted,
  and C.3's `enc` consumes the drained slices not the maps. Finding: [Nit] name the old comment block in
  the deletion set → **fixed** (§4).
- **Verification lens — VERDICT needs-fix.** [Major] §7.2 was mislabeled a "regression guard": a
  correctness test passes identically on buggy and fixed code, so it has zero discriminating power
  against re-introducing the footgun → **fixed** (§7 rewritten: §7.2 reframed as a COVERAGE correctness
  case explicitly NOT a perf guard; new §7.3 states plainly there is no mechanical CI guard and why
  — instrumenting the hot path is rejected, `AllocsPerRun` is backwards for Option A, timing tests are
  forbidden — and the durable guard is the code comment + PR/memory). [Minor] §7.3 missing covering-merge
  RSS + search parity + `-race` on the new test → **fixed** (now §7.4 + §7.2). [Nit] coverage gate is
  orthogonal → **fixed** (§7.5 notes it does not guard the regression).

Round 1 resolution: all Blocking/Major = 0 after fixes (the single Major resolved). Re-review pending
(Round 2) on the revised spec per the loop rule.

### Round 2 (2 fresh agents on the revised spec: verification re-review / holistic)

- **Verification re-review — VERDICT clean.** Confirmed the Round-1 Major is genuinely resolved: §7.2 now
  honestly framed as a coverage/correctness case (not a perf guard), §7.3's "no mechanical CI guard" is
  justified, and the "AllocsPerRun is backwards (Option A allocates MORE)" reasoning is correct. §7.4
  success criterion complete (build/disk/hits/RSS-cold+covering/search/`-race`). No new inconsistency.
- **Holistic — zero Blocking, zero Major; one Minor.** §4 named only the FIRST stale C.4 comment; a SECOND
  C.4 comment ("clear() the reused maps UNCONDITIONALLY here ...") sits above the two `clear()` calls and
  would be stranded → **fixed** (§4 second bullet now names it in the deletion set). Confirmed §4's
  delete/keep list matches the real `merge.go` (hoisted comment+decls + two `clear()` go; `enc` +
  `encodeForwardInto`/`encodeInvertedValueInto` stay) and produces a compiling, a52da8d-structured function.

**Convergence:** Round 2 returned **zero Blocking and zero Major** (the only finding was one Minor, now
applied). Per the loop rule the spec is converged. **Status → APPROVED for task breakdown (stage 3).**
