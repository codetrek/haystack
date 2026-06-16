package vectorstore

import (
	"path/filepath"
	"testing"
)

func TestStore_DeleteRoutesToSealedSegment(t *testing.T) {
	s := openTestStore(t, DotProduct)
	// Put two docs into the head.
	requireNoError(t, s.Put("a", []float32{1, 0, 0}, nil))
	requireNoError(t, s.Put("b", []float32{0, 1, 0}, nil))

	// Freeze the head into a sealed segment on disk and attach it, leaving the
	// head emptied — using the internal helper the seal pipeline will reuse.
	segDir := filepath.Join(s.dir, "seg-1-0")
	requireNoError(t, writeSealedSegment(segDir, s.seg))
	ss, err := openSealedSegment(segDir, DotProduct)
	requireNoError(t, err)
	s.attachSealedForTest(ss, 1)

	// "a" now lives in the sealed segment; Delete must tombstone it there.
	requireNoError(t, s.Delete("a"))
	if _, _, _, live := ss.read(0); live {
		t.Fatal("Delete(a) did not tombstone the sealed segment slot")
	}
	// "b" is still live in the sealed segment.
	if _, _, _, live := ss.read(1); !live {
		t.Fatal("Delete(a) wrongly affected b")
	}
	// Get(a) must now report not-found (sealed tombstone respected).
	_, _, found, err := s.Get("a")
	requireNoError(t, err)
	if found {
		t.Fatal("Get(a) should be not-found after sealed-segment delete")
	}
}

// TestStore_PutCrossSegmentUpdateTombstonesSealed exercises appendix fix #7: a
// re-Put of a docId that currently lives in a SEALED segment must tombstone that
// sealed slot (so it does not stay live in both head and sealed) and re-home the
// docId to the head.
func TestStore_PutCrossSegmentUpdateTombstonesSealed(t *testing.T) {
	s := openTestStore(t, DotProduct)
	requireNoError(t, s.Put("a", []float32{1, 0, 0}, nil))
	requireNoError(t, s.Put("b", []float32{0, 1, 0}, nil))

	segDir := filepath.Join(s.dir, "seg-1-0")
	requireNoError(t, writeSealedSegment(segDir, s.seg))
	ss, err := openSealedSegment(segDir, DotProduct)
	requireNoError(t, err)
	s.attachSealedForTest(ss, 1)

	// Re-Put "a" with a new vector; it must move to the head and its old sealed
	// slot must be tombstoned (no duplicate live copy).
	requireNoError(t, s.Put("a", []float32{0, 0, 1}, nil))
	if _, _, _, live := ss.read(0); live {
		t.Fatal("re-Put(a) did not tombstone the prior sealed slot")
	}
	// Get(a) returns the new vector from the head.
	v, _, found, err := s.Get("a")
	requireNoError(t, err)
	if !found {
		t.Fatal("Get(a) should be found in the head after re-Put")
	}
	if len(v) != 3 || v[2] != 1 {
		t.Fatalf("Get(a) returned stale vector %v, want {0,0,1}", v)
	}
}
