package vectorindex

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestMmapStoreGetVector(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Manually write a vector at slot 0.
	vec := []float32{1.0, 2.0, 3.0, 4.0}
	writeVecSlot(s, 0, vec)

	got, err := s.GetVector(0)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range got {
		if v != vec[i] {
			t.Errorf("vec[%d] = %f, want %f", i, v, vec[i])
		}
	}

	// GetVectorRef should return same values.
	ref, err := s.GetVectorRef(0)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range ref {
		if v != vec[i] {
			t.Errorf("ref[%d] = %f, want %f", i, v, vec[i])
		}
	}
}

func TestMmapStoreGetVectorOutOfRange(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_, err = s.GetVector(s.vecCapacity)
	if err == nil {
		t.Fatal("expected error for out-of-range id")
	}
}

func TestMmapStoreGetNeighborsL0(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Write neighbors at slot 5: count=3, neighbors=[10, 20, 30].
	writeL0Neighbors(s, 5, []uint64{10, 20, 30})

	got, err := s.GetNeighbors(5, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []uint64{10, 20, 30}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("neighbor[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestMmapStoreGetNeighborsUpper(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Set node 3's UpperSlot to 1.
	writeNodeSlot(s, 3, 2, 0, 1.5, 1)

	// Write upper neighbors at slot 1, layer 1 (index 0): [100, 200].
	writeUpperNeighbors(s, 1, 0, []uint64{100, 200})

	got, err := s.GetNeighbors(3, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 100 || got[1] != 200 {
		t.Errorf("upper neighbors = %v, want [100, 200]", got)
	}

	// Layer 2 (index 1) should be empty.
	got2, err := s.GetNeighbors(3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 0 {
		t.Errorf("layer 2 neighbors = %v, want empty", got2)
	}
}

func TestMmapStoreGetNormAndLevel(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	writeNodeSlot(s, 7, 3, 0, 2.5, 0)

	level, err := s.GetNodeLevel(7)
	if err != nil {
		t.Fatal(err)
	}
	if level != 3 {
		t.Errorf("level = %d, want 3", level)
	}

	norm, err := s.GetNorm(7)
	if err != nil {
		t.Fatal(err)
	}
	if norm != 2.5 {
		t.Errorf("norm = %f, want 2.5", norm)
	}
}

func TestMmapStoreGetNodeLevelDeleted(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	writeNodeSlot(s, 0, 1, nodeFlagDeleted, 1.0, 0)

	_, err = s.GetNodeLevel(0)
	if err == nil {
		t.Fatal("expected error for deleted node")
	}
}

func TestMmapStoreGetEntryPoint(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// No entry point initially.
	_, _, err = s.GetEntryPoint()
	if err == nil {
		t.Fatal("expected error when no entry point set")
	}

	// Set entry point.
	s.meta.EntryPoint = 42
	s.meta.EntryLevel = 3
	id, level, err := s.GetEntryPoint()
	if err != nil {
		t.Fatal(err)
	}
	if id != 42 || level != 3 {
		t.Errorf("entry = (%d, %d), want (42, 3)", id, level)
	}
}

func TestMmapStoreGetNodeId(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.docToNode["doc1"] = 99
	id, ok, err := s.GetNodeId("doc1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || id != 99 {
		t.Errorf("GetNodeId = (%d, %v), want (99, true)", id, ok)
	}

	_, ok, err = s.GetNodeId("missing")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected ok=false for missing doc")
	}
}

func TestMmapStoreReadUpperSlotOutOfRange(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.muNodes.RLock()
	_, err = s.readUpperSlot(s.nodeCapacity)
	s.muNodes.RUnlock()
	if err == nil {
		t.Fatal("expected error for out-of-range node id")
	}

	// Also test with nodeCapacity+1.
	s.muNodes.RLock()
	_, err = s.readUpperSlot(s.nodeCapacity + 1)
	s.muNodes.RUnlock()
	if err == nil {
		t.Fatal("expected error for node id beyond capacity")
	}
}

func TestMmapStoreGetNeighborsL0OutOfRange(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_, err = s.GetNeighbors(s.l0Capacity, 0)
	if err == nil {
		t.Fatal("expected error for out-of-range L0 id")
	}
}

func TestMmapStoreGetNeighborsUpperOutOfRange(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Node id out of range for readUpperSlot.
	_, err = s.GetNeighbors(s.nodeCapacity, 1)
	if err == nil {
		t.Fatal("expected error for out-of-range node id in upper path")
	}
}

func TestMmapStoreGetNeighborsUpperSlotZero(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Node 0 has UpperSlot=0 by default (no upper allocation).
	got, err := s.GetNeighbors(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil for upper slot 0, got %v", got)
	}
}

func TestMmapStoreGetNeighborsUpperBadLayer(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Set node 0's UpperSlot to 1.
	writeNodeSlot(s, 0, 2, 0, 1.0, 1)

	// Layer beyond maxLayers should return nil.
	got, err := s.GetNeighbors(0, s.maxLayers+1)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil for layer beyond maxLayers, got %v", got)
	}
}

func TestMmapStoreGetNeighborsUpperSlotOutOfRange(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Point node 0's UpperSlot to a value beyond upperCapacity.
	writeNodeSlot(s, 0, 2, 0, 1.0, uint32(s.upperCapacity))

	_, err = s.GetNeighbors(0, 1)
	if err == nil {
		t.Fatal("expected error for upper slot out of range")
	}
}

func TestMmapStoreGetNormOutOfRange(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_, err = s.GetNorm(s.nodeCapacity)
	if err == nil {
		t.Fatal("expected error for out-of-range id")
	}
}

func TestMmapStoreGetNodeLevelOutOfRange(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_, err = s.GetNodeLevel(s.nodeCapacity)
	if err == nil {
		t.Fatal("expected error for out-of-range id")
	}
}

// --- Test helpers: write directly into mmap regions ---

func writeVecSlot(s *MmapStore, id uint64, vec []float32) {
	offset := pageSize + int(id)*s.vecSlotSize
	for i, v := range vec {
		binary.LittleEndian.PutUint32(s.vectors[offset+i*4:], math.Float32bits(v))
	}
}

func writeNodeSlot(s *MmapStore, id uint64, level int, flags uint8, norm float32, upperSlot uint32) {
	offset := pageSize + int(id)*nodeSlotSize
	s.nodes[offset] = byte(level)
	s.nodes[offset+1] = flags
	binary.LittleEndian.PutUint32(s.nodes[offset+4:], math.Float32bits(norm))
	binary.LittleEndian.PutUint32(s.nodes[offset+8:], upperSlot)
}

func writeL0Neighbors(s *MmapStore, id uint64, neighbors []uint64) {
	offset := pageSize + int(id)*s.l0SlotSize
	binary.LittleEndian.PutUint32(s.graphL0[offset:], uint32(len(neighbors)))
	for i, n := range neighbors {
		binary.LittleEndian.PutUint64(s.graphL0[offset+4+i*8:], n)
	}
}

func writeUpperNeighbors(s *MmapStore, slot uint32, layerIdx int, neighbors []uint64) {
	layerSize := graphUpperLayerSize(s.m)
	offset := pageSize + int(slot)*s.upperSlotSz + layerIdx*layerSize
	binary.LittleEndian.PutUint32(s.graphUpper[offset:], uint32(len(neighbors)))
	for i, n := range neighbors {
		binary.LittleEndian.PutUint64(s.graphUpper[offset+4+i*8:], n)
	}
}
