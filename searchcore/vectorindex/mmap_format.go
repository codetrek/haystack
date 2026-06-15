package vectorindex

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"unsafe"
)

func init() {
	checkEndianProbe(0x01020304)
}

// checkEndianProbe panics unless probe's lowest-address byte is 0x04. init calls
// it with the constant 0x01020304, so it panics exactly on big-endian platforms
// (which the unsafe mmap reinterpretation in this package does not support).
// Parameterizing the probe keeps the panic branch reachable from tests.
func checkEndianProbe(probe uint32) {
	if *(*byte)(unsafe.Pointer(&probe)) != 0x04 {
		panic("mmap store requires little-endian platform")
	}
}

// File magic constants (4 bytes each).
var (
	magicMeta       = [4]byte{'H', 'N', 'S', 'W'}
	magicVectors    = [4]byte{'V', 'E', 'C', 'S'}
	magicNodes      = [4]byte{'N', 'O', 'D', 'E'}
	magicGraphL0    = [4]byte{'G', 'R', 'L', '0'}
	magicGraphUpper = [4]byte{'G', 'R', 'U', 'P'}
	magicIDMap      = [4]byte{'I', 'D', 'M', 'P'}
)

// pageSize is the header size for mmap data files (page-aligned).
const pageSize = 4096

// MetaHeader is the on-disk format for meta.bin (exactly 72 bytes).
// Fields are ordered to avoid implicit padding: uint32 group first, then uint64 group.
type MetaHeader struct {
	Magic      [4]byte // "HNSW"
	Version    uint32  // 1
	Dim        uint32  // vector dimension
	M          uint32  // HNSW M parameter
	MaxLevel   uint32  // current max level
	EntryLevel uint32  // entry point level
	Metric     uint32  // distance metric (0=cosine, 1=dot, 2=euclidean)
	_          uint32  // pad so the uint64 group is 8-byte aligned

	NodeCount        uint64 // active node count (excl. tombstones)
	TotalSlots       uint64 // allocated slots (incl. tombstones)
	EntryPoint       uint64 // entry node ID
	NextNodeId       uint64 // next allocatable node ID
	WalCheckpointLSN uint64 // WAL checkpoint LSN
}

// Compile-time size check: MetaHeader must be exactly 72 bytes.
var _ [72]byte = [unsafe.Sizeof(MetaHeader{})]byte{}

// VectorsHeader is the on-disk header for vectors.dat (lives in the first pageSize bytes).
type VectorsHeader struct {
	Magic    [4]byte
	Dim      uint32
	Capacity uint64
}

// NodesHeader is the on-disk header for nodes.dat.
type NodesHeader struct {
	Magic    [4]byte
	_        [4]byte // padding
	Capacity uint64
}

// GraphL0Header is the on-disk header for graph_l0.dat.
type GraphL0Header struct {
	Magic        [4]byte
	MaxNeighbors uint32
	Capacity     uint64
}

// GraphUpperHeader is the on-disk header for graph_upper.dat.
type GraphUpperHeader struct {
	Magic        [4]byte
	MaxNeighbors uint32
	MaxLayers    uint32
	_            [4]byte // padding
	Capacity     uint64
	NextSlot     uint64
}

// nodeSlotSize is the on-disk size of a single node metadata slot.
const nodeSlotSize = 16

// NodeSlot is the on-disk layout of a node in nodes.dat (16 bytes).
type NodeSlot struct {
	Level     uint8
	Flags     uint8
	_         [2]byte
	Norm      float32
	UpperSlot uint32
	_         [4]byte // reserved
}

const (
	nodeFlagDeleted  = 0x01 // slot's node was tombstoned by DeleteNode
	nodeFlagOccupied = 0x02 // slot holds a real node (set on PutNode / WAL replay)
)

// graphL0SlotSize returns the slot size for a level-0 neighbor list.
func graphL0SlotSize(mmax0 int) int {
	return 4 + mmax0*8 // count(uint32) + neighbors([]uint64)
}

// graphUpperLayerSize returns bytes per layer in an upper-graph slot.
func graphUpperLayerSize(m int) int {
	return 4 + m*8 // count(uint32) + neighbors([]uint64)
}

// graphUpperSlotSize returns the total slot size for an upper-graph entry.
func graphUpperSlotSize(m int, maxLayers int) int {
	return maxLayers * graphUpperLayerSize(m)
}

// defaultMaxLayers is the pre-allocated upper layer count per slot.
const defaultMaxLayers = 6

// writeMetaHeader atomically writes a MetaHeader to dir/meta.bin using
// write-to-temp + fsync + rename.
func writeMetaHeader(dir string, h *MetaHeader) error {
	h.Magic = magicMeta
	if h.Version == 0 {
		h.Version = 1
	}

	tmp := filepath.Join(dir, "meta.bin.tmp")
	final := filepath.Join(dir, "meta.bin")

	f, err := fsCreate(tmp)
	if err != nil {
		return fmt.Errorf("writeMetaHeader: create tmp: %w", err)
	}

	if err := binary.Write(f, binary.LittleEndian, h); err != nil {
		f.Close()
		fsRemove(tmp)
		return fmt.Errorf("writeMetaHeader: write: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		fsRemove(tmp)
		return fmt.Errorf("writeMetaHeader: sync: %w", err)
	}
	if err := f.Close(); err != nil {
		fsRemove(tmp)
		return fmt.Errorf("writeMetaHeader: close: %w", err)
	}
	if err := fsRename(tmp, final); err != nil {
		return fmt.Errorf("writeMetaHeader: rename: %w", err)
	}
	return nil
}

// readMetaHeader reads a MetaHeader from dir/meta.bin.
func readMetaHeader(dir string) (*MetaHeader, error) {
	path := filepath.Join(dir, "meta.bin")
	f, err := fsOpen(path)
	if err != nil {
		return nil, fmt.Errorf("readMetaHeader: %w", err)
	}
	defer f.Close()

	var h MetaHeader
	if err := binary.Read(f, binary.LittleEndian, &h); err != nil {
		return nil, fmt.Errorf("readMetaHeader: read: %w", err)
	}
	if h.Magic != magicMeta {
		return nil, fmt.Errorf("readMetaHeader: bad magic %q", h.Magic)
	}
	return &h, nil
}

// writeDataFileHeader writes a page-aligned header for a data file.
// The header struct is written at offset 0, and the file is padded to pageSize.
func writeDataFileHeader(path string, magic [4]byte, headerData any, totalSize int64) error {
	f, err := fsCreate(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := binary.Write(f, binary.LittleEndian, headerData); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	if err := f.Truncate(totalSize); err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	return f.Sync()
}
