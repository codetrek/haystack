package vectorstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSeal_EmptyHeadIsNoOp(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.Seal()) // empty head → no segment, no control commit
	if len(s.sealed) != 0 {
		t.Fatalf("empty-head Seal created %d segments, want 0", len(s.sealed))
	}
}

func TestSealedSegment_TombstoneOutOfRange(t *testing.T) {
	head := buildHeadSeg(DotProduct, []struct {
		doc int64
		v   []float32
		pl  Payload
	}{{1, []float32{1, 0}, nil}})
	dir := t.TempDir() + "/seg-1-0"
	requireNoError(t, writeSealedSegment(dir, head, nil))
	ss, err := openSealedSegment(dir, DotProduct)
	requireNoError(t, err)
	defer ss.close()
	if err := ss.tombstoneSlot(99); err == nil {
		t.Fatal("tombstoneSlot out of range should error")
	}
}

// TestRecovery_StrandedManifestTmpIgnored covers appendix #3 in the bbolt world: a
// legacy manifest / manifest.tmp file (left by the pre-bbolt control plane or a
// crashed legacy write) must NOT break recovery — the control store is loaded from
// control.db — and must be swept, never mistaken for live control state.
func TestRecovery_StrandedManifestTmpIgnored(t *testing.T) {
	kvStore := newTestKV(t)
	s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	requireNoError(t, s.Put("d-0", []float32{1, 0, 0, 0}, nil))
	requireNoError(t, s.Seal()) // commits the control store (control.db)
	requireNoError(t, s.WaitForIndex())

	tmp := filepath.Join(s.dir, "manifest.tmp")
	legacy := filepath.Join(s.dir, "manifest")
	requireNoError(t, os.WriteFile(tmp, []byte("garbage-not-a-manifest"), 0644))
	requireNoError(t, os.WriteFile(legacy, []byte("legacy-manifest-bytes"), 0644))

	s2 := reopenStore(t, s, kvStore)
	// The control store loaded (the doc is searchable) and both legacy files are swept.
	if _, statErr := os.Stat(tmp); !os.IsNotExist(statErr) {
		t.Fatal("stranded manifest.tmp not swept on recovery")
	}
	if _, statErr := os.Stat(legacy); !os.IsNotExist(statErr) {
		t.Fatal("legacy manifest file not swept on recovery")
	}
	res, err := s2.Search("default", []float32{1, 0, 0, 0}, 1, nil)
	requireNoError(t, err)
	if len(res) != 1 {
		t.Fatalf("recovered store lost the sealed doc: got %d results", len(res))
	}
}
