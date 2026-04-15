package vectorindex

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- Option tests ---

func TestWithM(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance, WithM(8))
	assert.Equal(t, 8, idx.M)
	assert.Equal(t, 16, idx.Mmax0, "Mmax0 should be 2*M")
}

func TestWithMBoundary(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance, WithM(1))
	assert.Equal(t, 1, idx.M)
	assert.Equal(t, 2, idx.Mmax0)
}

func TestWithEfConstruction(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance, WithEfConstruction(500))
	assert.Equal(t, 500, idx.efConstruction)
}

func TestWithEfConstructionDefault(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance)
	assert.Equal(t, DefaultEfConstruction, idx.efConstruction)
}

func TestWithEfSearch(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance, WithEfSearch(128))
	assert.Equal(t, 128, idx.efSearch)
}

func TestWithEfSearchDefault(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance)
	assert.Equal(t, DefaultEfSearch, idx.efSearch)
}

func TestMultipleOptions(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance, WithM(32), WithEfConstruction(400), WithEfSearch(256))
	assert.Equal(t, 32, idx.M)
	assert.Equal(t, 64, idx.Mmax0)
	assert.Equal(t, 400, idx.efConstruction)
	assert.Equal(t, 256, idx.efSearch)
}

// --- InsertBatch tests ---

func TestInsertBatchMemStore(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(), WithRand(rand.New(rand.NewSource(42))))

	items := []InsertItem{
		{DocId: "doc-0", Vector: []float32{1, 0, 0}},
		{DocId: "doc-1", Vector: []float32{0, 1, 0}},
		{DocId: "doc-2", Vector: []float32{0, 0, 1}},
	}

	err := idx.InsertBatch(items)
	requireNoError(t, err)

	// Verify all items are searchable.
	results, err := idx.Search([]float32{1, 0, 0}, 3)
	requireNoError(t, err)
	assert.Len(t, results, 3)

	// The closest to [1,0,0] should be doc-0 (id=1).
	assert.Equal(t, uint64(1), results[0].ID)
	assert.InDelta(t, 0.0, results[0].Distance, 1e-6)
}

func TestInsertBatchEmpty(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance())

	err := idx.InsertBatch(nil)
	requireNoError(t, err)

	results, err := idx.Search([]float32{1, 0, 0}, 1)
	requireNoError(t, err)
	assert.Empty(t, results)
}

func TestInsertBatchPebbleStore(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)
	idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(), WithRand(rand.New(rand.NewSource(42))))

	items := []InsertItem{
		{DocId: "a", Vector: []float32{1, 0, 0}},
		{DocId: "b", Vector: []float32{0, 1, 0}},
		{DocId: "c", Vector: []float32{0, 0, 1}},
		{DocId: "d", Vector: []float32{1, 1, 0}},
		{DocId: "e", Vector: []float32{0, 1, 1}},
	}

	err := idx.InsertBatch(items)
	requireNoError(t, err)

	// All 5 items should be searchable.
	results, err := idx.Search([]float32{1, 0, 0}, 5)
	requireNoError(t, err)
	assert.Len(t, results, 5)

	// Closest to [1,0,0] should be "a" (id=1).
	assert.Equal(t, uint64(1), results[0].ID)
}

func TestInsertBatchSingle(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(), WithRand(rand.New(rand.NewSource(42))))

	err := idx.InsertBatch([]InsertItem{
		{DocId: "only", Vector: []float32{1, 2, 3}},
	})
	requireNoError(t, err)

	results, err := idx.Search([]float32{1, 2, 3}, 1)
	requireNoError(t, err)
	requireLen(t, results, 1)
	assert.InDelta(t, 0.0, results[0].Distance, 1e-6)
}

// --- MemNodeStore SetNorm / GetNorm tests ---

func TestMemNodeStoreSetNorm(t *testing.T) {
	store := NewMemNodeStore()

	// SetNorm for a node that has been created via PutNode (PutNode computes a norm automatically).
	requireNoError(t, store.PutNode(1, 0, []float32{3, 4}))

	// Override the norm with a custom value.
	requireNoError(t, store.SetNorm(1, 99.0))

	got, err := store.GetNorm(1)
	requireNoError(t, err)
	assert.InDelta(t, 99.0, got, 1e-6)
}

func TestMemNodeStoreGetNormNotFound(t *testing.T) {
	store := NewMemNodeStore()

	_, err := store.GetNorm(999)
	assert.Error(t, err)
}

func TestMemNodeStoreGetVectorRef(t *testing.T) {
	store := NewMemNodeStore()
	requireNoError(t, store.PutNode(1, 0, []float32{1.5, 2.5, 3.5}))

	ref, err := store.GetVectorRef(1)
	requireNoError(t, err)
	assert.Equal(t, []float32{1.5, 2.5, 3.5}, ref)

	// GetVectorRef should return same underlying slice on repeated calls.
	ref2, err := store.GetVectorRef(1)
	requireNoError(t, err)
	assert.Equal(t, ref, ref2)
}

func TestMemNodeStoreGetVectorRefNotFound(t *testing.T) {
	store := NewMemNodeStore()
	_, err := store.GetVectorRef(999)
	assert.Error(t, err)
}

// --- PebbleNodeStore batch operation tests ---

func TestPebbleBatchDepth(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	assert.Equal(t, 0, store.BatchDepth())

	store.BeginBatch()
	assert.Equal(t, 1, store.BatchDepth())

	store.BeginBatch()
	assert.Equal(t, 2, store.BatchDepth())

	store.DiscardBatch()
	assert.Equal(t, 1, store.BatchDepth())

	store.DiscardBatch()
	assert.Equal(t, 0, store.BatchDepth())
}

func TestPebbleDiscardBatchNoActive(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	// DiscardBatch with no active batch should be a no-op.
	store.DiscardBatch()
	assert.Equal(t, 0, store.BatchDepth())
}

func TestPebbleCommitBatchNoActive(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	err := store.CommitBatch(true)
	assert.Error(t, err, "CommitBatch with no active batch should error")
}

func TestPebbleBatchWritesVisible(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	store.BeginBatch()
	requireNoError(t, store.PutNode(1, 0, []float32{1.0, 2.0}))

	// Data should be readable through the batch (indexed batch).
	vec, err := store.GetVector(1)
	requireNoError(t, err)
	assert.Equal(t, []float32{1.0, 2.0}, vec)

	requireNoError(t, store.CommitBatch(true))

	// Data should persist after commit.
	vec, err = store.GetVector(1)
	requireNoError(t, err)
	assert.Equal(t, []float32{1.0, 2.0}, vec)
}

func TestPebbleBatchNestedCommit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	store.BeginBatch()
	assert.Equal(t, 1, store.BatchDepth())

	store.BeginBatch()
	assert.Equal(t, 2, store.BatchDepth())

	// Inner commit should just decrement depth, not actually commit.
	requireNoError(t, store.CommitBatch(true))
	assert.Equal(t, 1, store.BatchDepth())

	requireNoError(t, store.PutNode(1, 0, []float32{5.0}))

	// Outer commit should actually commit.
	requireNoError(t, store.CommitBatch(true))
	assert.Equal(t, 0, store.BatchDepth())

	vec, err := store.GetVector(1)
	requireNoError(t, err)
	assert.Equal(t, []float32{5.0}, vec)
}

func TestPebbleDiscardBatchDiscardsWrites(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	// First, write and commit a known value.
	requireNoError(t, store.PutNode(1, 0, []float32{1.0}))

	// Start a new batch, write something, then discard.
	store.BeginBatch()
	requireNoError(t, store.SetNorm(2, 42.0))
	store.DiscardBatch()

	// The norm for node 2 should not exist since batch was discarded.
	_, err := store.GetNorm(2)
	assert.Error(t, err)
}

// --- PebbleNodeStore SetNorm / GetNorm tests ---

func TestPebbleSetNormAndGetNorm(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	// SetNorm directly (without batch).
	requireNoError(t, store.SetNorm(10, 3.14))

	got, err := store.GetNorm(10)
	requireNoError(t, err)
	assert.InDelta(t, 3.14, float64(got), 1e-6)
}

func TestPebbleSetNormInBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	store.BeginBatch()
	requireNoError(t, store.SetNorm(20, 2.71))

	// Should be visible within the batch.
	got, err := store.GetNorm(20)
	requireNoError(t, err)
	assert.InDelta(t, 2.71, float64(got), 1e-6)

	requireNoError(t, store.CommitBatch(false))

	// Should persist after commit.
	got, err = store.GetNorm(20)
	requireNoError(t, err)
	assert.InDelta(t, 2.71, float64(got), 1e-6)
}

func TestPebbleGetNormNotFound(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	_, err := store.GetNorm(999)
	assert.Error(t, err)
}

// --- PebbleNodeStore GetVectorRef tests ---

func TestPebbleGetVectorRef(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)
	requireNoError(t, store.PutNode(1, 0, []float32{1.0, 2.0, 3.0}))

	ref, err := store.GetVectorRef(1)
	requireNoError(t, err)
	assert.Equal(t, []float32{1.0, 2.0, 3.0}, ref)
}

func TestPebbleGetVectorRefNotFound(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	_, err := store.GetVectorRef(999)
	assert.Error(t, err)
}

// --- PebbleNodeStore cache eviction tests ---

func TestPebbleCachePutEviction(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)
	store.cacheSize = 3 // small cache for testing eviction

	for i := uint64(1); i <= 5; i++ {
		requireNoError(t, store.PutNode(i, 0, []float32{float32(i)}))
	}

	// Cache should only hold the most recent 3 entries.
	assert.LessOrEqual(t, store.cacheOrder.Len(), 3)

	// All vectors should still be retrievable (from disk if evicted).
	for i := uint64(1); i <= 5; i++ {
		vec, err := store.GetVector(i)
		requireNoError(t, err)
		assert.Equal(t, []float32{float32(i)}, vec)
	}
}

func TestPebbleCachePutUpdate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	requireNoError(t, store.PutNode(1, 0, []float32{1.0}))
	requireNoError(t, store.PutNode(1, 0, []float32{2.0}))

	vec, err := store.GetVector(1)
	requireNoError(t, err)
	assert.Equal(t, []float32{2.0}, vec)
}

// --- PebbleNodeStore SetEntryPoint with batch ---

func TestPebbleSetEntryPointInBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	store.BeginBatch()
	requireNoError(t, store.SetEntryPoint(5, 3))

	epId, maxLayer, err := store.GetEntryPoint()
	requireNoError(t, err)
	assert.Equal(t, uint64(5), epId)
	assert.Equal(t, 3, maxLayer)

	requireNoError(t, store.CommitBatch(true))

	epId, maxLayer, err = store.GetEntryPoint()
	requireNoError(t, err)
	assert.Equal(t, uint64(5), epId)
	assert.Equal(t, 3, maxLayer)
}

// --- neighborsPrefix ---

func TestPebbleNeighborsPrefix(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	prefix := store.neighborsPrefix(42)
	assert.Contains(t, string(prefix), "42")
	assert.Contains(t, string(prefix), "nb:")
}

// --- Fixture file loaders ---

func writeTestVectorFile(t *testing.T, dir string, name string, vecs [][]float32) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	defer f.Close()

	count := uint32(len(vecs))
	dim := uint32(0)
	if count > 0 {
		dim = uint32(len(vecs[0]))
	}

	if err := binary.Write(f, binary.LittleEndian, count); err != nil {
		t.Fatalf("write count: %v", err)
	}
	if err := binary.Write(f, binary.LittleEndian, dim); err != nil {
		t.Fatalf("write dim: %v", err)
	}
	for _, v := range vecs {
		if err := binary.Write(f, binary.LittleEndian, v); err != nil {
			t.Fatalf("write vec: %v", err)
		}
	}
	return path
}

func TestLoadVectors(t *testing.T) {
	dir := t.TempDir()
	expected := [][]float32{
		{1.0, 2.0, 3.0},
		{4.0, 5.0, 6.0},
	}
	path := writeTestVectorFile(t, dir, "vectors.bin", expected)

	got, err := LoadVectors(path)
	requireNoError(t, err)
	assert.Len(t, got, 2)
	assert.InDeltaSlice(t, expected[0], got[0], 1e-6)
	assert.InDeltaSlice(t, expected[1], got[1], 1e-6)
}

func TestLoadVectorsFileNotFound(t *testing.T) {
	_, err := LoadVectors("/nonexistent/path/vectors.bin")
	assert.Error(t, err)
}

func TestLoadQueries(t *testing.T) {
	dir := t.TempDir()
	expected := [][]float32{
		{0.1, 0.2},
		{0.3, 0.4},
		{0.5, 0.6},
	}
	path := writeTestVectorFile(t, dir, "queries.bin", expected)

	got, err := LoadQueries(path)
	requireNoError(t, err)
	assert.Len(t, got, 3)
	for i := range expected {
		assert.InDeltaSlice(t, expected[i], got[i], 1e-6)
	}
}

func TestLoadQueriesFileNotFound(t *testing.T) {
	_, err := LoadQueries("/nonexistent/path/queries.bin")
	assert.Error(t, err)
}

func writeTestGroundTruthFile(t *testing.T, dir string, name string, gt [][]uint32) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	defer f.Close()

	nq := uint32(len(gt))
	k := uint32(0)
	if nq > 0 {
		k = uint32(len(gt[0]))
	}

	if err := binary.Write(f, binary.LittleEndian, nq); err != nil {
		t.Fatalf("write nq: %v", err)
	}
	if err := binary.Write(f, binary.LittleEndian, k); err != nil {
		t.Fatalf("write k: %v", err)
	}
	for _, row := range gt {
		if err := binary.Write(f, binary.LittleEndian, row); err != nil {
			t.Fatalf("write row: %v", err)
		}
	}
	return path
}

func TestLoadGroundTruth(t *testing.T) {
	dir := t.TempDir()
	expected := [][]uint32{
		{0, 5, 10},
		{3, 7, 11},
	}
	path := writeTestGroundTruthFile(t, dir, "gt.bin", expected)

	got, err := LoadGroundTruth(path)
	requireNoError(t, err)
	assert.Len(t, got, 2)
	assert.Equal(t, expected[0], got[0])
	assert.Equal(t, expected[1], got[1])
}

func TestLoadGroundTruthFileNotFound(t *testing.T) {
	_, err := LoadGroundTruth("/nonexistent/path/gt.bin")
	assert.Error(t, err)
}

func TestLoadGroundTruthInvalidHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.bin")
	requireNoError(t, os.WriteFile(path, []byte{0x01}, 0o644))

	_, err := LoadGroundTruth(path)
	assert.Error(t, err)
}

func TestLoadVectorsInvalidHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.bin")
	requireNoError(t, os.WriteFile(path, []byte{0x01}, 0o644))

	_, err := LoadVectors(path)
	assert.Error(t, err)
}

func TestLoadVectorsTruncatedData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trunc.bin")
	f, err := os.Create(path)
	requireNoError(t, err)
	// Write header: count=2, dim=3 — but provide no data bytes.
	binary.Write(f, binary.LittleEndian, uint32(2))
	binary.Write(f, binary.LittleEndian, uint32(3))
	f.Close()

	_, err = LoadVectors(path)
	assert.Error(t, err)
}

// --- InsertBatch with WithM/WithEfConstruction/WithEfSearch ---

func TestInsertBatchWithCustomOptions(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance,
		WithCosineDistance(),
		WithM(4),
		WithEfConstruction(50),
		WithEfSearch(32),
		WithRand(rand.New(rand.NewSource(42))),
	)

	items := make([]InsertItem, 20)
	for i := range items {
		v := make([]float32, 8)
		for j := range v {
			v[j] = float32(i*8+j) + 0.1
		}
		items[i] = InsertItem{
			DocId:  fmt.Sprintf("doc-%d", i),
			Vector: v,
		}
	}

	requireNoError(t, idx.InsertBatch(items))

	// Verify search works with custom efSearch.
	results, err := idx.Search(items[0].Vector, 5)
	requireNoError(t, err)
	assert.NotEmpty(t, results)
	assert.Equal(t, uint64(1), results[0].ID, "closest result should be the query itself")

	// Verify neighbor limits with custom M=4.
	for nodeId := uint64(1); nodeId <= 20; nodeId++ {
		nb0, err := store.GetNeighbors(nodeId, 0)
		requireNoError(t, err)
		assert.LessOrEqual(t, len(nb0), idx.Mmax0, "layer 0 neighbors should be <= Mmax0")
	}
}

// --- PebbleNodeStore norm persists through PutNode ---

func TestPebblePutNodeStoresNorm(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	// [3, 4] has L2 norm = 5.0
	requireNoError(t, store.PutNode(1, 0, []float32{3, 4}))

	norm, err := store.GetNorm(1)
	requireNoError(t, err)
	assert.InDelta(t, 5.0, float64(norm), 1e-4)
}

// --- Encoding helpers roundtrip ---

func TestEncodeDecodeFloat32Roundtrip(t *testing.T) {
	values := []float32{0, 1.0, -1.0, math.MaxFloat32, math.SmallestNonzeroFloat32}
	for _, v := range values {
		encoded := encodeFloat32(v)
		decoded := decodeFloat32Single(encoded)
		assert.Equal(t, v, decoded, "roundtrip failed for %v", v)
	}
}

func TestEncodeDecodeFloat32sRoundtrip(t *testing.T) {
	v := []float32{1.5, -2.5, 0, 3.14}
	encoded := encodeFloat32s(v)
	decoded := decodeFloat32s(encoded)
	assert.Equal(t, v, decoded)
}

func TestEncodeDecodeUint64sRoundtrip(t *testing.T) {
	ids := []uint64{0, 1, 42, math.MaxUint64}
	encoded := encodeUint64s(ids)
	decoded := decodeUint64s(encoded)
	assert.Equal(t, ids, decoded)
}

func TestEncodeDecodeUint64Roundtrip(t *testing.T) {
	for _, v := range []uint64{0, 1, math.MaxUint64} {
		encoded := encodeUint64(v)
		decoded := decodeUint64(encoded)
		assert.Equal(t, v, decoded)
	}
}

// --- PebbleNodeStore batch with delete ---

func TestPebbleBatchDeleteNode(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	// Insert a node.
	requireNoError(t, store.PutNode(1, 1, []float32{1.0}))
	requireNoError(t, store.SetNeighbors(1, 0, []uint64{2}))
	requireNoError(t, store.SetNeighbors(1, 1, []uint64{3}))
	requireNoError(t, store.SetNodeMapping("doc-1", 1))

	// Delete within a batch.
	store.BeginBatch()
	requireNoError(t, store.DeleteNode(1))
	requireNoError(t, store.CommitBatch(true))

	_, err := store.GetVector(1)
	assert.Error(t, err)
	_, err = store.GetNodeLevel(1)
	assert.Error(t, err)
}

// --- HNSW with PebbleNodeStore full workflow ---

func TestHNSWWithPebbleStoreInsertAndSearch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)
	idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(), WithRand(rand.New(rand.NewSource(42))))

	requireNoError(t, idx.Insert("doc-0", []float32{1, 0, 0}))
	requireNoError(t, idx.Insert("doc-1", []float32{0, 1, 0}))
	requireNoError(t, idx.Insert("doc-2", []float32{0, 0, 1}))

	results, err := idx.Search([]float32{1, 0, 0}, 3)
	requireNoError(t, err)
	assert.Len(t, results, 3)
	assert.Equal(t, uint64(1), results[0].ID)
}

// --- PebbleNodeStore SetNeighbors with batch ---

func TestPebbleSetNeighborsInBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	store.BeginBatch()
	requireNoError(t, store.PutNode(1, 0, []float32{1.0}))
	requireNoError(t, store.SetNeighbors(1, 0, []uint64{2, 3}))
	requireNoError(t, store.CommitBatch(true))

	nb, err := store.GetNeighbors(1, 0)
	requireNoError(t, err)
	assert.Equal(t, []uint64{2, 3}, nb)
}

// --- PebbleNodeStore SetNodeMapping with batch ---

func TestPebbleSetNodeMappingInBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	store.BeginBatch()
	requireNoError(t, store.SetNodeMapping("doc-x", 42))
	requireNoError(t, store.CommitBatch(true))

	nodeId, found, err := store.GetNodeId("doc-x")
	requireNoError(t, err)
	assert.True(t, found)
	assert.Equal(t, uint64(42), nodeId)
}

// --- PebbleNodeStore DeleteNodeMapping with batch ---

func TestPebbleDeleteNodeMappingInBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	requireNoError(t, store.SetNodeMapping("doc-y", 50))

	store.BeginBatch()
	requireNoError(t, store.DeleteNodeMapping("doc-y"))
	requireNoError(t, store.CommitBatch(true))

	_, found, err := store.GetNodeId("doc-y")
	requireNoError(t, err)
	assert.False(t, found)
}

// --- PebbleNodeStore NextNodeId with batch ---

func TestPebbleNextNodeIdInBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	store.BeginBatch()
	id1, err := store.NextNodeId()
	requireNoError(t, err)
	assert.Equal(t, uint64(1), id1)

	id2, err := store.NextNodeId()
	requireNoError(t, err)
	assert.Equal(t, uint64(2), id2)
	requireNoError(t, store.CommitBatch(true))

	// After commit, next should continue from 2.
	id3, err := store.NextNodeId()
	requireNoError(t, err)
	assert.Equal(t, uint64(3), id3)
}

// --- PebbleNodeStore CommitBatch with NoSync ---

func TestPebbleCommitBatchNoSync(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	store.BeginBatch()
	requireNoError(t, store.PutNode(1, 0, []float32{1.0}))
	requireNoError(t, store.CommitBatch(false)) // doSync=false

	vec, err := store.GetVector(1)
	requireNoError(t, err)
	assert.Equal(t, []float32{1.0}, vec)
}
