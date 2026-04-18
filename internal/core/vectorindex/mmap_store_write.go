package vectorindex

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"

	"github.com/viterin/vek/vek32"
)

// PutNode stores a node's vector, level, and norm into the mmap files.
// WAL is written first, then the mmap regions.
// The docId for this node must have been set via SetNodeMapping before or after this call;
// the WAL INSERT record includes docId for crash recovery completeness.
func (s *MmapStore) PutNode(id uint64, level int, vector []float32) error {
	norm := vek32.Norm(vector)

	// Look up docId from the in-memory mapping (may be empty if not yet set).
	s.muDoc.RLock()
	docId := s.nodeToDoc[id]
	s.muDoc.RUnlock()

	// WAL
	if _, err := s.wal.Append(WalInsert, EncodeInsert(id, level, vector, norm, docId), s.batchMode); err != nil {
		return fmt.Errorf("MmapStore.PutNode: WAL: %w", err)
	}

	// Ensure capacity for vectors, nodes, and L0 graph.
	if err := s.ensureVecCapacity(id); err != nil {
		return err
	}
	if err := s.ensureNodeCapacity(id); err != nil {
		return err
	}
	if err := s.ensureL0Capacity(id); err != nil {
		return err
	}

	// Write vector.
	s.muVec.RLock()
	vecOff := int64(pageSize) + int64(id)*int64(s.vecSlotSize)
	for i, v := range vector {
		binary.LittleEndian.PutUint32(s.vectors[vecOff+int64(i*4):], math.Float32bits(v))
	}
	if !s.batchMode {
		mmapSync(s.vectors)
	}
	s.muVec.RUnlock()

	// Write node metadata (level, norm, upper slot).
	s.muNodes.RLock()
	nodeOff := int64(pageSize) + int64(id)*int64(nodeSlotSize)
	s.nodes[nodeOff] = uint8(level) // Level
	s.nodes[nodeOff+1] = 0          // Flags (not deleted)
	s.nodes[nodeOff+2] = 0          // padding
	s.nodes[nodeOff+3] = 0          // padding
	binary.LittleEndian.PutUint32(s.nodes[nodeOff+4:], math.Float32bits(norm))

	// If level > 0, allocate an upper slot.
	if level > 0 {
		if err := s.ensureUpperCapacity(s.readGraphUpperNextSlot()); err != nil {
			s.muNodes.RUnlock()
			return err
		}
		slot := s.allocUpperSlot()
		binary.LittleEndian.PutUint32(s.nodes[nodeOff+8:], slot)
	} else {
		binary.LittleEndian.PutUint32(s.nodes[nodeOff+8:], 0)
	}

	if !s.batchMode {
		mmapSync(s.nodes)
	}
	s.muNodes.RUnlock()

	// Update meta.
	if id >= s.meta.TotalSlots {
		s.meta.TotalSlots = id + 1
	}
	s.meta.NodeCount++
	if uint32(level) > s.meta.MaxLevel {
		s.meta.MaxLevel = uint32(level)
	}

	return nil
}

// SetNeighbors stores the neighbor list for a node and layer.
func (s *MmapStore) SetNeighbors(id uint64, layer int, neighbors []uint64) error {
	if _, err := s.wal.Append(WalSetNeighbors, EncodeSetNeighbors(id, layer, neighbors), s.batchMode); err != nil {
		return fmt.Errorf("MmapStore.SetNeighbors: WAL: %w", err)
	}

	s.muGraph.RLock()
	defer s.muGraph.RUnlock()

	if layer == 0 {
		return s.setNeighborsL0(id, neighbors)
	}
	return s.setNeighborsUpper(id, layer, neighbors)
}

func (s *MmapStore) setNeighborsL0(id uint64, neighbors []uint64) error {
	if id >= s.l0Capacity {
		return fmt.Errorf("MmapStore.SetNeighbors: id %d out of L0 range (cap %d)", id, s.l0Capacity)
	}

	offset := int64(pageSize) + int64(id)*int64(s.l0SlotSize)
	count := len(neighbors)
	if count > s.mmax0 {
		count = s.mmax0
	}
	binary.LittleEndian.PutUint32(s.graphL0[offset:], uint32(count))
	for i := 0; i < count; i++ {
		binary.LittleEndian.PutUint64(s.graphL0[offset+4+int64(i*8):], neighbors[i])
	}

	if !s.batchMode {
		mmapSync(s.graphL0)
	}
	return nil
}

func (s *MmapStore) setNeighborsUpper(id uint64, layer int, neighbors []uint64) error {
	s.muNodes.RLock()
	upperSlot, err := s.readUpperSlot(id)
	s.muNodes.RUnlock()
	if err != nil {
		return err
	}
	if upperSlot == 0 {
		return fmt.Errorf("MmapStore.SetNeighbors: node %d has no upper slot", id)
	}

	if uint64(upperSlot) >= s.upperCapacity {
		return fmt.Errorf("MmapStore.SetNeighbors: upper slot %d out of range (cap %d)", upperSlot, s.upperCapacity)
	}

	layerIdx := layer - 1
	if layerIdx < 0 || layerIdx >= s.maxLayers {
		return fmt.Errorf("MmapStore.SetNeighbors: layer %d out of range", layer)
	}

	layerSize := graphUpperLayerSize(s.m)
	slotOffset := int64(pageSize) + int64(upperSlot)*int64(s.upperSlotSz)
	layerOffset := slotOffset + int64(layerIdx)*int64(layerSize)

	count := len(neighbors)
	if count > s.m {
		count = s.m
	}
	binary.LittleEndian.PutUint32(s.graphUpper[layerOffset:], uint32(count))
	for i := 0; i < count; i++ {
		binary.LittleEndian.PutUint64(s.graphUpper[layerOffset+4+int64(i*8):], neighbors[i])
	}

	if !s.batchMode {
		mmapSync(s.graphUpper)
	}
	return nil
}

// SetNorm stores a precomputed L2 norm for a node's vector.
func (s *MmapStore) SetNorm(id uint64, norm float32) error {
	if _, err := s.wal.Append(WalSetNorm, EncodeSetNorm(id, norm), s.batchMode); err != nil {
		return fmt.Errorf("MmapStore.SetNorm: WAL: %w", err)
	}

	s.muNodes.RLock()
	defer s.muNodes.RUnlock()

	if id >= s.nodeCapacity {
		return fmt.Errorf("MmapStore.SetNorm: id %d out of range (cap %d)", id, s.nodeCapacity)
	}
	offset := int64(pageSize) + int64(id)*int64(nodeSlotSize)
	binary.LittleEndian.PutUint32(s.nodes[offset+4:], math.Float32bits(norm))

	if !s.batchMode {
		mmapSync(s.nodes)
	}
	return nil
}

// SetEntryPoint sets the HNSW entry point and max layer.
func (s *MmapStore) SetEntryPoint(id uint64, maxLayer int) error {
	if _, err := s.wal.Append(WalSetEntry, EncodeSetEntry(id, maxLayer), s.batchMode); err != nil {
		return fmt.Errorf("MmapStore.SetEntryPoint: WAL: %w", err)
	}

	s.meta.EntryPoint = id
	s.meta.EntryLevel = uint32(maxLayer)
	if uint32(maxLayer) > s.meta.MaxLevel {
		s.meta.MaxLevel = uint32(maxLayer)
	}
	return nil
}

// SetNodeMapping adds a docId ↔ nodeId mapping and persists it to idmap.dat.
func (s *MmapStore) SetNodeMapping(docId string, nodeId uint64) error {
	s.muDoc.Lock()
	s.docToNode[docId] = nodeId
	s.nodeToDoc[nodeId] = docId
	s.muDoc.Unlock()

	// Append to idmap.dat: NodeId(8) + DocIdLen(2) + DocId(var) + CRC32(4)
	docBytes := []byte(docId)
	entry := make([]byte, 10+len(docBytes)+4)
	binary.LittleEndian.PutUint64(entry[0:], nodeId)
	binary.LittleEndian.PutUint16(entry[8:], uint16(len(docBytes)))
	copy(entry[10:], docBytes)

	h := crc32.NewIEEE()
	h.Write(entry[:10+len(docBytes)])
	binary.LittleEndian.PutUint32(entry[10+len(docBytes):], h.Sum32())

	if _, err := s.idmapFile.Write(entry); err != nil {
		return fmt.Errorf("MmapStore.SetNodeMapping: write idmap: %w", err)
	}
	return nil
}

// DeleteNodeMapping removes a docId mapping from memory.
// idmap.dat is not updated (Phase 3 compact will clean it up).
func (s *MmapStore) DeleteNodeMapping(docId string) error {
	s.muDoc.Lock()
	defer s.muDoc.Unlock()

	if nodeId, ok := s.docToNode[docId]; ok {
		delete(s.nodeToDoc, nodeId)
	}
	delete(s.docToNode, docId)
	return nil
}

// DeleteNode marks a node as deleted (tombstone). Full implementation in Phase 3.
func (s *MmapStore) DeleteNode(id uint64) error {
	s.muNodes.RLock()
	defer s.muNodes.RUnlock()

	if id >= s.nodeCapacity {
		return fmt.Errorf("MmapStore.DeleteNode: id %d out of range (cap %d)", id, s.nodeCapacity)
	}

	offset := int64(pageSize) + int64(id)*int64(nodeSlotSize)
	s.nodes[offset+1] |= nodeFlagDeleted

	if s.meta.NodeCount > 0 {
		s.meta.NodeCount--
	}
	return nil
}

// readGraphUpperNextSlot reads the NextSlot field from graph_upper.dat header.
func (s *MmapStore) readGraphUpperNextSlot() uint64 {
	return binary.LittleEndian.Uint64(s.graphUpper[24:32]) // GraphUpperHeader.NextSlot at offset 24
}

// allocUpperSlot allocates the next upper graph slot and updates the header.
func (s *MmapStore) allocUpperSlot() uint32 {
	slot := s.readGraphUpperNextSlot()
	binary.LittleEndian.PutUint64(s.graphUpper[24:32], slot+1)
	return uint32(slot)
}

// --- BatchableStore implementation ---

// BeginBatch enters batch mode, deferring sync until CommitBatch.
func (s *MmapStore) BeginBatch() {
	s.batchDepth++
	s.batchMode = true
}

// CommitBatch exits one level of batch nesting. When depth reaches 0,
// flushes WAL and optionally syncs all mmap regions.
func (s *MmapStore) CommitBatch(sync bool) error {
	s.batchDepth--
	if s.batchDepth > 0 {
		return nil
	}
	s.batchMode = false

	if err := s.wal.Flush(); err != nil {
		return fmt.Errorf("MmapStore.CommitBatch: WAL flush: %w", err)
	}
	if sync {
		if err := s.wal.Sync(); err != nil {
			return fmt.Errorf("MmapStore.CommitBatch: WAL sync: %w", err)
		}
		if err := s.syncAll(); err != nil {
			return fmt.Errorf("MmapStore.CommitBatch: mmap sync: %w", err)
		}
	}
	return nil
}

// DiscardBatch resets batch state. Note: mmap writes cannot be rolled back.
func (s *MmapStore) DiscardBatch() {
	s.batchDepth = 0
	s.batchMode = false
}

// BatchDepth returns the current batch nesting depth.
func (s *MmapStore) BatchDepth() int {
	return s.batchDepth
}

// syncAll syncs all mmap regions to disk.
func (s *MmapStore) syncAll() error {
	if err := mmapSync(s.vectors); err != nil {
		return err
	}
	if err := mmapSync(s.nodes); err != nil {
		return err
	}
	if err := mmapSync(s.graphL0); err != nil {
		return err
	}
	return mmapSync(s.graphUpper)
}
