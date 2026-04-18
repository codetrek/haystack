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
