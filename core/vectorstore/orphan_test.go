package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestRecovery_OrphanSegmentSwept(t *testing.T) {
	kvStore := newTestKV(t)
	s, err := Open(Options{Dir: t.TempDir(), Metric: Cosine, KV: kvStore})
	requireNoError(t, err)
	rng := rand.New(rand.NewSource(71))
	dim := 8
	b := s.NewBatch()
	for i := 0; i < 60; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		b.Put("d-"+itoa(i), v, nil)
	}
	requireNoError(t, b.Commit())
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	// Simulate a crash mid-seal: a half-written seg dir the manifest never
	// referenced. seg-1-0 is the real (manifest) segment; seg-99-0 is an orphan.
	orphan := filepath.Join(s.dir, "seg-99-0")
	requireNoError(t, os.MkdirAll(orphan, 0755))
	requireNoError(t, os.WriteFile(filepath.Join(orphan, "vectors.dat"), []byte("garbage"), 0644))

	s2 := reopenStore(t, s, kvStore)
	_ = s2

	// The orphan must be gone; the real segment must remain.
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("orphan seg-99-0 not swept on recovery")
	}
	if _, err := os.Stat(filepath.Join(s2.dir, "seg-1-0")); err != nil {
		t.Fatalf("manifest-referenced seg-1-0 wrongly removed: %v", err)
	}
}
