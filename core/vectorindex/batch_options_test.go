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
	idx := NewHNSWIndex(store, WithM(8))
	assert.Equal(t, 8, idx.M)
	assert.Equal(t, 16, idx.Mmax0, "Mmax0 should be 2*M")
}

func TestWithMBoundary(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithM(1))
	assert.Equal(t, 1, idx.M)
	assert.Equal(t, 2, idx.Mmax0)
}

func TestWithEfConstruction(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithEfConstruction(500))
	assert.Equal(t, 500, idx.efConstruction)
}

func TestWithEfConstructionDefault(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store)
	assert.Equal(t, DefaultEfConstruction, idx.efConstruction)
}

func TestWithEfSearch(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithEfSearch(128))
	assert.Equal(t, 128, idx.efSearch)
}

func TestWithEfSearchDefault(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store)
	assert.Equal(t, DefaultEfSearch, idx.efSearch)
}

func TestMultipleOptions(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithM(32), WithEfConstruction(400), WithEfSearch(256))
	assert.Equal(t, 32, idx.M)
	assert.Equal(t, 64, idx.Mmax0)
	assert.Equal(t, 400, idx.efConstruction)
	assert.Equal(t, 256, idx.efSearch)
}

// The index takes its distance metric from the store, so the metric, the
// on-disk vector form, and the graph can never disagree.
func TestIndexMetricFromStore(t *testing.T) {
	for _, m := range []Metric{Cosine, DotProduct, Euclidean} {
		t.Run(m.String(), func(t *testing.T) {
			idx := NewHNSWIndex(NewMemNodeStore(m))
			assert.Equal(t, m, idx.metric)
		})
	}
}

// --- Batch tests ---

func TestBatchCommitMemStore(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithRand(rand.New(rand.NewSource(42))))

	b := idx.NewBatch()
	b.Put(0, []float32{1, 0, 0})
	b.Put(1, []float32{0, 1, 0})
	b.Put(2, []float32{0, 0, 1})
	err := b.Commit()
	requireNoError(t, err)

	// Verify all items are searchable.
	results, err := idx.Search([]float32{1, 0, 0}, 3)
	requireNoError(t, err)
	assert.Len(t, results, 3)

	// The closest to [1,0,0] should be docId=0 (distance ~0).
	assert.Equal(t, int64(0), results[0].DocID)
	assert.InDelta(t, 0.0, results[0].Distance, 1e-6)
}

func TestBatchEmptyNoItems(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store)

	err := idx.NewBatch().Commit() // empty batch is a no-op
	requireNoError(t, err)

	results, err := idx.Search([]float32{1, 0, 0}, 1)
	requireNoError(t, err)
	assert.Empty(t, results)
}

func TestBatchSinglePut(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithRand(rand.New(rand.NewSource(42))))

	b := idx.NewBatch()
	b.Put(42, []float32{1, 2, 3})
	requireNoError(t, b.Commit())

	results, err := idx.Search([]float32{1, 2, 3}, 1)
	requireNoError(t, err)
	requireLen(t, results, 1)
	assert.InDelta(t, 0.0, results[0].Distance, 1e-6)
}

// --- MemNodeStore SetNorm / GetNorm tests ---

func TestMemNodeStoreSetNorm(t *testing.T) {
	store := NewMemNodeStore()

	// SetNorm for a node that has been created via PutNode (PutNode computes a norm automatically).
	requireNoError(t, store.PutNode(1, 0, []float32{3, 4}, 10))

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
	store := NewMemNodeStore(DotProduct)
	requireNoError(t, store.PutNode(1, 0, []float32{1.5, 2.5, 3.5}, 100))

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

// --- Batch with WithM/WithEfConstruction/WithEfSearch ---

func TestBatchWithCustomOptions(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store,
		WithM(4),
		WithEfConstruction(50),
		WithEfSearch(32),
		WithRand(rand.New(rand.NewSource(42))),
	)

	b := idx.NewBatch()
	for i := 0; i < 20; i++ {
		v := make([]float32, 8)
		for j := range v {
			v[j] = float32(i*8+j) + 0.1
		}
		b.Put(int64(i), v)
	}
	requireNoError(t, b.Commit())

	// Verify search works with custom efSearch.
	v0 := make([]float32, 8)
	for j := range v0 {
		v0[j] = float32(0*8+j) + 0.1
	}
	results, err := idx.Search(v0, 5)
	requireNoError(t, err)
	assert.NotEmpty(t, results)
	assert.Equal(t, int64(0), results[0].DocID, "closest result should be docId=0")

	// Verify neighbor limits with custom M=4.
	for nodeId := uint64(1); nodeId <= 20; nodeId++ {
		nb0, err := store.GetNeighbors(nodeId, 0)
		requireNoError(t, err)
		assert.LessOrEqual(t, len(nb0), idx.Mmax0, "layer 0 neighbors should be <= Mmax0")
	}
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

// --- HNSW with MemNodeStore full workflow ---

func TestHNSWWithMemStoreInsertAndSearch(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithRand(rand.New(rand.NewSource(42))))

	requireNoError(t, idx.Insert(0, []float32{1, 0, 0}))
	requireNoError(t, idx.Insert(1, []float32{0, 1, 0}))
	requireNoError(t, idx.Insert(2, []float32{0, 0, 1}))

	results, err := idx.Search([]float32{1, 0, 0}, 3)
	requireNoError(t, err)
	assert.Len(t, results, 3)
	assert.Equal(t, int64(0), results[0].DocID)
}

// requireNoError fails the test immediately if err is non-nil.
func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMemNodeStoreTxnNoOps(t *testing.T) {
	var ns NodeStore = NewMemNodeStore()
	if err := ns.txnBegin(); err != nil {
		t.Fatalf("txnBegin: %v", err)
	}
	if err := ns.txnCommit(); err != nil {
		t.Fatalf("txnCommit: %v", err)
	}
	cause := fmt.Errorf("boom")
	if err := ns.txnAbort(cause); err != cause {
		t.Fatalf("txnAbort must return its cause, got %v", err)
	}
}
