//go:build benchmark

package vectorindex

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// SIFT fvecs / ivecs loaders
// ---------------------------------------------------------------------------

const siftDir = "testdata/sift/sift"

// loadSiftFvecs reads a .fvecs file (standard format: each vector prefixed by uint32 dim).
func loadSiftFvecs(path string, maxVecs int) ([][]float32, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	var dim uint32
	if err := binary.Read(f, binary.LittleEndian, &dim); err != nil {
		return nil, 0, fmt.Errorf("read dim: %w", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, 0, err
	}

	recordSize := 4 + int(dim)*4
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	totalVecs := int(info.Size()) / recordSize
	if maxVecs > 0 && totalVecs > maxVecs {
		totalVecs = maxVecs
	}

	buf := make([]byte, recordSize)
	vecs := make([][]float32, totalVecs)
	for i := 0; i < totalVecs; i++ {
		if _, err := f.Read(buf); err != nil {
			return nil, 0, fmt.Errorf("read vec %d: %w", i, err)
		}
		vec := make([]float32, dim)
		for j := 0; j < int(dim); j++ {
			vec[j] = math.Float32frombits(binary.LittleEndian.Uint32(buf[4+j*4:]))
		}
		vecs[i] = vec
	}
	return vecs, int(dim), nil
}

// loadSiftIvecs reads a .ivecs file (standard format: each list prefixed by uint32 count).
func loadSiftIvecs(path string, maxQueries int) ([][]uint32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var k uint32
	if err := binary.Read(f, binary.LittleEndian, &k); err != nil {
		return nil, fmt.Errorf("read k: %w", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}

	recordSize := 4 + int(k)*4
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	totalQueries := int(info.Size()) / recordSize
	if maxQueries > 0 && totalQueries > maxQueries {
		totalQueries = maxQueries
	}

	buf := make([]byte, recordSize)
	result := make([][]uint32, totalQueries)
	for i := 0; i < totalQueries; i++ {
		if _, err := f.Read(buf); err != nil {
			return nil, fmt.Errorf("read query %d: %w", i, err)
		}
		ids := make([]uint32, k)
		for j := 0; j < int(k); j++ {
			ids[j] = binary.LittleEndian.Uint32(buf[4+j*4:])
		}
		result[i] = ids
	}
	return result, nil
}

func siftAvailable() bool {
	_, err := os.Stat(filepath.Join(siftDir, "sift_base.fvecs"))
	return err == nil
}

// ---------------------------------------------------------------------------
// Existing 50K raw store benchmark (unchanged)
// ---------------------------------------------------------------------------

func BenchmarkMmapStore50KInsert(b *testing.B) {
	const (
		numVectors = 50000
		dim        = 128
		m          = 16
	)

	// Pre-generate random vectors.
	rng := rand.New(rand.NewSource(42))
	vectors := make([][]float32, numVectors)
	for i := range vectors {
		vec := make([]float32, dim)
		for j := range vec {
			vec[j] = rng.Float32()
		}
		vectors[i] = vec
	}

	for n := 0; n < b.N; n++ {
		dir := b.TempDir()
		s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: m})
		if err != nil {
			b.Fatal(err)
		}

		start := time.Now()

		s.BeginBatch()
		for i := 0; i < numVectors; i++ {
			id, err := s.NextNodeId()
			if err != nil {
				b.Fatal(err)
			}
			docId := fmt.Sprintf("doc-%d", id)
			if err := s.SetNodeMapping(docId, id); err != nil {
				b.Fatal(err)
			}
			if err := s.PutNode(id, 0, vectors[i]); err != nil {
				b.Fatal(err)
			}

			// Simulate HNSW L0 connections: random M*2 neighbors from already-inserted nodes.
			if i > 0 {
				nbCount := m * 2
				if i < nbCount {
					nbCount = i
				}
				nbs := make([]uint64, nbCount)
				for j := range nbs {
					nbs[j] = uint64(rng.Intn(i))
				}
				if err := s.SetNeighbors(id, 0, nbs); err != nil {
					b.Fatal(err)
				}
			}
		}
		if err := s.CommitBatch(true); err != nil {
			b.Fatal(err)
		}

		elapsed := time.Since(start)
		b.ReportMetric(elapsed.Seconds(), "total_sec")
		b.ReportMetric(float64(numVectors)/elapsed.Seconds(), "inserts/sec")

		// Spot-check correctness: sample 100 random nodes.
		for i := 0; i < 100; i++ {
			idx := uint64(rng.Intn(numVectors))
			got, err := s.GetVector(idx)
			if err != nil {
				b.Fatal(err)
			}
			assert.Equal(b, vectors[idx], got)

			docId := fmt.Sprintf("doc-%d", idx)
			nodeId, ok, err := s.GetNodeId(docId)
			if err != nil {
				b.Fatal(err)
			}
			assert.True(b, ok)
			assert.Equal(b, idx, nodeId)
		}

		if err := s.Close(); err != nil {
			b.Fatal(err)
		}

		// Assert < 90s (the task target).
		if elapsed > 90*time.Second {
			b.Fatalf("50K insert took %v, exceeds 90s target", elapsed)
		}
	}
}

// ---------------------------------------------------------------------------
// Task 3: SIFT-128 50K HNSW E2E Benchmarks
// ---------------------------------------------------------------------------

// BenchmarkHNSW_MemStore_50K_Insert benchmarks HNSW insert with MemStore (baseline).
func BenchmarkHNSW_MemStore_50K_Insert(b *testing.B) {
	if !siftAvailable() {
		b.Skip("SIFT data not available")
	}

	vecs, _, err := loadSiftFvecs(filepath.Join(siftDir, "sift_base.fvecs"), 50000)
	if err != nil {
		b.Fatal(err)
	}

	for n := 0; n < b.N; n++ {
		store := NewMemNodeStore()
		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
			WithRand(rand.New(rand.NewSource(42))))

		start := time.Now()
		for i, v := range vecs {
			if err := idx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
				b.Fatalf("Insert %d: %v", i, err)
			}
		}
		elapsed := time.Since(start)
		b.ReportMetric(elapsed.Seconds(), "total_sec")
		b.ReportMetric(float64(len(vecs))/elapsed.Seconds(), "inserts/sec")
	}
}

// BenchmarkHNSW_MmapStore_50K_Insert benchmarks HNSW insert with MmapStore. Target < 90s.
func BenchmarkHNSW_MmapStore_50K_Insert(b *testing.B) {
	if !siftAvailable() {
		b.Skip("SIFT data not available")
	}

	vecs, dim, err := loadSiftFvecs(filepath.Join(siftDir, "sift_base.fvecs"), 50000)
	if err != nil {
		b.Fatal(err)
	}

	for n := 0; n < b.N; n++ {
		dir := b.TempDir()
		store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
		if err != nil {
			b.Fatal(err)
		}

		start := time.Now()
		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(),
			WithRand(rand.New(rand.NewSource(42))))

		for i, v := range vecs {
			if err := idx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
				b.Fatalf("Insert %d: %v", i, err)
			}
		}

		elapsed := time.Since(start)
		b.ReportMetric(elapsed.Seconds(), "total_sec")
		b.ReportMetric(float64(len(vecs))/elapsed.Seconds(), "inserts/sec")

		if err := store.Close(); err != nil {
			b.Fatal(err)
		}

		if elapsed > 90*time.Second {
			b.Fatalf("50K SIFT insert took %v, exceeds 90s target", elapsed)
		}
	}
}

// BenchmarkHNSW_Search_MemStore_vs_MmapStore benchmarks search latency on 50K SIFT data.
func BenchmarkHNSW_Search_MemStore_vs_MmapStore(b *testing.B) {
	if !siftAvailable() {
		b.Skip("SIFT data not available")
	}

	vecs, dim, err := loadSiftFvecs(filepath.Join(siftDir, "sift_base.fvecs"), 50000)
	if err != nil {
		b.Fatal(err)
	}

	queryVecs, _, err := loadSiftFvecs(filepath.Join(siftDir, "sift_query.fvecs"), 1000)
	if err != nil {
		b.Fatal(err)
	}

	const k = 10
	hnswSeed := int64(42)

	// Build MemStore index.
	memStore := NewMemNodeStore()
	memIdx := NewHNSWIndex(memStore, CosineDistance, WithCosineDistance(),
		WithRand(rand.New(rand.NewSource(hnswSeed))))
	for i, v := range vecs {
		if err := memIdx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
			b.Fatalf("MemStore insert %d: %v", i, err)
		}
	}

	// Build MmapStore index.
	dir := b.TempDir()
	mmapStore, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
	if err != nil {
		b.Fatal(err)
	}
	defer mmapStore.Close()

	mmapIdx := NewHNSWIndex(mmapStore, CosineDistance, WithCosineDistance(),
		WithRand(rand.New(rand.NewSource(hnswSeed))))
	for i, v := range vecs {
		if err := mmapIdx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
			b.Fatalf("MmapStore insert %d: %v", i, err)
		}
	}

	b.Run("MemStore_Search_1000", func(b *testing.B) {
		searchBench(b, memIdx, queryVecs, k)
	})

	b.Run("MmapStore_Search_1000", func(b *testing.B) {
		searchBench(b, mmapIdx, queryVecs, k)
	})
}

func searchBench(b *testing.B, idx *HNSWIndex, queries [][]float32, k int) {
	b.Helper()

	for n := 0; n < b.N; n++ {
		latencies := make([]time.Duration, len(queries))
		for i, q := range queries {
			start := time.Now()
			_, err := idx.Search(q, k)
			latencies[i] = time.Since(start)
			if err != nil {
				b.Fatal(err)
			}
		}

		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		p50 := latencies[len(latencies)/2]
		p99 := latencies[int(float64(len(latencies))*0.99)]

		b.ReportMetric(float64(p50.Microseconds()), "p50_us")
		b.ReportMetric(float64(p99.Microseconds()), "p99_us")

		runtime.GC()
	}
}

// ---------------------------------------------------------------------------
// Task 4: Recall@10 verification (test, not benchmark)
// ---------------------------------------------------------------------------

// TestRecallAt10_SIFT requires SIFT dataset; run locally with -tags benchmark.
func TestRecallAt10_SIFT(t *testing.T) {
	if !siftAvailable() {
		t.Skip("SIFT data not available")
	}

	baseVecs, dim, err := loadSiftFvecs(filepath.Join(siftDir, "sift_base.fvecs"), 50000)
	if err != nil {
		t.Fatal(err)
	}

	queryVecs, _, err := loadSiftFvecs(filepath.Join(siftDir, "sift_query.fvecs"), 100)
	if err != nil {
		t.Fatal(err)
	}

	const k = 10
	hnswSeed := int64(42)

	// Build ground truth via brute force.
	groundTruth := make([][]int, len(queryVecs))
	for i, q := range queryVecs {
		groundTruth[i] = bruteForceKNN(q, baseVecs, k, CosineDistance)
	}

	// Test MemStore recall.
	memStore := NewMemNodeStore()
	memIdx := NewHNSWIndex(memStore, CosineDistance, WithCosineDistance(),
		WithRand(rand.New(rand.NewSource(hnswSeed))))
	for i, v := range baseVecs {
		if err := memIdx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
			t.Fatalf("MemStore insert %d: %v", i, err)
		}
	}

	memMapping := buildNodeToBaseIdxMap(memStore, len(baseVecs))
	var memRecallSum float64
	for i, q := range queryVecs {
		res, err := memIdx.Search(q, k)
		if err != nil {
			t.Fatal(err)
		}
		memRecallSum += recallAtKMapped(groundTruth[i], res, k, memMapping)
	}
	memRecall := memRecallSum / float64(len(queryVecs))
	t.Logf("MemStore recall@10 = %.4f", memRecall)
	assert.Greater(t, memRecall, 0.95, "MemStore recall@10 should be > 0.95")

	// Test MmapStore recall.
	dir := t.TempDir()
	mmapStore, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer mmapStore.Close()

	mmapIdx := NewHNSWIndex(mmapStore, CosineDistance, WithCosineDistance(),
		WithRand(rand.New(rand.NewSource(hnswSeed))))
	for i, v := range baseVecs {
		if err := mmapIdx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
			t.Fatalf("MmapStore insert %d: %v", i, err)
		}
	}

	mmapMapping := buildNodeToBaseIdxMap(mmapStore, len(baseVecs))
	var mmapRecallSum float64
	for i, q := range queryVecs {
		res, err := mmapIdx.Search(q, k)
		if err != nil {
			t.Fatal(err)
		}
		mmapRecallSum += recallAtKMapped(groundTruth[i], res, k, mmapMapping)
	}
	mmapRecall := mmapRecallSum / float64(len(queryVecs))
	t.Logf("MmapStore recall@10 = %.4f", mmapRecall)
	assert.Greater(t, mmapRecall, 0.95, "MmapStore recall@10 should be > 0.95")

	// Recall difference should be < 0.01.
	diff := math.Abs(memRecall - mmapRecall)
	t.Logf("Recall difference = %.4f", diff)
	assert.Less(t, diff, 0.01, "MmapStore vs MemStore recall difference should be < 0.01")
}
