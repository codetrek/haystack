package vectorindex

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/stretchr/testify/assert"
)

// HAY-006: Supplementary upsert test coverage.

func TestHNSWUpsertEntryPoint(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance)

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

func TestHNSWUpsertWithPebble(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := NewPebbleNodeStore(db, 1)
	idx := NewHNSWIndex(store, CosineDistance)

	err := idx.Insert("doc1", []float32{1, 0, 0})
	assert.NoError(t, err)
	err = idx.Insert("doc2", []float32{0, 1, 0})
	assert.NoError(t, err)

	// Upsert doc1
	err = idx.Insert("doc1", []float32{0, 0, 1})
	assert.NoError(t, err)

	// Verify updated vector
	results, err := idx.Search([]float32{0, 0, 1}, 1)
	assert.NoError(t, err)
	assert.Len(t, results, 1)
	assert.InDelta(t, 0.0, results[0].Distance, 0.01)

	// No orphans
	results, err = idx.Search([]float32{1, 1, 1}, 10)
	assert.NoError(t, err)
	assert.Len(t, results, 2, "should have exactly 2 nodes, no orphans")
}

func TestHNSWInsertBatchDuplicateDocId(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance)

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
	idx := NewHNSWIndex(es, CosineDistance)

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

func TestHNSWUpsertPersistRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Phase 1: Insert + Upsert
	db1, err := pebble.Open(dbPath, &pebble.Options{})
	assert.NoError(t, err)
	store1 := NewPebbleNodeStore(db1, 1)
	idx1 := NewHNSWIndex(store1, CosineDistance)

	err = idx1.Insert("doc1", []float32{1, 0, 0})
	assert.NoError(t, err)
	err = idx1.Insert("doc2", []float32{0, 1, 0})
	assert.NoError(t, err)
	err = idx1.Insert("doc1", []float32{0, 0, 1}) // upsert
	assert.NoError(t, err)

	db1.Close()

	// Phase 2: Reopen and verify
	db2, err := pebble.Open(dbPath, &pebble.Options{})
	assert.NoError(t, err)
	defer db2.Close()
	store2 := NewPebbleNodeStore(db2, 1)
	idx2 := NewHNSWIndex(store2, CosineDistance)

	// Updated vector should be findable
	results, err := idx2.Search([]float32{0, 0, 1}, 1)
	assert.NoError(t, err)
	assert.Len(t, results, 1)
	assert.InDelta(t, 0.0, results[0].Distance, 0.01, "after restart, upserted vector intact")

	// No orphans
	results, err = idx2.Search([]float32{1, 1, 1}, 10)
	assert.NoError(t, err)
	assert.Len(t, results, 2, "after restart, exactly 2 nodes, no orphans")
}
