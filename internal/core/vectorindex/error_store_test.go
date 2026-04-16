package vectorindex

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/viterin/vek/vek32"
)

// errorStore is a NodeStore mock that wraps MemNodeStore and lets callers
// inject configurable errors into any method. Set the corresponding Err
// field to a non-nil error to make that method fail. Thread-safe.
type errorStore struct {
	mu sync.RWMutex
	inner *MemNodeStore

	GetVectorErr        error
	GetVectorRefErr     error
	PutNodeErr          error
	DeleteNodeErr       error
	GetNeighborsErr     error
	SetNeighborsErr     error
	GetEntryPointErr    error
	SetEntryPointErr    error
	GetNodeLevelErr     error
	GetNodeIdErr        error
	SetNodeMappingErr   error
	DeleteNodeMappingErr error
	NextNodeIdErr       error
	GetNormErr          error
	SetNormErr          error
	CloseErr            error
}

func newErrorStore() *errorStore {
	return &errorStore{inner: NewMemNodeStore()}
}

func (e *errorStore) getErr(err error) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return err
}

func (e *errorStore) GetVector(id uint64) ([]float32, error) {
	if err := e.getErr(e.GetVectorErr); err != nil {
		return nil, err
	}
	return e.inner.GetVector(id)
}

func (e *errorStore) GetVectorRef(id uint64) ([]float32, error) {
	if err := e.getErr(e.GetVectorRefErr); err != nil {
		return nil, err
	}
	return e.inner.GetVectorRef(id)
}

func (e *errorStore) PutNode(id uint64, level int, vector []float32) error {
	if err := e.getErr(e.PutNodeErr); err != nil {
		return err
	}
	return e.inner.PutNode(id, level, vector)
}

func (e *errorStore) DeleteNode(id uint64) error {
	if err := e.getErr(e.DeleteNodeErr); err != nil {
		return err
	}
	return e.inner.DeleteNode(id)
}

func (e *errorStore) GetNeighbors(id uint64, layer int) ([]uint64, error) {
	if err := e.getErr(e.GetNeighborsErr); err != nil {
		return nil, err
	}
	return e.inner.GetNeighbors(id, layer)
}

func (e *errorStore) SetNeighbors(id uint64, layer int, neighbors []uint64) error {
	if err := e.getErr(e.SetNeighborsErr); err != nil {
		return err
	}
	return e.inner.SetNeighbors(id, layer, neighbors)
}

func (e *errorStore) GetEntryPoint() (uint64, int, error) {
	if err := e.getErr(e.GetEntryPointErr); err != nil {
		return 0, 0, err
	}
	return e.inner.GetEntryPoint()
}

func (e *errorStore) SetEntryPoint(id uint64, maxLayer int) error {
	if err := e.getErr(e.SetEntryPointErr); err != nil {
		return err
	}
	return e.inner.SetEntryPoint(id, maxLayer)
}

func (e *errorStore) GetNodeLevel(id uint64) (int, error) {
	if err := e.getErr(e.GetNodeLevelErr); err != nil {
		return 0, err
	}
	return e.inner.GetNodeLevel(id)
}

func (e *errorStore) GetNodeId(docId string) (uint64, bool, error) {
	if err := e.getErr(e.GetNodeIdErr); err != nil {
		return 0, false, err
	}
	return e.inner.GetNodeId(docId)
}

func (e *errorStore) SetNodeMapping(docId string, nodeId uint64) error {
	if err := e.getErr(e.SetNodeMappingErr); err != nil {
		return err
	}
	return e.inner.SetNodeMapping(docId, nodeId)
}

func (e *errorStore) DeleteNodeMapping(docId string) error {
	if err := e.getErr(e.DeleteNodeMappingErr); err != nil {
		return err
	}
	return e.inner.DeleteNodeMapping(docId)
}

func (e *errorStore) NextNodeId() (uint64, error) {
	if err := e.getErr(e.NextNodeIdErr); err != nil {
		return 0, err
	}
	return e.inner.NextNodeId()
}

func (e *errorStore) GetNorm(id uint64) (float32, error) {
	if err := e.getErr(e.GetNormErr); err != nil {
		return 0, err
	}
	return e.inner.GetNorm(id)
}

func (e *errorStore) SetNorm(id uint64, norm float32) error {
	if err := e.getErr(e.SetNormErr); err != nil {
		return err
	}
	return e.inner.SetNorm(id, norm)
}

func (e *errorStore) Close() error {
	if err := e.getErr(e.CloseErr); err != nil {
		return err
	}
	return e.inner.Close()
}

// Verify interface compliance at compile time.
var _ NodeStore = (*errorStore)(nil)

// =====================================================================
// HNSW Insert error paths using errorStore
// =====================================================================

func TestInsertNextNodeIdError(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance)

	es.NextNodeIdErr = fmt.Errorf("injected: NextNodeId")
	err := idx.Insert("doc1", []float32{1, 0})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "NextNodeId")
}

func TestInsertPutNodeError(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance)

	es.PutNodeErr = fmt.Errorf("injected: PutNode")
	err := idx.Insert("doc1", []float32{1, 0})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "PutNode")
}

func TestInsertSetNodeMappingError(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance)

	es.SetNodeMappingErr = fmt.Errorf("injected: SetNodeMapping")
	err := idx.Insert("doc1", []float32{1, 0})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "SetNodeMapping")
}

func TestInsertSetNeighborsError(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance)

	es.SetNeighborsErr = fmt.Errorf("injected: SetNeighbors")
	err := idx.Insert("doc1", []float32{1, 0})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "SetNeighbors")
}

// hnsw.go:155-163 — Insert hits the branch where the entry point exists
// but GetVectorRef on it fails (deleted node). The code resets the entry
// point to the new node and returns.
func TestInsertStaleEntryPointRecovers(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance)

	// Insert a first node so the index has an entry point.
	requireNoError(t, idx.Insert("doc1", []float32{1, 0}))

	// Now make GetVectorRef fail for the *existing* entry point.
	// The next Insert should detect the stale entry and reset it.
	es.GetVectorRefErr = fmt.Errorf("injected: stale entry")
	err := idx.Insert("doc2", []float32{0, 1})
	// Insert should succeed — the stale-entry branch sets the new node
	// as entry point and returns nil.
	requireNoError(t, err)
}

// Same branch, but SetEntryPoint itself fails during the recovery.
func TestInsertStaleEntryPointSetEntryPointError(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance)

	requireNoError(t, idx.Insert("doc1", []float32{1, 0}))

	es.GetVectorRefErr = fmt.Errorf("injected: stale entry")
	es.SetEntryPointErr = fmt.Errorf("injected: SetEntryPoint")
	err := idx.Insert("doc2", []float32{0, 1})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "SetEntryPoint")
}

// First-node insert path where SetEntryPoint fails.
func TestInsertFirstNodeSetEntryPointError(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance)

	es.SetEntryPointErr = fmt.Errorf("injected: SetEntryPoint")
	err := idx.Insert("doc1", []float32{1, 0})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "SetEntryPoint")
}

// =====================================================================
// HNSW Search error paths using errorStore
// =====================================================================

func TestSearchGetEntryPointError(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance)

	// Empty index — GetEntryPoint returns error. Search returns nil, nil.
	results, err := idx.Search([]float32{1, 0}, 5)
	requireNoError(t, err)
	assert.Empty(t, results)
}

func TestSearchStaleEntryPoint(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance)

	requireNoError(t, idx.Insert("doc1", []float32{1, 0}))

	// Make entry point appear deleted.
	es.GetVectorRefErr = fmt.Errorf("injected: stale")
	results, err := idx.Search([]float32{1, 0}, 5)
	requireNoError(t, err)
	assert.Empty(t, results)
}

// =====================================================================
// HNSW Delete error paths using errorStore
// =====================================================================

func TestDeleteGetNodeIdError(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance)

	es.GetNodeIdErr = fmt.Errorf("injected: GetNodeId")
	err := idx.Delete("doc1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "GetNodeId")
}

func TestDeleteGetNodeLevelError(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance)

	requireNoError(t, idx.Insert("doc1", []float32{1, 0}))

	es.GetNodeLevelErr = fmt.Errorf("injected: GetNodeLevel")
	err := idx.Delete("doc1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "GetNodeLevel")
}

func TestDeleteGetNeighborsError(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance)

	requireNoError(t, idx.Insert("doc1", []float32{1, 0}))

	es.GetNeighborsErr = fmt.Errorf("injected: GetNeighbors")
	err := idx.Delete("doc1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "GetNeighbors")
}

func TestDeleteDeleteNodeError(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance)

	requireNoError(t, idx.Insert("doc1", []float32{1, 0}))

	es.DeleteNodeErr = fmt.Errorf("injected: DeleteNode")
	err := idx.Delete("doc1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DeleteNode")
}

func TestDeleteDeleteNodeMappingError(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance)

	requireNoError(t, idx.Insert("doc1", []float32{1, 0}))

	es.DeleteNodeMappingErr = fmt.Errorf("injected: DeleteNodeMapping")
	err := idx.Delete("doc1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DeleteNodeMapping")
}

func TestDeleteSetNeighborsError(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance)

	// Insert enough nodes so that Delete has neighbors to reconnect.
	requireNoError(t, idx.Insert("doc1", []float32{1, 0, 0}))
	requireNoError(t, idx.Insert("doc2", []float32{0, 1, 0}))
	requireNoError(t, idx.Insert("doc3", []float32{0, 0, 1}))

	es.SetNeighborsErr = fmt.Errorf("injected: SetNeighbors")
	err := idx.Delete("doc2")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "SetNeighbors")
}

// =====================================================================
// nodeDistanceWithNorm — fallback when GetNorm fails
// =====================================================================

func TestNodeDistanceWithNormFallback(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance, WithCosineDistance())

	requireNoError(t, es.inner.PutNode(1, 0, []float32{1, 0}))
	requireNoError(t, es.inner.SetNorm(1, vek32.Norm([]float32{1, 0})))

	// Inject GetNorm error — nodeDistanceWithNorm should fall back to
	// computing the distance without precomputed norms.
	es.GetNormErr = fmt.Errorf("injected: GetNorm")
	dist, err := idx.nodeDistanceWithNorm(1, []float32{1, 0}, vek32.Norm([]float32{1, 0}))
	requireNoError(t, err)
	assert.InDelta(t, 0.0, dist, 1e-5)
}

// =====================================================================
// Insert with cosine distance — exercises nodeDistCalc / norm path
// =====================================================================

func TestInsertWithCosineDistanceMultiNode(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance, WithCosineDistance())

	// Insert several nodes to exercise phase-1 greedy traversal and
	// phase-2 neighbor selection with cosine + norm caching.
	for i := 0; i < 10; i++ {
		v := make([]float32, 8)
		v[i%8] = 1.0
		requireNoError(t, idx.Insert(fmt.Sprintf("doc-%d", i), v))
	}

	results, err := idx.Search([]float32{1, 0, 0, 0, 0, 0, 0, 0}, 3)
	requireNoError(t, err)
	assert.NotEmpty(t, results)
}

// =====================================================================
// InsertBatch error paths
// =====================================================================

func TestInsertBatchPropagatesError(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance)

	items := []InsertItem{
		{DocId: "a", Vector: []float32{1, 0}},
		{DocId: "b", Vector: []float32{0, 1}},
	}

	es.PutNodeErr = fmt.Errorf("injected: PutNode in batch")
	err := idx.InsertBatch(items)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "PutNode")
}

// =====================================================================
// PebbleNodeStore DeleteNode — count-read error paths
//
// store.go:510-512 (batch path) and store.go:570-572 (non-batch path)
// are the "else if err != pebble.ErrNotFound" branches on the count Get.
// These are hard to hit via PebbleNodeStore directly because they require
// a non-ErrNotFound, non-nil error from Get specifically on the count key.
// We cover them here by verifying the code structure through the existing
// poisoned-batch and read-only tests which hit them indirectly.
//
// The errorStore tests above cover the equivalent HNSW-level Delete paths.
// =====================================================================

// =====================================================================
// PebbleNodeStore PutNode — count-read error in batch path
// This is already covered by TestPutNodeBatchGetCountError in
// error_path_test.go via poisoned batch.
// =====================================================================

// =====================================================================
// Additional edge cases
// =====================================================================

// Delete a doc that was never inserted — should be a no-op.
func TestDeleteNonExistentDoc(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance)

	err := idx.Delete("never-inserted")
	requireNoError(t, err)
}

// Insert after all nodes are deleted — exercises the "first node" branch
// via GetEntryPoint error after the index was previously populated.
func TestInsertAfterDeleteAll(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es, CosineDistance, WithCosineDistance())

	requireNoError(t, idx.Insert("doc1", []float32{1, 0}))
	requireNoError(t, idx.Insert("doc2", []float32{0, 1}))
	requireNoError(t, idx.Delete("doc1"))
	requireNoError(t, idx.Delete("doc2"))

	// Index is now empty. Inserting should hit the first-node path again.
	requireNoError(t, idx.Insert("doc3", []float32{1, 1}))

	results, err := idx.Search([]float32{1, 1}, 1)
	requireNoError(t, err)
	assert.Len(t, results, 1)
}
