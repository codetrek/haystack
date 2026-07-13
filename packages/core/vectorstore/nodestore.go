package vectorstore

import "errors"

// errNoEntryPoint is the sentinel returned by graphNodeStore.GetEntryPoint when
// the graph has no entry point (empty). search treats it as "empty, no results".
var errNoEntryPoint = errors.New("vectorstore: no entry point set")

// graphNodeStore is the persistence seam for the migrated HNSW graph. Vectors are
// in the store's metric-natural stored form (cosine: unit vectors). GetVectorRef
// returns the stored form without copying (caller MUST NOT mutate). nodeId is a
// dense 0-based id assigned by NextNodeId; for a sealed segment it is a dense
// live-only build index (NOT the raw slot — a tombstone gap would break the
// graph's dense-id assumptions), with nodeSlot[nodeId] resolving the segment row.
type graphNodeStore interface {
	Metric() Metric
	Dim() int
	GetVectorRef(id uint64) ([]float32, error)
	PutNode(id uint64, level int, vector []float32, docId int64) error
	DeleteNode(id uint64) error
	GetNeighbors(id uint64, layer int) ([]uint64, error)
	// getNeighborsRef returns the node's layer neighbor slice WITHOUT copying, for
	// read-only consumers on the search path (searchLayer/searchLayerFiltered). The
	// caller MUST NOT mutate or retain the slice. Returned as []uint32 (the stored
	// edge width); callers widen each id with uint64(x). Safe because graphs are
	// mutated only single-threaded during build and are immutable once searched (and
	// memGraphStore mutations replace the slice rather than editing it in place).
	getNeighborsRef(id uint64, layer int) []uint32
	SetNeighbors(id uint64, layer int, neighbors []uint64) error
	GetEntryPoint() (uint64, int, error)
	SetEntryPoint(id uint64, maxLayer int) error
	ClearEntryPoint() error
	HighestLiveNodeExcluding(exclude uint64) (id uint64, level int, ok bool, err error)
	GetNodeLevel(id uint64) (int, error)
	GetNodeId(docId int64) (uint64, bool, error)
	GetDocId(id uint64) (int64, bool, error)
	NextNodeId() (uint64, error)
	txnBegin() error
	txnCommit() error
	txnAbort(cause error) error
}
