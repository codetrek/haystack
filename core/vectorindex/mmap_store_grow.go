package vectorindex

import (
	"encoding/binary"
	"fmt"
)

// fileType identifies which mmap'd file to grow.
type fileType int

const (
	fileVectors fileType = iota
	fileNodes
	fileGraphL0
	fileGraphUpper
)

// ensureVecCapacity grows vectors.dat if id >= current capacity.
func (s *MmapStore) ensureVecCapacity(id uint64) error {
	if id < s.vecCapacity {
		return nil
	}
	return s.growFile(fileVectors, id+1)
}

// ensureNodeCapacity grows nodes.dat if id >= current capacity.
func (s *MmapStore) ensureNodeCapacity(id uint64) error {
	if id < s.nodeCapacity {
		return nil
	}
	return s.growFile(fileNodes, id+1)
}

// ensureL0Capacity grows graph_l0.dat if id >= current capacity.
func (s *MmapStore) ensureL0Capacity(id uint64) error {
	if id < s.l0Capacity {
		return nil
	}
	return s.growFile(fileGraphL0, id+1)
}

// ensureUpperCapacity grows graph_upper.dat if slot >= current capacity.
func (s *MmapStore) ensureUpperCapacity(slot uint64) error {
	if slot < s.upperCapacity {
		return nil
	}
	return s.growFile(fileGraphUpper, slot+1)
}

// growFile doubles the capacity of the given file until it reaches requiredCap.
func (s *MmapStore) growFile(which fileType, requiredCap uint64) error {
	switch which {
	case fileVectors:
		return s.growVectors(requiredCap)
	case fileNodes:
		return s.growNodes(requiredCap)
	case fileGraphL0:
		return s.growL0(requiredCap)
	case fileGraphUpper:
		return s.growUpper(requiredCap)
	default:
		return fmt.Errorf("unknown file type %d", which)
	}
}

func (s *MmapStore) growVectors(requiredCap uint64) error {
	// Caller holds muWrite. We also need muVec to block concurrent readers
	// of s.vectors while remapFile replaces the slice.

	// Re-check capacity.
	if requiredCap <= s.vecCapacity {
		return nil
	}
	newCap := s.vecCapacity
	if newCap == 0 {
		newCap = defaultInitialCapacity
	}
	for newCap < requiredCap {
		newCap *= 2
	}

	s.muVec.Lock()
	err := s.remapFile(s.vecFile, &s.vectors, &s.vecCapacity, newCap, s.vecSlotSize, 8) // VectorsHeader.Capacity at offset 8
	s.muVec.Unlock()
	return err
}

func (s *MmapStore) growNodes(requiredCap uint64) error {
	// Caller holds muWrite. We also need muNodes to block concurrent readers
	// of s.nodes while remapFile replaces the slice.

	if requiredCap <= s.nodeCapacity {
		return nil
	}
	newCap := s.nodeCapacity
	if newCap == 0 {
		newCap = defaultInitialCapacity
	}
	for newCap < requiredCap {
		newCap *= 2
	}

	s.muNodes.Lock()
	err := s.remapFile(s.nodeFile, &s.nodes, &s.nodeCapacity, newCap, nodeSlotSize, 8) // NodesHeader.Capacity at offset 8
	s.muNodes.Unlock()
	return err
}

func (s *MmapStore) growL0(requiredCap uint64) error {
	// Caller holds muWrite. We also need muGraph to block concurrent readers
	// of s.graphL0 while remapFile replaces the slice.

	if requiredCap <= s.l0Capacity {
		return nil
	}
	newCap := s.l0Capacity
	if newCap == 0 {
		newCap = defaultInitialCapacity
	}
	for newCap < requiredCap {
		newCap *= 2
	}

	s.muGraph.Lock()
	err := s.remapFile(s.l0File, &s.graphL0, &s.l0Capacity, newCap, s.l0SlotSize, 8) // GraphL0Header.Capacity at offset 8
	s.muGraph.Unlock()
	return err
}

func (s *MmapStore) growUpper(requiredCap uint64) error {
	// Caller holds muWrite. We also need muGraph to block concurrent readers
	// of s.graphUpper while remapFile replaces the slice.

	if requiredCap <= s.upperCapacity {
		return nil
	}
	newCap := s.upperCapacity
	if newCap == 0 {
		newCap = 64
	}
	for newCap < requiredCap {
		newCap *= 2
	}

	s.muGraph.Lock()
	err := s.remapFile(s.upperFile, &s.graphUpper, &s.upperCapacity, newCap, s.upperSlotSz, 16) // GraphUpperHeader.Capacity at offset 16
	s.muGraph.Unlock()
	return err
}

// remapFile is the common grow logic. Windows cannot resize a file while it is
// mapped, so map-new-then-unmap-old is not portable; instead it unmaps first but
// nils the slice and zeros the capacity *before* the fallible truncate/mmap, so
// a read after a failed grow returns an out-of-range error instead of
// dereferencing freed memory (audit #3). The caller propagates the error (a
// batched grow aborts the txn → faults the store).
func (s *MmapStore) remapFile(f osFile, data *[]byte, cap *uint64, newCap uint64, slotSize int, capHeaderOffset int) error {
	_ = mmapFree(*data) // best-effort; the region is being replaced regardless
	*data = nil
	*cap = 0

	newSize := int64(pageSize) + int64(newCap)*int64(slotSize)
	if err := f.Truncate(newSize); err != nil {
		return fmt.Errorf("grow: truncate: %w", err)
	}
	mapped, err := mmapAlloc(f.Fd(), 0, int(newSize), mmapRead|mmapWrite)
	if err != nil {
		return fmt.Errorf("grow: mmap: %w", err)
	}
	*data = mapped
	binary.LittleEndian.PutUint64(mapped[capHeaderOffset:], newCap) // update header capacity
	*cap = newCap
	return nil
}
