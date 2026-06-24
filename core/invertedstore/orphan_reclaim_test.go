package invertedstore

import "testing"

// A crash in the DeleteTable window (catalog durably drops the table, but the volatile covering
// merge it scheduled never installs) leaves the dropped table's segments on disk. Open must: (i) NOT
// resurrect the table into liveByTable (catalog-gated recompute), and (ii) synchronously reclaim its
// bytes via a covering merge — independent of AutoMerge. Spec §6 / §8.5(c).
//
// Setup uses AutoMerge:false so DeleteTable leaves EXACTLY the crash-window on-disk state
// deterministically (its covering merge no-ops), with no goroutine to block or race.
func TestOrphanReclaim_DeleteTableWindowCrash(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, dir, Options{AutoMerge: false})
	a, err := s.CreateTable("A")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateTable("B")
	if err != nil {
		t.Fatal(err)
	}
	s.Update(a, 1, []string{"a1", "a2"})
	s.Update(b, 1, []string{"b1", "b2", "b3"})
	s.sync()
	s.spillForTest(a)
	s.spillForTest(b)
	if err := s.DeleteTable(b); err != nil { // catalog drops B; covering merge no-ops (AutoMerge off)
		t.Fatal(err)
	}
	if !segmentCoversTable(s, b) {
		t.Fatal("setup: B's orphan segment should still be present after DeleteTable (AutoMerge off)")
	}
	s.CloseAndWait()

	// Reopen with AutoMerge OFF — the orphan reclaim must STILL run (synchronous, not via the
	// AutoMerge-gated triggerMerge). This is the round-3-BLOCKER guard.
	s2 := openAt(t, dir, Options{AutoMerge: false})
	if _, ok := s2.LiveByTableForTest()[b]; ok {
		t.Fatalf("dropped table B resurrected into liveByTable: %v", s2.LiveByTableForTest())
	}
	if segmentCoversTable(s2, b) {
		t.Fatal("orphan B bytes not reclaimed on Open (synchronous covering merge did not run)")
	}
	// Table A survived intact.
	if got := s2.LiveByTableForTest()[a]; got != 2 {
		t.Fatalf("table A live=%d want 2 after orphan reclaim", got)
	}
	s2.assertCounterInvariantForTest(t)
}

// A clean reopen (no orphan tables) must NOT run an orphan covering merge.
func TestOrphanReclaim_CleanReopenNoMerge(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, dir, Options{AutoMerge: false})
	tid, err := s.CreateTable("t")
	if err != nil {
		t.Fatal(err)
	}
	s.Update(tid, 1, []string{"a", "b"})
	s.sync()
	s.spillForTest(tid)
	s.CloseAndWait()

	n := installCoveringCounter(t)
	s2 := openAt(t, dir, Options{AutoMerge: false})
	_ = s2
	if got := n.Load(); got != 0 {
		t.Fatalf("clean reopen ran %d covering merges, want 0", got)
	}
}

func segmentCoversTable(s *Store, tableId int) bool {
	for _, sm := range s.SegmentsForTest() {
		if sm.Postings > 0 && uint32(tableId) >= sm.MinTable && uint32(tableId) <= sm.MaxTable {
			return true
		}
	}
	return false
}
