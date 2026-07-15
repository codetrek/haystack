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
	ss, err := openSealedSegment(dir, DotProduct, 1, nil)
	requireNoError(t, err)
	defer ss.close()
	// An out-of-range slot is rejected (ok=false), not applied — and must not panic
	// on the in-memory tomb word index.
	if ss.markTombLocked(99) {
		t.Fatal("markTombLocked out of range should return false (no-op)")
	}
	// A tombGet for a slot beyond the mapped tomb words reports live (false), never
	// panics — the defensive w >= len(tomb) guard (a 1-row segment has one word
	// covering slots 0..63, so slot 64 is past it).
	if ss.tombGet(64) {
		t.Fatal("tombGet past the mapped tomb words should report live (false)")
	}
}

// TestRecovery_StrandedManifestTmpIgnored covers appendix #3 in the bbolt world: a
// legacy manifest / manifest.tmp / records.wal file (left by the pre-bbolt control
// plane or the pre-incr-2 head WAL) must NOT break recovery — the control store is
// loaded from control.db — and must be swept, never mistaken for live state.
func TestRecovery_StrandedManifestTmpIgnored(t *testing.T) {
	kvStore := newTestKV(t)
	s, err := Open(Options{Dir: t.TempDir(), Metric: Cosine, KV: kvStore})
	requireNoError(t, err)
	requireNoError(t, s.Put("d-0", []float32{1, 0, 0, 0}, nil))
	requireNoError(t, s.Seal()) // commits the control store (control.db)
	requireNoError(t, s.WaitForIndex())

	tmp := filepath.Join(s.dir, "manifest.tmp")
	legacy := filepath.Join(s.dir, "manifest")
	wal := filepath.Join(s.dir, "records.wal")
	requireNoError(t, os.WriteFile(tmp, []byte("garbage-not-a-manifest"), 0644))
	requireNoError(t, os.WriteFile(legacy, []byte("legacy-manifest-bytes"), 0644))
	requireNoError(t, os.WriteFile(wal, []byte("legacy-records-wal-bytes"), 0644))

	s2 := reopenStore(t, s, kvStore)
	// The control store loaded (the doc is searchable) and every legacy file is swept.
	if _, statErr := os.Stat(tmp); !os.IsNotExist(statErr) {
		t.Fatal("stranded manifest.tmp not swept on recovery")
	}
	if _, statErr := os.Stat(legacy); !os.IsNotExist(statErr) {
		t.Fatal("legacy manifest file not swept on recovery")
	}
	if _, statErr := os.Stat(wal); !os.IsNotExist(statErr) {
		t.Fatal("legacy records.wal not swept on recovery")
	}
	res, err := s2.Search("default", []float32{1, 0, 0, 0}, 1, nil)
	requireNoError(t, err)
	if len(res) != 1 {
		t.Fatalf("recovered store lost the sealed doc: got %d results", len(res))
	}
}
