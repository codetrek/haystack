package invertedstore

import "testing"

// forEachLiveSegmentForward surfaces each live docid's ORDS (newest-wins, tombstones excluded) —
// the data ForwardDocids discards but the Open live-recompute needs. Duplicate ords (a doc indexed
// with duplicate keywords) collapse under distinctOrds to the distinct-keyword count.
func TestForEachLiveSegmentForward_SurfacesOrds(t *testing.T) {
	s, tid := newUpdateStore(t)
	defer s.CloseAndWait() // release segment fds so Windows t.TempDir RemoveAll can delete seg-*.dat
	s.applyForTest(tid, 1, []string{"a", "b", "c"})
	s.applyForTest(tid, 2, []string{"a", "a", "b"}) // raw forward keeps the dup
	s.spillForTest(tid)

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
	if got[2] != 2 {
		t.Fatalf("doc 2 distinct ords (dup-collapsed) = %d, want 2", got[2])
	}
}
