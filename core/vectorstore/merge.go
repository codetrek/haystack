package vectorstore

import (
	"os"
	"path/filepath"
)

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

// mergePlan is the under-lock snapshot a merge builds before releasing s.mu for
// the slow write+graph-build. It captures everything the off-lock phase needs and
// nothing that the live store can mutate underneath it.
type mergePlan struct {
	inputs  []segID
	inputSS []*sealedSegment // parallel to inputs; kept mmap'd until the swap
	buckets []*segment       // packed live docs (≤ maxSegSize each)
	outIDs  []segID          // fresh segIds, one per bucket (allocated under lock)
	outDirs []string
	moved   map[int64]bool // docs whose global segId the swap rehomes
}

// planMergeLocked resolves inputIDs, packs their live docs into buckets, and
// allocates a fresh segId + dir per bucket. Returns (nil, nil) if any input id is
// already gone (a concurrent merge won the race) OR is not yet indexed — the
// caller treats that as a no-op. Caller holds s.mu. Allocating the output ids here
// (under the lock) keeps s.nextSeg monotonic and collision-free against concurrent
// seals.
//
// Why skip an input whose graph is not yet installed (appendix #8): a sealed
// segment that is still pending has a background buildAndPublish goroutine reading
// its mmap via eachLive OFF the store lock. The merge swap (step 2b) close()s the
// input — mmapFree on vecMap/tombMap/plMap — which would unmap memory the builder
// is mid-read, a SIGSEGV-on-free. A graph is installed (s.graphs[id] != nil) only
// as the builder's LAST action under s.mu, so requiring it proves no builder is
// in flight for that input. This also satisfies the "do not merge a just-sealed
// pending segment before its graph is built" fidelity point (appendix #3).
func (s *Store) planMergeLocked(inputIDs []segID) (*mergePlan, error) {
	return s.planMergeWithCapLocked(inputIDs, s.maxSegSize)
}

// planMergeWithCapLocked is planMergeLocked with an explicit per-output bucket
// row cap, so the two drivers can pack differently (appendix #2/#6):
//   - DELETE-driven repack uses bucketCap = maxSegSize: the goal is reclaiming
//     tombstone space, so deflated segments refill ~maxSegSize buckets.
//   - GROWTH-driven roll-up uses bucketCap = MaxMergedSize: the goal is BOUNDING
//     total segment count, so K like-sized inputs must fold into ~ONE larger
//     output (the next tier), not be re-split back into maxSegSize same-tier
//     segments — which would make zero progress on count (§4.9 "压住段数").
func (s *Store) planMergeWithCapLocked(inputIDs []segID, bucketCap int) (*mergePlan, error) {
	inputSS := make([]*sealedSegment, 0, len(inputIDs))
	for _, id := range inputIDs {
		ss := s.sealedByID(id)
		if ss == nil {
			return nil, nil // already merged/swept; nothing to do
		}
		if s.graphs[id] == nil {
			return nil, nil // still pending/building — defer (avoids close-during-build, appendix #8)
		}
		inputSS = append(inputSS, ss)
	}
	buckets, moved := packLiveDocs(inputSS, s.metric, bucketCap)
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
//
// Quiescence discipline (appendix #1): mergeDone fires when this returns. Every
// output's buildBeginLocked() happens at step 2e BEFORE the return (and under
// s.mu), so a waiter draining to quiescence (no merge AND no build in flight)
// sees every spawned build counted before this merge's mergeDone. New merges are
// gated by s.closing at the launch sites, so no launch races Close's drain.
func (s *Store) mergeAndPublish(p *mergePlan) error {
	defer s.mergeDone()
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

	// Off-lock merge window seam (test-only): outputs are written+reopened but the
	// swap has not taken s.mu yet. A test blocks here on a concurrent goroutine to
	// deterministically race a Delete/Put against the reconcile path (step 2a). It
	// holds no lock, so the concurrent mutation proceeds; we then take the swap lock.
	if s.testHookInMergeWindow != nil {
		s.testHookInMergeWindow(p)
	}

	// Crash-before-swap seam (test-only, appendix #4): the outputs are written +
	// fsynced + reopened but the manifest does NOT yet reference them. Returning
	// here simulates a process death in exactly that window. We close the opened
	// output mmaps (they were never installed in s.sealed, so Close won't) but LEAVE
	// their dirs on disk — recover()'s sweepOrphansLocked must reclaim them, and the
	// untouched inputs stay live. This exercises the real output segIds/dirs and the
	// real nextSeg accounting, unlike a hand-fabricated stray dir.
	if s.testHookAfterWrite != nil && s.testHookAfterWrite(p) {
		for _, ss := range outSS {
			if ss != nil {
				ss.close()
			}
		}
		return nil
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
	// segment owns a live doc (§4.6), so this is the exact liveness gate. The
	// tombstoneSlot persists+msyncs the bit into the output's tomb.dat, so the
	// reconciliation is durable independent of the manifest write below.
	inputSet := make(map[segID]bool, len(p.inputs))
	for _, id := range p.inputs {
		inputSet[id] = true
	}
	for _, ss := range outSS {
		for slot := 0; slot < ss.count(); slot++ {
			doc := ss.slotDoc(slot)
			owner, ok := s.docToSeg[doc]
			if !ok || !inputSet[owner] {
				// Deleted, or rehomed to head/another seg during the merge window.
				_ = ss.tombstoneSlot(slot)
			}
		}
	}

	// (2b) Drop inputs from the parallel sealed slices (delete by INDEX to keep
	// s.sealed and s.sealedID aligned — gotcha 6), closing + scheduling dir delete.
	// close()'s mmapFree is safe here: planMergeLocked required every input to be
	// indexed, so no background builder is reading the input mmap (appendix #8).
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

	// (2e) Background-build each output's HNSW (off-lock, like seal). The
	// buildBeginLocked() + goroutine launch happen UNDER s.mu — same as sealLocked's
	// step 4 — so a concurrent WaitForIndex()/Close() drain (which also holds s.mu to
	// read the counters) can never observe a false-quiescent state between this merge
	// and the builds it spawns: the increment is published under s.mu before this
	// function's deferred mergeDone decrement. The buildAndPublish goroutine re-takes
	// s.mu itself, so the build still runs off-lock; it flips pending→indexed (and may
	// re-trigger a merge, finding #1). Every buildBeginLocked() here precedes this
	// function's return (and thus the deferred mergeDone), so a quiescence drain holds.
	for i, ss := range outSS {
		s.buildBeginLocked()
		go s.buildAndPublish(p.outIDs[i], p.outDirs[i], ss)
	}
	s.mu.Unlock()

	// Crash-after-swap seam (test-only, appendix #5/#7): the manifest swap committed
	// (outputs referenced, inputs not) and any step-2a reconcile tombstone is already
	// msync'd to the output's tomb.dat, but the old input dirs are not yet deleted.
	// Returning here simulates a crash in that window: the old inputs are left on
	// disk as orphans (recover() must sweep them) and the background builds never
	// run (recover() must resume them). The installed output mmaps are owned by
	// s.sealed now, so Close() releases them — we must NOT close them here.
	if s.testHookAfterSwap != nil && s.testHookAfterSwap(p) {
		return nil
	}

	// (3) Delete old input dirs AFTER the swap committed (now orphans).
	for _, dir := range inputDirs {
		_ = os.RemoveAll(dir)
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
// mergeAndPublish on a tracked goroutine. WaitForMerge() awaits completion. It
// refuses to launch once the store is closing (appendix #1: a merges.Add must
// never race a zero-counter merges.Wait in Close).
func (s *Store) mergeNow(inputIDs []segID) error {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil
	}
	p, err := s.planMergeLocked(inputIDs)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if p == nil {
		s.mu.Unlock()
		return nil
	}
	s.mergeBeginLocked(1)
	s.mu.Unlock()
	go func() { _ = s.mergeAndPublish(p) }()
	return nil
}

// WaitForMerge blocks until every in-flight merge has published (or aborted). It
// does NOT wait for the merged segments' background graph builds — use
// WaitForIndex for that. Mirrors WaitForIndex but on the merge counter only.
func (s *Store) WaitForMerge() error {
	s.mu.Lock()
	s.waitMergesLocked()
	s.mu.Unlock()
	return nil
}

// Compact runs one round of the reclamation policy synchronously-launched: it
// merges every delete-driven candidate (live ratio < mergeFloor) and, if a size
// tier has reached fanout (or the live sealed count exceeds TargetSegCount), one
// growth-driven roll-up. It returns once the merges are launched on tracked
// goroutines; callers use WaitForMerge to await publication. A healthy store (no
// candidates) is a no-op. This is the manual entry point for tests and
// operator-triggered reclamation (architecture §4.9). Refuses to launch once the
// store is closing (appendix #1: a merges.Add must never race a zero-counter
// merges.Wait in Close).
func (s *Store) Compact() error {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil
	}
	plans := s.planReclamationLocked()
	s.mergeBeginLocked(len(plans))
	s.mu.Unlock()
	for _, p := range plans {
		p := p
		go func() { _ = s.mergeAndPublish(p) }()
	}
	return nil
}

// planReclamationLocked is the shared policy core for Compact() (manual) and
// maybeMergeLocked (background trigger): it snapshots the live sealed segments and
// builds the merge plans for one reclamation round — every delete-driven repack
// plus at most one growth-driven roll-up. Caller holds s.mu and is responsible for
// s.mergeBeginLocked(len(plans)) + launching mergeAndPublish. Returns nil when the store
// is healthy. The growth roll-up packs into MaxMergedSize buckets (not maxSegSize)
// so K like-sized inputs fold into ~one larger next-tier output, actually bounding
// total segment count (appendix #2/#6).
func (s *Store) planReclamationLocked() []*mergePlan {
	stats := s.segStatsLocked()
	var plans []*mergePlan
	// Delete-driven: each deflated segment is its own "merge of one" repack into
	// fresh ~maxSegSize buckets.
	for _, id := range pickDeleteDriven(stats, s.mcfg) {
		if p, err := s.planMergeLocked([]segID{id}); err == nil && p != nil {
			plans = append(plans, p)
		}
	}
	// Growth-driven: one tier roll-up (re-snapshot excludes ids already planned so
	// the growth pick never double-selects a delete-driven input). Packs into
	// MaxMergedSize buckets so K inputs consolidate into fewer larger segments.
	if g := pickGrowthMerge(s.statsExcludingLocked(stats, plans), s.mcfg); g != nil {
		if p, err := s.planMergeWithCapLocked(g, s.mcfg.MaxMergedSize); err == nil && p != nil {
			plans = append(plans, p)
		}
	}
	return plans
}

// pickGrowthMerge wraps pickGrowthTiered with the TargetSegCount count-cap guard
// (appendix #2): the §4.9 invariant is "k-NN searches every segment → read
// amplification = total segment count → bound total count". The tier+fanout pick
// alone can leave a store above target if no single tier has reached fanout (e.g.
// many segments spread thinly across tiers, one per tier). When the live sealed
// count exceeds TargetSegCount, force a greedy roll-up of the SMALLEST segments —
// even across tiers and even below fanout — so the count strictly trends down
// toward target. Without this, an all-singleton-tier over-target store would sit
// over target forever (finding #2): the old per-tier-only fallback could never
// pick across tiers, so it returned nil and the count never dropped.
func pickGrowthMerge(stats []segLiveStats, cfg mergeConfig) []segID {
	if g := pickGrowthTiered(stats, cfg); g != nil {
		return g
	}
	if len(stats) <= cfg.TargetSegCount {
		return nil
	}
	// Over target with no fanout-ready tier: greedily fold the SMALLEST segments
	// (cheapest merges, drains the long tail) whose combined live rows fit
	// MaxMergedSize. Sorting by count ascending lets us pack the smallest pair/run
	// first; this works whether they share a tier or are one-per-tier (finding #2's
	// all-singleton case). Returns a group of >= 2 so the count drops by >= 1; nil if
	// not even the two smallest fit the cap (no admissible merge that respects the
	// size bound — leave it to delete-driven reclamation / a later, larger budget).
	order := make([]segLiveStats, len(stats))
	copy(order, stats)
	sortStatsByCountAsc(order)
	liveSum := 0
	var ids []segID
	for _, st := range order {
		if liveSum+st.live > cfg.MaxMergedSize {
			break // adding this would overflow the size cap; stop with what we have
		}
		ids = append(ids, st.id)
		liveSum += st.live
	}
	if len(ids) < 2 {
		return nil // no pair fits the cap → no count-reducing merge available
	}
	return ids
}

// sortStatsByCountAsc sorts a segLiveStats slice ascending by total row count
// (ties keep input order via the stable insertion pass), so the count-cap fallback
// packs the smallest segments first.
func sortStatsByCountAsc(a []segLiveStats) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1].count > a[j].count; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
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

// maybeMergeLocked is the background trigger: after a structural change (a seal),
// it checks the reclamation policy and launches at most one delete-driven repack
// per deflated segment AND one growth-driven roll-up on tracked goroutines. Caller
// holds s.mu. Launches nothing when the store is healthy, so it is cheap to call
// on every seal. The actual write+build runs off-lock in mergeAndPublish (the
// goroutine re-takes the lock only for the swap), so this never blocks the write
// path.
//
// Anti-thrash (appendix #3): planReclamationLocked only selects INDEXED inputs
// (planMergeLocked skips a segment whose graph is not yet installed), so a
// just-sealed PENDING segment is never merged before its build completes — the
// trigger never discards an in-flight build nor close()s an input mmap a builder
// is mid-read. A merge thus only consumes segments whose (dominant) HNSW build
// cost has already been paid, and the growth roll-up only fires once a tier of
// indexed peers reaches fanout. The trigger never recurses (it does not call
// sealLocked). Gated by s.closing so a merges.Add never races Close's
// zero-counter merges.Wait (appendix #1).
func (s *Store) maybeMergeLocked() {
	if s.closing {
		return
	}
	plans := s.planReclamationLocked()
	s.mergeBeginLocked(len(plans))
	for _, p := range plans {
		p := p
		go func() { _ = s.mergeAndPublish(p) }()
	}
}
