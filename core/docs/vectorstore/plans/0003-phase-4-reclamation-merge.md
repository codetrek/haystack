# Phase 4 Implementation Plan — `core/vectorstore` Space Reclamation (Segment Merge / Compact)

## Goal

Deliver **space reclamation** for `core/vectorstore`: reclaim tombstone space and bound total segment count by **merging/compacting sealed segments**. This is the last piece that makes deletes actually free disk and keeps the N-way Search loop cheap as the corpus churns and grows. It closes VEC-015 (the legacy free-list problem) by the architecture's segment mechanism, not in-place reuse.

The deliverable is **one merge machine fed by two drivers**, plus crash-safety, a background trigger, and test observability:

- **One merge machine** — `mergeLocked(inputIDs)`: bin-pack the live (non-tombstoned) docs of the input sealed segments into one or more in-memory `*segment` buckets of `≤ maxSegSize`, write each bucket as a new sealed records-segment (reusing `writeSealedSegment` → `openSealedSegment`), rebuild its HNSW (`buildSegmentGraph`), then **one atomic manifest swap** replaces the N inputs with the M outputs, after which old input dirs are deleted. Single-segment compact = a merge of one input.
- **Two drivers** — `pickDeleteDriven` (segments with `liveRatio < mergeFloor`, heavy tombstones → repack) and `pickGrowthTiered` (size-tiered: a tier with `≥ K` segments merges up, capped by `maxMergedSize`, bounding total count).
- **Crash-safety, reused not reinvented** — merge = write-new + atomic manifest swap + delete-old. Crash before swap → new outputs are orphans (swept by `sweepOrphansLocked`); crash after swap → old inputs are orphans (swept); crash mid-build → output left `pending` in the manifest is re-built by `recover`. All three covered by existing primitives, each red-proofed by a test that injects the crash window.
- **Trigger + observability** — a background policy check after each seal, plus a manual `Compact()` entry point and `WaitForMerge()` for tests.

**Non-goals (do not touch):** filtering (Phase 5), multi-index / N metrics (Phase 6). Do **not** break `core/vectorindex` (separate module).

## Architecture

Built strictly on `docs/vectorstore/architecture.md` §4.5, §4.6, §4.8, §4.9 and the existing Phase-2 store. The load-bearing invariants:

1. **Vectors have no query-relevant key range** (§4.9) → every k-NN searches every segment → read amplification = total segment count → merge is **size-tiered + count-cap**, never RocksDB-leveled. Cost of a merge = rebuild HNSW over the live vectors (the dominant cost, §5).
2. **Two-level id** (§4.6): `docId` is stable across merge. A multi-input **merge** allocates a **new** `segId` (`s.nextSeg++`) and updates the global `docToSeg` map **only for the moved docs**; other segments' `segId` is untouched; per-segment slots renumber implicitly in the new bucket's append order. (Single-segment compact also allocates a fresh `segId` here — the simpler path that keeps `segDirName` unique and avoids the `Gen` bump; §4.6's "compact keeps segId, bumps Gen" is an optimization we deliberately do **not** take in v1, see Task 2 rationale.)
3. **The records-segment owns its vectors in metric-natural form** (§3). Vectors streamed out of an input via `eachLive` are already stored-form (cosine = unit + separate norm); they are appended into the new bucket **verbatim** — never re-run `metric.prepare` (would double-normalize).
4. **Manifest single-file atomic rewrite + immutable sealed segments + orphan-sweep recovery** (§4.8): the manifest swap is the single commit point; data files fsync before the manifest references them; old dirs deleted after the swap.

**The single hardest new correctness problem — delete-during-merge reconciliation.** The merge must build off the store lock (HNSW build is slow), but a concurrent `Delete`/`Put` can tombstone a doc in an input segment *after* the bucket snapshot but *before* the manifest swap. If we publish naively, that doc resurrects in the output. The design (Task 6) resolves this exactly the way Search post-filters: **snapshot the live set under `s.mu`, build off-lock, then re-acquire `s.mu` and, just before the swap, tombstone in each output bucket any doc that (a) is no longer in `s.docToSeg`, or (b) whose `s.docToSeg[doc]` no longer equals its input `segId` (it was re-homed to head by a concurrent Put).** Because `writeSealedSegment` already persists a tombstone bitmap and the output is freshly written, this is a cheap pre-swap fixup on the in-memory output `*segment` before it is dumped — or, since we dump before building, a `tombstoneSlot` on the opened output `sealedSegment`. We choose the latter (tombstone the opened output under `s.mu` right before the swap) so the build sees the full live set and only the swap reconciles.

**Commit order the merge mirrors** (from `sealLocked`, store.go:634, minus the head/WAL/idtable steps a merge does not need): write new seg dirs (fsync, `writeSealedSegment`) → under `s.mu`: reconcile late tombstones, drop input ids and append output ids in `s.sealed`/`s.sealedID`/`s.graphs`, set `s.docToSeg[doc]=newSegId` for moved docs → **one `writeManifestLocked`** (the atomic commit) → `ss.close()` + `os.RemoveAll` the old input dirs → background `buildAndPublish` for outputs (or publish indexed directly if built before swap). A merge does **not** call `alloc.Commit()` or `wal.Reset()` — moved docs' idtable mappings are already durable and the head/WAL is untouched (gotcha 4).

## Tech-Stack

- **Language:** Go (module `github.com/codetrek/haystack/core`, `go 1.23.0`; toolchain go1.25.6). Test/CI gates: `go build ./...`, `go vet ./...`, `go test ./vectorstore/...`, `go test -race ./vectorstore/...`.
- **Package:** `vectorstore` (flat — no subdirs; new files live next to `store.go`).
- **Reused Phase-2 primitives (verbatim, by exact signature):**
  - `writeSealedSegment(segDir string, head *segment) error` — seal.go:22
  - `openSealedSegment(segDir string, metric Metric) (*sealedSegment, error)` — seal.go:160
  - `buildSegmentGraph(segDir string, seg *sealedSegment, cfg graphConfig) (*segGraphStore, error)` — builder.go:30
  - `newBuiltIndex(gs *segGraphStore, cfg graphConfig) *builtIndex` — graphstore.go:190
  - `writeManifestLocked()` — store.go:726; `writeManifest` — manifest.go:118
  - `sweepOrphansLocked(m *manifest) error` — store.go:209
  - `recover()` pending-build resume loop — store.go:192-198
  - `buildAndPublish(id segID, segDir string, ss *sealedSegment)` — store.go:695 (template + reused)
  - `sealedSegment.eachLive/tombGet/tombCount/count/slotDoc/slotOfDoc/tombstoneSlot/close` — sealed.go
  - `segment.append/newSegment` — segment.go:26/19; `segDirName(id, gen)` — store.go:747
  - `s.builds` (WaitGroup), `s.buildMu`, `s.sealedByID` — store.go
- **Test harness (reuse):** `openTestStore(t, Metric)`, `reopenStore(t, s, kv)`, `newTestKV(t)`, `requireNoError`, `itoa`, `recallAtK`, `bruteForceKNN`, `s.idToDoc`, `s.attachSealedForTest`, `s.isIndexedForTest`.

## File Structure

```
core/vectorstore/
  store.go                 (EDIT: add merge fields + Compact/WaitForMerge + maybeMergeLocked trigger hook)
  merge.go                 (NEW: mergeConfig, mergeLocked, bin-pack, mergeAndPublish)
  mergepolicy.go           (NEW: pickDeleteDriven, pickGrowthTiered — pure selector fns)
  merge_test.go            (NEW: basic compact-of-one + multi-input merge correctness)
  mergepolicy_test.go      (NEW: pure selector tests, no I/O)
  merge_crash_test.go      (NEW: crash-before-swap, crash-after-swap, crash-mid-build recovery)
  merge_concurrent_test.go (NEW: delete-during-merge reconciliation, -race)
  merge_trigger_test.go    (NEW: background trigger fires + Compact()/WaitForMerge())
```

Every new symbol is defined before it is used. The plan is ordered so each task compiles and all gates pass at its commit.

---

## Conventions for every task

- **Run tests:** `cd /workspace/haystack/core && go test ./vectorstore/ -run <Name> -v`
- **Gates after every task (must be green before commit):**
  ```
  go build ./... && go vet ./vectorstore/... && \
  go test ./vectorstore/... && go test -race ./vectorstore/...
  ```
- **No placeholders.** Every test and impl block below is complete and compiles against the Phase-2 code as read.

---

## Task 1 — `mergeConfig`: tunables with safe defaults (no behavior yet)

Adds the parameter set (§4.9) to the store as a config struct with defaults, plus the `Store` field. Pure plumbing — provably inert until later tasks read it.

### 1a. Failing test — `merge_test.go` (new file)

```go
package vectorstore

import "testing"

func TestMergeConfig_Defaults(t *testing.T) {
	c := mergeConfig{}.withDefaults()
	if c.MergeFloor <= 0 || c.MergeFloor >= 1 {
		t.Fatalf("MergeFloor default = %v, want in (0,1)", c.MergeFloor)
	}
	if c.Fanout < 2 {
		t.Fatalf("Fanout default = %d, want >= 2", c.Fanout)
	}
	if c.MaxMergedSize <= 0 {
		t.Fatalf("MaxMergedSize default = %d, want > 0", c.MaxMergedSize)
	}
	if c.TargetSegCount <= 0 {
		t.Fatalf("TargetSegCount default = %d, want > 0", c.TargetSegCount)
	}
	// withDefaults must not clobber an operator-set value.
	got := mergeConfig{MergeFloor: 0.25, Fanout: 4}.withDefaults()
	if got.MergeFloor != 0.25 || got.Fanout != 4 {
		t.Fatalf("withDefaults clobbered set values: %+v", got)
	}
}

func TestStore_HasMergeConfig(t *testing.T) {
	s := openTestStore(t, Cosine)
	if s.mcfg.Fanout < 2 {
		t.Fatalf("store mergeConfig not initialized: %+v", s.mcfg)
	}
}
```

### 1b. Run → expect FAIL

`go test ./vectorstore/ -run 'TestMergeConfig_Defaults|TestStore_HasMergeConfig'` → **FAIL** (compile error: `undefined: mergeConfig`, `s.mcfg undefined`).

### 1c. Minimal impl — `merge.go` (new file)

```go
package vectorstore

// mergeConfig holds the space-reclamation tunables (architecture §4.9). All are
// measure-don't-assert placeholders the operator can override; defaults are safe
// for production-scale corpora and are deliberately shrinkable in tests so a
// handful of Puts can trigger a merge.
type mergeConfig struct {
	// MergeFloor: a sealed segment whose live ratio (live/count) is below this is
	// delete-driven merge bait (heavy tombstones). ~0.5 (§4.9 "段 live 占比 < ~50%").
	MergeFloor float32
	// Fanout K: a size tier with >= K segments is growth-driven merged up. ~8-10.
	Fanout int
	// MaxMergedSize caps a merge output's row count so the top tier never makes one
	// giant un-mergeable segment (§4.9 "封顶 maxMergedSize ~1M").
	MaxMergedSize int
	// TargetSegCount: the growth driver works to keep the live sealed-segment count
	// near this so the N-way Search loop stays cheap (§4.9 "目标活段数 ~几十").
	TargetSegCount int
}

const (
	defaultMergeFloor     = float32(0.5)
	defaultFanout         = 8
	defaultMaxMergedSize  = 1 << 20 // ~1M rows
	defaultTargetSegCount = 32
)

func (c mergeConfig) withDefaults() mergeConfig {
	if c.MergeFloor == 0 {
		c.MergeFloor = defaultMergeFloor
	}
	if c.Fanout == 0 {
		c.Fanout = defaultFanout
	}
	if c.MaxMergedSize == 0 {
		c.MaxMergedSize = defaultMaxMergedSize
	}
	if c.TargetSegCount == 0 {
		c.TargetSegCount = defaultTargetSegCount
	}
	return c
}
```

Edit `store.go`: add the field to `Store` (after `gcfg graphConfig` at store.go:59) and initialize it in `Open` (after `gcfg: ...` at store.go:101):

```go
	gcfg     graphConfig           // the single index's HNSW config
	mcfg     mergeConfig           // space-reclamation policy tunables (Phase 4)
```
```go
		gcfg:     graphConfig{}.withDefaults(),
		mcfg:     mergeConfig{}.withDefaults(),
		nextSeg:  1,
```

### 1d. Run → expect PASS, then gates green.
### 1e. Commit

`feat(vectorstore): merge config tunables (mergeFloor/fanout/maxMergedSize/targetSegCount)`

---

## Task 2 — `segLiveStats` + per-segment live/tomb snapshot helper

A tiny pure helper the drivers and the machine share: snapshot each live sealed segment's `(segId, live, count)` under the lock, so selectors are pure functions of a plain slice (testable with zero I/O). Defined before the selectors use it.

### 2a. Failing test — append to `merge_test.go`

```go
func TestStore_segStatsLocked(t *testing.T) {
	s := openTestStore(t, DotProduct)
	requireNoError(t, s.Put("a", []float32{1, 0, 0}, nil))
	requireNoError(t, s.Put("b", []float32{0, 1, 0}, nil))
	requireNoError(t, s.Put("c", []float32{0, 0, 1}, nil))
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	requireNoError(t, s.Delete("b")) // tombstone one of three → live 2 / count 3

	s.mu.RLock()
	stats := s.segStatsLocked()
	s.mu.RUnlock()

	if len(stats) != 1 {
		t.Fatalf("segStatsLocked len = %d, want 1", len(stats))
	}
	st := stats[0]
	if st.id != segID(1) || st.count != 3 || st.live != 2 {
		t.Fatalf("stats = %+v, want {id:1 count:3 live:2}", st)
	}
	if r := st.liveRatio(); r < 0.66 || r > 0.67 {
		t.Fatalf("liveRatio = %v, want ~0.666", r)
	}
}
```

### 2b. Run → expect FAIL (`undefined: s.segStatsLocked`, `segLiveStats`).

### 2c. Minimal impl — append to `merge.go`

```go
// segLiveStats is an immutable snapshot of one sealed segment's reclamation
// signal, taken under s.mu so the pure driver/selector logic never touches the
// live segment set. count includes tombstoned rows; live excludes them.
type segLiveStats struct {
	id    segID
	count int // total rows (incl. tombstoned)
	live  int // non-tombstoned rows
}

func (s segLiveStats) liveRatio() float32 {
	if s.count == 0 {
		return 1
	}
	return float32(s.live) / float32(s.count)
}

// segStatsLocked snapshots every live sealed segment's (id, count, live). Caller
// holds s.mu (R or W). It reads ss.count()/ss.tombCount(), which take the
// segment's own tomb RLock, so the snapshot is internally consistent per segment.
func (s *Store) segStatsLocked() []segLiveStats {
	out := make([]segLiveStats, len(s.sealed))
	for i, ss := range s.sealed {
		cnt := ss.count()
		out[i] = segLiveStats{
			id:    s.sealedID[i],
			count: cnt,
			live:  cnt - ss.tombCount(),
		}
	}
	return out
}
```

### 2d. Run → PASS, gates green.
### 2e. Commit `feat(vectorstore): segStatsLocked live/tomb snapshot for merge drivers`

---

## Task 3 — `pickDeleteDriven` selector (pure)

The delete driver: choose the segments whose `liveRatio < mergeFloor`. Pure function of `[]segLiveStats` + config — no store, no I/O.

### 3a. Failing test — `mergepolicy_test.go` (new file)

```go
package vectorstore

import (
	"reflect"
	"testing"
)

func TestPickDeleteDriven_SelectsBelowFloor(t *testing.T) {
	cfg := mergeConfig{MergeFloor: 0.5}.withDefaults()
	stats := []segLiveStats{
		{id: 1, count: 100, live: 30}, // 0.30 < 0.5 → pick
		{id: 2, count: 100, live: 90}, // 0.90 → skip
		{id: 3, count: 100, live: 49}, // 0.49 < 0.5 → pick
		{id: 4, count: 100, live: 50}, // 0.50 not < 0.5 → skip
	}
	got := pickDeleteDriven(stats, cfg)
	want := []segID{1, 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pickDeleteDriven = %v, want %v", got, want)
	}
}

func TestPickDeleteDriven_EmptyWhenAllHealthy(t *testing.T) {
	cfg := mergeConfig{MergeFloor: 0.5}.withDefaults()
	stats := []segLiveStats{{id: 1, count: 10, live: 10}, {id: 2, count: 10, live: 8}}
	if got := pickDeleteDriven(stats, cfg); got != nil {
		t.Fatalf("pickDeleteDriven = %v, want nil (all healthy)", got)
	}
}

func TestPickDeleteDriven_SkipsEmptySegments(t *testing.T) {
	// A fully-tombstoned segment (live 0) is still bait, but a zero-row segment is
	// not (nothing to reclaim) — liveRatio of count==0 is defined as 1.
	cfg := mergeConfig{MergeFloor: 0.5}.withDefaults()
	stats := []segLiveStats{{id: 1, count: 0, live: 0}, {id: 2, count: 10, live: 0}}
	got := pickDeleteDriven(stats, cfg)
	want := []segID{2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pickDeleteDriven = %v, want %v", got, want)
	}
}
```

### 3b. Run → expect FAIL (`undefined: pickDeleteDriven`).

### 3c. Minimal impl — `mergepolicy.go` (new file)

```go
package vectorstore

// pickDeleteDriven returns the ids of sealed segments whose live ratio is below
// mergeFloor — heavy-tombstone "deflated" segments whose live docs should be
// bin-packed into fresh ~maxSegSize buckets, reclaiming tombstone space and
// consolidating count (architecture §4.9, delete driver). Order follows the input
// (attach order). A segment with no rows is never picked (nothing to reclaim).
func pickDeleteDriven(stats []segLiveStats, cfg mergeConfig) []segID {
	var picks []segID
	for _, st := range stats {
		if st.count == 0 {
			continue
		}
		if st.liveRatio() < cfg.MergeFloor {
			picks = append(picks, st.id)
		}
	}
	return picks
}
```

### 3d. Run → PASS, gates green.
### 3e. Commit `feat(vectorstore): pickDeleteDriven selector (live-ratio < mergeFloor)`

---

## Task 4 — `pickGrowthTiered` selector (pure)

The growth driver: size-tier the segments and return the K-or-more same-tier set to merge up, capped by `maxMergedSize`. Pure function. Tier = `floor(log2(maxSegSize/count))`-style bucketing; we keep it deterministic and simple: bucket by power-of-two of `count`.

### 4a. Failing test — append to `mergepolicy_test.go`

```go
func TestSizeTier_PowerOfTwoBuckets(t *testing.T) {
	cases := []struct {
		count int
		tier  int
	}{
		{0, 0}, {1, 0}, {2, 1}, {3, 1}, {4, 2}, {7, 2}, {8, 3}, {16, 4},
	}
	for _, c := range cases {
		if got := sizeTier(c.count); got != c.tier {
			t.Fatalf("sizeTier(%d) = %d, want %d", c.count, got, c.tier)
		}
	}
}

func TestPickGrowthTiered_MergesWhenTierReachesFanout(t *testing.T) {
	cfg := mergeConfig{Fanout: 3, MaxMergedSize: 1000}.withDefaults()
	// Three segments in the same tier (count 4,5,6 → tier 2) → fanout reached.
	stats := []segLiveStats{
		{id: 1, count: 4, live: 4},
		{id: 2, count: 5, live: 5},
		{id: 3, count: 6, live: 6},
		{id: 4, count: 64, live: 64}, // tier 6, alone → not picked
	}
	got := pickGrowthTiered(stats, cfg)
	want := []segID{1, 2, 3}
	if !equalSegIDs(got, want) {
		t.Fatalf("pickGrowthTiered = %v, want %v", got, want)
	}
}

func TestPickGrowthTiered_NoneWhenBelowFanout(t *testing.T) {
	cfg := mergeConfig{Fanout: 3, MaxMergedSize: 1000}.withDefaults()
	stats := []segLiveStats{{id: 1, count: 4, live: 4}, {id: 2, count: 5, live: 5}}
	if got := pickGrowthTiered(stats, cfg); got != nil {
		t.Fatalf("pickGrowthTiered = %v, want nil (tier below fanout)", got)
	}
}

func TestPickGrowthTiered_RespectsMaxMergedSize(t *testing.T) {
	// Fanout reached but the live sum would exceed MaxMergedSize → do not merge
	// (the cap protects against an un-mergeable giant; §4.9).
	cfg := mergeConfig{Fanout: 2, MaxMergedSize: 10}.withDefaults()
	stats := []segLiveStats{{id: 1, count: 8, live: 8}, {id: 2, count: 9, live: 9}}
	if got := pickGrowthTiered(stats, cfg); got != nil {
		t.Fatalf("pickGrowthTiered = %v, want nil (would exceed MaxMergedSize)", got)
	}
}

func equalSegIDs(a, b []segID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

### 4b. Run → expect FAIL (`undefined: sizeTier`, `pickGrowthTiered`).

### 4c. Minimal impl — append to `mergepolicy.go`

```go
import "math/bits"

// sizeTier maps a segment row count to a power-of-two size tier: tier t holds
// counts in [2^t, 2^(t+1)). Counts 0 and 1 share tier 0. Size-tiered merge groups
// like-sized segments so a merge roughly doubles size each level, bounding the
// number of tiers (hence total segment count) logarithmically in the corpus size
// (architecture §4.9, growth driver; NOT leveled — vectors have no key range).
func sizeTier(count int) int {
	if count < 2 {
		return 0
	}
	return bits.Len(uint(count-1)) // ceil(log2(count)) for count>=2
}

// pickGrowthTiered returns the ids of the FIRST size tier (lowest first) that has
// accumulated >= Fanout segments AND whose combined live rows fit MaxMergedSize.
// Merging that tier folds K small segments into one larger one in the next tier,
// bounding total segment count as the corpus grows. Returns nil if no tier
// qualifies. Only the live-row sum is checked against the cap (the output holds
// only live docs).
func pickGrowthTiered(stats []segLiveStats, cfg mergeConfig) []segID {
	// Group ids by tier (preserve attach order within a tier).
	tiers := make(map[int][]segLiveStats)
	var order []int
	for _, st := range stats {
		t := sizeTier(st.count)
		if _, seen := tiers[t]; !seen {
			order = append(order, t)
		}
		tiers[t] = append(tiers[t], st)
	}
	// Lowest tier first → keeps merges small/cheap and drains the long tail.
	sortIntsAsc(order)
	for _, t := range order {
		group := tiers[t]
		if len(group) < cfg.Fanout {
			continue
		}
		liveSum := 0
		for _, st := range group {
			liveSum += st.live
		}
		if liveSum > cfg.MaxMergedSize {
			continue
		}
		ids := make([]segID, len(group))
		for i, st := range group {
			ids[i] = st.id
		}
		return ids
	}
	return nil
}

func sortIntsAsc(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}
```

### 4d. Run → PASS, gates green.
### 4e. Commit `feat(vectorstore): pickGrowthTiered size-tiered selector (fanout K, maxMergedSize cap)`

---

## Task 5 — bin-packing: `packLiveDocs` (in-memory buckets, no swap yet)

The first half of the merge machine: stream the live docs of input segments through `eachLive` and pack them into `≤ maxSegSize` in-memory `*segment` buckets, returning the buckets and the set of moved docIds. **No manifest mutation, no disk** — pure, fully unit-testable. `eachLive` hands stored-form vectors + norm; we `append` verbatim (no `metric.prepare`).

### 5a. Failing test — `merge_test.go`

```go
func TestPackLiveDocs_BinPacksAndCarriesPayload(t *testing.T) {
	s := openTestStore(t, DotProduct)
	// Build two sealed segments with known live docs.
	mk := func(ids []string, vecs [][]float32) *sealedSegment {
		seg := newSegment(DotProduct)
		for i, id := range ids {
			doc, err := s.docIDForAlloc(id)
			requireNoError(t, err)
			st, nrm := DotProduct.prepare(vecs[i])
			seg.append(doc, st, nrm, []byte(id)) // payload = id bytes (asserted below)
		}
		dir := t.TempDir()
		requireNoError(t, writeSealedSegment(dir, seg))
		ss, err := openSealedSegment(dir, DotProduct)
		requireNoError(t, err)
		return ss
	}
	a := mk([]string{"a0", "a1", "a2"}, [][]float32{{1, 0}, {0, 1}, {1, 1}})
	b := mk([]string{"b0", "b1"}, [][]float32{{2, 0}, {0, 2}})

	// maxSegSize 2 → 5 live docs pack into 3 buckets (2,2,1).
	buckets, moved := packLiveDocs([]*sealedSegment{a, b}, DotProduct, 2)
	if len(buckets) != 3 {
		t.Fatalf("buckets = %d, want 3", len(buckets))
	}
	total := 0
	seen := map[int64]bool{}
	for _, bk := range buckets {
		if len(bk.slotDoc) > 2 {
			t.Fatalf("bucket overflow: %d rows > maxSegSize 2", len(bk.slotDoc))
		}
		bk.eachLive(func(slot int, doc int64, stored []float32, norm float32) {
			total++
			seen[doc] = true
			// payload travels with the doc.
			_, _, pl, _ := bk.read(slot)
			if len(pl) == 0 {
				t.Fatalf("doc %d lost its payload during pack", doc)
			}
		})
	}
	if total != 5 {
		t.Fatalf("packed live docs = %d, want 5", total)
	}
	if len(moved) != 5 {
		t.Fatalf("moved set = %d, want 5", len(moved))
	}
	for d := range seen {
		if !moved[d] {
			t.Fatalf("doc %d packed but not in moved set", d)
		}
	}
}

func TestPackLiveDocs_ExcludesTombstoned(t *testing.T) {
	s := openTestStore(t, DotProduct)
	seg := newSegment(DotProduct)
	for _, id := range []string{"x", "y", "z"} {
		doc, err := s.docIDForAlloc(id)
		requireNoError(t, err)
		st, nrm := DotProduct.prepare([]float32{1, 0})
		seg.append(doc, st, nrm, nil)
	}
	dir := t.TempDir()
	requireNoError(t, writeSealedSegment(dir, seg))
	ss, err := openSealedSegment(dir, DotProduct)
	requireNoError(t, err)
	requireNoError(t, ss.tombstoneSlot(1)) // tombstone "y"

	buckets, moved := packLiveDocs([]*sealedSegment{ss}, DotProduct, 50)
	got := 0
	for _, bk := range buckets {
		got += len(bk.slotDoc)
	}
	if got != 2 || len(moved) != 2 {
		t.Fatalf("packed %d docs (moved %d), want 2 (tombstoned excluded)", got, len(moved))
	}
}
```

### 5b. Run → expect FAIL (`undefined: packLiveDocs`).

### 5c. Minimal impl — append to `merge.go`

```go
// packLiveDocs streams the live (non-tombstoned) docs of the input sealed
// segments through eachLive and bin-packs them into in-memory *segment buckets of
// at most maxSegSize rows each, returning the buckets and the set of moved docIds.
//
// Vectors from eachLive are already in metric-natural stored form (cosine = unit
// + separate norm); segment.append stores them VERBATIM — do NOT re-run
// metric.prepare (would double-normalize, gotcha 1). append copies the slice and
// payload, so aliasing the input mmap is safe. eachLive holds each input's tomb
// RLock for a consistent per-segment snapshot. The returned moved set is the
// authoritative list of docs whose global segId the swap must rehome.
func packLiveDocs(inputs []*sealedSegment, metric Metric, maxSegSize int) (buckets []*segment, moved map[int64]bool) {
	moved = make(map[int64]bool)
	cur := newSegment(metric)
	buckets = append(buckets, cur)
	for _, ss := range inputs {
		ss.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
			if len(cur.slotDoc) >= maxSegSize {
				cur = newSegment(metric)
				buckets = append(buckets, cur)
			}
			cur.append(docID, stored, norm, ss.payload(slot))
			moved[docID] = true
		})
	}
	// Drop a trailing empty bucket (all inputs were fully tombstoned).
	if len(buckets) > 1 && len(buckets[len(buckets)-1].slotDoc) == 0 {
		buckets = buckets[:len(buckets)-1]
	}
	return buckets, moved
}
```

Note: `ss.payload(slot)` is called inside `eachLive`'s callback; `payload` takes no tomb lock (it reads the immutable payload map), so there is no re-entrant RLock (gotcha noted in sealed.go:159 only forbids tomb methods). Confirmed against sealed.go:145.

### 5d. Run → PASS, gates green.
### 5e. Commit `feat(vectorstore): packLiveDocs bin-packer (live docs → ≤maxSegSize buckets)`

---

## Task 6 — the merge machine: `mergeLocked` + `mergeAndPublish` (atomic swap)

The core. `mergeLocked(inputIDs)` runs under `s.mu` for the snapshot+swap and off-lock for the writes/build. This task implements the **happy path** (no concurrent writes) end-to-end; Task 7 red-proofs crash-safety and Task 8 red-proofs the delete-during-merge race (the reconciliation hook is implemented here so Task 8 only adds the test).

**Control flow (mirrors `sealLocked` order, minus head/WAL/idtable):**

1. Under `s.mu` (already held by caller): resolve `inputIDs` → `[]*sealedSegment` via `sealedByID`; if any missing, skip (already merged). `pack` the live docs into buckets, compute `moved`. **Snapshot taken under the lock.**
2. Release `s.mu` for the slow part (caller's `mergeLocked` returns the buckets+plan; `mergeAndPublish` does write+build off-lock). To keep the lock discipline identical to `buildAndPublish` (which builds off-lock then re-takes `s.mu`), we structure it as: `mergeLocked` does steps 1, 3–6 itself but **drops and re-acquires** is avoided — instead the slow writes happen while holding `s.mu`? No. We must not hold `s.mu` across the HNSW build. So `mergeLocked` performs the pack under the caller's `s.mu`, then the writes+build happen with the lock **released**, then the swap re-takes it. To express that without unlocking inside a "Locked" method, we split: `Compact()` (Task 9) takes `s.mu`, calls a pure planner `planMergeLocked`, **unlocks**, runs `mergeAndPublish` (write+build off-lock), which re-locks for the swap. We implement that split now.

To stay precise: this task introduces `planMergeLocked` (under lock) → `mergeAndPublish` (off-lock write+build, then re-lock swap). The public `Compact()` wiring is Task 9; this task tests via a thin internal `mergeNow` helper.

### 6a. Failing test — `merge_test.go`

```go
import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// TestMerge_CompactOfOne reclaims a heavy-tombstone single segment: a "merge of
// one" rewrites only the live docs into a fresh segment, the old dir is deleted,
// docIds survive, and Search still returns the live set. (architecture §4.9
// "单段 compact = merge 1 个".)
func TestMerge_CompactOfOne(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(3))
	dim := 8
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	live := map[int64][]float32{}
	for i := 0; i < 40; i++ {
		id := "d-" + itoa(i)
		v := randVec()
		requireNoError(t, s.Put(id, v, nil))
		live[s.idToDoc[id]] = v
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	oldDir := filepath.Join(s.dir, "seg-1-0")

	// Delete 60% → liveRatio 0.4 < mergeFloor; merge reclaims it.
	for i := 0; i < 40; i++ {
		if i%5 < 3 { // 60% deleted
			id := "d-" + itoa(i)
			requireNoError(t, s.Delete(id))
			delete(live, s.idToDoc[id])
		}
	}

	requireNoError(t, s.mergeNow([]segID{1}))
	requireNoError(t, s.WaitForMerge())

	// Old input dir gone (space reclaimed); a NEW segId replaced it.
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatal("merge did not delete the old input segment dir")
	}
	s.mu.RLock()
	nSealed := len(s.sealed)
	newID := s.sealedID[0]
	newCount := s.sealed[0].count()
	newTomb := s.sealed[0].tombCount()
	s.mu.RUnlock()
	if nSealed != 1 {
		t.Fatalf("sealed segments after merge = %d, want 1", nSealed)
	}
	if newID == segID(1) {
		t.Fatal("merge reused old segId; multi-id model requires a fresh segId")
	}
	if newCount != 16 || newTomb != 0 {
		t.Fatalf("merged seg count=%d tomb=%d, want 16 live / 0 tomb (repacked)", newCount, newTomb)
	}

	// docToSeg rehomed every surviving doc to the new segment.
	s.mu.RLock()
	for doc := range live {
		if s.docToSeg[doc] != newID {
			s.mu.RUnlock()
			t.Fatalf("doc %d not rehomed to merged segId %d (got %d)", doc, newID, s.docToSeg[doc])
		}
	}
	s.mu.RUnlock()

	// Survivors readable, deleted gone, recall holds.
	if _, _, found, _ := s.Get("d-0"); found { // d-0: i%5==0 → deleted
		t.Fatal("deleted d-0 resurrected by merge")
	}
	if _, _, found, _ := s.Get("d-3"); !found { // d-3: i%5==3 → live
		t.Fatal("live d-3 lost by merge")
	}
	var sum float64
	for it := 0; it < 20; it++ {
		q := randVec()
		got, err := s.Search(q, 5)
		requireNoError(t, err)
		sum += recallAtK(got, bruteForceKNN(Cosine, q, live, 5))
	}
	if avg := sum / 20; avg < 0.8 {
		t.Fatalf("post-merge recall@5 = %.3f, want >= 0.8", avg)
	}
}

// TestMerge_MultiInputRehomesOnlyMovedDocs proves the two-level id invariant: a
// 2-input merge updates docToSeg ONLY for the merged docs; a third untouched
// segment keeps its segId and its docs' mapping (architecture §4.6).
func TestMerge_MultiInputRehomesOnlyMovedDocs(t *testing.T) {
	s := openTestStore(t, DotProduct)
	mkSeg := func(ids []string) {
		for _, id := range ids {
			requireNoError(t, s.Put(id, []float32{float32(len(id)), 1, 0}, nil))
		}
		requireNoError(t, s.Seal())
	}
	mkSeg([]string{"a", "aa"})       // seg 1
	mkSeg([]string{"b", "bb"})       // seg 2
	mkSeg([]string{"c", "cc"})       // seg 3 (the bystander)
	requireNoError(t, s.WaitForIndex())

	cDoc, ccDoc := s.idToDoc["c"], s.idToDoc["cc"]

	requireNoError(t, s.mergeNow([]segID{1, 2}))
	requireNoError(t, s.WaitForMerge())

	s.mu.RLock()
	defer s.mu.RUnlock()
	// Bystander seg 3 still present with its original segId.
	if s.sealedByID(3) == nil {
		t.Fatal("untouched segment 3 vanished after merging 1+2")
	}
	if s.docToSeg[cDoc] != segID(3) || s.docToSeg[ccDoc] != segID(3) {
		t.Fatalf("bystander docs c/cc wrongly rehomed: %d,%d (want 3)", s.docToSeg[cDoc], s.docToSeg[ccDoc])
	}
	// Merged docs point at a brand-new segId that is not 1, 2, or 3.
	aDoc := s.idToDoc["a"]
	newID := s.docToSeg[aDoc]
	if newID == 1 || newID == 2 || newID == 3 || newID == headSegID {
		t.Fatalf("merged doc 'a' segId = %d, want a fresh sealed id", newID)
	}
	// Inputs 1 and 2 are gone from the set.
	if s.sealedByID(1) != nil || s.sealedByID(2) != nil {
		t.Fatal("merge inputs 1/2 still in the sealed set after swap")
	}
}
```

### 6b. Run → expect FAIL (`undefined: s.mergeNow`, `s.WaitForMerge`).

### 6c. Minimal impl

Append to `merge.go`:

```go
import (
	"os"
	"path/filepath"
)

// mergePlan is the under-lock snapshot a merge builds before releasing s.mu for
// the slow write+graph-build. It captures everything the off-lock phase needs and
// nothing that the live store can mutate underneath it.
type mergePlan struct {
	inputs   []segID
	inputSS  []*sealedSegment // parallel to inputs; kept mmap'd until the swap
	buckets  []*segment       // packed live docs (≤ maxSegSize each)
	outIDs   []segID          // fresh segIds, one per bucket (allocated under lock)
	outDirs  []string
	moved    map[int64]bool   // docs whose global segId the swap rehomes
}

// planMergeLocked resolves inputIDs, packs their live docs into buckets, and
// allocates a fresh segId + dir per bucket. Returns (nil, nil) if any input id is
// already gone (a concurrent merge won the race) — the caller treats that as a
// no-op. Caller holds s.mu. Allocating the output ids here (under the lock) keeps
// s.nextSeg monotonic and collision-free against concurrent seals.
func (s *Store) planMergeLocked(inputIDs []segID) (*mergePlan, error) {
	inputSS := make([]*sealedSegment, 0, len(inputIDs))
	for _, id := range inputIDs {
		ss := s.sealedByID(id)
		if ss == nil {
			return nil, nil // already merged/swept; nothing to do
		}
		inputSS = append(inputSS, ss)
	}
	buckets, moved := packLiveDocs(inputSS, s.metric, s.maxSegSize)
	if len(buckets) == 1 && len(buckets[0].slotDoc) == 0 {
		// All inputs fully tombstoned: no output, but the inputs must still be
		// dropped + their dirs deleted. Represent that as a plan with zero buckets.
		buckets = nil
	}
	p := &mergePlan{inputs: inputIDs, inputSS: inputSS, buckets: buckets, moved: moved}
	for range buckets {
		id := s.nextSeg
		s.nextSeg++
		p.outIDs = append(p.outIDs, id)
		p.outDirs = append(p.outDirs, filepath.Join(s.dir, segDirName(id, 0)))
	}
	return p, nil
}

// mergeAndPublish runs the SLOW phase off the store lock: write each output bucket
// to disk (fsync via writeSealedSegment) and reopen it. It then re-takes buildMu +
// s.mu for the atomic swap: reconcile any tombstones that arrived on the inputs
// during the off-lock window, mutate the segment set (drop inputs, add outputs,
// rehome moved docs), write the manifest ONCE (the commit point), then delete the
// old input dirs and spawn the background graph builds. Mirrors buildAndPublish's
// lock discipline (build off-lock → buildMu → s.mu → install + writeManifestLocked)
// and sealLocked's commit order (data durable → manifest swap → delete old).
func (s *Store) mergeAndPublish(p *mergePlan) error {
	defer s.merges.Done()
	if p == nil {
		return nil
	}

	// (1) Off-lock: write + reopen every output bucket. Data files fsync inside
	// writeSealedSegment (+ dir fsync) BEFORE the manifest will reference them.
	outSS := make([]*sealedSegment, len(p.buckets))
	for i, bk := range p.buckets {
		if err := writeSealedSegment(p.outDirs[i], bk); err != nil {
			s.abortMerge(p, outSS, i)
			return err
		}
		ss, err := openSealedSegment(p.outDirs[i], s.metric)
		if err != nil {
			s.abortMerge(p, outSS, i)
			return err
		}
		outSS[i] = ss
	}

	// (2) Swap under buildMu (serializes manifest rewrites vs builders) + s.mu.
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	s.mu.Lock()

	// (2a) Reconcile the off-lock window: a concurrent Delete/Put may have
	// tombstoned (or rehomed to head) an input doc AFTER the pack snapshot. Such a
	// doc must NOT be live in the output. For every doc we moved, if it is no
	// longer mapped to ITS INPUT segment in docToSeg, tombstone it in whatever
	// output bucket carries it. docToSeg is the single source of truth for which
	// segment owns a live doc (§4.6), so this is the exact liveness gate.
	inputSet := make(map[segID]bool, len(p.inputs))
	for _, id := range p.inputs {
		inputSet[id] = true
	}
	for i, ss := range outSS {
		for slot := 0; slot < ss.count(); slot++ {
			doc := ss.slotDoc(slot)
			owner, ok := s.docToSeg[doc]
			if !ok || !inputSet[owner] {
				// Deleted, or rehomed to head/another seg during the merge window.
				_ = ss.tombstoneSlot(slot)
			}
			_ = i
		}
	}

	// (2b) Drop inputs from the parallel sealed slices (delete by INDEX to keep
	// s.sealed and s.sealedID aligned — gotcha 6), closing + scheduling dir delete.
	for _, id := range p.inputs {
		for i := 0; i < len(s.sealedID); i++ {
			if s.sealedID[i] == id {
				s.sealed[i].close()
				s.sealed = append(s.sealed[:i], s.sealed[i+1:]...)
				s.sealedID = append(s.sealedID[:i], s.sealedID[i+1:]...)
				break
			}
		}
		delete(s.graphs, id)
	}

	// (2c) Append outputs (pending) and rehome the surviving moved docs. A doc
	// tombstoned in 2a is no longer live in the output, so it is not (re)mapped.
	for i, ss := range outSS {
		s.sealed = append(s.sealed, ss)
		s.sealedID = append(s.sealedID, p.outIDs[i])
		for slot := 0; slot < ss.count(); slot++ {
			if !ss.tombGet(slot) {
				s.docToSeg[ss.slotDoc(slot)] = p.outIDs[i]
			}
		}
	}

	// (2d) ONE atomic manifest swap — the commit point replacing N inputs with M
	// outputs. A crash before this leaves the outputs unreferenced (swept on
	// recover); a crash after leaves the inputs unreferenced (swept). No
	// alloc.Commit / wal.Reset: idtable mappings for moved docs are already durable
	// and the head/WAL is untouched (gotcha 4).
	if err := s.writeManifestLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	inputDirs := make([]string, len(p.inputs))
	for i, id := range p.inputs {
		inputDirs[i] = filepath.Join(s.dir, segDirName(id, 0))
	}
	s.mu.Unlock()

	// (3) Delete old input dirs AFTER the swap committed (now orphans).
	for _, dir := range inputDirs {
		_ = os.RemoveAll(dir)
	}

	// (4) Background-build each output's HNSW (off-lock, like seal). Reuse the
	// builds WaitGroup so Close() drains them; buildAndPublish flips pending→indexed.
	for i, ss := range outSS {
		s.builds.Add(1)
		go s.buildAndPublish(p.outIDs[i], p.outDirs[i], ss)
	}
	return nil
}

// abortMerge cleans up partially-written output dirs when an off-lock write fails
// before the swap. The inputs are untouched (still referenced by the live
// manifest), so the store stays consistent; the half-written outputs are removed
// here and would also be swept on the next recover (defense in depth).
func (s *Store) abortMerge(p *mergePlan, outSS []*sealedSegment, upto int) {
	for i := 0; i < upto; i++ {
		if outSS[i] != nil {
			outSS[i].close()
		}
		_ = os.RemoveAll(p.outDirs[i])
	}
}

// mergeNow is the synchronous-launch test helper: plan under the lock, then run
// mergeAndPublish on a tracked goroutine. WaitForMerge() awaits completion.
func (s *Store) mergeNow(inputIDs []segID) error {
	s.mu.Lock()
	p, err := s.planMergeLocked(inputIDs)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if p == nil {
		s.mu.Unlock()
		return nil
	}
	s.merges.Add(1)
	s.mu.Unlock()
	go func() { _ = s.mergeAndPublish(p) }()
	return nil
}

// WaitForMerge blocks until every in-flight merge has published (or aborted). It
// does NOT wait for the merged segments' background graph builds — use
// WaitForIndex for that. Mirrors WaitForIndex.
func (s *Store) WaitForMerge() error {
	s.merges.Wait()
	return nil
}
```

Edit `store.go`: add the `merges` WaitGroup next to `builds` (store.go:65):

```go
	buildMu sync.Mutex     // serializes builder graph install + manifest rewrite
	builds  sync.WaitGroup // tracks in-flight background builds (WaitForIndex/Close)
	merges  sync.WaitGroup // tracks in-flight merges (WaitForMerge/Close)
```

Edit `Close()` (store.go:248) to drain merges before builds (a merge spawns builds, so merges must settle first, and both before `s.mu`):

```go
func (s *Store) Close() error {
	s.merges.Wait()
	s.builds.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
```

**Lock-discipline check (gotcha 8):** `Close` waits `merges` then `builds` before taking `s.mu`; `mergeAndPublish` and `buildAndPublish` both take `s.mu` only transiently. No deadlock. `mergeAndPublish` takes `buildMu` then `s.mu` in the same order as `buildAndPublish` (store.go:702-705) — no lock-order inversion.

### 6d. Run → PASS (both merge tests), gates green (including `-race`).
### 6e. Commit `feat(vectorstore): merge machine — pack+writeSealed+atomic manifest swap+delete-old`

---

## Task 7 — crash-safety: orphan sweep before/after swap + recovery mid-build (red-proofed)

Three concrete crash windows, each forced deterministically and proven by a test that would resurrect data or leak a dir if the swap/sweep were wrong. No new product code if Task 6 is correct — these tests **lock in** that `sweepOrphansLocked` + `recover` cover merge crashes. (If any fails, the fix lives in `mergeAndPublish` ordering or `recover`.)

### 7a. Failing tests — `merge_crash_test.go` (new file)

```go
package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// crashBeforeSwap: a merge wrote its output dir but crashed BEFORE the manifest
// swap. The output is unreferenced → recover must sweep it, and the inputs must
// stay live (no data loss). We simulate by manually writing an output seg dir that
// the committed manifest does not list, then reopening.
func TestMergeCrash_BeforeSwap_OutputSwept(t *testing.T) {
	kvStore := newTestKV(t)
	s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	rng := rand.New(rand.NewSource(11))
	for i := 0; i < 30; i++ {
		v := make([]float32, 8)
		for d := range v {
			v[d] = rng.Float32()
		}
		requireNoError(t, s.Put("d-"+itoa(i), v, nil))
	}
	requireNoError(t, s.Seal()) // seg-1-0 (the input, committed in the manifest)
	requireNoError(t, s.WaitForIndex())

	// Simulate the merge's pre-swap state: a fully-written output seg-9-0 that the
	// manifest never referenced (crash happened before writeManifestLocked).
	outDir := filepath.Join(s.dir, "seg-9-0")
	seg := newSegment(Cosine)
	st, nrm := Cosine.prepare([]float32{1, 0, 0, 0, 0, 0, 0, 0})
	doc, _ := s.docIDForAlloc("ghost")
	seg.append(doc, st, nrm, nil)
	requireNoError(t, writeSealedSegment(outDir, seg))

	s2 := reopenStore(t, s, kvStore)

	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Fatal("crash-before-swap: unreferenced merge output seg-9-0 not swept")
	}
	if _, err := os.Stat(filepath.Join(s2.dir, "seg-1-0")); err != nil {
		t.Fatalf("crash-before-swap: input seg-1-0 wrongly removed: %v", err)
	}
	// Input docs survive (no loss).
	if _, _, found, _ := s2.Get("d-5"); !found {
		t.Fatal("crash-before-swap: input doc d-5 lost")
	}
	// The ghost (output-only) doc did NOT leak in.
	if _, _, found, _ := s2.Get("ghost"); found {
		t.Fatal("crash-before-swap: orphan output doc leaked into the store")
	}
}

// crashAfterSwap: the merge committed (manifest lists the new output) but crashed
// BEFORE deleting the old input dirs. On recover the inputs are unreferenced →
// swept; the output's docs are authoritative. We drive a real merge, then resurrect
// a stale input dir on disk to prove the post-swap sweep reclaims it.
func TestMergeCrash_AfterSwap_OldInputSwept(t *testing.T) {
	kvStore := newTestKV(t)
	s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	rng := rand.New(rand.NewSource(12))
	live := map[int64][]float32{}
	for i := 0; i < 40; i++ {
		v := make([]float32, 8)
		for d := range v {
			v[d] = rng.Float32()
		}
		requireNoError(t, s.Put("d-"+itoa(i), v, nil))
		live[s.idToDoc["d-"+itoa(i)]] = v
	}
	requireNoError(t, s.Seal()) // seg-1-0
	requireNoError(t, s.WaitForIndex())
	requireNoError(t, s.mergeNow([]segID{1}))
	requireNoError(t, s.WaitForMerge())
	requireNoError(t, s.WaitForIndex())

	// The merge deleted seg-1-0. Simulate a crash that committed the swap but did
	// NOT delete the old input: recreate seg-1-0 on disk (now an orphan).
	staleInput := filepath.Join(s.dir, "seg-1-0")
	requireNoError(t, os.MkdirAll(staleInput, 0755))
	requireNoError(t, os.WriteFile(filepath.Join(staleInput, "vectors.dat"), []byte("stale"), 0644))

	s2 := reopenStore(t, s, kvStore)

	if _, err := os.Stat(staleInput); !os.IsNotExist(err) {
		t.Fatal("crash-after-swap: stale input seg-1-0 not swept on recovery")
	}
	// The merged output survived recovery with all live docs.
	for i := 0; i < 40; i++ {
		if _, _, found, _ := s2.Get("d-" + itoa(i)); !found {
			t.Fatalf("crash-after-swap: doc d-%d lost", i)
		}
	}
}

// crashMidBuild: a merge committed its output as PENDING (manifest state pending,
// no graph) then crashed before the background build finished. recover must
// re-spawn the build and the output must end up indexed + searchable.
func TestMergeCrash_MidBuild_RecoverResumes(t *testing.T) {
	kvStore := newTestKV(t)
	s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	rng := rand.New(rand.NewSource(13))
	live := map[int64][]float32{}
	for i := 0; i < 50; i++ {
		v := make([]float32, 8)
		for d := range v {
			v[d] = rng.Float32()
		}
		requireNoError(t, s.Put("d-"+itoa(i), v, nil))
		live[s.idToDoc["d-"+itoa(i)]] = v
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	requireNoError(t, s.mergeNow([]segID{1}))
	requireNoError(t, s.WaitForMerge()) // swap done; output published (maybe still pending)

	// The output segId is whatever replaced seg 1.
	s.mu.RLock()
	outID := s.sealedID[0]
	s.mu.RUnlock()

	// Crash here without waiting for the build → reopen. recover() must resume the
	// pending build for the merged output.
	s2 := reopenStore(t, s, kvStore)
	requireNoError(t, s2.WaitForIndex())
	if !s2.isIndexedForTest(outID) {
		t.Fatalf("crash-mid-build: merged output seg %d not re-indexed on recovery", outID)
	}
	var sum float64
	for it := 0; it < 20; it++ {
		q := make([]float32, 8)
		for d := range q {
			q[d] = rng.Float32()
		}
		got, err := s2.Search(q, 5)
		requireNoError(t, err)
		sum += recallAtK(got, bruteForceKNN(Cosine, q, live, 5))
	}
	if avg := sum / 20; avg < 0.8 {
		t.Fatalf("crash-mid-build: post-recovery recall@5 = %.3f, want >= 0.8", avg)
	}
}
```

### 7b. Run → expect FAIL for `TestMergeCrash_MidBuild_RecoverResumes` **if** `recover`'s pending-resume loop (store.go:194) hard-codes `segDirName(sid, 0)` and the merged output's `Gen` is non-zero. Since this plan keeps merge outputs at `Gen=0` (Task 6 allocates fresh ids via `s.nextSeg++`, leaving `Gen:0` in `writeManifestLocked`), `segDirName(sid, 0)` resolves correctly and this should **PASS**. The other two should **PASS** on Task 6 code.

Run the suite first to confirm. **Expected before any fix:** all three PASS (they validate Task 6). If `TestMergeCrash_MidBuild_RecoverResumes` fails, it pinpoints the gen-hardcode hazard (gotcha 6 from scouting). Because we deliberately keep merge outputs at `Gen=0`, no fix is needed — but the test is the guard that makes that decision load-bearing and red-proofed.

> If, contrary to expectation, the mid-build test fails, the fix is a one-line `recover` change at store.go:194 to use the entry's real `Gen`:
> ```go
> // before: segDir := filepath.Join(s.dir, segDirName(sid, 0))
> // after:  use the manifest entry's Gen instead of hardcoded 0
> ```
> and to thread the entry through the resume loop. Keeping outputs at Gen=0 avoids needing this; the test enforces the invariant either way.

### 7c. Impl — none expected (these lock in Task 6). If a test reveals a gap, apply the minimal `recover`/`mergeAndPublish` fix it points to.

### 7d. Run → all three PASS, gates green (`-race` included).
### 7e. Commit `test(vectorstore): red-proof merge crash-safety (orphan sweep before/after swap, mid-build recovery)`

---

## Task 8 — delete-during-merge reconciliation (red-proofed, `-race`)

The single biggest new correctness problem (Architecture §). The reconciliation hook lives in `mergeAndPublish` step 2a (Task 6); this task proves it with a deterministic concurrent scenario and the `-race` gate. We force the window by deleting docs *after* the plan snapshot but *before* the swap using a test seam: `planMergeLocked` returns the plan, the test deletes, then drives `mergeAndPublish` manually.

### 8a. Failing test — `merge_concurrent_test.go` (new file)

```go
package vectorstore

import (
	"math/rand"
	"testing"
)

// TestMerge_DeleteDuringWindow_NotResurrected proves the reconciliation gate: a
// doc deleted AFTER the merge's live-set snapshot but BEFORE the atomic swap must
// not come back to life in the output segment. We open the window explicitly:
// plan (snapshot) under the lock, release, Delete two docs, then publish. The
// output must tombstone those docs at swap time (docToSeg is the liveness oracle).
func TestMerge_DeleteDuringWindow_NotResurrected(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(21))
	dim := 8
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	live := map[int64][]float32{}
	for i := 0; i < 30; i++ {
		id := "d-" + itoa(i)
		v := randVec()
		requireNoError(t, s.Put(id, v, nil))
		live[s.idToDoc[id]] = v
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	// (1) Snapshot the merge plan under the lock (docs d-0..d-29 all live).
	s.mu.Lock()
	p, err := s.planMergeLocked([]segID{1})
	requireNoError(t, err)
	s.merges.Add(1)
	s.mu.Unlock()

	// (2) In the OFF-LOCK window, delete two docs that the plan captured as live.
	requireNoError(t, s.Delete("d-7"))
	requireNoError(t, s.Delete("d-13"))
	delete(live, s.idToDoc["d-7"])
	delete(live, s.idToDoc["d-13"])

	// (3) Publish: the swap must reconcile the two late deletes.
	requireNoError(t, s.mergeAndPublish(p))
	requireNoError(t, s.WaitForIndex())

	// The deleted docs must be gone everywhere.
	if _, _, found, _ := s.Get("d-7"); found {
		t.Fatal("d-7 deleted during merge window resurrected in the output")
	}
	if _, _, found, _ := s.Get("d-13"); found {
		t.Fatal("d-13 deleted during merge window resurrected in the output")
	}
	// They must not appear in Search results either.
	for it := 0; it < 30; it++ {
		got, err := s.Search(randVec(), 30)
		requireNoError(t, err)
		for _, r := range got {
			if r.DocID == s.idToDoc["d-7"] || r.DocID == s.idToDoc["d-13"] {
				t.Fatalf("deleted-during-merge doc %d appeared in Search", r.DocID)
			}
		}
	}
	// Survivors intact + recall holds.
	var sum float64
	for it := 0; it < 20; it++ {
		q := randVec()
		got, err := s.Search(q, 5)
		requireNoError(t, err)
		sum += recallAtK(got, bruteForceKNN(Cosine, q, live, 5))
	}
	if avg := sum / 20; avg < 0.8 {
		t.Fatalf("post-merge recall@5 = %.3f, want >= 0.8", avg)
	}
}

// TestMerge_PutRehomeDuringWindow_NotDuplicated: a concurrent Put re-homes an
// input doc to the head DURING the merge window. The merged output must NOT also
// claim it live (docToSeg now points at head, not the input) — no duplicate live
// copy, and Get returns the new head vector.
func TestMerge_PutRehomeDuringWindow_NotDuplicated(t *testing.T) {
	s := openTestStore(t, DotProduct)
	for i := 0; i < 10; i++ {
		requireNoError(t, s.Put("d-"+itoa(i), []float32{float32(i), 1, 0}, nil))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	s.mu.Lock()
	p, err := s.planMergeLocked([]segID{1})
	requireNoError(t, err)
	s.merges.Add(1)
	s.mu.Unlock()

	// Re-Put d-4 with a new vector during the window → rehomed to head.
	requireNoError(t, s.Put("d-4", []float32{99, 99, 99}, nil))
	requireNoError(t, s.mergeAndPublish(p))
	requireNoError(t, s.WaitForIndex())

	d4 := s.idToDoc["d-4"]
	s.mu.RLock()
	owner := s.docToSeg[d4]
	s.mu.RUnlock()
	if owner != headSegID {
		t.Fatalf("d-4 owner after merge = %d, want headSegID (rehomed by concurrent Put)", owner)
	}
	v, _, found, err := s.Get("d-4")
	requireNoError(t, err)
	if !found || len(v) != 3 || v[0] != 99 {
		t.Fatalf("Get(d-4) = (%v, found=%v), want new head vector {99,99,99}", v, found)
	}
	// d-4 must appear exactly once across Search (no duplicate from the output).
	got, err := s.Search([]float32{99, 99, 99}, 20)
	requireNoError(t, err)
	count := 0
	for _, r := range got {
		if r.DocID == d4 {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("d-4 appears %d times in Search, want exactly 1", count)
	}
}
```

### 8b. Run → expect PASS if Task 6 step 2a is correct; the test is the red-proof. To confirm it is a real guard, temporarily comment out step 2a's `tombstoneSlot` reconciliation and re-run — `TestMerge_DeleteDuringWindow_NotResurrected` must FAIL ("resurrected in the output"). Restore the code. (This is the explicit red-proof the architecture demands for the highest-risk path.)

### 8c. Impl — already in Task 6 (step 2a). No new code unless the red-proof exposes a gap.

### 8d. Run → both PASS, gates green (`-race` is the key gate here — it exercises the off-lock build vs. concurrent Delete on `tombMu`).
### 8e. Commit `test(vectorstore): red-proof delete/put-during-merge reconciliation (no resurrect/dup)`

---

## Task 9 — public `Compact()` entry point (manual trigger)

The manual entry for tests + operators: pick *all* delete-driven candidates and merge each, plus run the growth-tiered pick once. Returns after launching merges (caller uses `WaitForMerge`).

### 9a. Failing test — `merge_trigger_test.go` (new file)

```go
package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// TestCompact_ReclaimsDeleteDrivenSegments: Compact() finds the heavy-tombstone
// segment via the delete driver and reclaims it with no explicit id list.
func TestCompact_ReclaimsHeavyTombstoneSegment(t *testing.T) {
	s := openTestStore(t, Cosine)
	s.mcfg.MergeFloor = 0.5
	rng := rand.New(rand.NewSource(31))
	dim := 8
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	for i := 0; i < 40; i++ {
		requireNoError(t, s.Put("d-"+itoa(i), randVec(), nil))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	oldDir := filepath.Join(s.dir, "seg-1-0")
	for i := 0; i < 40; i++ {
		if i%5 < 3 { // 60% deleted → liveRatio 0.4 < 0.5
			requireNoError(t, s.Delete("d-"+itoa(i)))
		}
	}

	requireNoError(t, s.Compact())
	requireNoError(t, s.WaitForMerge())

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatal("Compact did not reclaim the heavy-tombstone segment")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.sealed) != 1 || s.sealed[0].tombCount() != 0 {
		t.Fatalf("after Compact: nSealed=%d tomb=%d, want 1 seg / 0 tomb", len(s.sealed), s.sealed[0].tombCount())
	}
}

// TestCompact_NoOpWhenHealthy: nothing to reclaim → Compact is a no-op (segId stable).
func TestCompact_NoOpWhenHealthy(t *testing.T) {
	s := openTestStore(t, Cosine)
	for i := 0; i < 20; i++ {
		requireNoError(t, s.Put("d-"+itoa(i), []float32{float32(i), 1, 0, 0, 0, 0, 0, 0}, nil))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	requireNoError(t, s.Compact())
	requireNoError(t, s.WaitForMerge())

	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.sealed) != 1 || s.sealedID[0] != segID(1) {
		t.Fatalf("healthy Compact mutated the set: nSealed=%d id=%d", len(s.sealed), s.sealedID[0])
	}
}
```

### 9b. Run → expect FAIL (`undefined: s.Compact`).

### 9c. Minimal impl — append to `merge.go`

```go
// Compact runs one round of the reclamation policy synchronously-launched: it
// merges every delete-driven candidate (live ratio < mergeFloor) and, if a size
// tier has reached fanout, one growth-driven merge. It returns once the merges are
// launched on tracked goroutines; callers use WaitForMerge to await publication.
// A healthy store (no candidates) is a no-op. This is the manual entry point for
// tests and operator-triggered reclamation (architecture §4.9).
func (s *Store) Compact() error {
	s.mu.Lock()
	stats := s.segStatsLocked()
	var plans []*mergePlan
	// Delete-driven: each deflated segment is its own "merge of one" repack.
	for _, id := range pickDeleteDriven(stats, s.mcfg) {
		p, err := s.planMergeLocked([]segID{id})
		if err != nil {
			s.mu.Unlock()
			return err
		}
		if p != nil {
			plans = append(plans, p)
		}
	}
	// Growth-driven: one tier roll-up (re-snapshot excludes ids already planned).
	if g := pickGrowthTiered(s.statsExcludingLocked(stats, plans), s.mcfg); g != nil {
		p, err := s.planMergeLocked(g)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		if p != nil {
			plans = append(plans, p)
		}
	}
	s.merges.Add(len(plans))
	s.mu.Unlock()
	for _, p := range plans {
		p := p
		go func() { _ = s.mergeAndPublish(p) }()
	}
	return nil
}

// statsExcludingLocked returns stats minus any segment already claimed by a
// planned merge, so the growth pick never double-selects a delete-driven input.
func (s *Store) statsExcludingLocked(stats []segLiveStats, plans []*mergePlan) []segLiveStats {
	claimed := make(map[segID]bool)
	for _, p := range plans {
		for _, id := range p.inputs {
			claimed[id] = true
		}
	}
	out := stats[:0:0]
	for _, st := range stats {
		if !claimed[st.id] {
			out = append(out, st)
		}
	}
	return out
}
```

### 9d. Run → PASS, gates green.
### 9e. Commit `feat(vectorstore): Compact() manual reclamation entry (delete+growth drivers)`

---

## Task 10 — background merge trigger after seal

Wire the policy into the write path: after a successful `sealLocked`, opportunistically launch reclamation if conditions hold — so callers never need to call `Compact()`. Bounded: launches at most one round per seal, on tracked goroutines.

### 10a. Failing test — append to `merge_trigger_test.go`

```go
// TestAutoMerge_GrowthTriggersOnSeal: with a small fanout, sealing enough segments
// auto-triggers a growth-driven merge in the background — no manual Compact call.
func TestAutoMerge_GrowthTriggersOnSeal(t *testing.T) {
	s := openTestStore(t, Cosine)
	s.maxSegSize = 5      // tiny segments
	s.mcfg.Fanout = 3     // a tier of 3 same-sized segments triggers a roll-up
	s.mcfg.MergeFloor = 0 // disable delete driver for this test
	rng := rand.New(rand.NewSource(41))
	dim := 8
	// 15 puts at maxSegSize 5 → three sealed segments of 5 rows each (same tier).
	for i := 0; i < 15; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		requireNoError(t, s.Put("d-"+itoa(i), v, nil))
	}
	requireNoError(t, s.WaitForMerge())
	requireNoError(t, s.WaitForIndex())

	// The three tier-3 segments rolled up into one (count cap not hit).
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.sealed) != 1 {
		t.Fatalf("after auto-merge: nSealed=%d, want 1 (3-way growth roll-up)", len(s.sealed))
	}
	if s.sealed[0].count() != 15 {
		t.Fatalf("rolled-up segment count=%d, want 15", s.sealed[0].count())
	}
}
```

### 10b. Run → expect FAIL (sealed stays 3 — no auto-trigger).

### 10c. Minimal impl

Append to `merge.go`:

```go
// maybeMergeLocked is the background trigger: after a structural change (a seal),
// it checks the reclamation policy and launches at most one delete-driven repack
// AND one growth-driven roll-up on tracked goroutines. Caller holds s.mu. Launches
// nothing when the store is healthy, so it is cheap to call on every seal. The
// actual write+build runs off-lock in mergeAndPublish (the goroutine re-takes the
// lock only for the swap), so this never blocks the write path.
func (s *Store) maybeMergeLocked() {
	stats := s.segStatsLocked()
	var plans []*mergePlan
	for _, id := range pickDeleteDriven(stats, s.mcfg) {
		if p, err := s.planMergeLocked([]segID{id}); err == nil && p != nil {
			plans = append(plans, p)
		}
	}
	if g := pickGrowthTiered(s.statsExcludingLocked(stats, plans), s.mcfg); g != nil {
		if p, err := s.planMergeLocked(g); err == nil && p != nil {
			plans = append(plans, p)
		}
	}
	s.merges.Add(len(plans))
	for _, p := range plans {
		p := p
		go func() { _ = s.mergeAndPublish(p) }()
	}
}
```

Edit `sealLocked` in `store.go` to call the trigger after the background build is spawned (store.go:686, just before `return nil`):

```go
	s.builds.Add(1)
	go s.buildAndPublish(id, segDir, ss)

	// Opportunistic background reclamation: a fresh seal may push a size tier to
	// fanout (growth) or expose a deflated segment (delete-driven). Launches merges
	// off the write path; healthy stores no-op. (Phase 4, architecture §4.9.)
	s.maybeMergeLocked()
	return nil
}
```

**Re-entrancy / lock check:** `maybeMergeLocked` is called with `s.mu` held (inside `sealLocked` ← `Put`/`Seal`). It only allocates ids and spawns goroutines under that lock; `planMergeLocked` is lock-free w.r.t. acquiring (caller holds). The spawned `mergeAndPublish` takes `s.mu` later, after `sealLocked` (and the caller's `Put`/`Seal`) has released it. No deadlock, no recursion (the trigger never calls `sealLocked`).

### 10d. Run → PASS, gates green (`-race`).
### 10e. Commit `feat(vectorstore): background merge trigger on seal (delete+growth, off write path)`

---

## Task 11 — manifest round-trips merge outputs across recovery (end-to-end)

A final integration test: a churning workload (puts, deletes, seals, an auto-merge) survives a restart with stable recall and correct survivor/deleted sets — proving the derived `docToSeg` reconstructs purely from the post-merge on-disk `slotDoc` + tombstones (gotcha 7). No new product code expected.

### 11a. Failing test — append to `merge_crash_test.go`

```go
// TestMerge_SurvivesRecovery_EndToEnd: a full churn → seal → merge → restart cycle.
// docToSeg is derived from on-disk slotDoc over live slots, so the merged set must
// reconstruct exactly on reopen (architecture §4.6/§4.8).
func TestMerge_SurvivesRecovery_EndToEnd(t *testing.T) {
	kvStore := newTestKV(t)
	s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	s.maxSegSize = 8
	s.mcfg.Fanout = 2
	s.mcfg.MergeFloor = 0.5
	rng := rand.New(rand.NewSource(51))
	dim := 8
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	live := map[int64][]float32{}
	for i := 0; i < 64; i++ {
		id := "d-" + itoa(i)
		v := randVec()
		requireNoError(t, s.Put(id, v, nil))
		live[s.idToDoc[id]] = v
	}
	// Delete a third to make some segments merge-bait.
	for i := 0; i < 64; i++ {
		if i%3 == 0 {
			requireNoError(t, s.Delete("d-"+itoa(i)))
			delete(live, s.idToDoc["d-"+itoa(i)])
		}
	}
	requireNoError(t, s.Compact())
	requireNoError(t, s.WaitForMerge())
	requireNoError(t, s.WaitForIndex())

	s2 := reopenStore(t, s, kvStore)
	requireNoError(t, s2.WaitForIndex())

	// Every survivor present, every deleted absent.
	for i := 0; i < 64; i++ {
		id := "d-" + itoa(i)
		_, _, found, err := s2.Get(id)
		requireNoError(t, err)
		wantLive := i%3 != 0
		if found != wantLive {
			t.Fatalf("after recovery Get(%s) found=%v, want %v", id, found, wantLive)
		}
	}
	var sum float64
	for it := 0; it < 30; it++ {
		q := randVec()
		got, err := s2.Search(q, 5)
		requireNoError(t, err)
		sum += recallAtK(got, bruteForceKNN(Cosine, q, live, 5))
	}
	if avg := sum / 30; avg < 0.8 {
		t.Fatalf("post-recovery (merged) recall@5 = %.3f, want >= 0.8", avg)
	}
}
```

### 11b. Run → expect PASS (locks in the full pipeline). If it fails, it points at a `docToSeg` reconstruction or manifest-entry bug in `mergeAndPublish` step 2.

### 11c. Impl — none expected.
### 11d. Run → PASS, gates green.
### 11e. Commit `test(vectorstore): end-to-end churn→merge→recovery preserves survivors + recall`

---

## Final state (all gates green after every task)

- **Tree adds:** `merge.go`, `mergepolicy.go`, `merge_test.go`, `mergepolicy_test.go`, `merge_crash_test.go`, `merge_concurrent_test.go`, `merge_trigger_test.go`. **Edits:** `store.go` (`mcfg` field + init, `merges` WaitGroup, `Close` drain order, `sealLocked` trigger hook).
- **One merge machine** (`packLiveDocs` → `writeSealedSegment`/`openSealedSegment` → atomic `writeManifestLocked` swap → `os.RemoveAll` old → background `buildAndPublish`), **two drivers** (`pickDeleteDriven`, `pickGrowthTiered`), single-segment compact as a merge-of-one.
- **Crash-safety** entirely on reused Phase-2 primitives (`sweepOrphansLocked`, `recover` pending-resume), with three forced crash windows red-proofed (before swap / after swap / mid-build).
- **Delete-during-merge reconciliation** implemented (step 2a) and red-proofed under `-race` (the architecture's highest-risk path).
- **Observability:** `Compact()`, `WaitForMerge()`, background trigger on seal.
- **Invariants honored:** two-level id (fresh segId for merge, `docToSeg` rehomed only for moved docs, bystander segments stable, `Gen=0` kept so the `recover` resume path stays correct), metric-natural storage preserved (no double-`prepare`), no `alloc.Commit`/`wal.Reset` in the merge path, index-aligned `s.sealed`/`s.sealedID` deletes, single manifest version bump per swap.
- **`core/vectorindex` untouched.**

Key grounding file paths: `/workspace/haystack/core/vectorstore/store.go`, `seal.go`, `sealed.go`, `segment.go`, `builder.go`, `manifest.go`, `graphstore.go`, `result.go`, `metric.go`; harness `/workspace/haystack/core/vectorstore/storehelpers_test.go`, `recovery_test.go`, `store_segset_test.go`, `orphan_test.go`; architecture `/workspace/haystack/core/docs/vectorstore/architecture.md` (§4.5/§4.6/§4.8/§4.9).

---

## Adversarial review — fixes to apply during execution

> Plan is the workflow **draft**; its 5-dimension adversarial review produced 22 findings. The **8 critical/high** below MUST be applied as you implement the relevant task (per-group + final adversarial review enforce them).

1. **[CRITICAL] refactor-safety** — merge.go mergeAndPublish (defer s.merges.Done() at top) + s.builds.Add(1) at step 4; store.go Close() merges.Wait() then builds.Wait()
   - issue: WaitGroup add-after-wait race that breaks Close and -race. mergeAndPublish does `defer s.merges.Done()` at function entry, but only calls `s.builds.Add(1)` for the output graph builds at step 4, AFTER the swap and AFTER the deferred Done() will eventually fire. More precisely: Close() does s.merges.Wait() THEN s.builds.Wait(). A merge goroutine can reach the end of mergeAndPublish, run `s.builds.Add(1); go s.buildAndPublish(...)`, then return so `defer s.merges.Done()` fires. But the plan claims Close drains merges 'first because a merge spawns builds, so merges must settle first' — the ordering only holds if every builds.Add happens-before the merges.Done of the SAME merge AND before Close passes merges.Wait(). It does within one goroutine (Add at step 4 precedes the return that fires the defer). However Task 10's maybeMergeLocked is called UNDER s.mu inside sealLocked, while a concurrent Close() is blocked on s.mu? No — Close does merges.Wait()/builds.Wait() BEFORE taking s.mu. So Close can be in merges.Wait() while a Put->sealLocked->maybeMergeLocked is concurrently doing s.merges.Add(len(plans)). `sync.WaitGroup: Add called concurrently with Wait when counter is zero` is a documented panic. With Close racing an auto-merge trigger this is a real panic, and -race/CI will catch it intermittently (flaky tree). The whole merges-vs-builds-vs-Close ordering is under-specified.
   - fix: Pick ONE WaitGroup discipline and prove it: (a) gate all merge launches behind a `closing` flag checked under s.mu so maybeMergeLocked/Compact never Add after Close started; AND (b) have mergeAndPublish increment a SINGLE builds-style WaitGroup for its own build before merges.Done, or fold merge tracking so Close drains a single 'background work' WaitGroup. Add a test: concurrent Close() + auto-merge-on-seal under -race, run 100x. Until that test is green the tree is not reliably green after Task 10.
2. **[HIGH] architecture-fidelity** — mergepolicy.go pickGrowthTiered (Task 4c) + merge.go packLiveDocs maxSegSize bucketing (Task 5c); architecture.md §4.9 growth driver + 'TargetSegCount' never read
   - issue: GROWTH DRIVER DEVIATES FROM ARCHITECTURE — it does not bound total segment count, and contradicts §4.9's 'merge a tier into the NEXT tier'. The plan's pickGrowthTiered (Task 4) merges a same-size tier's LIVE rows into ONE bucket capped at MaxMergedSize. But because packLiveDocs splits into ceil(liveSum/maxSegSize) buckets, and Compact/maybeMerge re-snapshot fresh, a tier of K segments each of ~maxSegSize live rows produces K new segments of ~maxSegSize each — i.e. it merges K small segments into K segments of the SAME tier, reducing count by ZERO. The architecture's whole point (§4.9: 'size-tiered 合上一层 … 压总段数', 'k-NN 必搜所有段 → 读放大 = 总段数 → 段数封顶') is that a tier roll-up roughly DOUBLES segment size and HALVES the tier population so total count stays logarithmic. The plan's bin-pack-to-maxSegSize defeats that: full same-tier segments never consolidate into a larger tier, the count cap (TargetSegCount) is defined but never enforced anywhere, and a churning-but-growing store can sit at K full segments per tier forever. This is the central architecture-fidelity miss.
   - fix: Make growth-driven merge actually grow segments: for the growth path, pack into buckets of the NEXT tier's target size (≈ K×maxSegSize or up to MaxMergedSize), NOT maxSegSize, so K tier-t segments fold into ~1 tier-(t+log2 K) segment. Keep the maxSegSize bucketing ONLY for the delete-driven repack (whose goal is reclaiming tombstones, not growing). Add an explicit guard that wires TargetSegCount into the policy (e.g. force a growth merge when len(sealed) > TargetSegCount) so the §4.9 'bound total segment count' invariant is testable and enforced, and add a test asserting count strictly decreases after a growth roll-up of full segments. Without this the growth driver is a no-op for the case it exists to solve.
3. **[HIGH] architecture-fidelity** — store.go sealLocked trigger hook (Task 10c) + merge.go maybeMergeLocked (Task 10c)
   - issue: AUTO-MERGE-ON-SEAL CAN MERGE A JUST-SEALED PENDING SEGMENT BEFORE ITS GRAPH IS BUILT, AND CAN THRASH. sealLocked spawns buildAndPublish(id) then immediately calls maybeMergeLocked() (Task 10). With a small Fanout, the freshly-sealed segment is eligible as a growth input on the SAME call: maybeMergeLocked re-snapshots, picks it, and mergeAndPublish writes a new segment + spawns a NEW build — discarding the build that was just launched (wasted HNSW build, the dominant cost per §5/§4.9). Worse, every Put that triggers a seal re-runs the policy, so under steady write load the store can continuously merge segments that were just produced (build → merge → build → merge), exactly the 'building young segments is waste' anti-pattern §4.2/§5 warns against. The plan's TestAutoMerge test (Task 10a) actively demonstrates merging 3 brand-new segments whose builds may not have run.
   - fix: Do not let a segment become a merge input until it has had a chance to index (or gate growth on indexed segments / a minimum age), and rate-limit the trigger (e.g. only evaluate growth when len(sealed) crosses TargetSegCount, only evaluate delete-driven for segments with real tombstones). Add a test that asserts a steady Put stream does NOT cause repeated build→merge→build of the same logical rows (measure build invocations, per measure-dont-assert).
4. **[HIGH] architecture-fidelity** — merge_crash_test.go TestMergeCrash_BeforeSwap_OutputSwept (Task 7a)
   - issue: CRASH-BEFORE-SWAP TEST DOES NOT EXERCISE THE REAL CODE PATH — it is a fabricated scenario, not a forced crash of mergeAndPublish. Task 7's TestMergeCrash_BeforeSwap_OutputSwept hand-writes a 'seg-9-0' dir with a never-referenced docId and reopens, asserting sweepOrphansLocked removes it. That only re-tests Phase-2 orphan sweep over an arbitrary stray dir; it never runs planMergeLocked/mergeAndPublish at all, so it proves nothing about the merge's actual pre-swap state (output dirs at the real output segIds, the real inputs still referenced, the real nextSeg accounting). A genuine bug — e.g. mergeAndPublish writing the manifest BEFORE all output dirs are fsynced, or allocating output ids that collide with a concurrent seal — would pass this test. The plan even admits 'No new product code if Task 6 is correct', i.e. these tests are not red-proofed against a seeded merge-path defect.
   - fix: Inject the crash inside the real merge path: add a test seam (e.g. a hook called right after writeSealedSegment of the outputs and BEFORE writeManifestLocked) that panics/returns, run a real mergeNow, then reopenStore and assert the real output dirs (p.outDirs) are swept AND the real inputs survive with all docs. Red-proof it by also asserting that if the manifest swap is (wrongly) moved before the output fsync, recovery loses data.
5. **[HIGH] refactor-safety** — Task 7 TestMergeCrash_AfterSwap_OldInputSwept; reopenStore in recovery_test.go:11-19
   - issue: The 'crash-after-swap' test does not test a crash-after-swap window and proves nothing about merge crash-safety. reopenStore calls s.Close(), which (per the plan's own Close edit) does s.merges.Wait() then s.builds.Wait() — so by the time the store reopens, the real merge has FULLY completed and already os.RemoveAll'd seg-1-0. The test then manually recreates seg-1-0 and asserts recovery sweeps it. That only exercises sweepOrphansLocked on an arbitrary stray dir — identical to the existing orphan_test coverage — not the merge commit/delete ordering. A genuine bug where mergeAndPublish wrote the manifest but a panic occurred before os.RemoveAll would be invisible to this test because Close() forces the delete to finish first.
   - fix: To actually red-proof crash-after-swap you must interpose BEFORE the os.RemoveAll: e.g. a test seam (injected hook/abort point) in mergeAndPublish between writeManifestLocked and the dir deletion, OR drive mergeAndPublish manually (like Task 8 does) and skip the post-swap RemoveAll, then reopen the SAME store dir over a fresh Store WITHOUT Close (open a second Store on the dir, accepting the WAL/mmap is still held — or copy the dir). Assert the committed manifest references the new seg and recovery sweeps the still-present old input. Mark the current test as what it is (a sweep test) or delete it.
6. **[HIGH] refactor-safety** — merge.go packLiveDocs + planMergeLocked: maxSegSize as bucket cap
   - issue: Bin-packing into buckets of s.maxSegSize directly contradicts architecture §4.9 'bin-pack live 文档进 ~maxSegSize 桶' only for the delete driver, but the GROWTH driver is size-tiered: merging K tier-t segments must produce ONE segment in tier t+1, not split it back into maxSegSize buckets. With maxSegSize=defaultMaxSegSize(50000) and small test segments this never bites, but at production scale a growth merge of K~8 segments of ~50k each = 400k live docs gets re-split into 8 buckets of 50k — i.e. the growth driver produces 8 outputs of the SAME tier it just merged, making ZERO progress on bounding segment count (the entire point of the growth driver, §4.9 '压住段数'). The plan packs both drivers through the same maxSegSize cap, so pickGrowthTiered's MaxMergedSize cap and the K-into-next-tier invariant are silently defeated. The architecture's bucket target is maxSegSize ONLY because that is also roughly the next tier for delete-driven repack of deflated segments; for growth it must be one (capped) output.
   - fix: Either (a) document and enforce that growth merges use a single output bucket capped at MaxMergedSize (not maxSegSize) so K small segments fold to one larger segment, or (b) prove via a test (count segments before/after a growth merge of K~Fanout segments where K*segSize > maxSegSize) that total segment count strictly decreases. Add that assertion to TestAutoMerge_GrowthTriggersOnSeal with segments large enough that K*size > maxSegSize — the current test uses 5-row segments so the split never triggers and the bug is masked.
7. **[HIGH] refactor-safety** — merge.go mergeAndPublish step 2a reconciliation vs recover() WAL-skip guard (store.go:316-322, 389-397)
   - issue: The delete-during-merge reconciliation only handles Delete and Put-rehome-to-head, but misses the cross-segment Update tombstone interaction with recovery, AND the reconciliation's liveness oracle is wrong for a doc re-Put into the head DURING the window. When a concurrent Put re-homes an input doc to head, Put (store.go:389) tombstones the doc's slot IN THE INPUT sealedSegment. After the swap, mergeAndPublish has already snapshotted that doc as live (it was live at pack time) and written it into the output. Step 2a checks `s.docToSeg[doc]` != input segId → tombstones it in the output. Good. BUT: the output segment's tombstone is set via ss.tombstoneSlot under s.mu, AFTER writeSealedSegment already persisted a tomb.dat with that bit CLEAR; tombstoneSlot msyncs it, so it is durable. That part works. The real gap: if the store CRASHES after the manifest swap (output committed) but the reconciliation tombstones in 2a were applied to the mmap and msync'd BEFORE the manifest write — order in the code is 2a (tombstone) → 2b/2c → 2d (manifest). Fine. But there is NO test that crashes between 2a and 2d to prove the reconciled tombstones survive in tomb.dat independent of the manifest. The plan asserts 'all three crash windows covered' but the window that matters for reconciliation (crash after reconcile-tombstone, before/after manifest) is untested.
   - fix: Add a crash test: open the merge window (planMergeLocked), Delete an input doc, drive mergeAndPublish to just past the manifest swap, then reopen and assert the deleted doc is NOT resurrected (proving the reconcile tombstone is in the durable tomb.dat, not just in-memory). This is the actual highest-risk durability path and Task 8 only tests it in-process (no restart).
8. **[HIGH] refactor-safety** — Task 9 Compact() and Task 10 maybeMergeLocked: delete-driven 'merge of one' of a heavy-tombstone segment that is concurrently being indexed
   - issue: planMergeLocked resolves inputs via s.sealedByID and packs, but a delete-driven candidate may still be PENDING (graph not yet built) or mid-build. mergeAndPublish step 2b does `s.sealed[i].close()` and deletes the input dir — but a background buildAndPublish goroutine for that same segId may be running off-lock (it reads the segment's mmap via eachLive WITHOUT holding s.mu, only the segment's own tombMu). close() calls mmapFree on vecMap/tombMap/plMap; a concurrent builder reading getVectorRef/tombGet then dereferences freed/unmapped memory → SIGSEGV. The plan never sequences merge-input close against an in-flight build of that input. sealLocked spawns buildAndPublish for every sealed segment; recover() resumes builds for pending segments. There is a real window where a segment is selected for delete-driven merge while its own HNSW build is still running.
   - fix: Before closing/deleting an input in step 2b, ensure no build is in flight for it: either skip candidates whose graph is not yet installed (s.graphs[id]==nil → still pending/building), or track per-segment build completion and wait for it, or make close() defer the mmapFree until the builder's WaitGroup for that segment drains. Add a -race test: select a segment for merge immediately after seal (build still running) and assert no crash. This is the SIGSEGV-on-free hazard the prior vectorindex audit already flagged as a class.
