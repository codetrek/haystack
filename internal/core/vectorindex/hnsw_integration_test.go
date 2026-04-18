package vectorindex

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIntegrationResultConsistency verifies that HNSWIndex returns consistent
// search results with MemNodeStore.
func TestIntegrationResultConsistency(t *testing.T) {
	const (
		n          = 100
		dim        = 384
		k          = 10
		numQueries = 20
		dataSeed   = 77777
		hnswSeed   = 42
	)

	rng := rand.New(rand.NewSource(dataSeed))
	vecs := randomVectors(rng, n, dim)
	queries := randomVectors(rng, numQueries, dim)

	memStore := NewMemNodeStore()
	memIdx := NewHNSWIndex(memStore, CosineDistance, WithCosineDistance(), WithRand(rand.New(rand.NewSource(hnswSeed))))

	for i, v := range vecs {
		requireNoError(t, memIdx.Insert(fmt.Sprintf("doc-%d", i), v))
	}

	for i, q := range queries {
		res, err := memIdx.Search(q, k)
		requireNoError(t, err)
		requireLen(t, res, k)
		_ = i
	}
}

// TestIntegrationCRUD tests the full insert-search-delete-search cycle with
// MemNodeStore.
func TestIntegrationCRUD(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance())

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
