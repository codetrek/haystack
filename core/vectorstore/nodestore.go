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
