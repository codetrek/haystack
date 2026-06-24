package invertedstore

import "testing"

// deadFraction endpoints on known inputs (AutoMerge off so the trigger doesn't move the value
// mid-assertion). cold build → 0 (the pathology guard); delete-all → 1.
func TestDeadFraction_Unit(t *testing.T) {
	s, tid := newUpdateStore(t) // AutoMerge off, large cap
	for d := int64(1); d <= 100; d++ {
		s.Update(tid, d, []string{"common", uniqWord(int(d))})
	}
	s.sync()
	s.spillForTest(tid)
	if df := s.DeadFractionForTest(); df != 0 {
		t.Fatalf("cold build deadFraction=%.4f want 0", df)
	}
	s.assertCounterInvariantForTest(t)

	// Delete the first 50 docs: written = 200 adds + 100 tombstones = 300; live = 50*2 = 100;
	// deadFraction = 1 - 100/300 = 0.667 (NOT 0.33 — the dead fraction is dead/written).
	for d := int64(1); d <= 50; d++ {
		s.Update(tid, d, nil)
	}
	s.sync()
	s.spillForTest(tid)
	if df := s.DeadFractionForTest(); df < 0.66 || df > 0.67 {
		t.Fatalf("delete-half deadFraction=%.4f want ~0.667", df)
	}
	s.assertCounterInvariantForTest(t)

	// Delete the rest → live = 0 → deadFraction = 1.
	for d := int64(51); d <= 100; d++ {
		s.Update(tid, d, nil)
	}
	s.sync()
	s.spillForTest(tid)
	if df := s.DeadFractionForTest(); df < 0.999 {
		t.Fatalf("delete-all deadFraction=%.4f want 1.0", df)
	}
}

// THE regression guard: a clean cold build that spills MANY segments must never fire a covering
// merge — deadFraction stays 0 because live tracks written. (With the old bottomDeadFraction this
// path ran a full-decompression scan after every spill; here it is a metadata sum.)
func TestDeadFraction_ColdBuildNoCoveringMerge(t *testing.T) {
	s, tid := newUpdateStoreOpts(t, Options{CapBytes: 4 << 10, AutoMerge: true})
	n := installCoveringCounter(t)
	for d := 0; d < 3000; d++ {
		s.Update(tid, int64(d), []string{"w", uniqWord(d)})
	}
	s.sync()
	s.waitMergeIdle()
	if df := s.DeadFractionForTest(); df >= coveringDeadThreshold {
		t.Fatalf("cold-build deadFraction=%.4f want < %.2f", df, coveringDeadThreshold)
	}
	if got := n.Load(); got != 0 {
		t.Fatalf("covering merges fired %d times on a clean cold build, want 0", got)
	}
	s.assertCounterInvariantForTest(t)
}

// The trigger still fires when garbage accumulates: delete a large fraction and a covering merge runs.
func TestDeadFraction_TriggerFiresOnDeletes(t *testing.T) {
	s, tid := newUpdateStoreOpts(t, Options{CapBytes: 4 << 10, AutoMerge: true})
	n := installCoveringCounter(t)
	for d := 0; d < 1000; d++ {
		s.Update(tid, int64(d), []string{"w", uniqWord(d)})
	}
	s.sync()
	s.waitMergeIdle()
	if got := n.Load(); got != 0 {
		t.Fatalf("covering fired during clean build: %d", got)
	}
	for d := 0; d < 600; d++ { // delete 60% -> well over threshold
		s.Update(tid, int64(d), nil)
	}
	s.sync()
	s.waitMergeIdle()
	if got := n.Load(); got < 1 {
		t.Fatalf("covering merge did not fire after 60%% deletes (count=%d)", got)
	}
	s.assertCounterInvariantForTest(t)
}

// A garbage-reclaiming covering merge on a CLEAN fixture preserves the live count while reclaiming
// written bytes (spec §4.2.3 / §8.4). (On an inconsistent input the self-heal path may drop a forward
// term — not asserted here.)
func TestCovering_PreservesLive_CleanFixture(t *testing.T) {
	s, tid := newUpdateStore(t) // AutoMerge off
	for d := int64(1); d <= 50; d++ {
		s.Update(tid, d, []string{"k", uniqWord(int(d))})
	}
	s.sync()
	s.spillForTest(tid)
	for d := int64(1); d <= 50; d++ { // re-post identical sets -> superseded copies (garbage, no inconsistency)
		s.Update(tid, d, []string{"k", uniqWord(int(d))})
	}
	s.sync()
	s.spillForTest(tid)

	liveBefore := s.LiveByTableForTest()[tid]
	writtenBefore := sumPostings(s)
	s.coveringMergeForTest(t)
	liveAfter := s.LiveByTableForTest()[tid]
	writtenAfter := sumPostings(s)

	if liveAfter != liveBefore {
		t.Fatalf("covering merge changed live: %d -> %d (must preserve)", liveBefore, liveAfter)
	}
	if writtenAfter >= writtenBefore {
		t.Fatalf("covering merge reclaimed nothing: written %d -> %d", writtenBefore, writtenAfter)
	}
	s.assertCounterInvariantForTest(t)
}

// Tiered merges + spills during a clean build leave liveByTable untouched (no covering merge, and
// live equals the exact distinct-pair count). Spec §4.2.3 (merge path never touches liveByTable).
func TestTieredMergeAndSpill_LeaveLiveUnchanged(t *testing.T) {
	s, tid := newUpdateStoreOpts(t, Options{CapBytes: 2 << 10, AutoMerge: true, Fanout: 4})
	n := installCoveringCounter(t)
	for d := 0; d < 1000; d++ {
		s.Update(tid, int64(d), []string{"k", uniqWord(d)}) // "k" shared, uniqWord distinct
	}
	s.sync()
	s.waitMergeIdle()
	if got := n.Load(); got != 0 {
		t.Fatalf("covering merge fired on a clean build (count=%d) — tiered only expected", got)
	}
	if got := s.LiveByTableForTest()[tid]; got != 2000 {
		t.Fatalf("live=%d want 2000 (1000 docs * 2 distinct kw); tiered merge/spill must not change it", got)
	}
	s.assertCounterInvariantForTest(t)
}

func sumPostings(s *Store) int64 {
	var n int64
	for _, sm := range s.SegmentsForTest() {
		n += sm.Postings
	}
	return n
}
