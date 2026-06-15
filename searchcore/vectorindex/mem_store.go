package vectorindex

import (
	"fmt"
	"sync"
)

// MemNodeStore is an in-memory implementation of NodeStore for testing.
type MemNodeStore struct {
	mu        sync.RWMutex
	metric    Metric
	vectors   map[uint64][]float32 // stored form (normalized for cosine)
	levels    map[uint64]int
	norms     map[uint64]float32          // original norm, used only to restore vectors
	neighbors map[uint64]map[int][]uint64 // nodeId -> layer -> neighbor ids
	docToNode map[int64]uint64
	nodeDoc   map[uint64]int64 // nodeDoc (not nodeToDoc) for GetDocId
	entryID   uint64
	maxLayer  int
	hasEntry  bool
	nextID    uint64
}

// NewMemNodeStore creates a new in-memory NodeStore. The distance metric
// defaults to Cosine when not specified.
func NewMemNodeStore(metric ...Metric) *MemNodeStore {
	m := Cosine
	if len(metric) > 0 {
		m = metric[0]
	}
	return &MemNodeStore{
		metric:    m,
		vectors:   make(map[uint64][]float32),
		levels:    make(map[uint64]int),
		norms:     make(map[uint64]float32),
		neighbors: make(map[uint64]map[int][]uint64),
		docToNode: make(map[int64]uint64),
		nodeDoc:   make(map[uint64]int64),
		nextID:    0,
	}
}

// Metric returns the store's distance metric.
func (m *MemNodeStore) Metric() Metric { return m.metric }

// GetVector returns a copy of the original vector for the given node. For cosine
// the stored unit vector is restored to its original scale via the stored norm.
func (m *MemNodeStore) GetVector(id uint64) ([]float32, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.vectors[id]
	if !ok {
		return nil, fmt.Errorf("node %d not found", id)
	}
	return m.metric.restore(v, m.norms[id]), nil
}

// GetVectorRef returns the internal stored vector slice directly without
// copying. The caller MUST NOT modify the returned slice.
func (m *MemNodeStore) GetVectorRef(id uint64) ([]float32, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.vectors[id]
	if !ok {
		return nil, fmt.Errorf("node %d not found", id)
	}
	return v, nil
}

func (m *MemNodeStore) PutNode(id uint64, level int, vector []float32, docId int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, norm := m.metric.prepare(vector)
	cp := make([]float32, len(stored))
	copy(cp, stored)
	m.vectors[id] = cp
	m.levels[id] = level
	m.norms[id] = norm
	if _, ok := m.neighbors[id]; !ok {
		m.neighbors[id] = make(map[int][]uint64)
	}
	m.docToNode[docId] = id
	m.nodeDoc[id] = docId
	return nil
}

func (m *MemNodeStore) DeleteNode(id uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.vectors[id]; !ok {
		return nil
	}
	delete(m.vectors, id)
	delete(m.levels, id)
	delete(m.norms, id)
	delete(m.neighbors, id)
	if doc, ok := m.nodeDoc[id]; ok {
		delete(m.docToNode, doc)
		delete(m.nodeDoc, id)
	}
	return nil
}

func (m *MemNodeStore) GetNeighbors(id uint64, layer int) ([]uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	layers, ok := m.neighbors[id]
	if !ok {
		return nil, nil
	}
	nb, ok := layers[layer]
	if !ok {
		return nil, nil
	}
	cp := make([]uint64, len(nb))
	copy(cp, nb)
	return cp, nil
}

func (m *MemNodeStore) SetNeighbors(id uint64, layer int, neighbors []uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.neighbors[id]; !ok {
		m.neighbors[id] = make(map[int][]uint64)
	}
	cp := make([]uint64, len(neighbors))
	copy(cp, neighbors)
	m.neighbors[id][layer] = cp
	return nil
}

func (m *MemNodeStore) GetEntryPoint() (uint64, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.hasEntry {
		return 0, 0, fmt.Errorf("entry point not set")
	}
	return m.entryID, m.maxLayer, nil
}

func (m *MemNodeStore) SetEntryPoint(id uint64, maxLayer int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entryID = id
	m.maxLayer = maxLayer
	m.hasEntry = true
	return nil
}

func (m *MemNodeStore) GetNodeLevel(id uint64) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	level, ok := m.levels[id]
	if !ok {
		return 0, fmt.Errorf("node %d not found", id)
	}
	return level, nil
}

func (m *MemNodeStore) GetNodeId(docId int64) (uint64, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.docToNode[docId]
	return id, ok, nil
}

func (m *MemNodeStore) GetDocId(id uint64) (int64, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	docId, ok := m.nodeDoc[id]
	return docId, ok, nil
}

func (m *MemNodeStore) NextNodeId() (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	return m.nextID, nil
}

func (m *MemNodeStore) GetNorm(id uint64) (float32, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	norm, ok := m.norms[id]
	if !ok {
		return 0, fmt.Errorf("norm for node %d not found", id)
	}
	return norm, nil
}

func (m *MemNodeStore) SetNorm(id uint64, norm float32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.norms[id] = norm
	return nil
}

// txnBegin is a no-op: MemNodeStore is in-memory and not a durability target.
func (m *MemNodeStore) txnBegin() error { return nil }

// txnCommit is a no-op. Atomicity for MemNodeStore is bounded by the index
// write lock held across Batch.Commit (no crash recovery to provide).
func (m *MemNodeStore) txnCommit() error { return nil }

// txnAbort returns the cause so callers propagate the original error. There is
// no in-memory rollback (the spec scopes crash-atomicity to MmapStore).
func (m *MemNodeStore) txnAbort(cause error) error { return cause }

func (m *MemNodeStore) Close() error {
	return nil
}
