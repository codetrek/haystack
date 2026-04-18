package vectorindex

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"

	"github.com/viterin/vek/vek32"
)

// PutNode stores a node's vector, level, and norm into the mmap files.
// WAL is written first, then the mmap regions.
// The docId for this node must have been set via SetNodeMapping before or after this call;
// the WAL INSERT record includes docId for crash recovery completeness.
func (s *MmapStore) PutNode(id uint64, level int, vector []float32) error {
	s.muWrite.Lock()
	defer s.muWrite.Unlock()

	norm := vek32.Norm(vector)

	// Look up docId from the in-memory mapping (may be empty if not yet set).
	docId := s.nodeToDoc[id]

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
	vecOff := int64(pageSize) + int64(id)*int64(s.vecSlotSize)
	for i, v := range vector {
		binary.LittleEndian.PutUint32(s.vectors[vecOff+int64(i*4):], math.Float32bits(v))
	}
	if !s.batchMode {
		mmapSync(s.vectors)
	}

	// If level > 0, allocate an upper slot.
	var upperSlotVal uint32
	if level > 0 {
		if err := s.ensureUpperCapacity(s.readGraphUpperNextSlot()); err != nil {
			return err
		}
		upperSlotVal = s.allocUpperSlot()
	}

	// Write node metadata (level, norm, upper slot).
	nodeOff := int64(pageSize) + int64(id)*int64(nodeSlotSize)
	s.nodes[nodeOff] = uint8(level) // Level
	s.nodes[nodeOff+1] = 0          // Flags (not deleted)
	s.nodes[nodeOff+2] = 0          // padding
	s.nodes[nodeOff+3] = 0          // padding
	binary.LittleEndian.PutUint32(s.nodes[nodeOff+4:], math.Float32bits(norm))
	binary.LittleEndian.PutUint32(s.nodes[nodeOff+8:], upperSlotVal)

	if !s.batchMode {
		mmapSync(s.nodes)
	}

	// Update meta.
	if id >= s.meta.TotalSlots {
		s.meta.TotalSlots = id + 1
	}
	s.meta.NodeCount++
	if uint32(level) > s.meta.MaxLevel {
		s.meta.MaxLevel = uint32(level)
	}

	return s.maybeCheckpoint()
}

// SetNeighbors stores the neighbor list for a node and layer.
func (s *MmapStore) SetNeighbors(id uint64, layer int, neighbors []uint64) error {
	s.muWrite.Lock()
	defer s.muWrite.Unlock()

	if _, err := s.wal.Append(WalSetNeighbors, EncodeSetNeighbors(id, layer, neighbors), s.batchMode); err != nil {
		return fmt.Errorf("MmapStore.SetNeighbors: WAL: %w", err)
	}

	var err2 error
	if layer == 0 {
		err2 = s.setNeighborsL0(id, neighbors)
	} else {
		err2 = s.setNeighborsUpper(id, layer, neighbors)
	}
	if err2 != nil {
		return err2
	}
	return s.maybeCheckpoint()
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
	upperSlot, err := s.readUpperSlot(id)
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
	s.muWrite.Lock()
	defer s.muWrite.Unlock()

	if _, err := s.wal.Append(WalSetNorm, EncodeSetNorm(id, norm), s.batchMode); err != nil {
		return fmt.Errorf("MmapStore.SetNorm: WAL: %w", err)
	}

	if id >= s.nodeCapacity {
		return fmt.Errorf("MmapStore.SetNorm: id %d out of range (cap %d)", id, s.nodeCapacity)
	}
	offset := int64(pageSize) + int64(id)*int64(nodeSlotSize)
	binary.LittleEndian.PutUint32(s.nodes[offset+4:], math.Float32bits(norm))

	if !s.batchMode {
		mmapSync(s.nodes)
	}
	return s.maybeCheckpoint()
}

// SetEntryPoint sets the HNSW entry point and max layer.
func (s *MmapStore) SetEntryPoint(id uint64, maxLayer int) error {
	s.muWrite.Lock()
	defer s.muWrite.Unlock()

	if _, err := s.wal.Append(WalSetEntry, EncodeSetEntry(id, maxLayer), s.batchMode); err != nil {
		return fmt.Errorf("MmapStore.SetEntryPoint: WAL: %w", err)
	}

	s.meta.EntryPoint = id
	s.meta.EntryLevel = uint32(maxLayer)
	if uint32(maxLayer) > s.meta.MaxLevel {
		s.meta.MaxLevel = uint32(maxLayer)
	}
	return s.maybeCheckpoint()
}

// SetNodeMapping adds a docId ↔ nodeId mapping and persists it to idmap.dat.
func (s *MmapStore) SetNodeMapping(docId string, nodeId uint64) error {
	s.muWrite.Lock()
	defer s.muWrite.Unlock()

	s.muDoc.Lock()
	defer s.muDoc.Unlock()

	s.docToNode[docId] = nodeId
	s.nodeToDoc[nodeId] = docId

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
	s.muWrite.Lock()
	defer s.muWrite.Unlock()

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
	s.muWrite.Lock()
	defer s.muWrite.Unlock()

	// Look up docId for WAL record.
	docId := s.nodeToDoc[id]

	if _, err := s.wal.Append(WalDelete, EncodeDelete(id, docId), s.batchMode); err != nil {
		return fmt.Errorf("MmapStore.DeleteNode: WAL: %w", err)
	}

	if id >= s.nodeCapacity {
		return fmt.Errorf("MmapStore.DeleteNode: id %d out of range (cap %d)", id, s.nodeCapacity)
	}

	offset := int64(pageSize) + int64(id)*int64(nodeSlotSize)
	s.nodes[offset+1] |= nodeFlagDeleted

	if s.meta.NodeCount > 0 {
		s.meta.NodeCount--
	}
	return s.maybeCheckpoint()
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
	s.muWrite.Lock()
	defer s.muWrite.Unlock()

	s.batchDepth++
	s.batchMode = true
}

// CommitBatch exits one level of batch nesting. When depth reaches 0,
// flushes WAL and syncs all mmap regions. The sync parameter is kept for
// interface compatibility; batch commits always sync to ensure durability.
func (s *MmapStore) CommitBatch(sync bool) error {
	s.muWrite.Lock()
	defer s.muWrite.Unlock()

	s.batchDepth--
	if s.batchDepth > 0 {
		return nil
	}
	s.batchMode = false

	// Batch commits always sync to ensure durability.
	sync = true

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
	if s.opsSinceCheckpoint >= s.checkpointInterval {
		return s.checkpointLocked()
	}
	return nil
}

// DiscardBatch resets batch state. Note: mmap writes cannot be rolled back.
func (s *MmapStore) DiscardBatch() {
	s.muWrite.Lock()
	defer s.muWrite.Unlock()

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

// maybeCheckpoint increments the ops counter and triggers a checkpoint
// if the threshold is reached. Caller must hold muWrite.
func (s *MmapStore) maybeCheckpoint() error {
	s.opsSinceCheckpoint++
	if !s.batchMode && s.opsSinceCheckpoint >= s.checkpointInterval {
		return s.checkpointLocked()
	}
	return nil
}

// Checkpoint persists the current state: msync all mmap regions, write
// meta.bin with the current WAL LSN, truncate the WAL, and compact idmap.
func (s *MmapStore) Checkpoint() error {
	s.muWrite.Lock()
	defer s.muWrite.Unlock()
	return s.checkpointLocked()
}

func (s *MmapStore) checkpointLocked() error {
	// 1. msync all mmap regions.
	if err := s.syncAll(); err != nil {
		return fmt.Errorf("MmapStore.Checkpoint: msync: %w", err)
	}
	// 2. Record current WAL LSN into meta and write meta.bin atomically.
	s.meta.WalCheckpointLSN = s.wal.LSN()
	if err := writeMetaHeader(s.dir, &s.meta); err != nil {
		return fmt.Errorf("MmapStore.Checkpoint: meta: %w", err)
	}
	// 3. Truncate WAL (LSN is preserved inside WAL).
	if err := s.wal.Reset(); err != nil {
		return fmt.Errorf("MmapStore.Checkpoint: WAL reset: %w", err)
	}
	// 4. Compact idmap.
	if err := s.compactIdmap(); err != nil {
		return fmt.Errorf("MmapStore.Checkpoint: idmap compact: %w", err)
	}
	// 5. Reset ops counter.
	s.opsSinceCheckpoint = 0
	return nil
}

// compactIdmap rewrites idmap.dat with only the current in-memory mappings,
// removing stale entries from deleted nodes.
func (s *MmapStore) compactIdmap() error {
	path := filepath.Join(s.dir, "idmap.dat")
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	s.muDoc.RLock()
	for docId, nodeId := range s.docToNode {
		docBytes := []byte(docId)
		entry := make([]byte, 10+len(docBytes)+4)
		binary.LittleEndian.PutUint64(entry[0:], nodeId)
		binary.LittleEndian.PutUint16(entry[8:], uint16(len(docBytes)))
		copy(entry[10:], docBytes)
		h := crc32.NewIEEE()
		h.Write(entry[:10+len(docBytes)])
		binary.LittleEndian.PutUint32(entry[10+len(docBytes):], h.Sum32())
		if _, err := f.Write(entry); err != nil {
			s.muDoc.RUnlock()
			f.Close()
			os.Remove(tmp)
			return err
		}
	}
	s.muDoc.RUnlock()

	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()

	if err := os.Rename(tmp, path); err != nil {
		return err
	}

	// Reopen idmap for append.
	nf, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	s.idmapFile.Close()
	s.idmapFile = nf
	return nil
}

// NextNodeId returns the next available node ID (simple auto-increment).
// Thread safety: callers must serialize via HNSW h.mu (Insert holds it).
func (s *MmapStore) NextNodeId() (uint64, error) {
	s.muWrite.Lock()
	defer s.muWrite.Unlock()

	// TODO Phase 3: check freelist before increment
	id := s.meta.NextNodeId
	s.meta.NextNodeId++
	return id, nil
}
