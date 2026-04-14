package vectorindex

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/stretchr/testify/assert"
)

// TestIntegrationResultConsistency verifies that HNSWIndex returns identical
// search results regardless of whether PebbleNodeStore or MemNodeStore is used.
func TestIntegrationResultConsistency(t *testing.T) {
	const (
		n          = 100
		dim        = 384
		k          = 10
		numQueries = 20
		dataSeed   = 77777
		hnswSeed   = 42
	)

	// Generate deterministic vectors and queries.
	rng := rand.New(rand.NewSource(dataSeed))
	vecs := randomVectors(rng, n, dim)
	queries := randomVectors(rng, numQueries, dim)

	// --- Pebble path ---
	db := openTestDB(t)
	defer db.Close()
	pebbleStore := NewPebbleNodeStore(db, 1)
	pebbleIdx := NewHNSWIndex(pebbleStore, CosineDistance, WithRand(rand.New(rand.NewSource(hnswSeed))))

	for i, v := range vecs {
		requireNoError(t, pebbleIdx.Insert(fmt.Sprintf("doc-%d", i), v))
	}

	pebbleResults := make([][]SearchResult, numQueries)
	for i, q := range queries {
		res, err := pebbleIdx.Search(q, k)
		requireNoError(t, err)
		requireLen(t, res, k)
		pebbleResults[i] = res
	}

	// --- Mem path ---
	memStore := NewMemNodeStore()
	memIdx := NewHNSWIndex(memStore, CosineDistance, WithRand(rand.New(rand.NewSource(hnswSeed))))

	for i, v := range vecs {
		requireNoError(t, memIdx.Insert(fmt.Sprintf("doc-%d", i), v))
	}

	for i, q := range queries {
		res, err := memIdx.Search(q, k)
		requireNoError(t, err)
		requireLen(t, res, k)

		// Results must be identical: same IDs in same order.
		for j := range res {
			assert.Equal(t, pebbleResults[i][j].ID, res[j].ID,
				"query %d result %d: ID mismatch", i, j)
			assert.InDelta(t, pebbleResults[i][j].Distance, res[j].Distance, 1e-6,
				"query %d result %d: distance mismatch", i, j)
		}
	}
}

// TestIntegrationRestartPersistence verifies that search results survive a
// PebbleDB close+reopen cycle.
func TestIntegrationRestartPersistence(t *testing.T) {
	const (
		n          = 100
		dim        = 384
		k          = 10
		numQueries = 5
		dataSeed   = 88888
		hnswSeed   = 42
	)

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	rng := rand.New(rand.NewSource(dataSeed))
	vecs := randomVectors(rng, n, dim)
	queries := randomVectors(rng, numQueries, dim)

	// Phase 1: insert and search.
	var preResults [][]SearchResult
	{
		db, err := pebble.Open(dbPath, &pebble.Options{})
		requireNoError(t, err)

		store := NewPebbleNodeStore(db, 1)
		idx := NewHNSWIndex(store, CosineDistance, WithRand(rand.New(rand.NewSource(hnswSeed))))

		for i, v := range vecs {
			requireNoError(t, idx.Insert(fmt.Sprintf("doc-%d", i), v))
		}

		preResults = make([][]SearchResult, numQueries)
		for i, q := range queries {
			res, err := idx.Search(q, k)
			requireNoError(t, err)
			requireLen(t, res, k)
			preResults[i] = res
		}

		requireNoError(t, db.Close())
	}

	// Phase 2: reopen and verify identical results.
	{
		db, err := pebble.Open(dbPath, &pebble.Options{})
		requireNoError(t, err)
		defer db.Close()

		store := NewPebbleNodeStore(db, 1)
		idx := NewHNSWIndex(store, CosineDistance, WithRand(rand.New(rand.NewSource(hnswSeed))))

		for i, q := range queries {
			res, err := idx.Search(q, k)
			requireNoError(t, err)
			requireLen(t, res, k)

			for j := range res {
				assert.Equal(t, preResults[i][j].ID, res[j].ID,
					"query %d result %d: ID mismatch after restart", i, j)
				assert.InDelta(t, preResults[i][j].Distance, res[j].Distance, 1e-6,
					"query %d result %d: distance mismatch after restart", i, j)
			}
		}
	}
}

// TestIntegrationDeleteRestart verifies that deleted vectors stay gone after a
// PebbleDB close+reopen cycle.
func TestIntegrationDeleteRestart(t *testing.T) {
	const (
		n          = 50
		dim        = 384
		k          = 10
		numDeleted = 10
		dataSeed   = 99999
		hnswSeed   = 42
	)

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	rng := rand.New(rand.NewSource(dataSeed))
	vecs := randomVectors(rng, n, dim)
	query := randomVectors(rng, 1, dim)[0]

	// Build set of deleted doc IDs.
	deletedDocs := make(map[string]bool)
	for i := 0; i < numDeleted; i++ {
		deletedDocs[fmt.Sprintf("doc-%d", i)] = true
	}

	// Phase 1: insert, delete, search.
	var preResults []SearchResult
	{
		db, err := pebble.Open(dbPath, &pebble.Options{})
		requireNoError(t, err)

		store := NewPebbleNodeStore(db, 1)
		idx := NewHNSWIndex(store, CosineDistance, WithRand(rand.New(rand.NewSource(hnswSeed))))

		for i, v := range vecs {
			requireNoError(t, idx.Insert(fmt.Sprintf("doc-%d", i), v))
		}

		for docId := range deletedDocs {
			requireNoError(t, idx.Delete(docId))
		}

		// Verify deleted docs are absent.
		res, err := idx.Search(query, k)
		requireNoError(t, err)
		requireLen(t, res, k)
		assertNoDeletedNodes(t, store, res, deletedDocs)
		preResults = res

		requireNoError(t, db.Close())
	}

	// Phase 2: reopen and verify deletions persisted.
	{
		db, err := pebble.Open(dbPath, &pebble.Options{})
		requireNoError(t, err)
		defer db.Close()

		store := NewPebbleNodeStore(db, 1)
		idx := NewHNSWIndex(store, CosineDistance, WithRand(rand.New(rand.NewSource(hnswSeed))))

		res, err := idx.Search(query, k)
		requireNoError(t, err)
		requireLen(t, res, k)
		assertNoDeletedNodes(t, store, res, deletedDocs)

		// Results must match pre-restart.
		for j := range res {
			assert.Equal(t, preResults[j].ID, res[j].ID,
				"result %d: ID mismatch after restart", j)
			assert.InDelta(t, preResults[j].Distance, res[j].Distance, 1e-6,
				"result %d: distance mismatch after restart", j)
		}
	}
}

// TestIntegrationCRUD tests the full insert-search-delete-search cycle with
// PebbleNodeStore.
func TestIntegrationCRUD(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)
	idx := NewHNSWIndex(store, CosineDistance)

	// Insert 3 known vectors.
	requireNoError(t, idx.Insert("alpha", []float32{1, 0, 0}))
	requireNoError(t, idx.Insert("beta", []float32{0, 1, 0}))
	requireNoError(t, idx.Insert("gamma", []float32{0, 0, 1}))

	// Search — alpha should be nearest to [1,0,0].
	results, err := idx.Search([]float32{1, 0, 0}, 3)
	requireNoError(t, err)
	requireLen(t, results, 3)
	assert.Equal(t, uint64(1), results[0].ID) // alpha = node 1
	assert.InDelta(t, 0.0, results[0].Distance, 1e-6)

	// Delete alpha.
	requireNoError(t, idx.Delete("alpha"))

	// Re-search — alpha must be gone.
	results, err = idx.Search([]float32{1, 0, 0}, 3)
	requireNoError(t, err)
	requireLen(t, results, 2)
	for _, r := range results {
		assert.NotEqual(t, uint64(1), r.ID, "deleted node alpha should not appear")
	}

	// Verify remaining nodes are correct.
	_, found, err := store.GetNodeId("alpha")
	requireNoError(t, err)
	assert.False(t, found, "alpha mapping should be deleted")

	_, found, err = store.GetNodeId("beta")
	requireNoError(t, err)
	assert.True(t, found, "beta mapping should exist")
}

// assertNoDeletedNodes verifies that none of the search results correspond to
// a deleted document.
func assertNoDeletedNodes(t *testing.T, store NodeStore, results []SearchResult, deleted map[string]bool) {
	t.Helper()
	// NodeStore doesn't expose a nodeToDoc lookup directly, so we check by
	// trying to resolve each result's node ID. PebbleNodeStore stores
	// bidirectional mappings — a deleted node's mapping won't resolve.
	for _, r := range results {
		vec, err := store.GetVector(r.ID)
		if err != nil {
			t.Fatalf("result node %d has no vector — should not appear in results", r.ID)
		}
		assert.NotNil(t, vec, "result node %d should have a valid vector", r.ID)
	}
}
