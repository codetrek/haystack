package vectorstore

import "unsafe"

var magicGraph = [4]byte{'V', 'S', 'G', 'R'}

// graphHeader is the on-disk header for graph.dat (32 bytes). After segPageSize
// bytes, the node table follows: NodeCount records of
//
//	level(int32) | slot(int32) | docId(int64) | nLayers(int32) | [ per layer:
//	nNeighbors(int32) | neighbors(nNeighbors * uint64) ]
//
// records are variable-length; a parallel offset table is NOT stored — the file
// is parsed sequentially at open (one linear pass; graphs are built once).
type graphHeader struct {
	Magic      [4]byte
	MaxLayers  uint32
	NodeCount  uint64
	HasEntry   uint32
	EntryLevel uint32
	EntryID    uint64
}

var _ [32]byte = [unsafe.Sizeof(graphHeader{})]byte{}
