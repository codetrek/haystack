package vectorindex

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// HAY-006: Supplementary upsert test coverage.

func TestHNSWUpsertEntryPoint(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store)

	// Insert single node — becomes entry point
	err := idx.Insert("doc1", []float32{1, 0, 0})
	assert.NoError(t, err)

	// Upsert entry point node with different vector
	err = idx.Insert("doc1", []float32{0, 0, 1})
	assert.NoError(t, err)

	// Search should work and find updated vector
	results, err := idx.Search([]float32{0, 0, 1}, 1)
	assert.NoError(t, err)
	assert.Len(t, results, 1)
	assert.InDelta(t, 0.0, results[0].Distance, 0.01, "should find the updated vector")

	// Insert another node and verify graph is healthy
	err = idx.Insert("doc2", []float32{0, 1, 0})
	assert.NoError(t, err)

	results, err = idx.Search([]float32{0, 1, 0}, 2)
	assert.NoError(t, err)
	assert.Len(t, results, 2, "should have 2 nodes total")
}

func TestHNSWInsertBatchDuplicateDocId(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store)

	items := []InsertItem{
		{DocId: "doc1", Vector: []float32{1, 0, 0}},
		{DocId: "doc2", Vector: []float32{0, 1, 0}},
		{DocId: "doc1", Vector: []float32{0, 0, 1}}, // duplicate
	}
	err := idx.InsertBatch(items)
	assert.NoError(t, err)

	// Should have exactly 2 nodes
	results, err := idx.Search([]float32{1, 1, 1}, 10)
	assert.NoError(t, err)
	assert.Len(t, results, 2, "duplicate docId handled, only 2 nodes")

	// Last doc1 vector should be the one stored
	results, err = idx.Search([]float32{0, 0, 1}, 1)
	assert.NoError(t, err)
	assert.Len(t, results, 1)
	assert.InDelta(t, 0.0, results[0].Distance, 0.01)
}

func TestHNSWInsertBatchPartialFailure(t *testing.T) {
	es := newErrorStore()
	idx := NewHNSWIndex(es)

	// Insert initial data
	err := idx.Insert("existing", []float32{1, 0, 0})
	assert.NoError(t, err)

	// Inject PutNode error
	es.mu.Lock()
	es.PutNodeErr = fmt.Errorf("injected PutNode error")
	es.mu.Unlock()

	// Insert should fail
	err = idx.Insert("new_doc", []float32{0, 1, 0})
	assert.Error(t, err)

	// Clear error
	es.mu.Lock()
	es.PutNodeErr = nil
	es.mu.Unlock()

	// Existing data should still be intact
	results, err := idx.Search([]float32{1, 0, 0}, 10)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(results), "existing node should survive failed insert")
}
