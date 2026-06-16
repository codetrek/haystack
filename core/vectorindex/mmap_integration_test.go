package vectorindex

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// TestMmapStoreIntegration manually constructs mmap files on disk, then opens
// them with OpenMmapStore and verifies every read path returns the expected data.
func TestMmapStoreIntegration(t *testing.T) {
	const (
		dim       = 3
		m         = 4
		mmax0     = m * 2
		maxLayers = defaultMaxLayers
		cap       = 8 // slot capacity for all files
	)

	dir := t.TempDir()

	// --- Build binary files from scratch ---

	vecSlotSize := dim * 4
	l0SlotSz := 4 + mmax0*8
	upperLayerSz := 4 + m*8
	upperSlotSz := maxLayers * upperLayerSz
	upperCap := uint64(cap / 4)
	if upperCap < 64 {
		upperCap = 64
	}

	// vectors.dat
	vecData := make([]byte, pageSize+cap*vecSlotSize)
	copy(vecData[0:4], magicVectors[:])
	binary.LittleEndian.PutUint32(vecData[4:], uint32(dim))
	binary.LittleEndian.PutUint64(vecData[8:], cap)
	// Node 0 vector: [1.0, 2.0, 3.0]
	writeFloat32s(vecData, pageSize+0*vecSlotSize, []float32{1.0, 2.0, 3.0})
	// Node 1 vector: [4.0, 5.0, 6.0]
	writeFloat32s(vecData, pageSize+1*vecSlotSize, []float32{4.0, 5.0, 6.0})
	// Node 2 vector: [7.0, 8.0, 9.0]
	writeFloat32s(vecData, pageSize+2*vecSlotSize, []float32{7.0, 8.0, 9.0})
	writeFile(t, filepath.Join(dir, "vectors.dat"), vecData)

	// nodes.dat
	nodeData := make([]byte, pageSize+cap*nodeSlotSize)
	copy(nodeData[0:4], magicNodes[:])
	// padding 4 bytes
	binary.LittleEndian.PutUint64(nodeData[8:], cap)
	// Node 0: level=2, occupied, norm=1.5, upperSlot=1
	writeNodeSlotRaw(nodeData, 0, 2, nodeFlagOccupied, 1.5, 1)
	// Node 1: level=0, occupied, norm=2.0, upperSlot=0
	writeNodeSlotRaw(nodeData, 1, 0, nodeFlagOccupied, 2.0, 0)
	// Node 2: level=1, occupied, norm=3.0, upperSlot=2
	writeNodeSlotRaw(nodeData, 2, 1, nodeFlagOccupied, 3.0, 2)
	writeFile(t, filepath.Join(dir, "nodes.dat"), nodeData)

	// graph_l0.dat
	l0Data := make([]byte, pageSize+cap*l0SlotSz)
	copy(l0Data[0:4], magicGraphL0[:])
	binary.LittleEndian.PutUint32(l0Data[4:], uint32(mmax0))
	binary.LittleEndian.PutUint64(l0Data[8:], cap)
	// Node 0 L0 neighbors: [1, 2]
	writeNeighborsRaw(l0Data, pageSize+0*l0SlotSz, []uint64{1, 2})
	// Node 1 L0 neighbors: [0]
	writeNeighborsRaw(l0Data, pageSize+1*l0SlotSz, []uint64{0})
	// Node 2 L0 neighbors: [0, 1]
	writeNeighborsRaw(l0Data, pageSize+2*l0SlotSz, []uint64{0, 1})
	writeFile(t, filepath.Join(dir, "graph_l0.dat"), l0Data)

	// graph_upper.dat
	upperData := make([]byte, pageSize+int(upperCap)*upperSlotSz)
	copy(upperData[0:4], magicGraphUpper[:])
	binary.LittleEndian.PutUint32(upperData[4:], uint32(m))
	binary.LittleEndian.PutUint32(upperData[8:], uint32(maxLayers))
	// padding 4 bytes at [12:16]
	binary.LittleEndian.PutUint64(upperData[16:], upperCap)
	binary.LittleEndian.PutUint64(upperData[24:], 3) // NextSlot=3
	// Node 0 has upperSlot=1, level=2 → layer 1 at index 0, layer 2 at index 1
	// Upper slot 1, layer index 0 (=level 1): neighbors [2]
	writeNeighborsRaw(upperData, pageSize+1*upperSlotSz+0*upperLayerSz, []uint64{2})
	// Upper slot 1, layer index 1 (=level 2): neighbors [] (empty)
	// Node 2 has upperSlot=2, level=1 → layer 1 at index 0
	// Upper slot 2, layer index 0 (=level 1): neighbors [0]
	writeNeighborsRaw(upperData, pageSize+2*upperSlotSz+0*upperLayerSz, []uint64{0})
	writeFile(t, filepath.Join(dir, "graph_upper.dat"), upperData)

	// meta.bin
	meta := &MetaHeader{
		Version:    2,
		Dim:        dim,
		M:          m,
		Metric:     uint32(DotProduct),
		MaxLevel:   2,
		EntryLevel: 2,
		NodeCount:  3,
		TotalSlots: cap,
		EntryPoint: 0,
		NextNodeId: 3,
	}
	if err := writeMetaHeader(dir, meta); err != nil {
		t.Fatalf("writeMetaHeader: %v", err)
	}

	// --- Open and verify ---

	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: dim, M: m})
	if err != nil {
		t.Fatalf("OpenMmapStore: %v", err)
	}
	defer s.Close()

	// Verify GetEntryPoint
	epID, epLevel, err := s.GetEntryPoint()
	if err != nil {
		t.Fatalf("GetEntryPoint: %v", err)
	}
	if epID != 0 || epLevel != 2 {
		t.Errorf("GetEntryPoint = (%d, %d), want (0, 2)", epID, epLevel)
	}

	// Verify GetVector for all 3 nodes
	wantVecs := [][]float32{
		{1.0, 2.0, 3.0},
		{4.0, 5.0, 6.0},
		{7.0, 8.0, 9.0},
	}
	for i, want := range wantVecs {
		got, err := s.GetVector(uint64(i))
		if err != nil {
			t.Fatalf("GetVector(%d): %v", i, err)
		}
		assertFloat32s(t, got, want, "GetVector(%d)", i)
	}

	// Verify GetNodeLevel
	wantLevels := []int{2, 0, 1}
	for i, want := range wantLevels {
		got, err := s.GetNodeLevel(uint64(i))
		if err != nil {
			t.Fatalf("GetNodeLevel(%d): %v", i, err)
		}
		if got != want {
			t.Errorf("GetNodeLevel(%d) = %d, want %d", i, got, want)
		}
	}

	// Verify GetNeighbors layer 0
	wantL0 := [][]uint64{
		{1, 2},
		{0},
		{0, 1},
	}
	for i, want := range wantL0 {
		got, err := s.GetNeighbors(uint64(i), 0)
		if err != nil {
			t.Fatalf("GetNeighbors(%d, 0): %v", i, err)
		}
		assertUint64s(t, got, want, "GetNeighbors(%d, 0)", i)
	}

	// Verify GetNeighbors upper layers
	// Node 0, layer 1 → [2]
	got, err := s.GetNeighbors(0, 1)
	if err != nil {
		t.Fatalf("GetNeighbors(0, 1): %v", err)
	}
	assertUint64s(t, got, []uint64{2}, "GetNeighbors(0, 1)")

	// Node 0, layer 2 → [] (empty)
	got, err = s.GetNeighbors(0, 2)
	if err != nil {
		t.Fatalf("GetNeighbors(0, 2): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("GetNeighbors(0, 2) = %v, want empty", got)
	}

	// Node 2, layer 1 → [0]
	got, err = s.GetNeighbors(2, 1)
	if err != nil {
		t.Fatalf("GetNeighbors(2, 1): %v", err)
	}
	assertUint64s(t, got, []uint64{0}, "GetNeighbors(2, 1)")

	// Node 1 has level=0, no upper neighbors
	got, err = s.GetNeighbors(1, 1)
	if err != nil {
		t.Fatalf("GetNeighbors(1, 1): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("GetNeighbors(1, 1) = %v, want empty", got)
	}
}

// --- Raw file writing helpers (independent of MmapStore) ---

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeFloat32s(buf []byte, offset int, vals []float32) {
	for i, v := range vals {
		binary.LittleEndian.PutUint32(buf[offset+i*4:], math.Float32bits(v))
	}
}

func writeNodeSlotRaw(buf []byte, id int, level int, flags uint8, norm float32, upperSlot uint32) {
	off := pageSize + id*nodeSlotSize
	buf[off] = byte(level)
	buf[off+1] = flags
	binary.LittleEndian.PutUint32(buf[off+4:], math.Float32bits(norm))
	binary.LittleEndian.PutUint32(buf[off+8:], upperSlot)
}

func writeNeighborsRaw(buf []byte, offset int, neighbors []uint64) {
	binary.LittleEndian.PutUint32(buf[offset:], uint32(len(neighbors)))
	for i, n := range neighbors {
		binary.LittleEndian.PutUint64(buf[offset+4+i*8:], n)
	}
}

func assertFloat32s(t *testing.T, got, want []float32, msg string, args ...any) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf(msg+": len = %d, want %d", append(args, len(got), len(want))...)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf(msg+"[%d] = %f, want %f", append(args, i, got[i], want[i])...)
		}
	}
}

func assertUint64s(t *testing.T, got, want []uint64, msg string, args ...any) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf(msg+": len = %d, want %d", append(args, len(got), len(want))...)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf(msg+"[%d] = %d, want %d", append(args, i, got[i], want[i])...)
		}
	}
}
