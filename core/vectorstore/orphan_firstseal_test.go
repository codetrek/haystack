package vectorstore

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRecovery_OrphanSweptOnMissingManifest is the red-proof for the orphan sweep
// skipped on a first-seal crash. recover()'s os.IsNotExist (no-manifest) branch
// returned replay() WITHOUT running the orphan sweep, so a crash during the very
// first seal — after the segment dir was written but before the manifest was
// committed — leaks a half-written seg-<id>-<gen>/ dir forever. The sweep must run
// on the missing-manifest branch too (with an empty referenced set, every seg-*
// dir is an orphan).
func TestRecovery_OrphanSweptOnMissingManifest(t *testing.T) {
	kvStore := newTestKV(t)
	dir := t.TempDir()

	// Simulate a crash during the FIRST seal: a half-written segment dir exists but
	// no manifest was ever committed.
	orphan := filepath.Join(dir, "seg-1-0")
	requireNoError(t, os.MkdirAll(orphan, 0755))
	requireNoError(t, os.WriteFile(filepath.Join(orphan, "vectors.dat"), []byte("garbage"), 0644))

	s, err := Open(Options{Dir: dir, KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("orphan seg-1-0 not swept on a missing-manifest (first-seal crash) recovery")
	}
}
