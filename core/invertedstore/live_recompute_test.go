package invertedstore

import (
	"testing"

	"github.com/codetrek/haystack/core/queue"
)

// openAt opens a store at a GIVEN dir (newUpdateStore uses a fresh TempDir, so it can't reopen).
// It stops the worker at test end; closing/crashing the store is the caller's choice.
func openAt(t *testing.T, dir string, opts Options) *Store {
	t.Helper()
	q := queue.NewMpsc("openat")
	q.Start()
	s, err := Open(dir, q, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Stop() })
	return s
}

// recomputeLive reproduces the incremental counter exactly from the segments' forward records.
func TestRecomputeLive_EqualsIncremental(t *testing.T) {
	s, tid := newUpdateStore(t)
	s.Update(tid, 1, []string{"a", "b", "c"})
	s.Update(tid, 2, []string{"a", "a", "b"}) // dup -> distinct 2
	s.sync()
	s.spillForTest(tid)

	want := s.LiveByTableForTest()[tid]
	if want != 5 {
		t.Fatalf("incremental live=%d want 5", want)
	}
	s.RecomputeLiveForTest() // zero, then rebuild from segment forward records
	if got := s.LiveByTableForTest()[tid]; got != want {
		t.Fatalf("recompute live=%d != incremental %d", got, want)
	}
}

// On a real reopen, Open's recomputeLive rebuilds liveByTable from the durable segments.
func TestRecomputeLive_OnReopen(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, dir, Options{})
	tid, err := s.CreateTable("t")
	if err != nil {
		t.Fatal(err)
	}
	s.Update(tid, 1, []string{"a", "b", "c"})
	s.Update(tid, 2, []string{"b", "c"})
	s.sync()
	s.spillForTest(tid)
	s.CloseAndWait()

	s2 := openAt(t, dir, Options{})
	if got := s2.LiveByTableForTest()[tid]; got != 5 {
		t.Fatalf("reopened live=%d want 5 (doc1 abc=3 + doc2 bc=2)", got)
	}
}
