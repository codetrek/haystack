package vectorindex

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
)

const defaultInitialCapacity = 1024

// MmapStoreOptions configures OpenMmapStore.
type MmapStoreOptions struct {
	Dim                int    // vector dimension (required)
	M                  int    // HNSW M parameter (required)
	Metric             Metric // distance metric (default Cosine); immutable once created
	CheckpointInterval int    // auto-checkpoint every N WAL appends (0 = default 1000)
}

// MmapStore implements NodeStore backed by mmap'd flat files.
//
// Concurrency model:
//
//   - All write methods (PutNode, SetNeighbors, SetNorm, SetEntryPoint,
//     DeleteNode, NextNodeId, and the transaction primitive txnBegin,
//     txnCommit, txnAbort) are serialised by muWrite.Lock().
//   - Read methods that touch the vector mmap (GetVector, GetVectorRef) and the
//     node slots (GetNorm, GetNodeLevel, GetDocId, GetEntryPoint) hold
//     muWrite.RLock, which excludes every writer (and thus any grow/remap), so
//     the mmap slices stay valid for the read. Graph reads use muGraph; the
//     docToNode map uses muDoc.
//   - Grow functions (ensureCapacity / growFile) are called under muWrite
//     and do not acquire additional locks.
type MmapStore struct {
	dir  string
	meta MetaHeader

	metric Metric

	vectors    []byte // vectors.dat mmap
	nodes      []byte // nodes.dat mmap
	graphL0    []byte // graph_l0.dat mmap
	graphUpper []byte // graph_upper.dat mmap

	vecFile   osFile
	nodeFile  osFile
	l0File    osFile
	upperFile osFile

	// WAL and transaction support
	wal                *WAL
	inTxn              bool  // a store transaction (txnBegin..txnCommit) is open
	faulted            error // first fatal write error; once set, writes are rejected
	opsSinceCheckpoint uint64
	checkpointInterval uint64

	// Forward mapping (docId → nodeId): built lazily on first write.
	docToNode      map[int64]uint64
	docToNodeBuilt bool // docToNode is built lazily on first write

	muWrite sync.RWMutex // serialises all write methods; readers use RLock for meta fields
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

	// Testing hooks — nil in production; zero overhead.
	crashAfterWALWrite  func() // called after WAL Append in PutNode
	crashAfterMsync     func() // called after syncAll in Checkpoint
	crashAfterMeta      func() // called after writeMetaHeader in Checkpoint
	crashBeforeTruncate func() // called before WAL Reset in Checkpoint
}

// Metric returns the store's immutable distance metric.
func (s *MmapStore) Metric() Metric { return s.metric }

// Dim returns the fixed vector dimension this store was created with.
func (s *MmapStore) Dim() int { return s.dim }

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
		metric:      opts.Metric,
		dim:         opts.Dim,
		m:           opts.M,
		mmax0:       opts.M * 2,
		vecSlotSize: opts.Dim * 4,
		l0SlotSize:  graphL0SlotSize(opts.M * 2),
		maxLayers:   defaultMaxLayers,
		docToNode:   make(map[int64]uint64),
	}
	s.upperSlotSz = graphUpperSlotSize(opts.M, s.maxLayers)
	s.checkpointInterval = 1000
	if opts.CheckpointInterval > 0 {
		s.checkpointInterval = uint64(opts.CheckpointInterval)
	}

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
		if h.Version != 2 {
			return nil, fmt.Errorf("MmapStore: unsupported on-disk version %d (want 2)", h.Version)
		}
		if int(h.Dim) != opts.Dim {
			return nil, fmt.Errorf("MmapStore: dim mismatch: file=%d, opts=%d", h.Dim, opts.Dim)
		}
		if int(h.M) != opts.M {
			return nil, fmt.Errorf("MmapStore: M mismatch: file=%d, opts=%d", h.M, opts.M)
		}
		if Metric(h.Metric) != opts.Metric {
			return nil, fmt.Errorf("MmapStore: metric mismatch: file=%s, opts=%s", Metric(h.Metric), opts.Metric)
		}
		s.metric = Metric(h.Metric)
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

	// Seed the WAL's LSN floor from the last checkpoint so new records get LSNs
	// strictly greater than any a future Replay(afterLSN=WalCheckpointLSN) skips.
	// A checkpoint truncates the WAL; on reopen scanLSN restarts at 0, which
	// would otherwise make post-reopen writes recoverable-then-discarded.
	wal.SeedLSN(s.meta.WalCheckpointLSN)

	// Replay WAL from checkpoint.
	if err := s.replayWAL(); err != nil { // nocov: WAL replay error during Open
		wal.Close()
		s.closeMmaps()
		return nil, fmt.Errorf("MmapStore: replay: %w", err)
	}

	// Checkpoint after replay to persist recovered state and truncate WAL,
	// so subsequent Opens don't re-replay the same records.
	if s.wal.LSN() > s.meta.WalCheckpointLSN {
		if err := s.checkpointLocked(); err != nil { // nocov: post-replay checkpoint failure requires msync/rename to fail
			wal.Close()
			s.closeMmaps()
			return nil, fmt.Errorf("MmapStore: post-replay checkpoint: %w", err)
		}
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

	// 1. Checkpoint: msync + writeMeta + WAL truncate.
	// A faulted store has uncommitted in-place writes; checkpointing here would
	// persist them and truncate the WAL, defeating crash-recovery discard. Skip it.
	if s.faulted == nil {
		setErr(s.checkpointLocked())
	}

	// 2. Close WAL.
	if s.wal != nil {
		setErr(s.wal.Close())
	}

	// 3. munmap all.
	setErr(mmapFree(s.vectors))
	setErr(mmapFree(s.nodes))
	setErr(mmapFree(s.graphL0))
	setErr(mmapFree(s.graphUpper))

	s.vectors = nil
	s.nodes = nil
	s.graphL0 = nil
	s.graphUpper = nil

	// 4. Close all files.
	setErr(s.vecFile.Close())
	setErr(s.nodeFile.Close())
	setErr(s.l0File.Close())
	setErr(s.upperFile.Close())

	return firstErr
}

// initAllFiles creates all data files for a new index.
func (s *MmapStore) initAllFiles(cap uint64) error {
	s.meta = MetaHeader{
		Version:    2,
		Dim:        uint32(s.dim),
		M:          uint32(s.m),
		Metric:     uint32(s.metric),
		EntryPoint: ^uint64(0), // sentinel: no entry point
	}

	// Create the 4 data files BEFORE publishing meta.bin. meta.bin is the
	// new-vs-existing sentinel (OpenMmapStore stats it), and writeMetaHeader now
	// fsyncs the rename so meta.bin is durable. If meta.bin were written first, a
	// crash before the .dat files exist would leave a durable sentinel with no
	// data files, and the reopen would take the existing-index branch and fail in
	// mmapAll. Publishing meta.bin last makes a half-built index reopen as "new".

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

	// fsync the directory so the newly-created data files' entries are durable
	// before we publish meta.bin as the existence sentinel.
	if err := fsyncDir(s.dir); err != nil {
		return fmt.Errorf("initAllFiles: fsync dir: %w", err)
	}

	// Publish meta.bin LAST: writeMetaHeader does its own atomic rename + dir
	// fsync, so once this returns the index is complete and durable.
	if err := writeMetaHeader(s.dir, &s.meta); err != nil {
		return err
	}

	return nil
}

// mmapAll opens and mmaps all data files.
func (s *MmapStore) mmapAll() error {
	type fileInfo struct {
		name string
		file *osFile
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
	var openedFiles []osFile
	var mappedRegions [][]byte
	cleanup := func() {
		for _, m := range mappedRegions {
			mmapFree(m)
		}
		for _, f := range openedFiles {
			f.Close()
		}
	}

	for _, fi := range files {
		path := filepath.Join(s.dir, fi.name)
		f, err := fsOpenFile(path, os.O_RDWR, 0644)
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

// ensureDocToNode builds docToNode from nodes.dat on first call.
// Must be called under muWrite (guards docToNodeBuilt flag).
// The map itself is populated under muDoc.
//
// It reads s.nodes without taking muNodes: running under muWrite.Lock() excludes
// every writer (and thus any concurrent growNodes remap of s.nodes), and HNSW's
// h.mu prevents a concurrent Search reader during any write, so the slice and
// its bytes are stable for the duration of this scan.
func (s *MmapStore) ensureDocToNode() {
	if s.docToNodeBuilt {
		return
	}
	mmapAdviseSequential(s.nodes)
	m := make(map[int64]uint64, s.meta.NodeCount)
	for i := uint64(0); i < s.meta.TotalSlots; i++ {
		off := int64(pageSize) + int64(i)*int64(nodeSlotSize)
		flags := s.nodes[off+1]
		if flags&nodeFlagOccupied == 0 || flags&nodeFlagDeleted != 0 {
			continue
		}
		docId := int64(binary.LittleEndian.Uint64(s.nodes[off+16:]))
		m[docId] = i
	}
	s.muDoc.Lock()
	s.docToNode = m
	s.muDoc.Unlock()
	s.docToNodeBuilt = true
}

// applyWALRecord applies a single decoded WAL record to mmap + meta state.
// Caller must hold muWrite (replay holds it implicitly: single-threaded Open).
// It contains exactly the logic previously inlined in the replayWAL callback.
func (s *MmapStore) applyWALRecord(typ WalRecordType, payload []byte) error {
	switch typ {
	case WalInsert:
		nodeId, level, vec, norm, docId := DecodeInsert(payload)
		if len(vec) != s.dim {
			return fmt.Errorf("applyWALRecord: WalInsert vec dim %d != store dim %d", len(vec), s.dim)
		}
		if err := s.ensureVecCapacity(nodeId); err != nil {
			return err
		}
		if err := s.ensureNodeCapacity(nodeId); err != nil {
			return err
		}
		if err := s.ensureL0Capacity(nodeId); err != nil {
			return err
		}
		vecOff := int64(pageSize) + int64(nodeId)*int64(s.vecSlotSize)
		for i, v := range vec {
			binary.LittleEndian.PutUint32(s.vectors[vecOff+int64(i*4):], math.Float32bits(v))
		}
		nodeOff := int64(pageSize) + int64(nodeId)*int64(nodeSlotSize)
		s.nodes[nodeOff] = uint8(level)
		s.nodes[nodeOff+1] = nodeFlagOccupied
		s.nodes[nodeOff+2] = 0
		s.nodes[nodeOff+3] = 0
		binary.LittleEndian.PutUint32(s.nodes[nodeOff+4:], math.Float32bits(norm))
		if level > 0 {
			if err := s.ensureUpperCapacity(s.readGraphUpperNextSlot()); err != nil {
				return err
			}
			slot := s.allocUpperSlot()
			binary.LittleEndian.PutUint32(s.nodes[nodeOff+8:], slot)
		} else {
			binary.LittleEndian.PutUint32(s.nodes[nodeOff+8:], 0)
		}
		binary.LittleEndian.PutUint32(s.nodes[nodeOff+12:], 0)             // pad[4]: keep slot fully defined for reuse
		binary.LittleEndian.PutUint64(s.nodes[nodeOff+16:], uint64(docId)) // DocId at offset 16
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
		nodeId := DecodeDelete(payload)
		if nodeId < s.nodeCapacity {
			offset := int64(pageSize) + int64(nodeId)*int64(nodeSlotSize)
			s.nodes[offset+1] |= nodeFlagDeleted
		}
		if s.meta.NodeCount > 0 {
			s.meta.NodeCount--
		}
	}
	return nil
}

// replayWAL replays WAL records after the checkpoint LSN to restore both
// mmap data and meta state. All 5 record types are handled so that crash
// recovery fully reconstructs the index.
func (s *MmapStore) replayWAL() error {
	var replayed int
	prevInTxn := s.inTxn
	s.inTxn = true
	defer func() { s.inTxn = prevInTxn }()

	// Transaction framing: records between WalTxnBegin and its matching
	// WalTxnCommit are buffered and applied atomically on COMMIT. An
	// unterminated trailing transaction (BEGIN with no COMMIT) is discarded.
	// Un-framed records (no open transaction) apply immediately — this keeps
	// pre-redesign WAL files and single-record streams working.
	//
	// Nested BEGIN contract: a WalTxnBegin while a transaction is already open
	// discards the prior (un-committed) buffer and restarts — consistent with
	// "uncommitted ⇒ discarded". The write side never emits nested BEGINs
	// (txnBegin rejects nesting); this is purely defensive on replay.
	type pending struct {
		typ     WalRecordType
		payload []byte
	}
	var inTxn bool
	var buf []pending

	err := s.wal.Replay(s.meta.WalCheckpointLSN, func(lsn uint64, typ WalRecordType, payload []byte) error {
		switch typ {
		case WalTxnBegin:
			inTxn = true
			buf = buf[:0]
			return nil
		case WalTxnCommit:
			if !inTxn {
				return nil // stray commit — ignore
			}
			for _, p := range buf {
				if err := s.applyWALRecord(p.typ, p.payload); err != nil {
					return err
				}
				replayed++
			}
			inTxn = false
			buf = buf[:0]
			return nil
		default:
			if inTxn {
				// Defensive copy: Replay currently allocates a fresh payload per
				// record, but we don't depend on that — own the slice so a future
				// Replay change cannot silently alias buffered payloads.
				cp := make([]byte, len(payload))
				copy(cp, payload)
				buf = append(buf, pending{typ, cp})
				return nil
			}
			replayed++
			return s.applyWALRecord(typ, payload)
		}
	})
	if err != nil {
		return err
	}
	// Unterminated trailing transaction (inTxn still true): buf is dropped.
	if replayed > 0 {
		s.rebuildNodeCount()
		if err := s.syncAll(); err != nil {
			return err
		}
	}
	return nil
}

// rebuildNodeCount scans nodes.dat and counts occupied, non-tombstone slots,
// replacing the WAL-replayed NodeCount with the authoritative value. Occupancy
// is marked explicitly by nodeFlagOccupied (set on insert and on WAL replay);
// a never-written slot is all-zero, so the flag is clear. This is independent
// of the stored norm, which is metric-specific (zero for the raw metrics).
func (s *MmapStore) rebuildNodeCount() {
	var count uint64
	for i := uint64(0); i < s.meta.TotalSlots; i++ {
		off := int64(pageSize) + int64(i)*int64(nodeSlotSize)
		flags := s.nodes[off+1]
		if flags&nodeFlagDeleted == 0 && flags&nodeFlagOccupied != 0 {
			count++
		}
	}
	s.meta.NodeCount = count
}
