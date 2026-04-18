package vectorindex

import (
	"encoding/binary"
	"fmt"
	"os"
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
	s.muVec.Lock()
	defer s.muVec.Unlock()

	// Re-check under lock.
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

	return s.remapFile(s.vecFile, &s.vectors, &s.vecCapacity, newCap, s.vecSlotSize, 8) // VectorsHeader.Capacity at offset 8
}

func (s *MmapStore) growNodes(requiredCap uint64) error {
	s.muNodes.Lock()
	defer s.muNodes.Unlock()

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

	return s.remapFile(s.nodeFile, &s.nodes, &s.nodeCapacity, newCap, nodeSlotSize, 8) // NodesHeader.Capacity at offset 8
}

func (s *MmapStore) growL0(requiredCap uint64) error {
	s.muGraph.Lock()
	defer s.muGraph.Unlock()

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

	return s.remapFile(s.l0File, &s.graphL0, &s.l0Capacity, newCap, s.l0SlotSize, 8) // GraphL0Header.Capacity at offset 8
}

func (s *MmapStore) growUpper(requiredCap uint64) error {
	s.muGraph.Lock()
	defer s.muGraph.Unlock()

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

	return s.remapFile(s.upperFile, &s.graphUpper, &s.upperCapacity, newCap, s.upperSlotSz, 16) // GraphUpperHeader.Capacity at offset 16
}

// remapFile is the common grow logic: munmap → ftruncate → update header capacity → re-mmap.
func (s *MmapStore) remapFile(f *os.File, data *[]byte, cap *uint64, newCap uint64, slotSize int, capHeaderOffset int) error {
	if err := mmapFree(*data); err != nil {
		return fmt.Errorf("grow: munmap: %w", err)
	}

	newSize := int64(pageSize) + int64(newCap)*int64(slotSize)
	if err := f.Truncate(newSize); err != nil {
		return fmt.Errorf("grow: truncate: %w", err)
	}

	mapped, err := mmapAlloc(f.Fd(), 0, int(newSize), mmapRead|mmapWrite)
	if err != nil {
		return fmt.Errorf("grow: mmap: %w", err)
	}
	*data = mapped

	// Update capacity in header.
	binary.LittleEndian.PutUint64(mapped[capHeaderOffset:], newCap)
	*cap = newCap
	return nil
}
