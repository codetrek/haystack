package vectorindex

import (
	"encoding/binary"
	"fmt"
	"math"
	"unsafe"
)

// GetVector returns a copy of the original vector for the given node ID. The
// returned slice is owned by the caller. Storage is raw for every metric now, so
// the original vector is exactly the stored bytes (no unit→scale restore).
//
// The copy is done while holding muWrite.RLock, which excludes a writer's grow
// (remap): the zero-copy mmap view must never escape the lock to be read by
// lock-less caller code, or a concurrent munmap turns it into a use-after-free
// (audit #2).
func (s *MmapStore) GetVector(id uint64) ([]float32, error) {
	s.muWrite.RLock()
	defer s.muWrite.RUnlock()

	if id >= s.vecCapacity {
		return nil, fmt.Errorf("MmapStore.GetVector: id %d out of range (cap %d)", id, s.vecCapacity)
	}
	if !s.nodeLive(id) {
		return nil, fmt.Errorf("MmapStore.GetVector: node %d is not live (deleted/zombie/unoccupied)", id)
	}
	offset := int64(pageSize) + int64(id)*int64(s.vecSlotSize)
	if offset%4 != 0 {
		return nil, fmt.Errorf("MmapStore.GetVector: unaligned offset %d for id %d", offset, id)
	}
	ptr := (*float32)(unsafe.Pointer(&s.vectors[offset]))
	ref := unsafe.Slice(ptr, s.dim) // valid only while the lock is held

	out := make([]float32, len(ref))
	copy(out, ref)
	return out, nil
}

// nodeLive reports whether id refers to a committed, occupied, non-deleted
// node. Caller must hold muWrite.RLock, which excludes all writers so
// meta.TotalSlots and the node's flags byte are stable. It rejects zombie slots
// (id >= TotalSlots — aborted/crashed-txn allocations whose Occupied bytes
// leaked to disk), unoccupied slots, and tombstoned slots, so the HNSW
// algorithm's skip-on-error logic keeps dead nodes out of search navigation and
// entry-point selection (mirrors GetDocId's guard).
func (s *MmapStore) nodeLive(id uint64) bool {
	// A faulted store has un-rolled-back in-place writes from an aborted/crashed
	// transaction (inflated TotalSlots, leaked Occupied bytes), so no node can be
	// trusted as live until a reopen replays only committed records. Recovery is
	// via reopen; reject every node here so the live instance stops serving
	// uncommitted state (VEC-009). Safe to read s.faulted: every nodeLive caller
	// holds muWrite.RLock, which excludes fault() (muWrite.Lock).
	if s.faulted != nil {
		return false
	}
	if id >= s.nodeCapacity || id >= s.meta.TotalSlots {
		return false
	}
	flags := s.nodes[int64(pageSize)+int64(id)*int64(nodeSlotSize)+1]
	return flags&nodeFlagOccupied != 0 && flags&nodeFlagDeleted == 0
}

// GetVectorRef returns a zero-copy view of the vector backed directly by the
// mmap region. Per the NodeStore contract the caller MUST NOT mutate the slice
// or retain it across a store mutation (which may remap the region). This avoids
// a per-call allocation on the hot distance path; the HNSW index serializes
// inserts (which grow/remap) against searches via its own RWMutex.
//
// Held under muWrite.RLock (like GetDocId): excludes writers, so the vector
// region, meta.TotalSlots, and the node flags are all stable, letting nodeLive
// reject deleted/zombie/unoccupied nodes instead of handing out stale bytes.
func (s *MmapStore) GetVectorRef(id uint64) ([]float32, error) {
	s.muWrite.RLock()
	defer s.muWrite.RUnlock()

	if id >= s.vecCapacity {
		return nil, fmt.Errorf("MmapStore.GetVectorRef: id %d out of range (cap %d)", id, s.vecCapacity)
	}
	if !s.nodeLive(id) {
		return nil, fmt.Errorf("MmapStore.GetVectorRef: node %d is not live (deleted/zombie/unoccupied)", id)
	}

	offset := int64(pageSize) + int64(id)*int64(s.vecSlotSize)
	if offset%4 != 0 {
		return nil, fmt.Errorf("MmapStore.GetVectorRef: unaligned offset %d for id %d", offset, id)
	}
	ptr := (*float32)(unsafe.Pointer(&s.vectors[offset]))
	return unsafe.Slice(ptr, s.dim), nil
}

// GetVectorRefWithNorm returns the zero-copy stored (raw) vector AND its
// precomputed L2 norm under a SINGLE muWrite.RLock. It deliberately inlines the
// nodeLive/offset checks and both the vectors.dat slice and the nodes.dat norm
// read rather than calling the separately-locking GetVectorRef/GetNorm: those
// each re-take muWrite.RLock, and a re-entrant RLock can deadlock against a
// queued writer. Same contract as GetVectorRef — the caller MUST NOT mutate or
// retain the slice across a store mutation (which may remap the region).
func (s *MmapStore) GetVectorRefWithNorm(id uint64) ([]float32, float32, error) {
	s.muWrite.RLock()
	defer s.muWrite.RUnlock()

	if id >= s.vecCapacity {
		return nil, 0, fmt.Errorf("MmapStore.GetVectorRefWithNorm: id %d out of range (cap %d)", id, s.vecCapacity)
	}
	if !s.nodeLive(id) {
		return nil, 0, fmt.Errorf("MmapStore.GetVectorRefWithNorm: node %d is not live (deleted/zombie/unoccupied)", id)
	}

	offset := int64(pageSize) + int64(id)*int64(s.vecSlotSize)
	if offset%4 != 0 {
		return nil, 0, fmt.Errorf("MmapStore.GetVectorRefWithNorm: unaligned offset %d for id %d", offset, id)
	}
	ptr := (*float32)(unsafe.Pointer(&s.vectors[offset]))
	ref := unsafe.Slice(ptr, s.dim)

	// Norm lives at bytes 4..8 of the node slot.
	normOff := int64(pageSize) + int64(id)*int64(nodeSlotSize)
	norm := math.Float32frombits(binary.LittleEndian.Uint32(s.nodes[normOff+4 : normOff+8]))
	return ref, norm, nil
}

// GetNeighbors returns the neighbor list for the given node and layer.
//
// Unlike the other read accessors, this is intentionally NOT gated on a faulted
// store (the VEC-009 guard). It reads under muGraph.RLock, not muWrite, so
// testing s.faulted here would either race with fault() (muWrite.Lock) or need a
// lock-order-inverting muWrite acquisition (writers take muWrite then muGraph in
// grow → deadlock). Every in-tree caller is already gated upstream on a faulted
// store: Search returns at GetEntryPoint (faulted) before reaching GetNeighbors,
// and write paths reject at txnBegin. A direct external caller on a faulted
// store may still observe uncommitted edges; recovery is via reopen.
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
	s.muWrite.RLock()
	defer s.muWrite.RUnlock()

	if id >= s.nodeCapacity {
		return 0, fmt.Errorf("MmapStore.GetNorm: id %d out of range (cap %d)", id, s.nodeCapacity)
	}
	if !s.nodeLive(id) {
		return 0, fmt.Errorf("MmapStore.GetNorm: node %d is not live (deleted/zombie/unoccupied)", id)
	}
	offset := int64(pageSize) + int64(id)*int64(nodeSlotSize)
	// Norm is at bytes 4..8 in the NodeSlot.
	bits := binary.LittleEndian.Uint32(s.nodes[offset+4 : offset+8])
	return math.Float32frombits(bits), nil
}

// GetNodeLevel returns the level of the given node.
func (s *MmapStore) GetNodeLevel(id uint64) (int, error) {
	s.muWrite.RLock()
	defer s.muWrite.RUnlock()

	if id >= s.nodeCapacity {
		return 0, fmt.Errorf("MmapStore.GetNodeLevel: id %d out of range (cap %d)", id, s.nodeCapacity)
	}
	if !s.nodeLive(id) {
		return 0, fmt.Errorf("MmapStore.GetNodeLevel: node %d is not live (deleted/zombie/unoccupied)", id)
	}
	offset := int64(pageSize) + int64(id)*int64(nodeSlotSize)
	return int(s.nodes[offset]), nil // Level is first byte
}

// GetEntryPoint returns the entry point node ID and its level.
func (s *MmapStore) GetEntryPoint() (uint64, int, error) {
	s.muWrite.RLock()
	if s.faulted != nil {
		s.muWrite.RUnlock()
		return 0, 0, s.faulted // VEC-009: don't serve an aborted/uncommitted entry point
	}
	ep := s.meta.EntryPoint
	el := s.meta.EntryLevel
	s.muWrite.RUnlock()

	if ep == ^uint64(0) {
		return 0, 0, errNoEntryPoint
	}
	return ep, int(el), nil
}

// HighestLiveNodeExcluding scans nodes.dat for the highest-level live node other
// than `exclude`. Live == occupied && !deleted && id < TotalSlots (same liveness
// test as nodeLive / GetDocId; rejects zombie/aborted-txn slots). The scan is
// inlined under muWrite.RLock and deliberately does NOT call nodeLive/
// GetNodeLevel — those re-take muWrite.RLock, and a re-entrant RLock can deadlock
// against a queued writer. The ascending-index scan keeps the lowest id at the
// max level (deterministic, matching MemNodeStore's tie-break).
func (s *MmapStore) HighestLiveNodeExcluding(exclude uint64) (uint64, int, bool, error) {
	s.muWrite.RLock()
	defer s.muWrite.RUnlock()

	if s.faulted != nil {
		return 0, 0, false, s.faulted // VEC-009: don't surface aborted-txn nodes on a faulted store
	}

	bestID := uint64(0)
	bestLevel := -1
	found := false
	total := s.meta.TotalSlots
	if total > s.nodeCapacity {
		total = s.nodeCapacity // defensive: TotalSlots <= capacity normally
	}
	for i := uint64(0); i < total; i++ {
		if i == exclude {
			continue
		}
		off := int64(pageSize) + int64(i)*int64(nodeSlotSize)
		flags := s.nodes[off+1] // Flags is byte 1
		if flags&nodeFlagOccupied == 0 || flags&nodeFlagDeleted != 0 {
			continue
		}
		lvl := int(s.nodes[off]) // Level is byte 0
		if !found || lvl > bestLevel {
			bestID = i
			bestLevel = lvl
			found = true
		}
	}
	if !found {
		return 0, 0, false, nil
	}
	return bestID, bestLevel, true, nil
}

// GetNodeId looks up the node ID for a document ID.
// Triggers a lazy build of docToNode on first call (write-path only; held under muWrite).
func (s *MmapStore) GetNodeId(docId int64) (uint64, bool, error) {
	s.muWrite.Lock()
	if s.faulted != nil {
		s.muWrite.Unlock()
		return 0, false, s.faulted // VEC-009: don't map docIds on a faulted store
	}
	s.ensureDocToNode()
	s.muWrite.Unlock()

	s.muDoc.RLock()
	id, ok := s.docToNode[docId]
	s.muDoc.RUnlock()
	return id, ok, nil
}

// GetDocId returns the document ID stored in the node's slot.
//
// Held under muWrite.RLock for the whole body (like GetEntryPoint): this
// excludes all writers, so both s.meta.TotalSlots and the slot bytes are stable
// and no separate muNodes lock is needed. The TotalSlots bound rejects slots in
// [TotalSlots, nodeCapacity) — these are uncommitted/zombie slots whose mmap
// writes from an aborted or crashed transaction leaked to disk with Occupied
// set. A committed node can hold a graph edge to such a slot, so Search may
// reach it; returning not-found here prevents surfacing aborted-txn data.
func (s *MmapStore) GetDocId(id uint64) (int64, bool, error) {
	s.muWrite.RLock()
	defer s.muWrite.RUnlock()

	if s.faulted != nil {
		return 0, false, s.faulted // VEC-009: don't surface aborted-txn docIds
	}
	if id >= s.nodeCapacity {
		return 0, false, fmt.Errorf("MmapStore.GetDocId: id %d out of range (cap %d)", id, s.nodeCapacity)
	}
	if id >= s.meta.TotalSlots {
		return 0, false, nil // uncommitted/zombie slot
	}
	offset := int64(pageSize) + int64(id)*int64(nodeSlotSize)
	flags := s.nodes[offset+1]
	if flags&nodeFlagOccupied == 0 || flags&nodeFlagDeleted != 0 {
		return 0, false, nil
	}
	docId := int64(binary.LittleEndian.Uint64(s.nodes[offset+16:]))
	return docId, true, nil
}
