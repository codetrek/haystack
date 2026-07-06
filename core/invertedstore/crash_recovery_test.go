package invertedstore

import "testing"

// add→del→add on ONE docid in ONE batch exercises the in-batch `old` selection (the 2nd/3rd ops
// read `old` from inBatch state, not a forward read) — the delta path most prone to a double-count.
// Final distinct set is {a} → live 1; the Open recompute must agree. Spec §8.7.
func TestLiveByTable_InBatchAddDelAdd(t *testing.T) {
	s, tid := newUpdateStore(t)
	bt := s.NewBatch()
	bt.Update(tid, 1, []string{"a", "b", "c"})
	bt.Update(tid, 1, nil)           // delete in the same batch
	bt.Update(tid, 1, []string{"a"}) // re-add, distinct 1
	bt.Commit()
	s.sync()

	if got := s.LiveByTableForTest()[tid]; got != 1 {
		t.Fatalf("in-batch add->del->add live=%d want 1", got)
	}
	s.spillForTest(tid)
	s.RecomputeLiveForTest()
	if got := s.LiveByTableForTest()[tid]; got != 1 {
		t.Fatalf("recompute after in-batch live=%d want 1", got)
	}
	s.assertCounterInvariantForTest(t)
}

// Crash shape (a): head-only loss + indexer over-replay must not double-count live. Docs spilled
// before the crash are durable; docs only in the head are lost; the indexer re-Updates ALL docs from
// its cursor. live must equal the true distinct-pair count (re-Updating a durable doc nets Δ0 because
// its old set is read from the segment), not an inflated value. Spec §8.5(a) (the round-2 BLOCKER).
func TestCrashRecovery_HeadOnlyLoss_NoDoubleCount(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, dir, Options{AutoMerge: false})
	tid, err := s.CreateTable("t")
	if err != nil {
		t.Fatal(err)
	}
	for d := int64(1); d <= 10; d++ {
		s.Update(tid, d, []string{"k", uniqWord(int(d))})
	}
	s.sync()
	s.spillForTest(tid) // docs 1-10 durable
	for d := int64(11); d <= 20; d++ {
		s.Update(tid, d, []string{"k", uniqWord(int(d))})
	}
	s.sync()                         // docs 11-20 only in the head
	s.dropHeadCloseSegmentsForTest() // crash: docs 11-20 lost

	s2 := openAt(t, dir, Options{AutoMerge: false})
	for d := int64(1); d <= 20; d++ { // indexer over-replays ALL docs
		s2.Update(tid, d, []string{"k", uniqWord(int(d))})
	}
	s2.sync()
	if got := s2.LiveByTableForTest()[tid]; got != 40 {
		t.Fatalf("after crash+over-replay live=%d want 40 (20 docs * 2 distinct, no double-count)", got)
	}
	s2.assertCounterInvariantForTest(t)
}

// Crash shape (b): part of a table durable, the rest lost with the head; over-replay. Verifies the
// durable docs' re-Update reads their durable forward (Δ0) while the lost docs are re-added once.
// Spec §8.5(b).
func TestCrashRecovery_PartiallyDurable_NoDoubleCount(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, dir, Options{AutoMerge: false})
	tid, err := s.CreateTable("t")
	if err != nil {
		t.Fatal(err)
	}
	// Two spilled batches (both durable), then a third batch left in the head (lost).
	for d := int64(1); d <= 5; d++ {
		s.Update(tid, d, []string{"k", uniqWord(int(d))})
	}
	s.sync()
	s.spillForTest(tid)
	for d := int64(6); d <= 10; d++ {
		s.Update(tid, d, []string{"k", uniqWord(int(d))})
	}
	s.sync()
	s.spillForTest(tid)
	for d := int64(11); d <= 15; d++ {
		s.Update(tid, d, []string{"k", uniqWord(int(d))})
	}
	s.sync() // 11-15 only in head
	s.dropHeadCloseSegmentsForTest()

	s2 := openAt(t, dir, Options{AutoMerge: false})
	// recompute alone (before replay) sees the 10 durable docs.
	if got := s2.LiveByTableForTest()[tid]; got != 20 {
		t.Fatalf("post-crash recompute live=%d want 20 (10 durable docs * 2)", got)
	}
	for d := int64(1); d <= 15; d++ {
		s2.Update(tid, d, []string{"k", uniqWord(int(d))})
	}
	s2.sync()
	if got := s2.LiveByTableForTest()[tid]; got != 30 {
		t.Fatalf("after over-replay live=%d want 30 (15 docs * 2, no double-count of the 10 durable)", got)
	}
	s2.assertCounterInvariantForTest(t)
}
