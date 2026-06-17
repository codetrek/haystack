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
	nodeDoc   map[uint64]int64 // nodeId -> docId, backs GetDocId
	entryID   uint64
	maxLayer  int
	hasEntry  bool
	nextID    uint64
	dim       int // fixed vector dimension, learned lazily from the first PutNode
}

// NewMemNodeStore creates a new in-memory NodeStore. The distance metric
// defaults to Cosine when not specified.
//
// Deprecated: use core/vectorstore (see the package doc).
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

// Dim returns the fixed vector dimension (0 until the first PutNode).
func (m *MemNodeStore) Dim() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dim
}

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
	if m.dim == 0 && len(vector) > 0 {
		m.dim = len(vector) // learn dim from the first inserted vector
	} else if m.dim != 0 && len(vector) != m.dim {
		return fmt.Errorf("MemNodeStore.PutNode: vector dimension mismatch: got %d, want %d", len(vector), m.dim)
	}
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
		return 0, 0, errNoEntryPoint
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

// ClearEntryPoint resets the entry point so GetEntryPoint reports no entry.
// MemNodeStore keys "no entry" off hasEntry (not the ^uint64(0) sentinel), so
// SetEntryPoint(^uint64(0), 0) alone would NOT make GetEntryPoint error — this
// dedicated method keeps clearing consistent with MmapStore.
func (m *MemNodeStore) ClearEntryPoint() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hasEntry = false
	m.entryID = 0
	m.maxLayer = 0
	return nil
}

// HighestLiveNodeExcluding returns the highest-level node other than `exclude`.
// A node is live iff it is present in m.vectors (DeleteNode removes it). Ties
// are resolved by lowest id for deterministic test behavior, matching
// MmapStore's ascending-index scan.
func (m *MemNodeStore) HighestLiveNodeExcluding(exclude uint64) (uint64, int, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	bestID := uint64(0)
	bestLevel := -1
	found := false
	for id := range m.vectors {
		if id == exclude {
			continue
		}
		lvl := m.levels[id]
		if !found || lvl > bestLevel || (lvl == bestLevel && id < bestID) {
			bestID = id
			bestLevel = lvl
			found = true
		}
	}
	if !found {
		return 0, 0, false, nil
	}
	return bestID, bestLevel, true, nil
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
