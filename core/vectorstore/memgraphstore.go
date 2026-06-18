package vectorstore

import (
	"fmt"
	"sync"
)

// memGraphStore is an in-memory graphNodeStore for testing/parity, copied and
// an in-memory graph node store: it holds the vectors itself (unlike
// segGraphStore, which delegates to a sealed segment), so it serves as the
// reference implementation the migrated graph is validated against. The dropped
// GetVector/GetNorm/SetNorm/Close are not on the graphNodeStore interface.
type memGraphStore struct {
	mu        sync.RWMutex
	metric    Metric
	vectors   map[uint64][]float32 // stored form (normalized for cosine)
	levels    map[uint64]int
	neighbors map[uint64]map[int][]uint64 // nodeId -> layer -> neighbor ids
	docToNode map[int64]uint64
	nodeDoc   map[uint64]int64 // nodeId -> docId, backs GetDocId
	entryID   uint64
	maxLayer  int
	hasEntry  bool
	nextID    uint64
	dim       int // fixed vector dimension, learned lazily from the first PutNode
}

// newMemGraphStore creates a new in-memory graphNodeStore. The distance metric
// defaults to Cosine when not specified.
func newMemGraphStore(metric ...Metric) *memGraphStore {
	m := Cosine
	if len(metric) > 0 {
		m = metric[0]
	}
	return &memGraphStore{
		metric:    m,
		vectors:   make(map[uint64][]float32),
		levels:    make(map[uint64]int),
		neighbors: make(map[uint64]map[int][]uint64),
		docToNode: make(map[int64]uint64),
		nodeDoc:   make(map[uint64]int64),
		nextID:    0,
	}
}

// Metric returns the store's distance metric.
func (m *memGraphStore) Metric() Metric { return m.metric }

// Dim returns the fixed vector dimension (0 until the first PutNode).
func (m *memGraphStore) Dim() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dim
}

// GetVectorRef returns the internal stored vector slice directly without
// copying. The caller MUST NOT modify the returned slice.
func (m *memGraphStore) GetVectorRef(id uint64) ([]float32, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.vectors[id]
	if !ok {
		return nil, fmt.Errorf("node %d not found", id)
	}
	return v, nil
}

func (m *memGraphStore) PutNode(id uint64, level int, vector []float32, docId int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dim == 0 && len(vector) > 0 {
		m.dim = len(vector) // learn dim from the first inserted vector
	} else if m.dim != 0 && len(vector) != m.dim {
		return fmt.Errorf("memGraphStore.PutNode: vector dimension mismatch: got %d, want %d", len(vector), m.dim)
	}
	stored, _ := m.metric.prepare(vector)
	cp := make([]float32, len(stored))
	copy(cp, stored)
	m.vectors[id] = cp
	m.levels[id] = level
	if _, ok := m.neighbors[id]; !ok {
		m.neighbors[id] = make(map[int][]uint64)
	}
	m.docToNode[docId] = id
	m.nodeDoc[id] = docId
	return nil
}

func (m *memGraphStore) DeleteNode(id uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.vectors[id]; !ok {
		return nil
	}
	delete(m.vectors, id)
	delete(m.levels, id)
	delete(m.neighbors, id)
	if doc, ok := m.nodeDoc[id]; ok {
		delete(m.docToNode, doc)
		delete(m.nodeDoc, id)
	}
	return nil
}

func (m *memGraphStore) GetNeighbors(id uint64, layer int) ([]uint64, error) {
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

// getNeighborsRef returns the layer slice without copying (read-only; see interface).
// SetNeighbors replaces the slice rather than editing in place, so a returned ref
// stays valid even if the node is later updated.
func (m *memGraphStore) getNeighborsRef(id uint64, layer int) []uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	layers, ok := m.neighbors[id]
	if !ok {
		return nil
	}
	return layers[layer]
}

func (m *memGraphStore) SetNeighbors(id uint64, layer int, neighbors []uint64) error {
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

func (m *memGraphStore) GetEntryPoint() (uint64, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.hasEntry {
		return 0, 0, errNoEntryPoint
	}
	return m.entryID, m.maxLayer, nil
}

func (m *memGraphStore) SetEntryPoint(id uint64, maxLayer int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entryID = id
	m.maxLayer = maxLayer
	m.hasEntry = true
	return nil
}

// ClearEntryPoint resets the entry point so GetEntryPoint reports no entry.
func (m *memGraphStore) ClearEntryPoint() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hasEntry = false
	m.entryID = 0
	m.maxLayer = 0
	return nil
}

// HighestLiveNodeExcluding returns the highest-level node other than `exclude`.
// A node is live iff it is present in m.vectors (DeleteNode removes it). Ties
// are resolved by lowest id for deterministic behavior.
func (m *memGraphStore) HighestLiveNodeExcluding(exclude uint64) (uint64, int, bool, error) {
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

func (m *memGraphStore) GetNodeLevel(id uint64) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	level, ok := m.levels[id]
	if !ok {
		return 0, fmt.Errorf("node %d not found", id)
	}
	return level, nil
}

func (m *memGraphStore) GetNodeId(docId int64) (uint64, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.docToNode[docId]
	return id, ok, nil
}

func (m *memGraphStore) GetDocId(id uint64) (int64, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	docId, ok := m.nodeDoc[id]
	return docId, ok, nil
}

// NextNodeId returns dense 0-based ids (0,1,2,...) to match segGraphStore, so
// both graphNodeStore implementations share the same id space (appendix #10/#21:
// the migrated graph's visitedSet assumes dense 0-based ids; the two stores must
// agree so the seg path is actually equivalent to the mem reference).
func (m *memGraphStore) NextNodeId() (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextID
	m.nextID++
	return id, nil
}

// txnBegin is a no-op: memGraphStore is in-memory and not a durability target.
func (m *memGraphStore) txnBegin() error { return nil }

// txnCommit is a no-op. Atomicity is bounded by the index write lock held across
// commit (no crash recovery to provide).
func (m *memGraphStore) txnCommit() error { return nil }

// txnAbort returns the cause so callers propagate the original error.
func (m *memGraphStore) txnAbort(cause error) error { return cause }
