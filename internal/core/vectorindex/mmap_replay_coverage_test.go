package vectorindex

import (
	"math"
	"testing"
)

// TestReplayWAL_UpperLevelInsert exercises the level>0 branch in replayWAL's
// WalInsert handler (mmap_store.go:441).
func TestReplayWAL_UpperLevelInsert(t *testing.T) {
	dir := t.TempDir()
	const dim = 4

	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 4, CheckpointInterval: 100_000})
	if err != nil {
		t.Fatal(err)
	}

	// Insert node 0 at level 0, node 1 at level 2.
	vec0 := []float32{1, 0, 0, 0}
	vec1 := []float32{0, 1, 0, 0}
	if err := s.PutNode(0, 0, vec0); err != nil {
		t.Fatal(err)
	}
	if err := s.PutNode(1, 2, vec1); err != nil {
		t.Fatal(err)
	}

	// Crash without Close — WAL has both inserts un-checkpointed.
	simulateCrash(s)

	// Reopen triggers replayWAL which must handle the level>0 insert.
	s2, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 4})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	// Verify node 1 vector recovered.
	got, err := s2.GetVector(1)
	if err != nil {
		t.Fatalf("GetVector(1): %v", err)
	}
	if got[1] != 1.0 {
		t.Fatalf("vec[1] = %f, want 1.0", got[1])
	}

	// Verify node 1 has level 2 recorded in metadata.
	nodeOff := int64(pageSize) + int64(1)*int64(nodeSlotSize)
	level := s2.nodes[nodeOff]
	if level != 2 {
		t.Fatalf("node 1 level = %d, want 2", level)
	}

	// Verify MaxLevel was updated.
	if s2.meta.MaxLevel < 2 {
		t.Fatalf("MaxLevel = %d, want >= 2", s2.meta.MaxLevel)
	}
}

// TestReplayWAL_SetNeighborsUpper exercises the layer>0 branch in replayWAL's
// WalSetNeighbors handler (mmap_store.go:469).
func TestReplayWAL_SetNeighborsUpper(t *testing.T) {
	dir := t.TempDir()
	const dim = 4

	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 4, CheckpointInterval: 100_000})
	if err != nil {
		t.Fatal(err)
	}

	// Insert two nodes; node 0 at level 1 so it has an upper slot.
	vec := []float32{1, 0, 0, 0}
	if err := s.PutNode(0, 1, vec); err != nil {
		t.Fatal(err)
	}
	if err := s.PutNode(1, 0, vec); err != nil {
		t.Fatal(err)
	}

	// Set upper-layer neighbors: node 0, layer 1 → [1].
	if err := s.SetNeighbors(0, 1, []uint64{1}); err != nil {
		t.Fatal(err)
	}
	// Also set L0 neighbors so we cover both branches.
	if err := s.SetNeighbors(0, 0, []uint64{1}); err != nil {
		t.Fatal(err)
	}

	simulateCrash(s)

	s2, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 4})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	// Verify upper-layer neighbors recovered.
	nbs, err := s2.GetNeighbors(0, 1)
	if err != nil {
		t.Fatalf("GetNeighbors(0, 1): %v", err)
	}
	if len(nbs) != 1 || nbs[0] != 1 {
		t.Fatalf("upper neighbors = %v, want [1]", nbs)
	}

	// Verify L0 neighbors recovered.
	nbs0, err := s2.GetNeighbors(0, 0)
	if err != nil {
		t.Fatalf("GetNeighbors(0, 0): %v", err)
	}
	if len(nbs0) != 1 || nbs0[0] != 1 {
		t.Fatalf("L0 neighbors = %v, want [1]", nbs0)
	}
}

// TestReplayWAL_SetNorm exercises the WalSetNorm branch in replayWAL
// (mmap_store.go:475).
func TestReplayWAL_SetNorm(t *testing.T) {
	dir := t.TempDir()
	const dim = 4

	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 4, CheckpointInterval: 100_000})
	if err != nil {
		t.Fatal(err)
	}

	vec := []float32{1, 0, 0, 0}
	if err := s.PutNode(0, 0, vec); err != nil {
		t.Fatal(err)
	}

	// Overwrite the norm with a known value.
	if err := s.SetNorm(0, 42.5); err != nil {
		t.Fatal(err)
	}

	simulateCrash(s)

	s2, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 4})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	norm, err := s2.GetNorm(0)
	if err != nil {
		t.Fatalf("GetNorm(0): %v", err)
	}
	if math.Abs(float64(norm-42.5)) > 0.01 {
		t.Fatalf("norm = %f, want 42.5", norm)
	}
}

// TestReplayWAL_SetEntry exercises the WalSetEntry branch in replayWAL
// (mmap_store.go:482-488).
func TestReplayWAL_SetEntry(t *testing.T) {
	dir := t.TempDir()
	const dim = 4

	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 4, CheckpointInterval: 100_000})
	if err != nil {
		t.Fatal(err)
	}

	// Insert a few nodes so the store is non-trivial.
	vec := []float32{1, 0, 0, 0}
	for i := uint64(0); i < 3; i++ {
		if err := s.PutNode(i, 0, vec); err != nil {
			t.Fatal(err)
		}
	}

	// Set node 2 as the entry point at level 3.
	if err := s.SetEntryPoint(2, 3); err != nil {
		t.Fatal(err)
	}

	simulateCrash(s)

	s2, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 4})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	id, level, err := s2.GetEntryPoint()
	if err != nil {
		t.Fatalf("GetEntryPoint: %v", err)
	}
	if id != 2 {
		t.Fatalf("entry id = %d, want 2", id)
	}
	if level != 3 {
		t.Fatalf("entry level = %d, want 3", level)
	}
}
