package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// sealedStoreWithAux opens a store with one sealed segment and a built second index
// "aux", so DropVectorIndex("aux") has real graph-aux.dat files to unlink.
func sealedStoreWithAux(t *testing.T) *Store {
	t.Helper()
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(55))
	for i := 0; i < 40; i++ {
		v := make([]float32, 8)
		for d := range v {
			v[d] = rng.Float32()
		}
		requireNoError(t, s.Put("d-"+itoa(i), v, nil))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.CreateVectorIndex("aux", VectorIndexConfig{
		Type: "hnsw", Metric: Cosine, M: 16, EfConstruction: 200, EfSearch: 64,
	}))
	requireNoError(t, s.WaitForIndex())
	return s
}

// TestDropVectorIndex_GraphUnlinkError surfaces a non-ENOENT unlink failure during
// the durable graph removal (dropGraphFilesLocked) as a DropVectorIndex error before
// the manifest commit (appendix #21: the manifest must never get ahead of a
// non-durable unlink). The dropped index is healed on the next recover because the
// manifest still references it.
func TestDropVectorIndex_GraphUnlinkError(t *testing.T) {
	s := sealedStoreWithAux(t)
	orig := fsRemove
	fsRemove = func(p string) error {
		if filepath.Base(p) == graphFileName("aux") {
			return errInjected
		}
		return orig(p)
	}
	defer func() { fsRemove = orig }()

	if err := s.DropVectorIndex("aux"); err == nil {
		t.Fatal("expected DropVectorIndex to fail when the graph unlink errors")
	}
}

// TestDropVectorIndex_GraphAlreadyAbsent covers the pending leg of
// dropGraphFilesLocked: a sealed segment whose graph-<name>.dat is absent (the
// (index,segment) was still pending, so no file was ever written) must drop cleanly —
// the missing file is fine and forces no dir fsync.
func TestDropVectorIndex_GraphAlreadyAbsent(t *testing.T) {
	s := sealedStoreWithAux(t)
	orig := fsRemove
	fsRemove = func(p string) error {
		if filepath.Base(p) == graphFileName("aux") {
			return os.ErrNotExist // simulate a still-pending (index,seg): graph never written
		}
		return orig(p)
	}
	defer func() { fsRemove = orig }()

	requireNoError(t, s.DropVectorIndex("aux"))
	if _, err := s.Search("aux", make([]float32, 8), 5, nil); err == nil {
		t.Fatal("aux index should be gone after a successful drop")
	}
}
