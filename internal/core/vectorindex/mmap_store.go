package vectorindex

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"sync"
)

const defaultInitialCapacity = 1024

// MmapStoreOptions configures OpenMmapStore.
type MmapStoreOptions struct {
	Dim int // vector dimension (required)
	M   int // HNSW M parameter (required)
}

// MmapStore implements NodeStore backed by mmap'd flat files.
//
// Lock ordering (acquire outer before inner to avoid deadlocks):
//
//	muGraph → muNodes → muVec
//
// getNeighborsUpper acquires muNodes while muGraph is held — this is
// consistent with the ordering above. Any future write path must follow
// the same order.
type MmapStore struct {
	dir  string
	meta MetaHeader

	vectors    []byte // vectors.dat mmap
	nodes      []byte // nodes.dat mmap
	graphL0    []byte // graph_l0.dat mmap
	graphUpper []byte // graph_upper.dat mmap

	vecFile   *os.File
	nodeFile  *os.File
	l0File    *os.File
	upperFile *os.File

	// WAL and batch support
	wal        *WAL
	batchMode  bool
	batchDepth int

	// ID mapping (doc ↔ node)
	docToNode map[string]uint64
	nodeToDoc map[uint64]string
	idmapFile *os.File // idmap.dat append handle

	muVec   sync.RWMutex
	muGraph sync.RWMutex
	muNodes sync.RWMutex
	muDoc   sync.RWMutex // protects docToNode

	dim           int
	m             int
	mmax0         int
	vecSlotSize   int
	l0SlotSize    int
	upperSlotSz   int
	maxLayers     int
	vecCapacity   uint64
	nodeCapacity  uint64
	l0Capacity    uint64
	upperCapacity uint64
}

// OpenMmapStore opens or creates an mmap-backed store in dir.
func OpenMmapStore(dir string, opts MmapStoreOptions) (*MmapStore, error) {
	if opts.Dim <= 0 {
		return nil, fmt.Errorf("MmapStore: dim must be > 0")
	}
	if opts.M <= 0 {
		return nil, fmt.Errorf("MmapStore: M must be > 0")
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("MmapStore: mkdir: %w", err)
	}

	s := &MmapStore{
		dir:         dir,
		dim:         opts.Dim,
		m:           opts.M,
		mmax0:       opts.M * 2,
		vecSlotSize: opts.Dim * 4,
		l0SlotSize:  graphL0SlotSize(opts.M * 2),
		maxLayers:   defaultMaxLayers,
		docToNode:   make(map[string]uint64),
		nodeToDoc:   make(map[uint64]string),
	}
	s.upperSlotSz = graphUpperSlotSize(opts.M, s.maxLayers)

	metaPath := filepath.Join(dir, "meta.bin")
	if _, err := os.Stat(metaPath); os.IsNotExist(err) {
		// New index — initialize all files.
		if err := s.initAllFiles(defaultInitialCapacity); err != nil {
			return nil, fmt.Errorf("MmapStore: init: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("MmapStore: stat meta: %w", err)
	} else {
		// Existing index — read meta.
		h, err := readMetaHeader(dir)
		if err != nil {
			return nil, err
		}
		if int(h.Dim) != opts.Dim {
			return nil, fmt.Errorf("MmapStore: dim mismatch: file=%d, opts=%d", h.Dim, opts.Dim)
		}
		if int(h.M) != opts.M {
			return nil, fmt.Errorf("MmapStore: M mismatch: file=%d, opts=%d", h.M, opts.M)
		}
		s.meta = *h
	}

	// mmap all data files.
	if err := s.mmapAll(); err != nil {
		return nil, fmt.Errorf("MmapStore: mmap: %w", err)
	}

	// Open WAL.
	wal, err := OpenWAL(dir)
	if err != nil {
		s.closeMmaps()
		return nil, fmt.Errorf("MmapStore: WAL: %w", err)
	}
	s.wal = wal

	// Load idmap.dat (docId ↔ nodeId mappings).
	if err := s.loadIdmap(); err != nil {
		wal.Close()
		s.closeMmaps()
		return nil, fmt.Errorf("MmapStore: idmap: %w", err)
	}

	// Replay WAL from checkpoint.
	if err := s.replayWAL(); err != nil {
		s.idmapFile.Close()
		wal.Close()
		s.closeMmaps()
		return nil, fmt.Errorf("MmapStore: replay: %w", err)
	}

	return s, nil
}

// Close unmaps all files and flushes metadata.
func (s *MmapStore) Close() error {
	var firstErr error
	setErr := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// 1. msync all mmap regions.
	setErr(mmapSync(s.vectors))
	setErr(mmapSync(s.nodes))
	setErr(mmapSync(s.graphL0))
	setErr(mmapSync(s.graphUpper))

	// 2. WAL sync.
	if s.wal != nil {
		setErr(s.wal.Sync())
	}

	// 3. Write final meta header.
	setErr(writeMetaHeader(s.dir, &s.meta))

	// 4. Close WAL.
	if s.wal != nil {
		setErr(s.wal.Close())
	}

	// 5. Close idmap file.
	if s.idmapFile != nil {
		setErr(s.idmapFile.Close())
	}

	// 6. munmap all.
	setErr(mmapFree(s.vectors))
	setErr(mmapFree(s.nodes))
	setErr(mmapFree(s.graphL0))
	setErr(mmapFree(s.graphUpper))

	s.vectors = nil
	s.nodes = nil
	s.graphL0 = nil
	s.graphUpper = nil

	// 7. Close all files.
	setErr(s.vecFile.Close())
	setErr(s.nodeFile.Close())
	setErr(s.l0File.Close())
	setErr(s.upperFile.Close())

	return firstErr
}

// initAllFiles creates all data files for a new index.
func (s *MmapStore) initAllFiles(cap uint64) error {
	s.meta = MetaHeader{
		Version:    1,
		Dim:        uint32(s.dim),
		M:          uint32(s.m),
		EntryPoint: ^uint64(0), // sentinel: no entry point
	}

	if err := writeMetaHeader(s.dir, &s.meta); err != nil {
		return err
	}

	// vectors.dat
	vecHdr := VectorsHeader{Magic: magicVectors, Dim: uint32(s.dim), Capacity: cap}
	vecSize := int64(pageSize) + int64(cap)*int64(s.vecSlotSize)
	if err := writeDataFileHeader(filepath.Join(s.dir, "vectors.dat"), magicVectors, &vecHdr, vecSize); err != nil {
		return fmt.Errorf("vectors.dat: %w", err)
	}

	// nodes.dat
	nodeHdr := NodesHeader{Magic: magicNodes, Capacity: cap}
	nodeSize := int64(pageSize) + int64(cap)*int64(nodeSlotSize)
	if err := writeDataFileHeader(filepath.Join(s.dir, "nodes.dat"), magicNodes, &nodeHdr, nodeSize); err != nil {
		return fmt.Errorf("nodes.dat: %w", err)
	}

	// graph_l0.dat
	l0Hdr := GraphL0Header{Magic: magicGraphL0, MaxNeighbors: uint32(s.mmax0), Capacity: cap}
	l0Size := int64(pageSize) + int64(cap)*int64(s.l0SlotSize)
	if err := writeDataFileHeader(filepath.Join(s.dir, "graph_l0.dat"), magicGraphL0, &l0Hdr, l0Size); err != nil {
		return fmt.Errorf("graph_l0.dat: %w", err)
	}

	// graph_upper.dat (initial capacity for upper slots, much smaller)
	upperCap := cap / 4 // ~25% initial allocation for upper slots
	if upperCap < 64 {
		upperCap = 64
	}
	upperHdr := GraphUpperHeader{Magic: magicGraphUpper, MaxNeighbors: uint32(s.m), MaxLayers: uint32(s.maxLayers), Capacity: upperCap, NextSlot: 1}
	upperSize := int64(pageSize) + int64(upperCap)*int64(s.upperSlotSz)
	if err := writeDataFileHeader(filepath.Join(s.dir, "graph_upper.dat"), magicGraphUpper, &upperHdr, upperSize); err != nil {
		return fmt.Errorf("graph_upper.dat: %w", err)
	}

	return nil
}

// mmapAll opens and mmaps all data files.
func (s *MmapStore) mmapAll() error {
	type fileInfo struct {
		name string
		file **os.File
		data *[]byte
		cap  *uint64
	}

	files := []fileInfo{
		{"vectors.dat", &s.vecFile, &s.vectors, &s.vecCapacity},
		{"nodes.dat", &s.nodeFile, &s.nodes, &s.nodeCapacity},
		{"graph_l0.dat", &s.l0File, &s.graphL0, &s.l0Capacity},
		{"graph_upper.dat", &s.upperFile, &s.graphUpper, &s.upperCapacity},
	}

	// Track opened files and mappings for cleanup on error.
	var openedFiles []*os.File
	var mappedRegions [][]byte
	cleanup := func() { // nocov: error cleanup path — requires partial mmap failure to trigger
		for _, m := range mappedRegions {
			mmapFree(m)
		}
		for _, f := range openedFiles {
			f.Close()
		}
	}

	for _, fi := range files {
		path := filepath.Join(s.dir, fi.name)
		f, err := os.OpenFile(path, os.O_RDWR, 0644)
		if err != nil {
			cleanup()
			return fmt.Errorf("open %s: %w", fi.name, err)
		}
		*fi.file = f
		openedFiles = append(openedFiles, f)

		info, err := f.Stat()
		if err != nil {
			cleanup()
			return fmt.Errorf("stat %s: %w", fi.name, err)
		}
		size := int(info.Size())

		mapped, err := mmapAlloc(f.Fd(), 0, size, mmapRead|mmapWrite)
		if err != nil {
			cleanup()
			return fmt.Errorf("mmap %s: %w", fi.name, err)
		}
		*fi.data = mapped
		mappedRegions = append(mappedRegions, mapped)
	}

	// Read capacities from headers.
	s.vecCapacity = binary.LittleEndian.Uint64(s.vectors[8:16])       // VectorsHeader.Capacity at offset 8
	s.nodeCapacity = binary.LittleEndian.Uint64(s.nodes[8:16])        // NodesHeader.Capacity at offset 8
	s.l0Capacity = binary.LittleEndian.Uint64(s.graphL0[8:16])        // GraphL0Header.Capacity at offset 8
	s.upperCapacity = binary.LittleEndian.Uint64(s.graphUpper[16:24]) // GraphUpperHeader.Capacity at offset 16

	return nil
}

// closeMmaps unmaps and closes all data files (cleanup helper).
func (s *MmapStore) closeMmaps() {
	mmapFree(s.vectors)
	mmapFree(s.nodes)
	mmapFree(s.graphL0)
	mmapFree(s.graphUpper)
	if s.vecFile != nil {
		s.vecFile.Close()
	}
	if s.nodeFile != nil {
		s.nodeFile.Close()
	}
	if s.l0File != nil {
		s.l0File.Close()
	}
	if s.upperFile != nil {
		s.upperFile.Close()
	}
}

// loadIdmap reads idmap.dat and populates docToNode/nodeToDoc maps.
func (s *MmapStore) loadIdmap() error {
	path := filepath.Join(s.dir, "idmap.dat")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	s.idmapFile = f

	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	off := 0
	for off+14 <= len(data) { // min entry: 8(nodeId) + 2(docIdLen) + 0(docId) + 4(crc) = 14
		nodeId := binary.LittleEndian.Uint64(data[off:])
		docIdLen := int(binary.LittleEndian.Uint16(data[off+8:]))
		entryEnd := off + 10 + docIdLen + 4
		if entryEnd > len(data) {
			break
		}

		entryData := data[off : off+10+docIdLen]
		storedCRC := binary.LittleEndian.Uint32(data[off+10+docIdLen:])

		h := crc32.NewIEEE()
		h.Write(entryData)
		if h.Sum32() != storedCRC {
			break
		}

		docId := string(data[off+10 : off+10+docIdLen])
		s.docToNode[docId] = nodeId
		s.nodeToDoc[nodeId] = docId
		off = entryEnd
	}

	if _, err := f.Seek(0, 2); err != nil {
		return err
	}
	return nil
}

// replayWAL replays WAL records after the checkpoint LSN to restore both
// mmap data and meta state. All 5 record types are handled so that crash
// recovery fully reconstructs the index.
func (s *MmapStore) replayWAL() error {
	return s.wal.Replay(s.meta.WalCheckpointLSN, func(lsn uint64, typ WalRecordType, payload []byte) error {
		switch typ {
		case WalInsert:
			nodeId, level, vec, norm, _ := DecodeInsert(payload)

			// Ensure capacity for all regions before writing.
			if err := s.ensureVecCapacity(nodeId); err != nil {
				return err
			}
			if err := s.ensureNodeCapacity(nodeId); err != nil {
				return err
			}
			if err := s.ensureL0Capacity(nodeId); err != nil {
				return err
			}

			// Write vector to vectors.dat.
			vecOff := int64(pageSize) + int64(nodeId)*int64(s.vecSlotSize)
			for i, v := range vec {
				binary.LittleEndian.PutUint32(s.vectors[vecOff+int64(i*4):], math.Float32bits(v))
			}

			// Write node metadata to nodes.dat (level, flags=0, norm).
			nodeOff := int64(pageSize) + int64(nodeId)*int64(nodeSlotSize)
			s.nodes[nodeOff] = uint8(level)
			s.nodes[nodeOff+1] = 0 // flags: not deleted
			s.nodes[nodeOff+2] = 0
			s.nodes[nodeOff+3] = 0
			binary.LittleEndian.PutUint32(s.nodes[nodeOff+4:], math.Float32bits(norm))

			// If level > 0, allocate an upper slot.
			if level > 0 {
				if err := s.ensureUpperCapacity(s.readGraphUpperNextSlot()); err != nil {
					return err
				}
				slot := s.allocUpperSlot()
				binary.LittleEndian.PutUint32(s.nodes[nodeOff+8:], slot)
			} else {
				binary.LittleEndian.PutUint32(s.nodes[nodeOff+8:], 0)
			}

			// Update meta.
			if nodeId >= s.meta.TotalSlots {
				s.meta.TotalSlots = nodeId + 1
			}
			if nodeId+1 > s.meta.NextNodeId {
				s.meta.NextNodeId = nodeId + 1
			}
			s.meta.NodeCount++
			if uint32(level) > s.meta.MaxLevel {
				s.meta.MaxLevel = uint32(level)
			}

		case WalSetNeighbors:
			nodeId, layer, neighbors := DecodeSetNeighbors(payload)
			if layer == 0 {
				if err := s.setNeighborsL0(nodeId, neighbors); err != nil {
					return err
				}
			} else {
				if err := s.setNeighborsUpper(nodeId, layer, neighbors); err != nil {
					return err
				}
			}

		case WalSetNorm:
			nodeId, norm := DecodeSetNorm(payload)
			if nodeId < s.nodeCapacity {
				offset := int64(pageSize) + int64(nodeId)*int64(nodeSlotSize)
				binary.LittleEndian.PutUint32(s.nodes[offset+4:], math.Float32bits(norm))
			}

		case WalSetEntry:
			entryId, maxLevel := DecodeSetEntry(payload)
			s.meta.EntryPoint = entryId
			s.meta.EntryLevel = uint32(maxLevel)
			if uint32(maxLevel) > s.meta.MaxLevel {
				s.meta.MaxLevel = uint32(maxLevel)
			}

		case WalDelete:
			nodeId, _ := DecodeDelete(payload)
			if nodeId < s.nodeCapacity {
				offset := int64(pageSize) + int64(nodeId)*int64(nodeSlotSize)
				s.nodes[offset+1] |= nodeFlagDeleted
			}
			if s.meta.NodeCount > 0 {
				s.meta.NodeCount--
			}
		}
		return nil
	})
}
