package vectorindex

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"testing"
)

// exportMemStoreToMmap exports all data from a MemNodeStore into an MmapStore directory.
// This is a test helper for populating mmap files with known data.
func exportMemStoreToMmap(ms *MemNodeStore, dir string, dim, m int) (*MmapStore, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	// Determine required capacity.
	var maxID uint64
	for id := range ms.vectors {
		if id >= maxID {
			maxID = id + 1
		}
	}
	if maxID < defaultInitialCapacity {
		maxID = defaultInitialCapacity
	}

	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: m})
	if err != nil {
		return nil, err
	}

	// Count upper-layer nodes for upper slot allocation.
	var nextUpperSlot uint32 = 1 // slot 0 is reserved (0 means "no upper slot")

	// Write all nodes.
	for id, vec := range ms.vectors {
		if id >= s.vecCapacity {
			return nil, fmt.Errorf("exportMemStoreToMmap: id %d >= capacity %d, grow not implemented in Phase 1", id, s.vecCapacity)
		}

		// Write vector.
		vecOffset := pageSize + int(id)*s.vecSlotSize
		for i, v := range vec {
			binary.LittleEndian.PutUint32(s.vectors[vecOffset+i*4:], math.Float32bits(v))
		}

		// Write node slot.
		level := ms.levels[id]
		norm := ms.norms[id]
		var upperSlot uint32

		if level > 0 {
			upperSlot = nextUpperSlot
			nextUpperSlot++
			if uint64(upperSlot) >= s.upperCapacity {
				return nil, fmt.Errorf("exportMemStoreToMmap: upper slot %d >= capacity %d", upperSlot, s.upperCapacity)
			}
		}

		nodeOffset := pageSize + int(id)*nodeSlotSize
		s.nodes[nodeOffset] = byte(level)
		s.nodes[nodeOffset+1] = 0 // flags
		binary.LittleEndian.PutUint32(s.nodes[nodeOffset+4:], math.Float32bits(norm))
		binary.LittleEndian.PutUint32(s.nodes[nodeOffset+8:], upperSlot)

		// Write L0 neighbors.
		if layers, ok := ms.neighbors[id]; ok {
			if nb, ok := layers[0]; ok {
				l0Offset := pageSize + int(id)*s.l0SlotSize
				count := len(nb)
				if count > s.mmax0 {
					count = s.mmax0
				}
				binary.LittleEndian.PutUint32(s.graphL0[l0Offset:], uint32(count))
				for i := 0; i < count; i++ {
					binary.LittleEndian.PutUint64(s.graphL0[l0Offset+4+i*8:], nb[i])
				}
			}

			// Write upper neighbors.
			if upperSlot > 0 {
				for layer := 1; layer <= level; layer++ {
					if nb, ok := layers[layer]; ok {
						layerSize := graphUpperLayerSize(m)
						layerIdx := layer - 1
						offset := pageSize + int(upperSlot)*s.upperSlotSz + layerIdx*layerSize
						count := len(nb)
						if count > m {
							count = m
						}
						binary.LittleEndian.PutUint32(s.graphUpper[offset:], uint32(count))
						for i := 0; i < count; i++ {
							binary.LittleEndian.PutUint64(s.graphUpper[offset+4+i*8:], nb[i])
						}
					}
				}
			}
		}
	}

	// Set meta.
	s.meta.NodeCount = uint64(len(ms.vectors))
	s.meta.TotalSlots = maxID
	s.meta.NextNodeId = ms.nextID
	if ms.hasEntry {
		s.meta.EntryPoint = ms.entryID
		s.meta.EntryLevel = uint32(ms.maxLayer)
	}

	// Copy ID mappings.
	for doc, nodeId := range ms.docToNode {
		s.docToNode[doc] = nodeId
	}
	for nodeId, doc := range ms.nodeToDoc {
		s.nodeToDoc[nodeId] = doc
	}

	return s, nil
}

func TestExportMemStoreToMmap(t *testing.T) {
	// Build a small HNSW index with MemStore.
	memStore := NewMemNodeStore()
	dim := 32
	n := 1000
	rng := rand.New(rand.NewSource(42))

	// Insert n vectors.
	for i := 0; i < n; i++ {
		id := uint64(i)
		vec := make([]float32, dim)
		for j := range vec {
			vec[j] = rng.Float32()
		}
		memStore.PutNode(id, 0, vec)
		memStore.SetNodeMapping(fmt.Sprintf("doc%d", i), id)
		memStore.SetNeighbors(id, 0, []uint64{uint64((i + 1) % n), uint64((i + 2) % n)})
	}
	memStore.nextID = uint64(n)
	memStore.SetEntryPoint(0, 0)

	// Export to mmap.
	dir := t.TempDir()
	mmapS, err := exportMemStoreToMmap(memStore, dir, dim, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer mmapS.Close()

	// Verify all vectors match.
	for i := 0; i < n; i++ {
		memVec, _ := memStore.GetVector(uint64(i))
		mmapVec, err := mmapS.GetVector(uint64(i))
		if err != nil {
			t.Fatalf("GetVector(%d): %v", i, err)
		}
		for j := range memVec {
			if memVec[j] != mmapVec[j] {
				t.Fatalf("vec[%d][%d]: mem=%f, mmap=%f", i, j, memVec[j], mmapVec[j])
			}
		}
	}

	// Verify neighbors.
	for i := 0; i < n; i++ {
		memNb, _ := memStore.GetNeighbors(uint64(i), 0)
		mmapNb, err := mmapS.GetNeighbors(uint64(i), 0)
		if err != nil {
			t.Fatalf("GetNeighbors(%d, 0): %v", i, err)
		}
		if len(memNb) != len(mmapNb) {
			t.Fatalf("neighbors[%d] len: mem=%d, mmap=%d", i, len(memNb), len(mmapNb))
		}
		for j := range memNb {
			if memNb[j] != mmapNb[j] {
				t.Fatalf("neighbors[%d][%d]: mem=%d, mmap=%d", i, j, memNb[j], mmapNb[j])
			}
		}
	}

	// Verify norms.
	for i := 0; i < n; i++ {
		memNorm, _ := memStore.GetNorm(uint64(i))
		mmapNorm, err := mmapS.GetNorm(uint64(i))
		if err != nil {
			t.Fatalf("GetNorm(%d): %v", i, err)
		}
		if memNorm != mmapNorm {
			t.Fatalf("norm[%d]: mem=%f, mmap=%f", i, memNorm, mmapNorm)
		}
	}

	// Verify entry point.
	id, level, err := mmapS.GetEntryPoint()
	if err != nil {
		t.Fatal(err)
	}
	if id != 0 || level != 0 {
		t.Errorf("entry = (%d, %d), want (0, 0)", id, level)
	}

	// Verify ID mappings.
	for i := 0; i < n; i++ {
		docId := fmt.Sprintf("doc%d", i)
		nodeId, ok, err := mmapS.GetNodeId(docId)
		if err != nil {
			t.Fatal(err)
		}
		if !ok || nodeId != uint64(i) {
			t.Fatalf("GetNodeId(%s) = (%d, %v), want (%d, true)", docId, nodeId, ok, i)
		}
	}

	// Close and reopen — verify meta persisted.
	mmapS.Close()

	mmapS2, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer mmapS2.Close()

	if mmapS2.meta.NodeCount != uint64(n) {
		t.Errorf("NodeCount after reopen = %d, want %d", mmapS2.meta.NodeCount, n)
	}

	// Vectors should still be readable.
	v, err := mmapS2.GetVector(0)
	if err != nil {
		t.Fatal(err)
	}
	memV, _ := memStore.GetVector(0)
	for j := range v {
		if v[j] != memV[j] {
			t.Fatalf("after reopen vec[0][%d]: %f != %f", j, v[j], memV[j])
		}
	}
}
