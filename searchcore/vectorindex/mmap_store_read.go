package vectorindex

import (
	"encoding/binary"
	"fmt"
	"math"
	"unsafe"
)

// GetVector returns a copy of the original vector for the given node ID. The
// returned slice is owned by the caller. For cosine the stored unit vector is
// restored to its original scale via the stored norm; for the raw metrics the
// stored vector is the original.
func (s *MmapStore) GetVector(id uint64) ([]float32, error) {
	ref, err := s.GetVectorRef(id)
	if err != nil {
		return nil, err
	}
	if s.metric.storesNormalized() {
		norm, err := s.GetNorm(id)
		if err != nil {
			return nil, err
		}
		return s.metric.restore(ref, norm), nil // allocates a fresh slice
	}
	vec := make([]float32, len(ref))
	copy(vec, ref)
	return vec, nil
}

// GetVectorRef returns a zero-copy view of the vector backed directly by the
// mmap region. Per the NodeStore contract the caller MUST NOT mutate the slice
// or retain it across a store mutation (which may remap the region). This
// avoids a per-call allocation on the hot distance-computation path; the HNSW
// index serializes inserts (which grow/remap) against searches via its own
// RWMutex, so refs stay valid for the duration of their use.
func (s *MmapStore) GetVectorRef(id uint64) ([]float32, error) {
	s.muVec.RLock()
	defer s.muVec.RUnlock()

	if id >= s.vecCapacity {
		return nil, fmt.Errorf("MmapStore.GetVectorRef: id %d out of range (cap %d)", id, s.vecCapacity)
	}

	offset := int64(pageSize) + int64(id)*int64(s.vecSlotSize)
	if offset%4 != 0 {
		return nil, fmt.Errorf("MmapStore.GetVectorRef: unaligned offset %d for id %d", offset, id)
	}
	ptr := (*float32)(unsafe.Pointer(&s.vectors[offset]))
	return unsafe.Slice(ptr, s.dim), nil
}

// GetNeighbors returns the neighbor list for the given node and layer.
func (s *MmapStore) GetNeighbors(id uint64, layer int) ([]uint64, error) {
	s.muGraph.RLock()
	defer s.muGraph.RUnlock()

	if layer == 0 {
		return s.getNeighborsL0(id)
	}
	return s.getNeighborsUpper(id, layer)
}

func (s *MmapStore) getNeighborsL0(id uint64) ([]uint64, error) {
	if id >= s.l0Capacity {
		return nil, fmt.Errorf("MmapStore.GetNeighbors: id %d out of range (l0 cap %d)", id, s.l0Capacity)
	}

	offset := int64(pageSize) + int64(id)*int64(s.l0SlotSize)
	count := int(binary.LittleEndian.Uint32(s.graphL0[offset : offset+4]))
	if count > s.mmax0 {
		count = s.mmax0
	}

	neighbors := make([]uint64, count)
	base := offset + 4
	for i := int64(0); i < int64(count); i++ {
		neighbors[i] = binary.LittleEndian.Uint64(s.graphL0[base+i*8 : base+i*8+8])
	}
	return neighbors, nil
}

func (s *MmapStore) getNeighborsUpper(id uint64, layer int) ([]uint64, error) {
	// Read the UpperSlot index from nodes.dat.
	// Lock order: muGraph (held by caller) → muNodes (acquired here). See MmapStore doc.
	s.muNodes.RLock()
	upperSlot, err := s.readUpperSlot(id)
	s.muNodes.RUnlock()
	if err != nil {
		return nil, err
	}
	if upperSlot == 0 {
		return nil, nil // no upper slot allocated
	}

	if uint64(upperSlot) >= s.upperCapacity {
		return nil, fmt.Errorf("MmapStore.GetNeighbors: upper slot %d out of range (cap %d)", upperSlot, s.upperCapacity)
	}

	layerIdx := layer - 1 // layer 1 maps to index 0 in upper slot
	if layerIdx < 0 || layerIdx >= s.maxLayers {
		return nil, nil
	}

	layerSize := graphUpperLayerSize(s.m)
	slotOffset := int64(pageSize) + int64(upperSlot)*int64(s.upperSlotSz)
	layerOffset := slotOffset + int64(layerIdx)*int64(layerSize)

	count := int(binary.LittleEndian.Uint32(s.graphUpper[layerOffset : layerOffset+4]))
	if count > s.m {
		count = s.m
	}

	neighbors := make([]uint64, count)
	base := layerOffset + 4
	for i := int64(0); i < int64(count); i++ {
		neighbors[i] = binary.LittleEndian.Uint64(s.graphUpper[base+i*8 : base+i*8+8])
	}
	return neighbors, nil
}

// readUpperSlot reads the UpperSlot field from nodes.dat for the given node.
// Caller must hold muNodes.RLock.
func (s *MmapStore) readUpperSlot(id uint64) (uint32, error) {
	if id >= s.nodeCapacity {
		return 0, fmt.Errorf("MmapStore: node id %d out of range (cap %d)", id, s.nodeCapacity)
	}
	offset := int64(pageSize) + int64(id)*int64(nodeSlotSize)
	// UpperSlot is at bytes 8..12 in the NodeSlot (after Level[1] + Flags[1] + pad[2] + Norm[4]).
	return binary.LittleEndian.Uint32(s.nodes[offset+8 : offset+12]), nil
}

// GetNorm returns the precomputed L2 norm for a node.
func (s *MmapStore) GetNorm(id uint64) (float32, error) {
	s.muNodes.RLock()
	defer s.muNodes.RUnlock()

	if id >= s.nodeCapacity {
		return 0, fmt.Errorf("MmapStore.GetNorm: id %d out of range (cap %d)", id, s.nodeCapacity)
	}
	offset := int64(pageSize) + int64(id)*int64(nodeSlotSize)
	// Norm is at bytes 4..8 in the NodeSlot.
	bits := binary.LittleEndian.Uint32(s.nodes[offset+4 : offset+8])
	return math.Float32frombits(bits), nil
}

// GetNodeLevel returns the level of the given node.
func (s *MmapStore) GetNodeLevel(id uint64) (int, error) {
	s.muNodes.RLock()
	defer s.muNodes.RUnlock()

	if id >= s.nodeCapacity {
		return 0, fmt.Errorf("MmapStore.GetNodeLevel: id %d out of range (cap %d)", id, s.nodeCapacity)
	}
	offset := int64(pageSize) + int64(id)*int64(nodeSlotSize)
	level := int(s.nodes[offset]) // Level is first byte
	flags := s.nodes[offset+1]
	if flags&nodeFlagDeleted != 0 {
		return 0, fmt.Errorf("MmapStore.GetNodeLevel: node %d is deleted", id)
	}
	return level, nil
}

// GetEntryPoint returns the entry point node ID and its level.
func (s *MmapStore) GetEntryPoint() (uint64, int, error) {
	s.muWrite.RLock()
	ep := s.meta.EntryPoint
	el := s.meta.EntryLevel
	s.muWrite.RUnlock()

	if ep == ^uint64(0) {
		return 0, 0, fmt.Errorf("MmapStore.GetEntryPoint: no entry point set")
	}
	return ep, int(el), nil
}

// GetNodeId looks up the node ID for a document ID.
// Triggers a lazy build of docToNode on first call (write-path only; held under muWrite).
func (s *MmapStore) GetNodeId(docId int64) (uint64, bool, error) {
	s.muWrite.Lock()
	s.ensureDocToNode()
	s.muWrite.Unlock()

	s.muDoc.RLock()
	id, ok := s.docToNode[docId]
	s.muDoc.RUnlock()
	return id, ok, nil
}

// GetDocId returns the document ID stored in the node's slot.
func (s *MmapStore) GetDocId(id uint64) (int64, bool, error) {
	s.muNodes.RLock()
	defer s.muNodes.RUnlock()

	if id >= s.nodeCapacity {
		return 0, false, fmt.Errorf("MmapStore.GetDocId: id %d out of range (cap %d)", id, s.nodeCapacity)
	}
	offset := int64(pageSize) + int64(id)*int64(nodeSlotSize)
	flags := s.nodes[offset+1]
	if flags&nodeFlagOccupied == 0 || flags&nodeFlagDeleted != 0 {
		return 0, false, nil
	}
	docId := int64(binary.LittleEndian.Uint64(s.nodes[offset+16:]))
	return docId, true, nil
}
