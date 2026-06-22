package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// putRandomSegment fills s with n random dim-d vectors and seals one segment,
// returning the per-doc vectors for an oracle. The single sealed segment is seg-1-0.
func putRandomSegment(t *testing.T, s *Store, n, dim int, seed int64) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	b := s.NewBatch()
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		b.Put("d-"+itoa(i), v, nil)
	}
	requireNoError(t, b.Commit())
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
}

// TestRecovery_StrayGraphSweptFromLiveSegDir covers the appendix #21 recovery
// invariant (sweepStrayGraphsLocked): a crash after a DropVectorIndex manifest
// commit but before that index's graph-<name>.dat unlink reached disk leaves a stray
// graph file inside a LIVE seg dir. recover() must sweep it — so a later re-Create of
// the same name cannot open the stale graph — while leaving the still-referenced
// index's graph file and the records intact.
func TestRecovery_StrayGraphSweptFromLiveSegDir(t *testing.T) {
	kvStore := newTestKV(t)
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, Metric: Cosine})
	requireNoError(t, err)
	putRandomSegment(t, s, 60, 8, 91)

	segDir := filepath.Join(s.dir, "seg-1-0")
	live := filepath.Join(segDir, graphFileName("default")) // referenced by the manifest
	stray := filepath.Join(segDir, graphFileName("ghost"))  // orphan: no such index in manifest
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("expected the built default graph at %s: %v", live, err)
	}
	requireNoError(t, os.WriteFile(stray, []byte("stale graph bytes"), 0644))

	s2 := reopenStore(t, s, kvStore)

	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatal("stray graph-ghost.dat in a live seg dir was not swept on recovery")
	}
	if _, err := os.Stat(filepath.Join(s2.dir, "seg-1-0", graphFileName("default"))); err != nil {
		t.Fatalf("still-referenced graph-default.dat was wrongly removed: %v", err)
	}
	if _, _, found, _ := s2.Get("d-0"); !found {
		t.Fatal("record d-0 lost after the stray-graph sweep")
	}
}

// TestRecovery_StrayGraphSweep_FsRemoveError surfaces an unlink failure during the
// stray-graph sweep as a recover error, not a silent partial recovery (the error
// leg of sweepStrayGraphsLocked).
func TestRecovery_StrayGraphSweep_FsRemoveError(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, Metric: Cosine})
	requireNoError(t, err)
	putRandomSegment(t, s, 60, 8, 92)
	stray := filepath.Join(s.dir, "seg-1-0", graphFileName("ghost"))
	requireNoError(t, os.WriteFile(stray, []byte("stale"), 0644))
	requireNoError(t, s.Close())

	orig := fsRemove
	fsRemove = func(p string) error {
		if filepath.Base(p) == graphFileName("ghost") {
			return errInjected
		}
		return orig(p)
	}
	defer func() { fsRemove = orig }()

	s2, err := Open(Options{Dir: dir, Metric: Cosine})
	if err == nil {
		_ = s2.Close()
		t.Fatal("expected recover to surface the stray-graph unlink failure")
	}
}
