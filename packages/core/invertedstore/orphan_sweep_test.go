package invertedstore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/codetrek/haystack/core/queue"
)

func TestOrphanSweep_RemovesUnlistedSegmentOnOpen(t *testing.T) {
	dir := t.TempDir()
	q := queue.NewMpsc("orphansweep")
	q.Start()
	s, err := Open(dir, q, Options{})
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := s.CreateTable("files")
	s.applyForTest(tid, 1, []string{"alpha"})
	s.spillForTest(tid) // one LIVE segment, in the MANIFEST
	live := s.SegmentsForTest()
	if len(live) != 1 {
		t.Fatalf("want 1 live segment, got %d", len(live))
	}
	s.CloseAndWait()

	// Simulate a crash-after-reserve orphan: a seg file at an id NOT in the MANIFEST.
	orphan := filepath.Join(dir, segFileName(999999))
	if err := os.WriteFile(orphan, []byte("garbage-not-a-real-segment"), 0o644); err != nil {
		t.Fatal(err)
	}

	q2 := queue.NewMpsc("orphansweep2")
	q2.Start()
	s2, err := Open(dir, q2, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.CloseAndWait()
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan segment was not swept on Open (stat err=%v)", err)
	}
	// The live segment + its data survive.
	if got := s2.SegmentsForTest(); len(got) != 1 || got[0].Id != live[0].Id {
		t.Fatalf("live segment lost after sweep: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, segFileName(live[0].Id))); err != nil {
		t.Fatalf("live segment file removed by sweep: %v", err)
	}
}

// TestOrphanSweep_RemovesTempSpillFileOnOpen: an off-worker spill that crashed before install left a
// seg-tmp-*.dat behind (never live in the MANIFEST). Open must sweep it (item G / F v5), while
// leaving unrelated files (MANIFEST) and any subdirectory untouched.
func TestOrphanSweep_RemovesTempSpillFileOnOpen(t *testing.T) {
	dir := t.TempDir()
	q := queue.NewMpsc("orphansweep-tmp")
	q.Start()
	s, err := Open(dir, q, Options{})
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := s.CreateTable("files")
	s.applyForTest(tid, 1, []string{"alpha"})
	s.spillForTest(tid) // one LIVE segment, in the MANIFEST
	live := s.SegmentsForTest()
	if len(live) != 1 {
		t.Fatalf("want 1 live segment, got %d", len(live))
	}
	s.CloseAndWait()

	// A crash-mid-encode orphan: an off-worker spill temp file the install never renamed.
	tmpOrphan := filepath.Join(dir, segTempFileName(7))
	if err := os.WriteFile(tmpOrphan, []byte("half-written-temp-encode"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A subdirectory the sweep must SKIP (e.IsDir branch), not try to remove/parse.
	subDir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	q2 := queue.NewMpsc("orphansweep-tmp2")
	q2.Start()
	s2, err := Open(dir, q2, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.CloseAndWait()

	if _, err := os.Stat(tmpOrphan); !os.IsNotExist(err) {
		t.Fatalf("seg-tmp orphan was not swept on Open (stat err=%v)", err)
	}
	// The subdirectory is left alone (IsDir skip).
	if fi, err := os.Stat(subDir); err != nil || !fi.IsDir() {
		t.Fatalf("sweep must not touch a subdirectory: err=%v", err)
	}
	// The live segment survives.
	if got := s2.SegmentsForTest(); len(got) != 1 || got[0].Id != live[0].Id {
		t.Fatalf("live segment lost after temp sweep: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, segFileName(live[0].Id))); err != nil {
		t.Fatalf("live segment file removed by sweep: %v", err)
	}
}
