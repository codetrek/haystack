package vectorstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSeal_EmptyHeadIsNoOp(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.Seal()) // empty head → no segment, no manifest
	if len(s.sealed) != 0 {
		t.Fatalf("empty-head Seal created %d segments, want 0", len(s.sealed))
	}
}

func TestManifest_WriteFaultRemovesTmp(t *testing.T) {
	dir := t.TempDir()
	orig := fsCreate
	t.Cleanup(func() { fsCreate = orig })
	fsCreate = func(name string) (osFile, error) {
		f, err := orig(name)
		if err != nil {
			return nil, err
		}
		return &faultFile{osFile: f, failSync: true}, nil
	}
	if err := writeManifest(dir, sampleManifest()); err == nil {
		t.Fatal("writeManifest should fail when fsync is injected to fail")
	}
	if _, statErr := os.Stat(dir + "/manifest.tmp"); !os.IsNotExist(statErr) {
		t.Fatal("manifest.tmp must be removed on write fault")
	}
}

func TestSealedSegment_TombstoneOutOfRange(t *testing.T) {
	head := buildHeadSeg(DotProduct, []struct {
		doc int64
		v   []float32
		pl  []byte
	}{{1, []float32{1, 0}, nil}})
	dir := t.TempDir() + "/seg-1-0"
	requireNoError(t, writeSealedSegment(dir, head))
	ss, err := openSealedSegment(dir, DotProduct)
	requireNoError(t, err)
	defer ss.close()
	if err := ss.tombstoneSlot(99); err == nil {
		t.Fatal("tombstoneSlot out of range should error")
	}
}

// TestRecovery_StrandedManifestTmpIgnored covers appendix #3: a manifest.tmp left
// by a crashed write must be ignored (readManifest reads the committed "manifest")
// and swept by recovery, never mistaken for a committed manifest.
func TestRecovery_StrandedManifestTmpIgnored(t *testing.T) {
	kvStore := newTestKV(t)
	s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	requireNoError(t, s.Put("d-0", []float32{1, 0, 0, 0}, nil))
	requireNoError(t, s.Seal()) // writes a committed manifest
	requireNoError(t, s.WaitForIndex())

	tmp := filepath.Join(s.dir, "manifest.tmp")
	requireNoError(t, os.WriteFile(tmp, []byte("garbage-not-a-manifest"), 0644))

	s2 := reopenStore(t, s, kvStore)
	// The committed manifest loaded (the doc is searchable) and the tmp is swept.
	if _, statErr := os.Stat(tmp); !os.IsNotExist(statErr) {
		t.Fatal("stranded manifest.tmp not swept on recovery")
	}
	res, err := s2.Search([]float32{1, 0, 0, 0}, 1)
	requireNoError(t, err)
	if len(res) != 1 {
		t.Fatalf("recovered store lost the sealed doc: got %d results", len(res))
	}
}
