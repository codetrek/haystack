package vectorindex

import (
	"encoding/binary"
	"math"
)

// NodeStore defines the persistence interface for HNSW graph nodes.
//
// Vectors are stored in the form dictated by the store's Metric: cosine stores
// unit vectors (so distance reduces to 1 - dot), the raw metrics store vectors
// unchanged. GetVectorRef returns the *stored* form (ready for metric.distance);
// GetVector returns the original vector (cosine restores via the stored norm).
type NodeStore interface {
	// Metric returns the immutable distance metric this store was created with.
	Metric() Metric
	// Dim returns the fixed vector dimension, or 0 if the store has no fixed
	// dimension yet (e.g. an in-memory store before its first write). Used to
	// validate vector inputs before they reach the write/distance paths.
	Dim() int
	GetVector(id uint64) ([]float32, error)
	// GetVectorRef returns the stored vector form without copying. The caller
	// MUST NOT modify the returned slice. Use GetVector for the original vector.
	GetVectorRef(id uint64) ([]float32, error)
	PutNode(id uint64, level int, vector []float32, docId int64) error
	DeleteNode(id uint64) error
	GetNeighbors(id uint64, layer int) ([]uint64, error)
	SetNeighbors(id uint64, layer int, neighbors []uint64) error
	GetEntryPoint() (uint64, int, error)
	SetEntryPoint(id uint64, maxLayer int) error
	// ClearEntryPoint resets the entry point to the no-entry sentinel,
	// consistently across stores (MmapStore: WAL-logged + meta sentinel ^uint64(0);
	// MemNodeStore: hasEntry=false). After this GetEntryPoint returns an error.
	// Used when the last live node is deleted.
	ClearEntryPoint() error
	// HighestLiveNodeExcluding returns the live (occupied, non-deleted) node with
	// the highest level, excluding the node `exclude`. ok is false if no other
	// live node exists. Used by deleteNodeLocked to reseat the entry point when
	// the deleted node was the EP and its own neighbor lists were empty.
	HighestLiveNodeExcluding(exclude uint64) (id uint64, level int, ok bool, err error)
	GetNodeLevel(id uint64) (int, error)
	GetNodeId(docId int64) (uint64, bool, error)
	GetDocId(id uint64) (int64, bool, error)
	// docId↔nodeId is carried inline on the node slot; there are no separate
	// mapping mutators on this interface.
	NextNodeId() (uint64, error)
	// GetNorm returns the precomputed L2 norm for a node's vector.
	GetNorm(id uint64) (float32, error)
	// SetNorm stores a precomputed L2 norm for a node's vector.
	SetNorm(id uint64, norm float32) error
	// Transaction primitive (internal). Batch.Commit brackets a group of
	// mutations as one durable, crash-atomic unit. MemNodeStore implements
	// these as no-ops (no durability target); MmapStore frames them in the WAL.
	txnBegin() error
	txnCommit() error
	txnAbort(cause error) error
	Close() error
}

// --- encoding helpers ---

func encodeFloat32s(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

func decodeFloat32s(data []byte) []float32 {
	n := len(data) / 4
	v := make([]float32, n)
	for i := 0; i < n; i++ {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return v
}

func encodeUint64s(ids []uint64) []byte {
	buf := make([]byte, len(ids)*8)
	for i, id := range ids {
		binary.LittleEndian.PutUint64(buf[i*8:], id)
	}
	return buf
}

func decodeUint64s(data []byte) []uint64 {
	n := len(data) / 8
	ids := make([]uint64, n)
	for i := 0; i < n; i++ {
		ids[i] = binary.LittleEndian.Uint64(data[i*8:])
	}
	return ids
}

func encodeUint64(v uint64) []byte {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, v)
	return buf
}

func decodeUint64(data []byte) uint64 {
	return binary.LittleEndian.Uint64(data)
}

func encodeFloat32(v float32) []byte {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, math.Float32bits(v))
	return buf
}

func decodeFloat32Single(data []byte) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(data))
}
