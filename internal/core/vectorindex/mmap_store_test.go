package vectorindex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMmapStoreOpenClose(t *testing.T) {
	dir := t.TempDir()

	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 128, M: 16})
	if err != nil {
		t.Fatalf("OpenMmapStore: %v", err)
	}

	// All data files should exist.
	for _, name := range []string{"meta.bin", "vectors.dat", "nodes.dat", "graph_l0.dat", "graph_upper.dat"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing file %s: %v", name, err)
		}
	}

	// Check capacities.
	if s.vecCapacity != defaultInitialCapacity {
		t.Errorf("vecCapacity = %d, want %d", s.vecCapacity, defaultInitialCapacity)
	}
	if s.nodeCapacity != defaultInitialCapacity {
		t.Errorf("nodeCapacity = %d, want %d", s.nodeCapacity, defaultInitialCapacity)
	}
	if s.l0Capacity != defaultInitialCapacity {
		t.Errorf("l0Capacity = %d, want %d", s.l0Capacity, defaultInitialCapacity)
	}

	// Verify file sizes = header + capacity * slotSize.
	vecInfo, _ := os.Stat(filepath.Join(dir, "vectors.dat"))
	wantVecSize := int64(pageSize) + int64(defaultInitialCapacity)*int64(128*4)
	if vecInfo.Size() != wantVecSize {
		t.Errorf("vectors.dat size = %d, want %d", vecInfo.Size(), wantVecSize)
	}

	nodeInfo, _ := os.Stat(filepath.Join(dir, "nodes.dat"))
	wantNodeSize := int64(pageSize) + int64(defaultInitialCapacity)*int64(nodeSlotSize)
	if nodeInfo.Size() != wantNodeSize {
		t.Errorf("nodes.dat size = %d, want %d", nodeInfo.Size(), wantNodeSize)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-open should succeed with same params.
	s2, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 128, M: 16})
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	if s2.meta.Dim != 128 {
		t.Errorf("re-open dim = %d, want 128", s2.meta.Dim)
	}
	if err := s2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestMmapStoreOpenMismatch(t *testing.T) {
	dir := t.TempDir()

	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 128, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Dim mismatch.
	_, err = OpenMmapStore(dir, MmapStoreOptions{Dim: 256, M: 16})
	if err == nil {
		t.Fatal("expected error for dim mismatch")
	}

	// M mismatch.
	_, err = OpenMmapStore(dir, MmapStoreOptions{Dim: 128, M: 32})
	if err == nil {
		t.Fatal("expected error for M mismatch")
	}
}

func TestMmapStoreInvalidOpts(t *testing.T) {
	dir := t.TempDir()

	if _, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 0, M: 16}); err == nil {
		t.Fatal("expected error for dim=0")
	}
	if _, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 128, M: 0}); err == nil {
		t.Fatal("expected error for M=0")
	}
}
