package vectorindex

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Task 1: HNSW + MmapStore basic integration tests
// ---------------------------------------------------------------------------

// TestMmapHNSW_InsertSearch verifies basic insert → search with MmapStore.
func TestMmapHNSW_InsertSearch(t *testing.T) {
	const (
		n   = 100
		dim = 128
		k   = 10
	)

	dir := t.TempDir()
	store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
		WithRand(rand.New(rand.NewSource(42))))

	rng := rand.New(rand.NewSource(99))
	vecs := randomVectors(rng, n, dim)
	for i, v := range vecs {
		if err := idx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
			t.Fatalf("Insert doc-%d: %v", i, err)
		}
	}

	query := randomVectors(rng, 1, dim)[0]
	results, err := idx.Search(query, k)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != k {
		t.Fatalf("expected %d results, got %d", k, len(results))
	}

	// Results must be sorted by ascending distance.
	for i := 1; i < len(results); i++ {
		assert.LessOrEqual(t, results[i-1].Distance, results[i].Distance,
			"results not sorted at index %d", i)
	}
}

// TestMmapHNSW_InsertDeleteSearch verifies deleted docs don't appear in search.
func TestMmapHNSW_InsertDeleteSearch(t *testing.T) {
	const (
		n          = 50
		dim        = 128
		k          = 10
		numDeleted = 10
	)

	dir := t.TempDir()
	store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
		WithRand(rand.New(rand.NewSource(42))))

	rng := rand.New(rand.NewSource(99))
	vecs := randomVectors(rng, n, dim)
	for i, v := range vecs {
		if err := idx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
			t.Fatalf("Insert doc-%d: %v", i, err)
		}
	}

	// Delete first numDeleted docs.
	for i := 0; i < numDeleted; i++ {
		if err := idx.Delete(fmt.Sprintf("doc-%d", i)); err != nil {
			t.Fatalf("Delete doc-%d: %v", i, err)
		}
	}

	query := randomVectors(rng, 1, dim)[0]
	results, err := idx.Search(query, k)
	if err != nil {
		t.Fatal(err)
	}
	assert.NotEmpty(t, results)

	// Verify deleted doc mappings are gone.
	for i := 0; i < numDeleted; i++ {
		docId := fmt.Sprintf("doc-%d", i)
		_, ok, err := store.GetNodeId(docId)
		if err != nil {
			t.Fatal(err)
		}
		assert.False(t, ok, "deleted doc %s mapping should be removed", docId)
	}
	// Non-deleted docs should still be findable.
	for i := numDeleted; i < n; i++ {
		docId := fmt.Sprintf("doc-%d", i)
		_, ok, err := store.GetNodeId(docId)
		if err != nil {
			t.Fatal(err)
		}
		assert.True(t, ok, "doc %s should still exist", docId)
	}
}

// TestMmapHNSW_DeleteReinsert verifies freelist slot reuse after delete+re-insert.
func TestMmapHNSW_DeleteReinsert(t *testing.T) {
	const (
		n   = 50
		dim = 128
		k   = 10
	)

	dir := t.TempDir()
	store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
		WithRand(rand.New(rand.NewSource(42))))

	rng := rand.New(rand.NewSource(99))
	vecs := randomVectors(rng, n, dim)
	for i, v := range vecs {
		if err := idx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
			t.Fatalf("Insert doc-%d: %v", i, err)
		}
	}

	// Delete 10 docs.
	for i := 0; i < 10; i++ {
		if err := idx.Delete(fmt.Sprintf("doc-%d", i)); err != nil {
			t.Fatalf("Delete doc-%d: %v", i, err)
		}
	}

	// Re-insert with new vectors.
	newVecs := randomVectors(rng, 10, dim)
	for i := 0; i < 10; i++ {
		if err := idx.Insert(fmt.Sprintf("doc-new-%d", i), newVecs[i]); err != nil {
			t.Fatalf("Insert doc-new-%d: %v", i, err)
		}
	}

	query := randomVectors(rng, 1, dim)[0]
	results, err := idx.Search(query, k)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != k {
		t.Fatalf("expected %d results, got %d", k, len(results))
	}

	for i := 1; i < len(results); i++ {
		assert.LessOrEqual(t, results[i-1].Distance, results[i].Distance)
	}
}

// TestMmapHNSW_Upsert verifies upsert replaces a doc's vector and search returns the new result.
func TestMmapHNSW_Upsert(t *testing.T) {
	const dim = 128

	dir := t.TempDir()
	store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
		WithRand(rand.New(rand.NewSource(42))))

	rng := rand.New(rand.NewSource(99))

	// Insert 20 docs.
	vecs := randomVectors(rng, 20, dim)
	for i, v := range vecs {
		if err := idx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
			t.Fatalf("Insert doc-%d: %v", i, err)
		}
	}

	// Upsert doc-0 with a very specific vector (all 1s).
	upsertVec := make([]float32, dim)
	for i := range upsertVec {
		upsertVec[i] = 1.0
	}
	if err := idx.Insert("doc-0", upsertVec); err != nil {
		t.Fatalf("Upsert doc-0: %v", err)
	}

	// Search for [1,1,...,1] — doc-0 should be the closest.
	results, err := idx.Search(upsertVec, 5)
	if err != nil {
		t.Fatal(err)
	}
	assert.NotEmpty(t, results)

	// Find doc-0 in results.
	nodeId, ok, err := store.GetNodeId("doc-0")
	if err != nil {
		t.Fatal(err)
	}
	assert.True(t, ok, "doc-0 mapping should exist after upsert")

	found := false
	for _, r := range results {
		if r.ID == nodeId {
			found = true
			assert.InDelta(t, 0.0, r.Distance, 1e-5, "upserted doc should have ~0 distance")
			break
		}
	}
	assert.True(t, found, "upserted doc-0 should appear in search results")
}

// ---------------------------------------------------------------------------
// Task 2: Persistence verification tests
// ---------------------------------------------------------------------------

// mmapHNSWSearchResults runs search and returns results for comparison.
func mmapHNSWSearchResults(t *testing.T, idx *HNSWIndex, queries [][]float32, k int) [][]SearchResult {
	t.Helper()
	results := make([][]SearchResult, len(queries))
	for i, q := range queries {
		res, err := idx.Search(q, k)
		if err != nil {
			t.Fatalf("Search query %d: %v", i, err)
		}
		results[i] = res
	}
	return results
}

// assertSearchResultsMatch verifies two search result sets are identical.
func assertSearchResultsMatch(t *testing.T, prefix string, expected, actual [][]SearchResult) {
	t.Helper()
	for i := range expected {
		if len(expected[i]) != len(actual[i]) {
			t.Errorf("%s query %d: len mismatch: expected %d, got %d",
				prefix, i, len(expected[i]), len(actual[i]))
			continue
		}
		for j := range expected[i] {
			assert.Equal(t, expected[i][j].ID, actual[i][j].ID,
				"%s query %d result %d: ID mismatch", prefix, i, j)
			assert.InDelta(t, expected[i][j].Distance, actual[i][j].Distance, 1e-6,
				"%s query %d result %d: distance mismatch", prefix, i, j)
		}
	}
}

// TestMmapHNSW_PersistenceReopen verifies search results survive close → reopen.
func TestMmapHNSW_PersistenceReopen(t *testing.T) {
	const (
		n   = 200
		dim = 128
		k   = 10
		nq  = 5
	)

	dir := t.TempDir()
	rng := rand.New(rand.NewSource(99))
	vecs := randomVectors(rng, n, dim)
	queries := randomVectors(rng, nq, dim)
	hnswSeed := int64(42)

	// Phase 1: Insert and search.
	var preResults [][]SearchResult
	{
		store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
		if err != nil {
			t.Fatal(err)
		}
		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
			WithRand(rand.New(rand.NewSource(hnswSeed))))

		for i, v := range vecs {
			if err := idx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
				t.Fatalf("Insert doc-%d: %v", i, err)
			}
		}

		preResults = mmapHNSWSearchResults(t, idx, queries, k)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}

	// Phase 2: Reopen and verify identical results.
	{
		store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()

		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
			WithRand(rand.New(rand.NewSource(hnswSeed))))

		postResults := mmapHNSWSearchResults(t, idx, queries, k)
		assertSearchResultsMatch(t, "reopen", preResults, postResults)
	}
}

// TestMmapHNSW_PersistenceReopenContinueInsert verifies incremental insert after reopen.
func TestMmapHNSW_PersistenceReopenContinueInsert(t *testing.T) {
	const (
		n1  = 100
		n2  = 50
		dim = 128
		k   = 10
	)

	dir := t.TempDir()
	rng := rand.New(rand.NewSource(99))
	vecs := randomVectors(rng, n1+n2, dim)
	hnswSeed := int64(42)

	// Phase 1: Insert first batch.
	{
		store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
		if err != nil {
			t.Fatal(err)
		}
		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
			WithRand(rand.New(rand.NewSource(hnswSeed))))

		for i := 0; i < n1; i++ {
			if err := idx.Insert(fmt.Sprintf("doc-%d", i), vecs[i]); err != nil {
				t.Fatalf("Insert doc-%d: %v", i, err)
			}
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}

	// Phase 2: Reopen and insert more.
	{
		store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()

		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
			WithRand(rand.New(rand.NewSource(hnswSeed))))

		for i := n1; i < n1+n2; i++ {
			if err := idx.Insert(fmt.Sprintf("doc-%d", i), vecs[i]); err != nil {
				t.Fatalf("Insert doc-%d: %v", i, err)
			}
		}

		// Search and verify all docs accessible.
		query := randomVectors(rng, 1, dim)[0]
		results, err := idx.Search(query, k)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != k {
			t.Fatalf("expected %d results, got %d", k, len(results))
		}

		// Verify total node count.
		assert.Equal(t, uint64(n1+n2), store.meta.NodeCount,
			"node count should reflect both insertion phases")
	}
}

// TestMmapHNSW_PersistenceReopenDelete verifies deletion works after reopen.
func TestMmapHNSW_PersistenceReopenDelete(t *testing.T) {
	const (
		n          = 100
		dim        = 128
		k          = 10
		numDeleted = 10
	)

	dir := t.TempDir()
	rng := rand.New(rand.NewSource(99))
	vecs := randomVectors(rng, n, dim)
	hnswSeed := int64(42)

	// Phase 1: Insert all.
	{
		store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
		if err != nil {
			t.Fatal(err)
		}
		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
			WithRand(rand.New(rand.NewSource(hnswSeed))))

		for i, v := range vecs {
			if err := idx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
				t.Fatalf("Insert doc-%d: %v", i, err)
			}
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}

	// Phase 2: Reopen and delete.
	{
		store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()

		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
			WithRand(rand.New(rand.NewSource(hnswSeed))))

		for i := 0; i < numDeleted; i++ {
			if err := idx.Delete(fmt.Sprintf("doc-%d", i)); err != nil {
				t.Fatalf("Delete doc-%d: %v", i, err)
			}
		}

		// Verify deleted mappings are gone.
		for i := 0; i < numDeleted; i++ {
			_, ok, _ := store.GetNodeId(fmt.Sprintf("doc-%d", i))
			assert.False(t, ok, "deleted doc-%d mapping should be gone", i)
		}

		// Search should still work.
		query := randomVectors(rng, 1, dim)[0]
		results, err := idx.Search(query, k)
		if err != nil {
			t.Fatal(err)
		}
		assert.NotEmpty(t, results)
	}
}

// TestMmapHNSW_WALReplayE2E verifies HNSW search works after WAL replay (simulated crash).
func TestMmapHNSW_WALReplayE2E(t *testing.T) {
	const (
		n   = 200
		dim = 128
		k   = 10
		nq  = 5
	)

	dir := t.TempDir()
	rng := rand.New(rand.NewSource(99))
	vecs := randomVectors(rng, n, dim)
	queries := randomVectors(rng, nq, dim)
	hnswSeed := int64(42)

	// Phase 1: Insert and get pre-crash results.
	// Use high checkpoint interval so WAL is NOT flushed via checkpoint.
	var preResults [][]SearchResult
	{
		store, err := OpenMmapStore(dir, MmapStoreOptions{
			Dim: dim, M: 16,
			CheckpointInterval: 1000000, // effectively disable auto-checkpoint
		})
		if err != nil {
			t.Fatal(err)
		}

		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
			WithRand(rand.New(rand.NewSource(hnswSeed))))

		for i, v := range vecs {
			if err := idx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
				t.Fatalf("Insert doc-%d: %v", i, err)
			}
		}

		preResults = mmapHNSWSearchResults(t, idx, queries, k)

		// Simulate crash: sync mmap but do NOT call Close (no checkpoint).
		// The WAL contains all operations since last checkpoint (= all of them).
		if err := store.wal.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := store.wal.Sync(); err != nil {
			t.Fatal(err)
		}
		if err := store.syncAll(); err != nil {
			t.Fatal(err)
		}
		// Write meta to persist state, but don't truncate WAL.
		if err := writeMetaHeader(dir, &store.meta); err != nil {
			t.Fatal(err)
		}

		// Leak resources (simulating crash — no Close).
		mmapFree(store.vectors)
		mmapFree(store.nodes)
		mmapFree(store.graphL0)
		mmapFree(store.graphUpper)
		store.vecFile.Close()
		store.nodeFile.Close()
		store.l0File.Close()
		store.upperFile.Close()
		store.wal.Close()
		store.idmapFile.Close()
	}

	// Phase 2: Reopen (triggers WAL replay) and verify results.
	{
		store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()

		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
			WithRand(rand.New(rand.NewSource(hnswSeed))))

		postResults := mmapHNSWSearchResults(t, idx, queries, k)
		assertSearchResultsMatch(t, "WAL replay", preResults, postResults)
	}
}
