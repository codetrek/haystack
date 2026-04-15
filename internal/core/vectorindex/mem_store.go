package vectorindex

import (
	"fmt"
	"sync"
)

// MemNodeStore is an in-memory implementation of NodeStore for testing.
type MemNodeStore struct {
	mu        sync.RWMutex
	vectors   map[uint64][]float32
	levels    map[uint64]int
	neighbors map[uint64]map[int][]uint64 // nodeId -> layer -> neighbor ids
	docToNode map[string]uint64
	nodeToDoc map[uint64]string
	entryID   uint64
	maxLayer  int
	hasEntry  bool
	nextID    uint64
}

// NewMemNodeStore creates a new in-memory NodeStore.
func NewMemNodeStore() *MemNodeStore {
	return &MemNodeStore{
		vectors:   make(map[uint64][]float32),
		levels:    make(map[uint64]int),
		neighbors: make(map[uint64]map[int][]uint64),
		docToNode: make(map[string]uint64),
		nodeToDoc: make(map[uint64]string),
		nextID:    0,
	}
}

func (m *MemNodeStore) GetVector(id uint64) ([]float32, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.vectors[id]
	if !ok {
		return nil, fmt.Errorf("node %d not found", id)
	}
	cp := make([]float32, len(v))
	copy(cp, v)
	return cp, nil
}

// GetVectorRef returns the internal vector slice directly without copying.
// The caller MUST NOT modify the returned slice.
func (m *MemNodeStore) GetVectorRef(id uint64) ([]float32, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.vectors[id]
	if !ok {
		return nil, fmt.Errorf("node %d not found", id)
	}
	return v, nil
}

func (m *MemNodeStore) PutNode(id uint64, level int, vector []float32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]float32, len(vector))
	copy(cp, vector)
	m.vectors[id] = cp
	m.levels[id] = level
	if _, ok := m.neighbors[id]; !ok {
		m.neighbors[id] = make(map[int][]uint64)
	}
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
	delete(m.neighbors, id)
	if doc, ok := m.nodeToDoc[id]; ok {
		delete(m.docToNode, doc)
		delete(m.nodeToDoc, id)
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

func (m *MemNodeStore) GetNodeId(docId string) (uint64, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.docToNode[docId]
	return id, ok, nil
}

func (m *MemNodeStore) SetNodeMapping(docId string, nodeId uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.docToNode[docId] = nodeId
	m.nodeToDoc[nodeId] = docId
	return nil
}

func (m *MemNodeStore) DeleteNodeMapping(docId string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if nodeId, ok := m.docToNode[docId]; ok {
		delete(m.nodeToDoc, nodeId)
	}
	delete(m.docToNode, docId)
	return nil
}

func (m *MemNodeStore) NextNodeId() (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	return m.nextID, nil
}

func (m *MemNodeStore) Close() error {
	return nil
}
