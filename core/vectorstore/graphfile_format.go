package vectorstore

import "unsafe"

// magicGraph identifies the flat/CSR graph file (format "VSG2"). The magic is a
// hard break from the old sequential record stream ("VSGR"): an old reader sees a
// different magic and rejects the file instead of mis-parsing the new layout, and
// vice versa. On-disk back-compat is intentionally NOT preserved — graph files are
// derived from the segment and rebuilt on open, so a format change needs no
// migration (architecture: graphs are built once / rebuildable).
var magicGraph = [4]byte{'V', 'S', 'G', '2'}

// graphFormatVersion is bumped on any layout change within the VSG2 magic.
const graphFormatVersion uint32 = 1

// graphHeader is the on-disk header for graph-<name>.dat. It occupies the first
// segPageSize bytes; all fields are LittleEndian at the byte offsets below (the
// struct documents the layout and the compile-time guard pins its size — reads and
// writes use explicit offsets, so the Go field order is documentation only).
//
// After the header, four sections follow, in order:
//
//	META       [segPageSize ..]        NodeCount records, 16 bytes each:
//	                                   level(int32) | slot(int32) | docId(int64)
//	NODE_BASE  [after META]            (NodeCount+1) uint32: per-node CSR base —
//	                                   nodeBase[id]..nodeBase[id+1] is node id's
//	                                   half-open range of layer slots (count is
//	                                   level+1 for a live node, 0 for a tombstone).
//	LAYER_START[after NODE_BASE]       (LayerSlots+1) uint32: per-(node,layer) CSR
//	                                   base into POOL — layerStart[ls]..[ls+1] is
//	                                   that layer's half-open range of edges.
//	POOL       [PoolOff, page-aligned] PoolLen uint64: every neighbor id, grouped
//	                                   by node ascending then layer ascending.
//
// The index sections (NODE_BASE, LAYER_START) are uint32 prefix sums; they index
// positions, not node ids. writeGraphFile asserts LayerSlots and PoolLen stay below
// MaxUint32 (returning an error otherwise) — that assert, not any row cap, is what
// guarantees the uint32 offsets never wrap (defaultMaxSegSize is only a tunable
// default; growth merges pack up to defaultMaxMergedSize≈1<<20 rows, ~31M edges,
// still far below MaxUint32). POOL is uint64 (a zero-copy view feeds getNeighborsRef's
// []uint64 return) and is page-aligned so a future change can mmap it in place (it is
// the one large section).
type graphHeader struct {
	Magic      [4]byte // [0:4]   "VSG2"
	Version    uint32  // [4:8]   graphFormatVersion
	MaxLayers  uint32  // [8:12]  defaultMaxLayers (informational)
	HasEntry   uint32  // [12:16] 1 if an entry point is set
	NodeCount  uint64  // [16:24] N
	EntryID    uint64  // [24:32] entry node id (valid iff HasEntry)
	EntryLevel uint32  // [32:36] entry node level
	_          uint32  // [36:40] pad to 8-align the offsets below
	LayerSlots uint64  // [40:48] total (node,layer) slots; LAYER_START len = +1
	PoolLen    uint64  // [48:56] total edges; POOL bytes = PoolLen*8
	PoolOff    uint64  // [56:64] absolute byte offset of POOL (page-aligned)
}

var _ [64]byte = [unsafe.Sizeof(graphHeader{})]byte{}
