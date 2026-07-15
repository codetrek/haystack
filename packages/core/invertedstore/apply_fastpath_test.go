package invertedstore

import "testing"

// A warm 1-op edit (drop a keyword) MUST still diff against the forward and tombstone the dropped
// keyword — the fast path must not skip the diff. (Guards that len(ops)==1 still reads `old`.)
func TestApplyFastPath_WarmEditTombstonesDroppedKeyword(t *testing.T) {
	s, tid := newForwardSkipStore(t, Options{CapBytes: 1 << 20})
	s.Update(tid, 1, []string{"alpha", "beta"})
	s.spillForTest(tid)                      // seal so the next edit reads the forward from a segment
	s.Update(tid, 1, []string{"alpha"})      // drop "beta"
	s.q.RunFunc(func() error { return nil }) // drain
	// "beta" must no longer resolve to docid 1.
	if got := searchDocidsForTest(t, s, tid, "beta"); len(got) != 0 {
		t.Fatalf("beta still maps to %v after the warm 1-op edit dropped it", got)
	}
	if got := searchDocidsForTest(t, s, tid, "alpha"); len(got) != 1 || got[0] != 1 {
		t.Fatalf("alpha should still map to {1}, got %v", got)
	}
}

func TestApplyFastPath_TakenForOneOpNotMultiOp(t *testing.T) {
	s, tid := newForwardSkipStore(t, Options{CapBytes: 1 << 20})
	var fast int
	applyFastPathTaken = func() { fast++ }
	t.Cleanup(func() { applyFastPathTaken = nil })

	s.Update(tid, 1, []string{"a"}) // 1-op → fast path
	s.q.RunFunc(func() error { return nil })
	if fast != 1 {
		t.Fatalf("1-op apply took the fast path %d times, want 1", fast)
	}
	b := s.NewBatch()
	b.Update(tid, 2, []string{"b"}).Update(tid, 3, []string{"c"}) // 2-op → multi-op loop
	b.Commit()
	s.q.RunFunc(func() error { return nil })
	if fast != 1 {
		t.Fatalf("multi-op batch took the 1-op fast path (fast=%d, want still 1)", fast)
	}
}
