package vectorindex

import (
	"fmt"
	"math"
	"testing"
)

// TestKill9Recovery_E2E simulates a kill -9 scenario:
// Insert 1000 vectors → "crash" (skip Close) → reopen → verify all data intact.
func TestKill9Recovery_E2E(t *testing.T) {
	dir := t.TempDir()
	const N = 1000
	const dim = 32

	// Phase 1: Write 1000 vectors in batches, then crash (no Close).
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 8, CheckpointInterval: 200})
	if err != nil {
		t.Fatal(err)
	}

	// Build vectors and insert with doc mappings.
	s.BeginBatch()
	for i := 0; i < N; i++ {
		vec := make([]float32, dim)
		for d := 0; d < dim; d++ {
			vec[d] = float32(i*dim + d)
		}
		if err := s.PutNode(uint64(i), 0, vec); err != nil {
			t.Fatal(err)
		}
		if err := s.SetNodeMapping(fmt.Sprintf("doc-%d", i), uint64(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CommitBatch(true); err != nil {
		t.Fatal(err)
	}

	// Set some neighbor relationships.
	for i := 0; i < N-1; i++ {
		if err := s.SetNeighbors(uint64(i), 0, []uint64{uint64(i + 1)}); err != nil {
			t.Fatal(err)
		}
	}

	// Delete a few nodes to test tombstone recovery.
	for _, id := range []uint64{10, 50, 100, 500, 999} {
		if err := s.DeleteNode(id); err != nil {
			t.Fatal(err)
		}
	}

	// Simulate kill -9: sync WAL but skip Close entirely.
	simulateCrash(s)

	// Phase 2: Reopen and verify everything recovered.
	s2, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 8})
	if err != nil {
		t.Fatalf("reopen after crash: %v", err)
	}
	defer s2.Close()

	deletedSet := map[uint64]bool{10: true, 50: true, 100: true, 500: true, 999: true}

	// Verify vectors.
	for i := 0; i < N; i++ {
		id := uint64(i)
		vec, err := s2.GetVector(id)
		if err != nil {
			t.Fatalf("GetVector(%d): %v", i, err)
		}
		for d := 0; d < dim; d++ {
			expected := float32(i*dim + d)
			if vec[d] != expected {
				t.Fatalf("vec[%d][%d]: got %f, want %f", i, d, vec[d], expected)
			}
		}
	}

	// Verify tombstones.
	for i := 0; i < N; i++ {
		id := uint64(i)
		nodeOff := int64(pageSize) + int64(id)*int64(nodeSlotSize)
		flags := s2.nodes[nodeOff+1]
		isDeleted := flags&nodeFlagDeleted != 0
		if deletedSet[id] && !isDeleted {
			t.Fatalf("node %d should be deleted", i)
		}
		if !deletedSet[id] && isDeleted {
			t.Fatalf("node %d should NOT be deleted", i)
		}
	}

	// Verify norms for non-deleted nodes are positive.
	for i := 0; i < N; i++ {
		id := uint64(i)
		if deletedSet[id] {
			continue
		}
		nodeOff := int64(pageSize) + int64(id)*int64(nodeSlotSize)
		norm := math.Float32frombits(
			uint32(s2.nodes[nodeOff+4]) |
				uint32(s2.nodes[nodeOff+5])<<8 |
				uint32(s2.nodes[nodeOff+6])<<16 |
				uint32(s2.nodes[nodeOff+7])<<24,
		)
		if norm <= 0 {
			t.Fatalf("node %d norm should be positive, got %f", i, norm)
		}
	}

	// Verify neighbor relationships.
	for i := 0; i < N-1; i++ {
		nbs, err := s2.GetNeighbors(uint64(i), 0)
		if err != nil {
			t.Fatalf("GetNeighbors(%d, 0): %v", i, err)
		}
		if len(nbs) != 1 || nbs[0] != uint64(i+1) {
			t.Fatalf("neighbors[%d]: got %v, want [%d]", i, nbs, i+1)
		}
	}

	// Verify node count (N minus deleted).
	expectedCount := uint64(N - len(deletedSet))
	if s2.meta.NodeCount != expectedCount {
		t.Fatalf("NodeCount: got %d, want %d", s2.meta.NodeCount, expectedCount)
	}
}
