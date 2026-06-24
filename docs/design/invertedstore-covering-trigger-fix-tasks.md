# invertedstore Covering-Merge Trigger Fix — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to
> implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the O(spills × bottom-level) full-decompression `bottomDeadFraction` scan
(73% of build CPU) with an O(#segments) metadata `deadFraction`, restoring build to ~30s on
the linux corpus, with no change to search/disk/data semantics.

**Architecture:** `written = Σ segMeta.Postings` (per-segment metadata, crash-consistent);
`live` = per-table `liveByTable` counter, recomputed on `Open` from the segments' `[F]` region
(reusing reconcile.go's newest-wins resolver), maintained incrementally in `applyBatch`,
catalog-gated. `deadFraction = clamp₀(1 − live/written)`. Orphan dead-table bytes left by a
`DeleteTable`-window crash are reclaimed by a synchronous covering merge on `Open`.

**Tech stack:** Go 1.24, `core/invertedstore` package. Test with `GOWORK=off go test` (core
module alone) and the whole-workspace gate. Spec: `docs/design/invertedstore-covering-trigger-fix-spec.md`.

**Real test helpers (use these exact names; the snippets below may abbreviate):**
`newUpdateStore(t) (*Store, int)` / `newUpdateStoreOpts(t, opts)` build a store + first table;
`s.applyForTest(tid, docid, kw)` = synchronous cold-doc apply; `s.spillForTest(tid)` =
synchronous spill; `s.coveringMergeForTest(t)` = synchronous covering merge;
`s.dropHeadCloseSegmentsForTest()` = crash simulation. `must`/`applySync`/`forceSpillForTest`/
`newTestStore` in snippets map to these — wire to the real names when implementing.

**Build/test commands (run from the worktree root):**
- Package tests: `cd core && GOWORK=off go test ./invertedstore/ -run <Name> -v`
- Race: `cd core && GOWORK=off go test ./invertedstore/ -race`
- Per-fn coverage gate: `cd core && GOWORK=off go-cov ./invertedstore/...` (must pass before push)
- Whole workspace: `go test ./...` at root AND `cd core && GOWORK=off go test ./...` (root `./...`
  does NOT descend into `./core`)

---

## File map (what each task touches)

| File | Change |
|------|--------|
| `core/invertedstore/manifest.go` | add `Postings int64` to `segMeta`; bump `FormatVersion` |
| `core/invertedstore/head.go` | spill: accumulate `len(adds)+len(dels)` → `sm.Postings`; `liveByTable` not touched here |
| `core/invertedstore/merge.go` | mergeSegments: accumulate `Postings` on the `keep` path → `res.sm.Postings`; **replace** `bottomDeadFraction` with `deadFraction`; add orphan detection helper |
| `core/invertedstore/reconcile.go` | extract `forEachLiveSegmentForward` core; `ForwardDocids` becomes a wrapper; add the segments-only `recomputeLive` |
| `core/invertedstore/keys.go` | (read-only) `decodeForward` already returns ords |
| `core/invertedstore/store.go` | `Store.liveByTable map[int]int64`; init + `recomputeLive` + orphan reclaim in `Open`; `CreateTable`/`DeleteTable` adjust `liveByTable`; `applyBatch` increment lives in `update.go` |
| `core/invertedstore/update.go` | `applyBatch`: in-lock `liveByTable` deltas with `dedup(old)` |
| `core/invertedstore/export_test.go` | `beforeCoveringInstall` hook + `LiveByTableForTest`/`DeadFractionForTest`/`RecomputeLiveForTest` accessors |
| `core/invertedstore/*_test.go` | the §8 suite (new `trigger_test.go`, `live_count_test.go`, additions to crash/differential tests) |

**Helpers used (already exist):** `decodeForward(v) (ords []uint32, deleted bool)` (keys.go),
`forwardKeyPrefix(tid)` / `scanPrefix` (reconcile.go/segment.go), `coveringMerge()` (merge.go),
`s.q.RunFunc` (synchronous worker task), `s.man.Tables` (catalog), `segMeta.MinTable/MaxTable`.

---

### Task 1: `segMeta.Postings` — per-segment inverted-entry count

**Files:** Modify `manifest.go` (segMeta + FormatVersion), `head.go` (spill), `merge.go`
(mergeSegments keep-path); Test `segmeta_postings_test.go` (new), `export_test.go` (accessor).

- [ ] **Step 1: Write the failing test.** New file `core/invertedstore/segmeta_postings_test.go`:

```go
package invertedstore

import "testing"

// A spilled segment's Postings equals the inverted (add+del) entries it stores, and an
// empty covering-merge output has Postings 0.
func TestSegMetaPostings_SpillAndMerge(t *testing.T) {
	s := newTestStore(t, Options{}) // existing helper; one head, one table
	tid, err := s.CreateTable("t")
	must(t, err)
	// doc 1 -> {a,b,c}; doc 2 -> {b,c} : 5 inverted add entries, 0 dels.
	applySync(t, s, tid, 1, []string{"a", "b", "c"})
	applySync(t, s, tid, 2, []string{"b", "c"})
	forceSpillForTest(t, s, tid)

	var total int64
	for _, sm := range s.SegmentsForTest() {
		total += sm.Postings
	}
	if total != 5 {
		t.Fatalf("Postings = %d, want 5 (3+2 adds)", total)
	}
}
```

(`newTestStore`, `applySync`, `forceSpillForTest`, `must` — use the existing test helpers; if a
name differs in the current tree, match it. `SegmentsForTest()` is added in Step 3.)

- [ ] **Step 2: Run it, expect FAIL** (`SegmentsForTest`/`Postings` undefined):
  `cd core && GOWORK=off go test ./invertedstore/ -run TestSegMetaPostings -v` → compile error.

- [ ] **Step 3: Implement.**
  - `manifest.go`: add `Postings int64 \`json:"postings"\`` to `segMeta`; bump `FormatVersion`
    const to the next integer (greenfield — §6).
  - `export_test.go`: add `func (s *Store) SegmentsForTest() []segMeta { s.mu.RLock(); defer s.mu.RUnlock(); return append([]segMeta(nil), s.man.Segments...) }`.
  - `head.go` spill, the `for _, t := range terms` loop (~121–126): accumulate
    `postings += int64(len(adds) + len(dels))`; set `sm.Postings = postings` in the `segMeta{…}`
    literal (~162).
  - `merge.go` `mergeSegments`: declare `var postings int64`; in the inverted branch **inside
    the `if keep {` block** (~287), `postings += int64(len(addList) + len(delList))`; set
    `sm.Postings = postings` in the `segMeta{…}` literal (~314).

- [ ] **Step 4: Run, expect PASS.** Then add the empty-covering-output assertion to the same
  test (delete both docs, force a covering merge via `coveringMergeForTest`, assert the output
  segMeta has `Postings == 0`). Run again → PASS.

- [ ] **Step 5: Commit.**
```bash
git add core/invertedstore/manifest.go core/invertedstore/head.go core/invertedstore/merge.go core/invertedstore/segmeta_postings_test.go core/invertedstore/export_test.go
git commit -m "feat(invertedstore): segMeta.Postings — per-segment inverted-entry count"
```

---

### Task 2: `forEachLiveSegmentForward` — extract reconcile.go's segment newest-wins core

**Files:** Modify `reconcile.go` (extract helper, rewrite `ForwardDocids` as wrapper); Test:
existing `reconcile_test.go` must stay green (regression), plus `forEachLiveSegmentForward_test.go`.

- [ ] **Step 1: Write the failing test.** New `core/invertedstore/foreach_forward_test.go`:

```go
package invertedstore

import "testing"

// forEachLiveSegmentForward surfaces each live docid's ORDS (newest-wins, tombstones excluded),
// which ForwardDocids previously discarded.
func TestForEachLiveSegmentForward_SurfacesOrds(t *testing.T) {
	s := newTestStore(t, Options{})
	tid, err := s.CreateTable("t")
	must(t, err)
	applySync(t, s, tid, 1, []string{"a", "b", "c"}) // 3 distinct kw
	forceSpillForTest(t, s, tid)

	got := map[int64]int{}
	s.mu.RLock()
	segs := append([]*segment(nil), s.segs...)
	s.mu.RUnlock()
	s.forEachLiveSegmentForward(tid, map[int64]struct{}{}, segs,
		func(docid int64, ords []uint32, deleted bool) bool {
			if !deleted {
				got[docid] = len(distinctOrds(ords))
			}
			return true
		})
	if got[1] != 3 {
		t.Fatalf("doc 1 distinct ords = %d, want 3", got[1])
	}
}
```

- [ ] **Step 2: Run, expect FAIL** (`forEachLiveSegmentForward`/`distinctOrds` undefined).

- [ ] **Step 3: Implement** in `reconcile.go`:
  - Add `func distinctOrds(ords []uint32) []uint32` (sorted-dedup; ords come sorted from
    `decodeForward`, so a single dedup pass: skip `ords[i] == ords[i-1]`).
  - Extract the segment loop (current lines 79–101) into:
    `func (s *Store) forEachLiveSegmentForward(tableId int, decided map[int64]struct{}, segs []*segment, visit func(docid int64, ords []uint32, deleted bool) (keepGoing bool))`
    — same body, but `ords, del := decodeForward(value)` (was `_, del`) and call
    `visit(docid, ords, del)`; on `del` still mark `decided` and continue; honor the `keepGoing`
    bool for early-stop.
  - Rewrite `ForwardDocids` to: catalog gate (unchanged) → head snapshot into `decided`+`headLive`
    + acquire segs (unchanged) → yield `headLive` via `fn` (unchanged) → call
    `forEachLiveSegmentForward(tableId, decided, segs, func(d, _, del) bool { if del { return true }; return fn(d) })`.

- [ ] **Step 4: Run.** `TestForEachLiveSegmentForward_SurfacesOrds` PASS **and** the whole
  `reconcile_test.go` (incl. `TestForwardDocids_AcrossSegments`, `TestForwardDocids_EarlyStop`)
  PASS — `cd core && GOWORK=off go test ./invertedstore/ -run 'Forward|ForEach' -v`.

- [ ] **Step 5: Commit.**
```bash
git add core/invertedstore/reconcile.go core/invertedstore/foreach_forward_test.go
git commit -m "refactor(invertedstore): extract forEachLiveSegmentForward; ForwardDocids wraps it"
```

---

### Task 3: `liveByTable` — per-table live counter (init + incremental + DeleteTable)

**Files:** Modify `store.go` (struct field, init in `Open`, `CreateTable`/`DeleteTable`),
`update.go` (`applyBatch` in-lock deltas); Test `live_count_test.go` (new), `export_test.go`
(`LiveByTableForTest`).

- [ ] **Step 1: Write failing tests** (`core/invertedstore/live_count_test.go`) covering §8.7's
  branches against `LiveByTableForTest()`:

```go
func TestLiveByTable_DeltaBranches(t *testing.T) {
	s, tid := newUpdateStore(t)
	s.applyForTest(tid, 1, []string{"a", "b", "c"})
	if got := s.LiveByTableForTest()[tid]; got != 3 { t.Fatalf("cold add live=%d want 3", got) }
	s.applyForTest(tid, 1, []string{"a", "b", "c", "d"}) // grow +1
	if got := s.LiveByTableForTest()[tid]; got != 4 { t.Fatalf("grow live=%d want 4", got) }
	s.applyForTest(tid, 1, []string{"a"})               // shrink to 1
	if got := s.LiveByTableForTest()[tid]; got != 1 { t.Fatalf("shrink live=%d want 1", got) }
	s.applyForTest(tid, 1, nil)                          // delete
	if got := s.LiveByTableForTest()[tid]; got != 0 { t.Fatalf("delete live=%d want 0", got) }
	s.applyForTest(tid, 99, nil)                         // delete unknown — Δ0
	if got := s.LiveByTableForTest()[tid]; got != 0 { t.Fatalf("del-unknown live=%d want 0", got) }
	s.applyForTest(tid, 2, []string{"x", "x", "y"})      // duplicate-keyword → distinct 2
	if got := s.LiveByTableForTest()[tid]; got != 2 { t.Fatalf("dup-kw live=%d want 2", got) }
}
```
  Plus `TestLiveByTable_DeleteTableDropsPartition` (two tables, DeleteTable B, partition gone).

- [ ] **Step 2: Run, expect FAIL** (`LiveByTableForTest`/field undefined).

- [ ] **Step 3: Implement.**
  - `store.go`: add `liveByTable map[int]int64` to `Store`; init `liveByTable: map[int]int64{}`
    in the `Open` `&Store{…}` literal (alongside `head:`); `CreateTable` may leave it (missing
    key reads 0 — do NOT add a seed loop); `DeleteTable` add `delete(s.liveByTable, tableId)`
    under the existing lock (next to `delete(s.man.Tables, …)`).
  - `export_test.go`: `func (s *Store) LiveByTableForTest() map[int]int64 { s.mu.RLock(); defer s.mu.RUnlock(); out := map[int]int64{}; for k, v := range s.liveByTable { out[k] = v }; return out }`.
  - `update.go` `applyBatch`, **inside the `s.mu.Lock()` window** (~122–156): `oldN :=
    distinctStrings(old)` once; DELETE branch `s.liveByTable[op.tableId] -= int64(oldN)`;
    FULL-RE-POST branch `s.liveByTable[op.tableId] += int64(len(newSet)) - int64(oldN)`
    (`newSet` is the map already built at ~140). Add `func distinctStrings(ss []string) int`.

- [ ] **Step 4: Run, expect PASS** — `cd core && GOWORK=off go test ./invertedstore/ -run TestLiveByTable -v`.

- [ ] **Step 5: Commit.**
```bash
git add core/invertedstore/store.go core/invertedstore/update.go core/invertedstore/live_count_test.go core/invertedstore/export_test.go
git commit -m "feat(invertedstore): per-table liveByTable counter (incremental, distinct, DeleteTable-aware)"
```

---

### Task 4: `recomputeLive` on `Open` (catalog-gated, via the shared resolver)

**Files:** Modify `reconcile.go` (add `recomputeLive`), `store.go` (call in `Open`); Test
`live_recompute_test.go` (new), `export_test.go` (`RecomputeLiveForTest`).

- [ ] **Step 1: Write the failing test** (§8.6): build, spill, capture incremental
  `LiveByTableForTest()`, zero `s.liveByTable`, `RecomputeLiveForTest()`, assert equal **per
  table**, including a duplicate-ord doc → distinct count.

```go
func TestRecomputeLive_EqualsIncremental(t *testing.T) {
	s, tid := newUpdateStore(t)
	s.applyForTest(tid, 1, []string{"a", "b", "c"})
	s.applyForTest(tid, 2, []string{"a", "a", "b"}) // dup → distinct 2
	s.spillForTest(tid)
	want := s.LiveByTableForTest()
	s.RecomputeLiveForTest()
	got := s.LiveByTableForTest()
	if got[tid] != want[tid] || got[tid] != 5 {
		t.Fatalf("recompute %v != incremental %v (want 5)", got, want)
	}
}
```
  Plus a real-reopen test: build, spill, `Open` the same dir, assert `LiveByTableForTest()` matches.

- [ ] **Step 2: Run, expect FAIL** (`RecomputeLiveForTest`/`recomputeLive` undefined).

- [ ] **Step 3: Implement** `func (s *Store) recomputeLive()` in `reconcile.go`: reset
  `s.liveByTable = map[int]int64{}`; `for tid := range s.man.Tables { s.forEachLiveSegmentForward(tid, map[int64]struct{}{}, s.segs, func(_ int64, ords []uint32, del bool) bool { if !del { s.liveByTable[tid] += int64(len(distinctOrds(ords))) }; return true }) }`.
  Ensure `forEachLiveSegmentForward` takes `segs` as a param and does NOT re-acquire `s.mu`
  (Task 2 already made it segs-param). Call `s.recomputeLive()` in `Open` **after**
  `s.publishSnapshotLocked()` (store.go:165), before `startMergeLoop()`. `export_test.go`:
  `RecomputeLiveForTest` zeroes + calls it.

- [ ] **Step 4: Run, expect PASS** — `-run TestRecomputeLive`.

- [ ] **Step 5: Commit.**
```bash
git add core/invertedstore/reconcile.go core/invertedstore/store.go core/invertedstore/live_recompute_test.go core/invertedstore/export_test.go
git commit -m "feat(invertedstore): recomputeLive on Open from segment forward records (catalog-gated)"
```

---

### Task 5: `deadFraction` replaces `bottomDeadFraction` (the core swap — the perf win)

**Files:** Modify `merge.go` (delete `bottomDeadFraction`, add `deadFraction`, rewire
`maybeCoveringMerge`); Test `trigger_test.go` (new), `export_test.go`.

- [ ] **Step 1: Write failing tests** (`trigger_test.go`): §8.1 unit (cold→0, delete half→≈0.33,
  all→1) via `DeadFractionForTest()`; §8.2 the regression guard (the test that would have caught
  the bug):

```go
func TestDeadFraction_ColdBuildIsZero_NoCoveringMerge(t *testing.T) {
	s, tid := newUpdateStoreOpts(t, Options{CapBytes: 4 << 10, AutoMerge: true}) // tiny cap → many spills
	n := installCoveringCounter(t, s) // hook; *n = covering merges fired
	for d := 0; d < 2000; d++ { s.applyForTest(tid, int64(d), []string{"w", uniqWord(d)}) }
	s.waitMergeIdleForTest()
	if got := s.DeadFractionForTest(); got >= 0.25 {
		t.Fatalf("cold-build deadFraction=%.3f, want <0.25", got)
	}
	if *n != 0 { t.Fatalf("covering merges fired %d on a clean build, want 0", *n) }
}
```
  Plus §8.3 (delete ≥ threshold → exactly one covering merge, segment count drops).

- [ ] **Step 2: Run, expect FAIL** (`deadFraction`/`DeadFractionForTest`/counter undefined).

- [ ] **Step 3: Implement.** In `merge.go`: **delete** `bottomDeadFraction` (~578–681) + its doc
  comment; add `deadFraction()` per spec §4.3 (Σ `segMeta.Postings`; Σ `liveByTable` **catalog-
  gated**; `written<=0→0`; clamp negative). In `maybeCoveringMerge` replace
  `s.bottomDeadFraction()` with `s.deadFraction()`. `export_test.go`: `DeadFractionForTest`;
  `installCoveringCounter` (package hook bumped on the covering-merge path).

- [ ] **Step 4: Run, expect PASS** for §8.1–8.3; then FULL package race:
  `cd core && GOWORK=off go test ./invertedstore/ -race` — all green (swap must not break merge tests).

- [ ] **Step 5: Commit.**
```bash
git add core/invertedstore/merge.go core/invertedstore/trigger_test.go core/invertedstore/export_test.go
git commit -m "perf(invertedstore): replace bottomDeadFraction full scan with O(#segments) deadFraction"
```

---

### Task 6: synchronous orphan reclamation on `Open` (DeleteTable-window crash)

**Files:** Modify `store.go`/`merge.go` (orphan detection + synchronous covering merge in `Open`),
`manifest.go` (the `beforeCoveringInstall` test hook), `merge.go` `installMerge` (fire the hook on
the covering path); Test `orphan_reclaim_test.go` (new).

- [ ] **Step 1: Write the failing test** (§8.5(c)): two tables; `DeleteTable(B)` with B's covering
  merge blocked before install (the `beforeCoveringInstall` hook blocks once); crash
  (`dropHeadCloseSegmentsForTest`); reopen. Assert (i) `LiveByTableForTest()` has no B; (ii)
  `DeadFractionForTest()` equals an A-only store; (iii) after Open's synchronous reclaim, no
  `segMeta` covers B (`MinTable<=B<=MaxTable`), i.e. B's bytes are gone.

```go
func TestOrphanReclaim_DeleteTableWindowCrash(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, dir, Options{AutoMerge: true})
	a, _ := s.CreateTable("A"); b, _ := s.CreateTable("B")
	s.applyForTest(a, 1, []string{"a1", "a2"})
	s.applyForTest(b, 1, []string{"b1", "b2"})
	s.spillForTest(a); s.spillForTest(b)
	blockNextCoveringInstall(t)        // hook: B's DeleteTable covering merge won't install
	must(t, s.DeleteTable(b))
	s.dropHeadCloseSegmentsForTest()   // crash before the (blocked) merge installs
	s2 := openAt(t, dir, Options{AutoMerge: true}) // Open runs the synchronous orphan reclaim
	s2.waitForOrphanReclaimForTest()
	if _, ok := s2.LiveByTableForTest()[b]; ok { t.Fatal("table B resurrected into liveByTable") }
	for _, sm := range s2.SegmentsForTest() {
		if uint32(b) >= sm.MinTable && uint32(b) <= sm.MaxTable {
			t.Fatalf("orphan B bytes not reclaimed: seg covers B")
		}
	}
}
```

- [ ] **Step 2: Run, expect FAIL** (hook + reclaim undefined; without the fix, B is resurrected
  or its bytes leak).

- [ ] **Step 3: Implement.**
  - `manifest.go`/`export_test.go`: add a package var `beforeCoveringInstall func()` fired in
    `installMerge` only on the covering path (or in `coveringMerge` before its install);
    `blockNextCoveringInstall(t)` sets it to a one-shot blocking gate.
  - `Open` (after `recomputeLive`, after `startMergeLoop`): detect orphans — `orphan := false;
    for _, sm := range s.man.Segments { for tt := sm.MinTable; tt <= sm.MaxTable; tt++ { if _, ok
    := s.man.Tables[int(tt)]; !ok { orphan = true } } }`. If `orphan`, run
    `_ = s.q.RunFunc(func() error { return s.coveringMerge() })` — **synchronous, AutoMerge-
    independent** (NOT `triggerMerge`). Expose `waitForOrphanReclaimForTest` (a no-op if the
    RunFunc already returned synchronously, or a small drain).

- [ ] **Step 4: Run, expect PASS.** Confirm with `AutoMerge:false` too (the reclaim must still run
  — it uses `q.RunFunc`, not the merge loop).

- [ ] **Step 5: Commit.**
```bash
git add core/invertedstore/store.go core/invertedstore/merge.go core/invertedstore/manifest.go core/invertedstore/orphan_reclaim_test.go core/invertedstore/export_test.go
git commit -m "fix(invertedstore): synchronous orphan dead-table reclaim on Open (DeleteTable-window crash)"
```

---

### Task 7: crash shapes + threshold + differential + whole-workspace gate

**Files:** Test additions to `crash`/`differential`/`reconcile` tests; no production change
expected (this task is the safety net).

- [ ] **Step 1: §8.5(a)(b) crash shapes.** (a) build ≥2 tables, spill A only, B in head, crash,
  reopen, indexer over-replay → `DeadFractionForTest()` equals a clean store. (b) spill SOME of
  B's segments, lose the rest + head, over-replay → equals clean (verifies replay's
  `forwardKeywords` reads the durable forward, `old==new`→Δ0). Reuse the differential harness's
  `crashAndReopen`/over-replay.

- [ ] **Step 2: §8.9 threshold revalidation.** Measure `DeadFractionForTest()` at known
  delete/re-post ratios; assert the covering merge fires where intended at `coveringDeadThreshold`
  (0.25). If the measured global-metric distribution warrants, adjust the constant **here** and
  document why in a comment (only with evidence).

- [ ] **Step 3: §8.10 differential unchanged.** Run `differential_test.go` (invertedstore vs
  invertedindex identical search) — must stay green. `cd core && GOWORK=off go test ./invertedstore/ -run Differential -v`.

- [ ] **Step 4: Whole gate.** `cd core && GOWORK=off go test ./invertedstore/ -race` (all green);
  `cd core && GOWORK=off go-cov ./invertedstore/...` (per-fn coverage passes — add tests for any
  uncovered new error branch); root `go test ./...` AND `cd core && GOWORK=off go test ./...`
  (both modules green).

- [ ] **Step 5: Commit.**
```bash
git add core/invertedstore/
git commit -m "test(invertedstore): crash-shape + threshold + differential guards for the trigger fix"
```

---

### Task 8: re-measure (acceptance) + record

**Files:** none (measurement). Uses `core/cmd/idxbench`.

- [ ] **Step 1: Build & measure** on real disk (`/workspace/idxb`, ext4 — NOT tmpfs):
  `cd core && go build -o /tmp/idxbench ./cmd/idxbench/` then
  `/tmp/idxbench -impl=store -tokens=/workspace/blugespike/lx.gob -data=/workspace/idxb/store`.
  Expected (§9): build **≈30s** (was 6+ min), disk unchanged, `hits=2414505` (matches pebble),
  buildPeakRSS in range.
- [ ] **Step 2: Profile** `-buildprofile=/tmp/build.prof`; `go tool pprof -top -cum` must show
  `deadFraction` at **< 1%** (was 73% as `bottomDeadFraction`). Capture the `Open` recompute cost
  (forward scan) and record the measured ms.
- [ ] **Step 3: Race + interleave** a pebble vs store apple-to-apple pass; confirm search us/query
  and disk unchanged from the pre-fix store, build now < pebble.
- [ ] **Step 4: Record** the numbers in the PR body and the memory card
  `sortruns-invertedindex-build-design`. No throwaway measurement test is committed (per the
  no-CPU-burn-measurement-tests rule).

---

## Self-review (spec coverage)

- §4.1 written/Postings → Task 1. §4.2.1 recompute + shared resolver → Tasks 2,4. §4.2.2
  incremental → Task 3. §4.2.3 DeleteTable drop → Task 3; covering-preserves-live (clean) →
  Task 7. §4.3 deadFraction + catalog-gate → Task 5. §6 crash/persistence + orphan reclaim →
  Tasks 4,6. §8 tests → Tasks 1–7 (each test mapped). §9 acceptance → Task 8.
- Ordering is dependency-correct: Postings (1) and the resolver (2) are prerequisites for the
  counter (3) and recompute (4); the trigger swap (5) needs both terms; orphan reclaim (6) needs
  the catalog-gated recompute (4); the gate (7) and measure (8) come last.
- No production code change in Task 7/8 — they are the safety net and the proof.

---

## Plan-review corrections (2 reviewers, applied during implementation)

**BLOCKER — counter tests must use the REAL apply path.** `applyForTest` (export_test.go)
bypasses `applyBatch` (direct head mutation, no diff), so the `liveByTable` delta never runs
under it. ALL `liveByTable`/`deadFraction` tests (Tasks 3–6) drive `s.Update(tid,docid,kw)` +
`s.sync()` (or a real `Batch`+`Commit`). Do NOT add `liveByTable` maintenance to `applyForTest`
(that re-implements the logic in test code — a tautology). `applyForTest` stays fine for Task 1/2.

**Test hooks — exact wiring (define in Task 5 Step 0):**
- `coveringMergeCount` package int, incremented at the TOP of `coveringMerge()` — counts BOTH the
  dead-fraction-triggered AND the DeleteTable/orphan forced paths. Read via `CoveringMergeCountForTest()`.
- `beforeCoveringInstall func()` fired in `coveringMerge()` right before `return s.installMerge(…)`
  (covering-only, NOT the shared `installMerge`). `blockNextCoveringInstall(t)` = one-shot gate that
  blocks once then unblocks on test cleanup (so `dropHeadCloseSegmentsForTest`'s `stopMergeLoop`
  cannot deadlock).
- Use existing `waitMergeIdle()` (concurrency.go:265), NOT `waitMergeIdleForTest`. Drop
  `waitForOrphanReclaimForTest` (`q.RunFunc` in Open is already synchronous).
- Add `openAt(t, dir, opts) *Store` (open a GIVEN dir; `newUpdateStoreOpts` uses a fresh TempDir) —
  Task 4 reopen + all of Task 6 need it.

**Added tests (coverage gaps the reviewers found):**
- §8.4 covering-preserves-live (clean fixture) → Task 5: after a garbage-reclaiming covering merge
  on a clean build, `Σ liveByTable` unchanged while `written` drops.
- §4.2.3 tiered/spill invariance → a tiered merge (N≥Fanout L0 segs) + an isolated spill leave
  `Σ liveByTable` unchanged.
- §5 `live − written ≤ headCap` invariant → `assertCounterInvariantForTest(t)` called at the end of
  the §8.6/§8.7 tests (catches an over-count the clamp would otherwise hide).
- §8.7 add→del→add in ONE batch → real `Batch` (3 `Update`s one docid, 1 `Commit`); assert
  `Σ liveByTable == 1` and the Open recompute agrees (pins the in-batch `old` path).
- Task 1 §8.8 → decode the spilled `[I]` records and assert `Σ Postings == Σ decoded(adds+dels)`,
  not just the constant 5.
- §8.1 runs `AutoMerge:false` + direct spill so the trigger doesn't move the value mid-assertion.
- Task 6 also asserts `CoveringMergeCountForTest() >= 1` after Open (the reclaim actually ran).

**Split Task 7** → 7a (crash shapes §8.5a/b), 7b (threshold revalidation §8.9 — may change the
constant, own commit + evidence), 7c (differential §8.10 + whole gate).

**Spec §6 alignment:** the "`Open` may reject/rebuild an older `FormatVersion`" line is downgraded to
"bump only; greenfield, no back-compat path." Applied to the spec.

