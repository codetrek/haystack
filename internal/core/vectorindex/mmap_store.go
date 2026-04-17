package vectorindex

import (
	"encoding/binary"
	"fmt"
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

	docToNode map[string]uint64
	nodeToDoc map[uint64]string

	muVec   sync.RWMutex
	muGraph sync.RWMutex
	muNodes sync.RWMutex

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

	setErr(mmapFree(s.vectors))
	setErr(mmapFree(s.nodes))
	setErr(mmapFree(s.graphL0))
	setErr(mmapFree(s.graphUpper))

	s.vectors = nil
	s.nodes = nil
	s.graphL0 = nil
	s.graphUpper = nil

	setErr(s.vecFile.Close())
	setErr(s.nodeFile.Close())
	setErr(s.l0File.Close())
	setErr(s.upperFile.Close())

	setErr(writeMetaHeader(s.dir, &s.meta))

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
	upperHdr := GraphUpperHeader{Magic: magicGraphUpper, MaxNeighbors: uint32(s.m), MaxLayers: uint32(s.maxLayers), Capacity: upperCap}
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

	for _, fi := range files {
		path := filepath.Join(s.dir, fi.name)
		f, err := os.OpenFile(path, os.O_RDWR, 0644)
		if err != nil {
			return fmt.Errorf("open %s: %w", fi.name, err)
		}
		*fi.file = f

		info, err := f.Stat()
		if err != nil {
			return fmt.Errorf("stat %s: %w", fi.name, err)
		}
		size := int(info.Size())

		mapped, err := mmapAlloc(f.Fd(), 0, size, mmapRead|mmapWrite)
		if err != nil {
			return fmt.Errorf("mmap %s: %w", fi.name, err)
		}
		*fi.data = mapped
	}

	// Read capacities from headers.
	s.vecCapacity = binary.LittleEndian.Uint64(s.vectors[8:16])       // VectorsHeader.Capacity at offset 8
	s.nodeCapacity = binary.LittleEndian.Uint64(s.nodes[8:16])        // NodesHeader.Capacity at offset 8
	s.l0Capacity = binary.LittleEndian.Uint64(s.graphL0[8:16])        // GraphL0Header.Capacity at offset 8
	s.upperCapacity = binary.LittleEndian.Uint64(s.graphUpper[16:24]) // GraphUpperHeader.Capacity at offset 16

	return nil
}
