package invertedstore

import "testing"

// liveByTable tracks distinct live (keyword,docid) pairs per table. These tests drive the REAL
// apply path (Update+sync → applyBatch), where the counter delta lives — NOT applyForTest, which
// bypasses applyBatch and would leave the counter at 0.
func TestLiveByTable_DeltaBranches(t *testing.T) {
	s, tid := newUpdateStore(t)
	defer s.CloseAndWait() // release segment fds so Windows t.TempDir RemoveAll can delete seg-*.dat
	up := func(docid int64, kw []string) { s.Update(tid, docid, kw); s.sync() }
	live := func() int64 { return s.LiveByTableForTest()[tid] }

	up(1, []string{"a", "b", "c"})
	if live() != 3 {
		t.Fatalf("cold add live=%d want 3", live())
	}
	up(1, []string{"a", "b", "c", "d"}) // grow +1
	if live() != 4 {
		t.Fatalf("grow live=%d want 4", live())
	}
	up(1, []string{"a"}) // shrink 4 -> 1
	if live() != 1 {
		t.Fatalf("shrink live=%d want 1", live())
	}
	up(1, nil) // delete
	if live() != 0 {
		t.Fatalf("delete live=%d want 0", live())
	}
	up(99, nil) // delete an unknown docid -> Δ0
	if live() != 0 {
		t.Fatalf("del-unknown live=%d want 0", live())
	}
	up(99, nil) // double-delete -> Δ0
	if live() != 0 {
		t.Fatalf("double-delete live=%d want 0", live())
	}
	up(2, []string{"x", "x", "y"}) // duplicate keyword -> distinct 2
	if live() != 2 {
		t.Fatalf("dup-kw live=%d want 2", live())
	}
}

// DeleteTable drops the table's whole liveByTable partition in O(1) and leaves other tables intact.
func TestLiveByTable_DeleteTableDropsPartition(t *testing.T) {
	s, a := newUpdateStore(t)
	defer s.CloseAndWait() // release segment fds so Windows t.TempDir RemoveAll can delete seg-*.dat
	b, err := s.CreateTable("B")
	if err != nil {
		t.Fatal(err)
	}
	s.Update(a, 1, []string{"a1", "a2"})
	s.Update(b, 1, []string{"b1", "b2", "b3"})
	s.sync()
	if got := s.LiveByTableForTest(); got[a] != 2 || got[b] != 3 {
		t.Fatalf("live=%v want a=2 b=3", got)
	}
	if err := s.DeleteTable(b); err != nil {
		t.Fatal(err)
	}
	got := s.LiveByTableForTest()
	if _, ok := got[b]; ok {
		t.Fatalf("liveByTable still has dropped table B: %v", got)
	}
	if got[a] != 2 {
		t.Fatalf("table A live changed after DeleteTable(B): %v", got)
	}
}
