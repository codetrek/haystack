package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/codetrek/haystack/core/kv"
	"github.com/codetrek/haystack/core/kv/pebblekv"
)

// TestAdv_CreatedIndexSurvivesReopen proves CreateVectorIndex's crash-safety
// comment ("Persist the new index ... so a crash mid-build is resumed by recover()")
// now holds under manifest v4: a Created index is persisted (config + per-(index,
// segment) state), survives a reopen, has its graph reopened from disk, and is
// queryable. (Before v4 — manifest v3 — the index vanished on reopen and its
// graph-<name>.dat was orphaned; Tasks 7/8 fixed both.)
func TestAdv_CreatedIndexSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	kvDir := t.TempDir()
	store, err := pebblekv.Open(kvDir, 16<<20)
	requireNoError(t, err)
	var kvStore kv.Store = store

	s, err := Open(Options{Dir: dir, KV: kvStore, Metric: Cosine})
	requireNoError(t, err)

	rng := rand.New(rand.NewSource(7))
	dim := 16
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	for i := 0; i < 40; i++ {
		requireNoError(t, s.Put("d-"+itoa(i), randVec(), nil))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	requireNoError(t, s.CreateVectorIndex("aux", VectorIndexConfig{Type: "hnsw", Metric: Cosine, M: 8}))
	requireNoError(t, s.WaitForIndex())

	segDir := filepath.Join(dir, segDirName(segID(1), 0))
	if _, err := os.Stat(filepath.Join(segDir, "graph-aux.dat")); err != nil {
		t.Fatalf("graph-aux.dat should exist after build: %v", err)
	}

	requireNoError(t, s.Close())

	s2, err := Open(Options{Dir: dir, KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	defer s2.Close()
	requireNoError(t, s2.WaitForIndex())

	// The created index SURVIVES reopen (persisted in the v4 manifest).
	if _, ok := s2.indexes["aux"]; !ok {
		t.Fatal("aux index must survive reopen under manifest v4")
	}
	// Its config + indexed state recovered: fully indexed on the one sealed segment.
	info := indexInfoByName(t, s2, "aux")
	if info.M != 8 || info.Metric != Cosine {
		t.Fatalf("aux config not recovered: %+v", info)
	}
	if info.Indexed != info.Segments || info.Segments != 1 {
		t.Fatalf("aux not fully indexed after reopen: %+v", info)
	}

	// Its graph file is still on disk (reopened, not orphaned).
	if _, err := os.Stat(filepath.Join(segDir, "graph-aux.dat")); err != nil {
		t.Fatalf("graph-aux.dat must survive reopen: %v", err)
	}

	// Search on the recovered index works — the user does not lose it on restart.
	if _, err := s2.Search("aux", randVec(), 5, nil); err != nil {
		t.Fatalf("Search(aux) after reopen failed: %v", err)
	}
	_ = kvStore
}
