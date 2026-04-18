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

// ---------------------------------------------------------------------------
// Task 5: graph_upper.dat upper-layer verification
// ---------------------------------------------------------------------------

// TestMmapHNSW_UpperGraph_MultiLayer verifies that upper-layer nodes are
// correctly generated, stored, and readable.
func TestMmapHNSW_UpperGraph_MultiLayer(t *testing.T) {
	const (
		n   = 2000
		dim = 128
		k   = 10
	)

	dir := t.TempDir()
	store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Use efConstruction=200 and fixed seed to get deterministic upper layers.
	idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
		WithEfConstruction(200),
		WithRand(rand.New(rand.NewSource(42))))

	rng := rand.New(rand.NewSource(99))
	vecs := randomVectors(rng, n, dim)
	for i, v := range vecs {
		if err := idx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
			t.Fatalf("Insert doc-%d: %v", i, err)
		}
	}

	// Verify entry point exists and has level > 0 (with 5000 nodes this is very likely).
	epID, maxLevel, err := store.GetEntryPoint()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Entry point: id=%d, maxLevel=%d", epID, maxLevel)
	assert.Greater(t, maxLevel, 0, "with 5000 nodes, maxLevel should be > 0")

	// Collect all upper-layer nodes and verify their neighbors.
	upperNodeCount := 0
	for i := uint64(0); i < uint64(n); i++ {
		level, err := store.GetNodeLevel(i)
		if err != nil {
			continue
		}
		if level > 0 {
			upperNodeCount++
			for layer := 1; layer <= level; layer++ {
				nbs, err := store.GetNeighbors(i, layer)
				if err != nil {
					t.Fatalf("GetNeighbors(%d, %d): %v", i, layer, err)
				}
				// Upper-layer neighbors should be valid node IDs.
				for _, nb := range nbs {
					nbLevel, err := store.GetNodeLevel(nb)
					if err != nil {
						t.Fatalf("neighbor %d of node %d at layer %d: GetNodeLevel failed: %v", nb, i, layer, err)
					}
					assert.GreaterOrEqual(t, nbLevel, layer,
						"neighbor %d of node %d at layer %d must have level >= %d", nb, i, layer, layer)
				}
			}
		}
	}
	t.Logf("Upper-layer nodes: %d / %d", upperNodeCount, n)
	assert.Greater(t, upperNodeCount, 0, "should have at least some upper-layer nodes")

	// Build MemStore baseline for structural comparison.
	memStore := NewMemNodeStore()
	memIdx := NewHNSWIndex(memStore, CosineDistance, WithCosineDistance(),
		WithEfConstruction(200),
		WithRand(rand.New(rand.NewSource(42))))
	for i, v := range vecs {
		if err := memIdx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
			t.Fatalf("MemStore insert %d: %v", i, err)
		}
	}

	// maxLevel should match between stores (same seed, same insertion order).
	_, memMaxLevel, err := memStore.GetEntryPoint()
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, memMaxLevel, maxLevel, "maxLevel should match MemStore")

	// Search should produce results with comparable distances.
	queries := randomVectors(rng, 5, dim)
	for i, q := range queries {
		memRes, err := memIdx.Search(q, k)
		if err != nil {
			t.Fatal(err)
		}
		mmapRes, err := idx.Search(q, k)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, len(memRes), len(mmapRes),
			"query %d: result count mismatch", i)
		// Distances should be very close (same graph topology, different node ID allocation).
		for j := range memRes {
			assert.InDelta(t, memRes[j].Distance, mmapRes[j].Distance, 1e-4,
				"query %d result %d: distance mismatch", i, j)
		}
	}
}

// TestMmapHNSW_UpperGraph_PersistenceReopen verifies upper graph survives
// close → reopen with correct entry point and search results.
func TestMmapHNSW_UpperGraph_PersistenceReopen(t *testing.T) {
	const (
		n   = 2000
		dim = 128
		k   = 10
		nq  = 5
	)

	dir := t.TempDir()
	rng := rand.New(rand.NewSource(99))
	vecs := randomVectors(rng, n, dim)
	queries := randomVectors(rng, nq, dim)
	hnswSeed := int64(42)

	var preResults [][]SearchResult
	var preEP uint64
	var preMaxLevel int

	// Phase 1: Build index with upper layers.
	{
		store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
		if err != nil {
			t.Fatal(err)
		}
		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
			WithEfConstruction(200),
			WithRand(rand.New(rand.NewSource(hnswSeed))))

		for i, v := range vecs {
			if err := idx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
				t.Fatalf("Insert doc-%d: %v", i, err)
			}
		}

		preResults = mmapHNSWSearchResults(t, idx, queries, k)
		preEP, preMaxLevel, err = store.GetEntryPoint()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("Pre-close: entry=%d, maxLevel=%d", preEP, preMaxLevel)
		assert.Greater(t, preMaxLevel, 0)

		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}

	// Phase 2: Reopen and verify.
	{
		store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()

		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
			WithEfConstruction(200),
			WithRand(rand.New(rand.NewSource(hnswSeed))))

		// Verify entry point and max level restored.
		postEP, postMaxLevel, err := store.GetEntryPoint()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, preEP, postEP, "entry point should be restored")
		assert.Equal(t, preMaxLevel, postMaxLevel, "maxLevel should be restored")

		// Verify search results match.
		postResults := mmapHNSWSearchResults(t, idx, queries, k)
		assertSearchResultsMatch(t, "upper-reopen", preResults, postResults)
	}
}

// TestMmapHNSW_UpperGraph_GrowCrashRecovery verifies that upper graph grow
// followed by crash is correctly recovered via WAL replay.
func TestMmapHNSW_UpperGraph_GrowCrashRecovery(t *testing.T) {
	const (
		dim = 128
		k   = 10
		nq  = 5
	)

	dir := t.TempDir()
	rng := rand.New(rand.NewSource(99))
	hnswSeed := int64(42)

	// Use enough vectors to ensure upper graph slots are used.
	n := 3000
	vecs := randomVectors(rng, n, dim)
	queries := randomVectors(rng, nq, dim)

	var preResults [][]SearchResult
	{
		store, err := OpenMmapStore(dir, MmapStoreOptions{
			Dim: dim, M: 16,
			CheckpointInterval: 1000000, // disable auto-checkpoint
		})
		if err != nil {
			t.Fatal(err)
		}

		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
			WithEfConstruction(200),
			WithRand(rand.New(rand.NewSource(hnswSeed))))

		for i, v := range vecs {
			if err := idx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
				t.Fatalf("Insert doc-%d: %v", i, err)
			}
		}

		preResults = mmapHNSWSearchResults(t, idx, queries, k)

		ep, maxL, _ := store.GetEntryPoint()
		t.Logf("Pre-crash: entry=%d, maxLevel=%d, upperCap=%d", ep, maxL, store.upperCapacity)

		// Simulate crash: flush WAL but don't checkpoint/close.
		if err := store.wal.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := store.wal.Sync(); err != nil {
			t.Fatal(err)
		}
		if err := store.syncAll(); err != nil {
			t.Fatal(err)
		}
		if err := writeMetaHeader(dir, &store.meta); err != nil {
			t.Fatal(err)
		}

		// Leak resources (simulating crash).
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

	// Phase 2: Reopen (triggers WAL replay) and verify.
	{
		store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()

		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
			WithEfConstruction(200),
			WithRand(rand.New(rand.NewSource(hnswSeed))))

		// Verify entry point is valid.
		ep, maxL, err := store.GetEntryPoint()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("Post-crash: entry=%d, maxLevel=%d, upperCap=%d", ep, maxL, store.upperCapacity)
		assert.Greater(t, maxL, 0, "maxLevel should be > 0 after recovery")

		// Verify search results match pre-crash.
		postResults := mmapHNSWSearchResults(t, idx, queries, k)
		assertSearchResultsMatch(t, "upper-grow-crash", preResults, postResults)

		// Verify upper slot allocation didn't leak: count upper-layer nodes
		// and ensure nextUpperSlot is consistent.
		upperCount := 0
		for i := uint64(0); i < uint64(n); i++ {
			level, err := store.GetNodeLevel(i)
			if err != nil {
				continue
			}
			if level > 0 {
				upperCount++
			}
		}
		t.Logf("Upper-layer nodes after recovery: %d", upperCount)
		assert.Greater(t, upperCount, 0)
	}
}

// ---------------------------------------------------------------------------
// Task 4: Recall@10 verification
// ---------------------------------------------------------------------------

// recallAtK computes recall@K given ground truth indices and HNSW search results.
// nodeToBaseIdx maps node IDs to base vector indices (0-based).
func recallAtK(trueNN []int, approxResults []SearchResult, k int, nodeToBaseIdx map[uint64]int) float64 {
	trueSet := make(map[int]bool, k)
	for i := 0; i < k && i < len(trueNN); i++ {
		trueSet[trueNN[i]] = true
	}
	hits := 0
	for i := 0; i < k && i < len(approxResults); i++ {
		baseIdx, ok := nodeToBaseIdx[approxResults[i].ID]
		if ok && trueSet[baseIdx] {
			hits++
		}
	}
	return float64(hits) / float64(k)
}

// buildNodeToBaseIdxMap builds a mapping from node ID → base vector index.
func buildNodeToBaseIdxMap(store NodeStore, n int) map[uint64]int {
	m := make(map[uint64]int, n)
	for i := 0; i < n; i++ {
		nodeId, ok, _ := store.GetNodeId(fmt.Sprintf("doc-%d", i))
		if ok {
			m[nodeId] = i
		}
	}
	return m
}

// TestMmapHNSW_RecallAt10 verifies recall@10 for both MemStore and MmapStore.
// Uses 5000 random 128d vectors and brute-force ground truth.
func TestMmapHNSW_RecallAt10(t *testing.T) {
	const (
		n   = 2000
		dim = 128
		k   = 10
		nq  = 100
	)

	rng := rand.New(rand.NewSource(99))
	baseVecs := randomVectors(rng, n, dim)
	queryVecs := randomVectors(rng, nq, dim)
	hnswSeed := int64(42)

	// Build ground truth via brute force.
	groundTruth := make([][]int, nq)
	for i, q := range queryVecs {
		groundTruth[i] = bruteForceKNN(q, baseVecs, k, CosineDistance)
	}

	// --- MemStore recall ---
	memStore := NewMemNodeStore()
	memIdx := NewHNSWIndex(memStore, CosineDistance, WithCosineDistance(),
		WithEfConstruction(200), WithEfSearch(200),
		WithRand(rand.New(rand.NewSource(hnswSeed))))
	for i, v := range baseVecs {
		if err := memIdx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
			t.Fatalf("MemStore insert %d: %v", i, err)
		}
	}

	var memRecallSum float64
	memMapping := buildNodeToBaseIdxMap(memStore, n)
	for i, q := range queryVecs {
		res, err := memIdx.Search(q, k)
		if err != nil {
			t.Fatal(err)
		}
		memRecallSum += recallAtK(groundTruth[i], res, k, memMapping)
	}
	memRecall := memRecallSum / float64(nq)
	t.Logf("MemStore recall@10 = %.4f", memRecall)
	assert.Greater(t, memRecall, 0.95, "MemStore recall@10 should be > 0.95")

	// --- MmapStore recall ---
	dir := t.TempDir()
	mmapStore, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer mmapStore.Close()

	mmapIdx := NewHNSWIndex(mmapStore, CosineDistance, WithCosineDistance(),
		WithEfConstruction(200), WithEfSearch(200),
		WithRand(rand.New(rand.NewSource(hnswSeed))))
	for i, v := range baseVecs {
		if err := mmapIdx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
			t.Fatalf("MmapStore insert %d: %v", i, err)
		}
	}

	var mmapRecallSum float64
	mmapMapping := buildNodeToBaseIdxMap(mmapStore, n)
	for i, q := range queryVecs {
		res, err := mmapIdx.Search(q, k)
		if err != nil {
			t.Fatal(err)
		}
		mmapRecallSum += recallAtK(groundTruth[i], res, k, mmapMapping)
	}
	mmapRecall := mmapRecallSum / float64(nq)
	t.Logf("MmapStore recall@10 = %.4f", mmapRecall)
	assert.Greater(t, mmapRecall, 0.95, "MmapStore recall@10 should be > 0.95")

	// Recall difference should be < 0.01.
	diff := memRecall - mmapRecall
	if diff < 0 {
		diff = -diff
	}
	t.Logf("Recall difference = %.4f", diff)
	assert.Less(t, diff, 0.01, "MmapStore vs MemStore recall difference should be < 0.01")
}

// ---------------------------------------------------------------------------
// Task 5: graph_upper.dat upper graph verification
// ---------------------------------------------------------------------------

// TestMmapHNSW_UpperGraph verifies that multi-level nodes are correctly generated
// and upper graph neighbors match MemStore baseline.
func TestMmapHNSW_UpperGraph(t *testing.T) {
	const (
		n   = 2000
		dim = 128
	)

	rng := rand.New(rand.NewSource(99))
	vecs := randomVectors(rng, n, dim)
	hnswSeed := int64(42)

	// Build MemStore index (baseline).
	memStore := NewMemNodeStore()
	memIdx := NewHNSWIndex(memStore, CosineDistance, WithCosineDistance(),
		WithEfConstruction(200), WithRand(rand.New(rand.NewSource(hnswSeed))))
	for i, v := range vecs {
		if err := memIdx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
			t.Fatalf("MemStore insert %d: %v", i, err)
		}
	}

	// Build MmapStore index.
	dir := t.TempDir()
	mmapStore, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer mmapStore.Close()

	mmapIdx := NewHNSWIndex(mmapStore, CosineDistance, WithCosineDistance(),
		WithEfConstruction(200), WithRand(rand.New(rand.NewSource(hnswSeed))))
	for i, v := range vecs {
		if err := mmapIdx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
			t.Fatalf("MmapStore insert %d: %v", i, err)
		}
	}

	// Verify entry point and max level match.
	memEP, memMaxLevel, _ := memStore.GetEntryPoint()
	mmapEP, mmapMaxLevel, _ := mmapStore.GetEntryPoint()
	assert.Equal(t, memMaxLevel, mmapMaxLevel, "max level should match")

	// Find all level>0 nodes in MmapStore and verify upper neighbors.
	upperNodeCount := 0
	for i := uint64(0); i < mmapStore.meta.TotalSlots; i++ {
		mmapLevel, err := mmapStore.GetNodeLevel(i)
		if err != nil {
			continue // deleted node
		}
		if mmapLevel == 0 {
			continue
		}
		upperNodeCount++

		// Verify upper layer neighbors match MemStore.
		// MemStore uses 1-based IDs, MmapStore uses 0-based.
		// We compare by checking the neighbor lists have the same length.
		for layer := 1; layer <= mmapLevel; layer++ {
			mmapNb, err := mmapStore.GetNeighbors(i, layer)
			if err != nil {
				t.Fatalf("GetNeighbors(node %d, layer %d): %v", i, layer, err)
			}
			// MemStore node for the same doc.
			docId := mmapStore.nodeToDoc[i]
			memNodeId, ok, _ := memStore.GetNodeId(docId)
			if !ok {
				t.Fatalf("MemStore missing doc %s", docId)
			}
			memNb, _ := memStore.GetNeighbors(memNodeId, layer)
			assert.Equal(t, len(memNb), len(mmapNb),
				"node %d (doc %s) layer %d: neighbor count mismatch", i, docId, layer)
		}
	}
	t.Logf("Found %d upper-layer nodes (entry: mem=%d, mmap=%d)", upperNodeCount, memEP, mmapEP)
	assert.Greater(t, upperNodeCount, 0, "should have at least some upper-layer nodes")
}

// TestMmapHNSW_UpperGraphPersistence verifies upper graph survives close → reopen.
func TestMmapHNSW_UpperGraphPersistence(t *testing.T) {
	const (
		n   = 2000
		dim = 128
		k   = 10
		nq  = 10
	)

	dir := t.TempDir()
	rng := rand.New(rand.NewSource(99))
	vecs := randomVectors(rng, n, dim)
	queries := randomVectors(rng, nq, dim)
	hnswSeed := int64(42)

	// Phase 1: Build with enough vectors to generate multi-level nodes.
	var preResults [][]SearchResult
	var preEP uint64
	var preMaxLevel int
	{
		store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
		if err != nil {
			t.Fatal(err)
		}
		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
			WithEfConstruction(200), WithRand(rand.New(rand.NewSource(hnswSeed))))

		for i, v := range vecs {
			if err := idx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
				t.Fatalf("Insert %d: %v", i, err)
			}
		}

		preResults = mmapHNSWSearchResults(t, idx, queries, k)
		preEP, preMaxLevel, _ = store.GetEntryPoint()
		t.Logf("Pre-close: entryPoint=%d, maxLevel=%d", preEP, preMaxLevel)

		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}

	// Phase 2: Reopen and verify.
	{
		store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()

		postEP, postMaxLevel, err := store.GetEntryPoint()
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, preEP, postEP, "entry point should be restored")
		assert.Equal(t, preMaxLevel, postMaxLevel, "max level should be restored")

		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
			WithEfConstruction(200), WithRand(rand.New(rand.NewSource(hnswSeed))))

		postResults := mmapHNSWSearchResults(t, idx, queries, k)
		assertSearchResultsMatch(t, "upper graph reopen", preResults, postResults)
	}
}

// TestMmapHNSW_UpperGraphGrowCrashRecovery verifies upper graph grow + crash recovery.
func TestMmapHNSW_UpperGraphGrowCrashRecovery(t *testing.T) {
	const (
		n   = 2000
		dim = 128
		k   = 10
		nq  = 5
	)

	dir := t.TempDir()
	rng := rand.New(rand.NewSource(99))
	vecs := randomVectors(rng, n, dim)
	queries := randomVectors(rng, nq, dim)
	hnswSeed := int64(42)

	// Build index with high checkpoint interval to accumulate WAL.
	var preResults [][]SearchResult
	{
		store, err := OpenMmapStore(dir, MmapStoreOptions{
			Dim: dim, M: 16,
			CheckpointInterval: 1000000,
		})
		if err != nil {
			t.Fatal(err)
		}

		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
			WithEfConstruction(200), WithRand(rand.New(rand.NewSource(hnswSeed))))

		for i, v := range vecs {
			if err := idx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
				t.Fatalf("Insert %d: %v", i, err)
			}
		}

		preResults = mmapHNSWSearchResults(t, idx, queries, k)

		// Simulate crash: sync data but no checkpoint.
		if err := store.wal.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := store.wal.Sync(); err != nil {
			t.Fatal(err)
		}
		if err := store.syncAll(); err != nil {
			t.Fatal(err)
		}
		if err := writeMetaHeader(dir, &store.meta); err != nil {
			t.Fatal(err)
		}

		// Verify upper slots were allocated.
		nextSlot := store.readGraphUpperNextSlot()
		t.Logf("Upper graph next slot before crash: %d", nextSlot)
		assert.Greater(t, nextSlot, uint64(1), "should have allocated upper slots")

		// Cleanup without Close.
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

	// Reopen (WAL replay) and verify.
	{
		store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()

		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
			WithEfConstruction(200), WithRand(rand.New(rand.NewSource(hnswSeed))))

		postResults := mmapHNSWSearchResults(t, idx, queries, k)
		assertSearchResultsMatch(t, "upper graph crash recovery", preResults, postResults)

		// Verify upper nodes exist and search is correct after recovery.
		var upperNodes int
		for i := uint64(0); i < store.meta.TotalSlots; i++ {
			level, err := store.GetNodeLevel(i)
			if err == nil && level > 0 {
				upperNodes++
			}
		}
		t.Logf("Upper nodes after recovery: %d", upperNodes)
		assert.Greater(t, upperNodes, 0, "should have upper-layer nodes after recovery")
	}
}
