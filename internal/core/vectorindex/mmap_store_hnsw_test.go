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

	idx := NewHNSWIndex(store,
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

	idx := NewHNSWIndex(store,
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

	idx := NewHNSWIndex(store,
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

	idx := NewHNSWIndex(store,
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
// insertAllBatch builds an index from vecs via a single InsertBatch using doc
// IDs "doc-%d". For MmapStore this collapses ~N per-insert WAL syncs into one,
// which is far faster than a serial Insert loop while producing an identical
// graph (same insertion order → same random levels → same topology).
func insertAllBatch(t *testing.T, idx *HNSWIndex, vecs [][]float32) {
	t.Helper()
	items := make([]InsertItem, len(vecs))
	for i, v := range vecs {
		items[i] = InsertItem{DocId: fmt.Sprintf("doc-%d", i), Vector: v}
	}
	if err := idx.InsertBatch(items); err != nil {
		t.Fatalf("InsertBatch (%d items): %v", len(vecs), err)
	}
}

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
		idx := NewHNSWIndex(store,
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

		idx := NewHNSWIndex(store,
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
		idx := NewHNSWIndex(store,
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

		idx := NewHNSWIndex(store,
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
		idx := NewHNSWIndex(store,
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

		idx := NewHNSWIndex(store,
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
		crashCleanup := func() {
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
		t.Cleanup(crashCleanup)

		idx := NewHNSWIndex(store,
			WithRand(rand.New(rand.NewSource(hnswSeed))))

		// Build under a batch: defers the per-op msync (the dominant cost on
		// Windows) without weakening the test — CommitBatch(false) flushes the
		// WAL but does NOT msync the mmaps, so recovery still has to replay.
		store.BeginBatch()
		for i, v := range vecs {
			if err := idx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
				t.Fatalf("Insert doc-%d: %v", i, err)
			}
		}
		if err := store.CommitBatch(false); err != nil {
			t.Fatalf("CommitBatch: %v", err)
		}

		preResults = mmapHNSWSearchResults(t, idx, queries, k)

		// Simulate crash: close file handles without syncing mmaps or
		// writing meta. Data exists only in the WAL; recovery must replay it.
		crashCleanup()
	}

	// Phase 2: Reopen (triggers WAL replay) and verify results.
	{
		store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })

		idx := NewHNSWIndex(store,
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
	idx := NewHNSWIndex(store,
		WithEfConstruction(200),
		WithRand(rand.New(rand.NewSource(42))))

	rng := rand.New(rand.NewSource(99))
	vecs := randomVectors(rng, n, dim)
	insertAllBatch(t, idx, vecs)

	// Verify entry point exists and has level > 0 (with 2000 nodes this is very likely).
	epID, maxLevel, err := store.GetEntryPoint()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Entry point: id=%d, maxLevel=%d", epID, maxLevel)
	assert.Greater(t, maxLevel, 0, "with 2000 nodes, maxLevel should be > 0")

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
	memIdx := NewHNSWIndex(memStore,
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
		idx := NewHNSWIndex(store,
			WithEfConstruction(200),
			WithRand(rand.New(rand.NewSource(hnswSeed))))

		insertAllBatch(t, idx, vecs)

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

		idx := NewHNSWIndex(store,
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
		crashCleanup := func() {
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
		t.Cleanup(crashCleanup)

		idx := NewHNSWIndex(store,
			WithEfConstruction(200),
			WithRand(rand.New(rand.NewSource(hnswSeed))))

		insertAllBatch(t, idx, vecs)

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

		crashCleanup()
	}

	// Phase 2: Reopen (triggers WAL replay) and verify.
	{
		store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })

		idx := NewHNSWIndex(store,
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

// recallAtKMapped computes recall@K given ground truth indices and HNSW search results.
// nodeToBaseIdx maps node IDs to base vector indices (0-based).
func recallAtKMapped(trueNN []int, approxResults []SearchResult, k int, nodeToBaseIdx map[uint64]int) float64 {
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
// docFmt is the format string used for doc IDs (e.g. "doc-%d" or "%d").
func buildNodeToBaseIdxMap(store NodeStore, n int, docFmt string) map[uint64]int {
	m := make(map[uint64]int, n)
	for i := 0; i < n; i++ {
		nodeId, ok, _ := store.GetNodeId(fmt.Sprintf(docFmt, i))
		if ok {
			m[nodeId] = i
		}
	}
	return m
}

// TestMmapHNSW_RecallAt10 verifies recall@10 for both MemStore and MmapStore.
// Uses 2000 random 128d vectors (reduced from 5000 for CI speed) and brute-force ground truth.
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
	memIdx := NewHNSWIndex(memStore,
		WithEfConstruction(200), WithEfSearch(200),
		WithRand(rand.New(rand.NewSource(hnswSeed))))
	for i, v := range baseVecs {
		if err := memIdx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
			t.Fatalf("MemStore insert %d: %v", i, err)
		}
	}

	var memRecallSum float64
	memMapping := buildNodeToBaseIdxMap(memStore, n, "doc-%d")
	for i, q := range queryVecs {
		res, err := memIdx.Search(q, k)
		if err != nil {
			t.Fatal(err)
		}
		memRecallSum += recallAtKMapped(groundTruth[i], res, k, memMapping)
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

	mmapIdx := NewHNSWIndex(mmapStore,
		WithEfConstruction(200), WithEfSearch(200),
		WithRand(rand.New(rand.NewSource(hnswSeed))))
	insertAllBatch(t, mmapIdx, baseVecs)

	var mmapRecallSum float64
	mmapMapping := buildNodeToBaseIdxMap(mmapStore, n, "doc-%d")
	for i, q := range queryVecs {
		res, err := mmapIdx.Search(q, k)
		if err != nil {
			t.Fatal(err)
		}
		mmapRecallSum += recallAtKMapped(groundTruth[i], res, k, mmapMapping)
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
// Task 6: MemStore → MmapStore export integration
// ---------------------------------------------------------------------------

// TestMmapHNSW_ExportRecall verifies recall is preserved after MemStore → MmapStore export.
func TestMmapHNSW_ExportRecall(t *testing.T) {
	const (
		n   = 1000
		dim = 128
		k   = 10
		nq  = 50
	)

	rng := rand.New(rand.NewSource(99))
	baseVecs := randomVectors(rng, n, dim)
	queryVecs := randomVectors(rng, nq, dim)
	hnswSeed := int64(42)

	memStore := NewMemNodeStore()
	memIdx := NewHNSWIndex(memStore,
		WithEfConstruction(200), WithEfSearch(200),
		WithRand(rand.New(rand.NewSource(hnswSeed))))
	for i, v := range baseVecs {
		if err := memIdx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
			t.Fatalf("MemStore insert %d: %v", i, err)
		}
	}

	memResults := mmapHNSWSearchResults(t, memIdx, queryVecs, k)

	dir := t.TempDir()
	mmapStore, err := exportMemStoreToMmap(memStore, dir, dim, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer mmapStore.Close()

	mmapIdx := NewHNSWIndex(mmapStore,
		WithEfConstruction(200), WithEfSearch(200),
		WithRand(rand.New(rand.NewSource(hnswSeed))))

	mmapResults := mmapHNSWSearchResults(t, mmapIdx, queryVecs, k)
	assertSearchResultsMatch(t, "export recall", memResults, mmapResults)

	groundTruth := make([][]int, nq)
	for i, q := range queryVecs {
		groundTruth[i] = bruteForceKNN(q, baseVecs, k, CosineDistance)
	}
	mmapMapping := buildNodeToBaseIdxMap(mmapStore, n, "doc-%d")
	var recallSum float64
	for i, q := range queryVecs {
		res, err := mmapIdx.Search(q, k)
		if err != nil {
			t.Fatal(err)
		}
		recallSum += recallAtKMapped(groundTruth[i], res, k, mmapMapping)
	}
	recall := recallSum / float64(nq)
	t.Logf("Export MmapStore recall@10 = %.4f", recall)
	assert.Greater(t, recall, 0.95, "exported MmapStore recall@10 should be > 0.95")
}

// TestMmapHNSW_ExportThenInsertDelete verifies incremental ops work after export.
func TestMmapHNSW_ExportThenInsertDelete(t *testing.T) {
	const (
		n       = 500
		nExtra  = 100
		dim     = 128
		k       = 10
		nDelete = 50
	)

	rng := rand.New(rand.NewSource(99))
	baseVecs := randomVectors(rng, n+nExtra, dim)
	hnswSeed := int64(42)

	memStore := NewMemNodeStore()
	memIdx := NewHNSWIndex(memStore,
		WithEfConstruction(200),
		WithRand(rand.New(rand.NewSource(hnswSeed))))
	for i := 0; i < n; i++ {
		if err := memIdx.Insert(fmt.Sprintf("doc-%d", i), baseVecs[i]); err != nil {
			t.Fatalf("MemStore insert %d: %v", i, err)
		}
	}

	dir := t.TempDir()
	mmapStore, err := exportMemStoreToMmap(memStore, dir, dim, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer mmapStore.Close()

	mmapIdx := NewHNSWIndex(mmapStore,
		WithEfConstruction(200),
		WithRand(rand.New(rand.NewSource(hnswSeed))))

	for i := n; i < n+nExtra; i++ {
		if err := mmapIdx.Insert(fmt.Sprintf("doc-%d", i), baseVecs[i]); err != nil {
			t.Fatalf("Post-export insert doc-%d: %v", i, err)
		}
	}

	for i := 0; i < nDelete; i++ {
		if err := mmapIdx.Delete(fmt.Sprintf("doc-%d", i)); err != nil {
			t.Fatalf("Post-export delete doc-%d: %v", i, err)
		}
	}

	for i := 0; i < nDelete; i++ {
		_, ok, _ := mmapStore.GetNodeId(fmt.Sprintf("doc-%d", i))
		assert.False(t, ok, "deleted doc-%d should be gone", i)
	}

	query := randomVectors(rng, 1, dim)[0]
	results, err := mmapIdx.Search(query, k)
	if err != nil {
		t.Fatal(err)
	}
	assert.Len(t, results, k, "should return k results after insert+delete")

	expectedCount := uint64(n + nExtra - nDelete)
	assert.Equal(t, expectedCount, mmapStore.meta.NodeCount,
		"node count should reflect inserts and deletes")
}
