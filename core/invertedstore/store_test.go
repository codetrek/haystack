package invertedstore

import (
	"path/filepath"
	"testing"

	"github.com/codetrek/haystack/core/queue"
)

func openTestStore(t *testing.T, dir string) *Store {
	t.Helper()
	q := queue.NewMpsc("invtest")
	q.Start()
	s, err := Open(dir, q, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestOpenCreatesMissingDir guards the production wiring ergonomic: Open is called
// on a versioned subdir (storagePath/<version>/invertedstore) that does NOT exist
// on first boot, so Open must MkdirAll it before reading the MANIFEST rather than
// failing with "no such file or directory" on MANIFEST.tmp.
func TestOpenCreatesMissingDir(t *testing.T) {
	// A nested path none of whose components exist yet.
	dir := filepath.Join(t.TempDir(), "v1.6", "invertedstore")
	q := queue.NewMpsc("invtest-mkdir")
	q.Start()
	s, err := Open(dir, q, Options{})
	if err != nil {
		t.Fatalf("Open on non-existent dir: %v", err)
	}
	// A fresh store must be usable: create a table and reopen to confirm the
	// MANIFEST was written into the just-created dir.
	id, err := s.CreateTable("files")
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	s.CloseAndWait()

	s2 := openTestStore(t, dir)
	defer s2.CloseAndWait()
	if _, ok := s2.tableInfo(id); !ok {
		t.Fatalf("table %d not persisted after Open created the dir", id)
	}
}

func TestCreateDeleteTablePersist(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	id, err := s.CreateTable("files")
	if err != nil || id != 1 {
		t.Fatalf("CreateTable: id=%d err=%v", id, err)
	}
	id2, _ := s.CreateTable("symbols")
	if id2 != 2 {
		t.Fatalf("second table id=%d, want 2", id2)
	}
	s.CloseAndWait()

	// reopen: catalog persisted, next id continues
	s2 := openTestStore(t, dir)
	defer s2.CloseAndWait()
	id3, _ := s2.CreateTable("third")
	if id3 != 3 {
		t.Fatalf("after reopen next id=%d, want 3", id3)
	}
	if err := s2.DeleteTable(1); err != nil {
		t.Fatalf("DeleteTable: %v", err)
	}
	if _, ok := s2.tableInfo(1); ok {
		t.Fatal("table 1 should be gone from the catalog after DeleteTable")
	}
}

func TestSpillAndReopen(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	tbl, _ := s.CreateTable("files")
	// the internal building blocks Update (P7) will call: doc 10 = {alpha,gamma}; doc 11 = {beta}
	s.applyForTest(tbl, 10, []string{"alpha", "gamma"})
	s.applyForTest(tbl, 11, []string{"beta"})
	s.spillForTest(tbl) // force a spill
	s.CloseAndWait()

	s2 := openTestStore(t, dir)
	defer s2.CloseAndWait()
	if len(s2.segs) != 1 {
		t.Fatalf("expected 1 sealed segment after reopen, got %d", len(s2.segs))
	}
	seg := s2.segs[0]
	lo := invertedKey(uint32(tbl), "alpha")
	var hits []int64
	seg.scanPrefix(lo, prefixUpper(lo), func(_ []byte, v []byte) {
		ab, _ := splitInvertedValue(v)
		decodeDocs(ab, func(d int64) { hits = append(hits, d) })
	})
	if len(hits) != 1 || hits[0] != 10 {
		t.Fatalf("alpha postings after reopen = %v, want [10]", hits)
	}
	fv, ok := seg.lookupForward(forwardKey(uint32(tbl), 10))
	if !ok {
		t.Fatal("forward lookup miss for doc 10")
	}
	ords, _ := decodeForward(fv)
	need := map[uint32]struct{}{}
	for _, o := range ords {
		need[o] = struct{}{}
	}
	if got := seg.resolveOrds(need); len(got) != 2 {
		t.Fatalf("doc 10 resolved to %d keywords, want 2: %v", len(got), got)
	}
}
