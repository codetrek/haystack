//go:build benchmark

package vectorindex

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

const (
	fixtureDir      = "testdata"
	vectorsFile     = "vectors_100k_384d.bin"
	queriesFile     = "queries_50_384d.bin"
	groundTruthFile = "ground_truth_top10.bin"
	benchmarkK      = 10
	minRecallAt10   = 0.95
	maxP99LatencyMs = 20.0
)

// TestBenchmarkSearchLatency loads pre-generated fixtures, builds an HNSW index
// backed by MemNodeStore, and measures search latency and recall against
// brute-force ground truth.
//
// Run gen-testdata first:
//
//	go run ./cmd/gen-testdata/
//	go test ./internal/core/vectorindex/ -run TestBenchmarkSearchLatency -v
func TestBenchmarkSearchLatency(t *testing.T) {
	vecPath := filepath.Join(fixtureDir, vectorsFile)
	if _, err := os.Stat(vecPath); os.IsNotExist(err) {
		t.Skip("fixture files not found — run: go run ./cmd/gen-testdata/")
	}

	t.Log("Loading fixtures...")
	vectors, err := LoadVectors(vecPath)
	if err != nil {
		t.Fatalf("load vectors: %v", err)
	}
	queries, err := LoadQueries(filepath.Join(fixtureDir, queriesFile))
	if err != nil {
		t.Fatalf("load queries: %v", err)
	}
	groundTruth, err := LoadGroundTruth(filepath.Join(fixtureDir, groundTruthFile))
	if err != nil {
		t.Fatalf("load ground truth: %v", err)
	}

	t.Logf("Loaded %d vectors, %d queries, %d ground truth sets", len(vectors), len(queries), len(groundTruth))

	store := NewMemNodeStore()
	idx := NewHNSWIndex(store,
		WithEfConstruction(200),
		WithEfSearch(128),
	)

	t.Log("Inserting vectors into HNSW index...")
	insertStart := time.Now()
	for i, v := range vectors {
		if err := idx.Insert(fmt.Sprintf("%d", i), v); err != nil {
			t.Fatalf("insert vector %d: %v", i, err)
		}
		if (i+1)%10000 == 0 {
			t.Logf("  inserted %d/%d vectors", i+1, len(vectors))
		}
	}
	insertElapsed := time.Since(insertStart).Round(time.Millisecond)
	t.Logf("Index built in %v", insertElapsed)

	// Search and measure latency.
	t.Log("Running search queries...")
	nodeMapping := buildNodeToBaseIdxMap(store, len(vectors), "%d")
	gtInt := make([][]int, len(groundTruth))
	for i, gt := range groundTruth {
		gtInt[i] = make([]int, len(gt))
		for j, v := range gt {
			gtInt[i][j] = int(v)
		}
	}
	latencies := make([]time.Duration, len(queries))
	recalls := make([]float64, len(queries))

	for qi, q := range queries {
		start := time.Now()
		results, err := idx.Search(q, benchmarkK)
		latencies[qi] = time.Since(start)

		if err != nil {
			t.Fatalf("search query %d: %v", qi, err)
		}

		recalls[qi] = recallAtKMapped(gtInt[qi], results, benchmarkK, nodeMapping)
	}

	// Compute latency percentiles.
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50 := percentile(latencies, 0.50)
	p95 := percentile(latencies, 0.95)
	p99 := percentile(latencies, 0.99)

	// Compute mean recall.
	var totalRecall float64
	for _, r := range recalls {
		totalRecall += r
	}
	meanRecall := totalRecall / float64(len(recalls))

	t.Logf("Search latency (n=%d):", len(queries))
	t.Logf("  p50: %v", p50)
	t.Logf("  p95: %v", p95)
	t.Logf("  p99: %v", p99)
	t.Logf("Recall@%d: %.4f", benchmarkK, meanRecall)

	// Assertions.
	p99Ms := float64(p99.Microseconds()) / 1000.0
	if p99Ms > maxP99LatencyMs {
		t.Errorf("p99 latency %.2fms exceeds threshold %.2fms", p99Ms, maxP99LatencyMs)
	}
	if meanRecall < minRecallAt10 {
		t.Errorf("recall@%d = %.4f, want >= %.4f", benchmarkK, meanRecall, minRecallAt10)
	}
}

// TestBenchmarkSearchLatency10K is a quick in-memory benchmark with 10K
// generated vectors.
func TestBenchmarkSearchLatency10K(t *testing.T) {
	const (
		n          = 10000
		dim        = 128
		k          = 10
		numQueries = 50
		seed       = 12345
	)

	rng := rand.New(rand.NewSource(seed))
	vectors := randomVectors(rng, n, dim)
	queries := randomVectors(rng, numQueries, dim)

	// Compute brute-force ground truth.
	groundTruth := make([][]int, numQueries)
	for qi, q := range queries {
		groundTruth[qi] = bruteForceKNN(q, vectors, k, CosineDistance)
	}

	store := NewMemNodeStore()
	idx := NewHNSWIndex(store,
		WithEfConstruction(200),
		WithEfSearch(128),
	)

	t.Log("Inserting 10K vectors into HNSW index (in-memory)...")
	insertStart := time.Now()
	for i, v := range vectors {
		if err := idx.Insert(fmt.Sprintf("%d", i), v); err != nil {
			t.Fatalf("insert vector %d: %v", i, err)
		}
	}
	t.Logf("Index built in %v", time.Since(insertStart).Round(time.Millisecond))

	// Search and measure latency.
	t.Log("Running search queries...")
	nodeMapping := buildNodeToBaseIdxMap(store, n, "%d")
	latencies := make([]time.Duration, numQueries)
	recalls := make([]float64, numQueries)

	for qi, q := range queries {
		start := time.Now()
		results, err := idx.Search(q, k)
		latencies[qi] = time.Since(start)
		if err != nil {
			t.Fatalf("search query %d: %v", qi, err)
		}

		recalls[qi] = recallAtKMapped(groundTruth[qi], results, k, nodeMapping)
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50 := percentile(latencies, 0.50)
	p95 := percentile(latencies, 0.95)
	p99 := percentile(latencies, 0.99)

	var totalRecall float64
	for _, r := range recalls {
		totalRecall += r
	}
	meanRecall := totalRecall / float64(len(recalls))

	t.Logf("Search latency (n=%d, 10K vectors):", numQueries)
	t.Logf("  p50: %v", p50)
	t.Logf("  p95: %v", p95)
	t.Logf("  p99: %v", p99)
	t.Logf("Recall@%d: %.4f", k, meanRecall)

	if meanRecall < 0.80 {
		t.Errorf("recall@%d = %.4f, want >= 0.80", k, meanRecall)
	}

	if p99 > 5*time.Millisecond {
		t.Errorf("p99 latency = %v, want <= 5ms", p99)
	}
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// ---------------------------------------------------------------------------
// Go benchmark functions (testing.B)
// ---------------------------------------------------------------------------

// BenchmarkHNSWInsert measures the throughput of inserting 128-dim vectors into
// an HNSW index backed by MemNodeStore.
func BenchmarkHNSWInsert(b *testing.B) {
	const dim = 128
	rng := rand.New(rand.NewSource(42))

	// Pre-generate enough vectors for the benchmark.
	vecs := randomVectors(rng, b.N, dim)

	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithRand(rand.New(rand.NewSource(99))))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := idx.Insert(fmt.Sprintf("%d", i), vecs[i]); err != nil {
			b.Fatalf("insert %d: %v", i, err)
		}
	}
}

// BenchmarkHNSWSearch measures the latency of searching top-10 on a
// pre-built 1000-vector HNSW index backed by MemNodeStore.
func BenchmarkHNSWSearch(b *testing.B) {
	const (
		n   = 1000
		dim = 128
		k   = 10
	)

	rng := rand.New(rand.NewSource(42))
	vecs := randomVectors(rng, n, dim)

	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithRand(rand.New(rand.NewSource(99))))

	for i, v := range vecs {
		if err := idx.Insert(fmt.Sprintf("%d", i), v); err != nil {
			b.Fatalf("insert %d: %v", i, err)
		}
	}

	// Pre-generate query vectors for the benchmark.
	queries := randomVectors(rng, b.N, dim)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := idx.Search(queries[i], k); err != nil {
			b.Fatalf("search %d: %v", i, err)
		}
	}
}

// BenchmarkHNSWBatchInsert measures per-item throughput of inserting b.N items
// through a single NewBatch + Commit (one transaction, one fsync). It is the
// batch-throughput counterpart to BenchmarkHNSWInsert (one txn per item).
func BenchmarkHNSWBatchInsert(b *testing.B) {
	const dim = 128
	rng := rand.New(rand.NewSource(42))
	vecs := randomVectors(rng, b.N, dim)

	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithRand(rand.New(rand.NewSource(99))))

	b.ResetTimer()
	batch := idx.NewBatch()
	for i := 0; i < b.N; i++ {
		batch.Put(fmt.Sprintf("%d", i), vecs[i])
	}
	if err := batch.Commit(); err != nil {
		b.Fatalf("commit: %v", err)
	}
}

// BenchmarkHNSWDelete measures the latency of deleting existing nodes. The
// index is pre-built with b.N nodes (untimed); the timed loop deletes each.
func BenchmarkHNSWDelete(b *testing.B) {
	const dim = 128
	rng := rand.New(rand.NewSource(42))
	vecs := randomVectors(rng, b.N, dim)

	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithRand(rand.New(rand.NewSource(99))))
	for i := 0; i < b.N; i++ {
		if err := idx.Insert(fmt.Sprintf("%d", i), vecs[i]); err != nil {
			b.Fatalf("setup insert %d: %v", i, err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := idx.Delete(fmt.Sprintf("%d", i)); err != nil {
			b.Fatalf("delete %d: %v", i, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Recall@10 test with 1000 vectors
// ---------------------------------------------------------------------------

func TestRecallAt10_1000Vectors(t *testing.T) {
	const (
		n    = 1000
		dim  = 128
		k    = 10
		seed = 42
	)

	rng := rand.New(rand.NewSource(seed))
	vecs := randomVectors(rng, n, dim)

	store := NewMemNodeStore()
	idx := NewHNSWIndex(store,
		WithEfConstruction(200),
		WithEfSearch(128),
		WithRand(rand.New(rand.NewSource(seed))),
	)

	for i, v := range vecs {
		if err := idx.Insert(fmt.Sprintf("%d", i), v); err != nil {
			t.Fatalf("insert vector %d: %v", i, err)
		}
	}

	nodeMapping := buildNodeToBaseIdxMap(store, n, "%d")
	var totalRecall float64
	for i, v := range vecs {
		results, err := idx.Search(v, k)
		if err != nil {
			t.Fatalf("search vector %d: %v", i, err)
		}

		gt := bruteForceKNN(v, vecs, k, CosineDistance)
		totalRecall += recallAtKMapped(gt, results, k, nodeMapping)
	}

	meanRecall := totalRecall / float64(n)
	t.Logf("Mean recall@%d over %d vectors: %.4f", k, n, meanRecall)

	if meanRecall < 0.95 {
		t.Errorf("mean recall@%d = %.4f, want >= 0.95", k, meanRecall)
	}
}

// ---------------------------------------------------------------------------
// Edge case tests
// ---------------------------------------------------------------------------

func TestEdgeCases(t *testing.T) {
	t.Run("k_greater_than_graph_size", func(t *testing.T) {
		rng := rand.New(rand.NewSource(42))
		dim := 128
		n := 5

		vecs := randomVectors(rng, n, dim)

		store := NewMemNodeStore()
		idx := NewHNSWIndex(store, WithRand(rand.New(rand.NewSource(42))))

		for i, v := range vecs {
			if err := idx.Insert(fmt.Sprintf("%d", i), v); err != nil {
				t.Fatalf("insert %d: %v", i, err)
			}
		}

		// Search with k=100 on a 5-vector graph: should not panic.
		results, err := idx.Search(vecs[0], 100)
		assert.NoError(t, err)
		assert.Len(t, results, n, "should return all %d vectors when k > graph size", n)
	})

	t.Run("empty_graph_search", func(t *testing.T) {
		store := NewMemNodeStore()
		idx := NewHNSWIndex(store)

		query := make([]float32, 128)
		for i := range query {
			query[i] = float32(i)
		}

		// Search on empty graph: should not panic, returns empty or error.
		results, err := idx.Search(query, 10)
		if err != nil {
			// An error is acceptable for empty graph search.
			return
		}
		assert.Empty(t, results, "expected empty results for empty graph")
	})

	t.Run("delete_then_search", func(t *testing.T) {
		rng := rand.New(rand.NewSource(42))
		dim := 128
		n := 10
		nDelete := 5

		vecs := randomVectors(rng, n, dim)

		store := NewMemNodeStore()
		idx := NewHNSWIndex(store, WithRand(rand.New(rand.NewSource(42))))

		for i, v := range vecs {
			if err := idx.Insert(fmt.Sprintf("%d", i), v); err != nil {
				t.Fatalf("insert %d: %v", i, err)
			}
		}

		// Delete first 5 vectors.
		for i := 0; i < nDelete; i++ {
			if err := idx.Delete(fmt.Sprintf("%d", i)); err != nil {
				t.Fatalf("delete %d: %v", i, err)
			}
		}

		// Search should not panic after deletions.
		results, err := idx.Search(vecs[0], 10)
		assert.NoError(t, err)

		// Should only return non-deleted vectors.
		deletedIDs := make(map[uint64]bool, nDelete)
		for i := 0; i < nDelete; i++ {
			deletedIDs[uint64(i+1)] = true // node IDs are 1-indexed
		}
		for _, r := range results {
			assert.False(t, deletedIDs[r.ID],
				"search returned deleted node %d", r.ID)
		}
	})
}

// TestBenchmarkParametric runs benchmarks at different scales and efSearch values.
func TestBenchmarkParametric(t *testing.T) {
	scales := []int{1000, 5000, 10000, 20000, 40000}
	efSearchValues := []int{64, 128, 200, 400}
	dim := 128
	k := 10

	rng := rand.New(rand.NewSource(42))

	for _, n := range scales {
		vectors := make([][]float32, n)
		for i := range vectors {
			v := make([]float32, dim)
			for j := range v {
				v[j] = rng.Float32()*2 - 1
			}
			vectors[i] = v
		}

		nQueries := 50
		queries := make([][]float32, nQueries)
		for i := range queries {
			q := make([]float32, dim)
			for j := range q {
				q[j] = rng.Float32()*2 - 1
			}
			queries[i] = q
		}

		groundTruth := make([][]int, nQueries)
		for qi, q := range queries {
			type distIdx struct {
				dist float32
				idx  int
			}
			dists := make([]distIdx, n)
			for vi, v := range vectors {
				dists[vi] = distIdx{CosineDistance(q, v), vi}
			}
			sort.Slice(dists, func(a, b int) bool { return dists[a].dist < dists[b].dist })
			gt := make([]int, k)
			for i := 0; i < k && i < len(dists); i++ {
				gt[i] = dists[i].idx
			}
			groundTruth[qi] = gt
		}

		store := NewMemNodeStore()
		idx := NewHNSWIndex(store)

		t.Run(fmt.Sprintf("N=%d/insert", n), func(t *testing.T) {
			start := time.Now()
			for i, v := range vectors {
				err := idx.Insert(fmt.Sprintf("%d", i), v)
				if err != nil {
					t.Fatalf("insert %d: %v", i, err)
				}
			}
			elapsed := time.Since(start)
			t.Logf("Insert %d vectors: %v (%.2fms/op)", n, elapsed, float64(elapsed.Milliseconds())/float64(n))
		})

		nodeMapping := buildNodeToBaseIdxMap(store, n, "%d")

		for _, ef := range efSearchValues {
			t.Run(fmt.Sprintf("N=%d/efSearch=%d", n, ef), func(t *testing.T) {
				idx.mu.Lock()
				idx.efSearch = ef
				idx.mu.Unlock()

				latencies := make([]time.Duration, len(queries))
				recalls := make([]float64, len(queries))

				for qi, q := range queries {
					start := time.Now()
					results, err := idx.Search(q, k)
					latencies[qi] = time.Since(start)
					if err != nil {
						t.Fatalf("search %d: %v", qi, err)
					}

					recalls[qi] = recallAtKMapped(groundTruth[qi], results, k, nodeMapping)
				}

				sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
				p50 := percentile(latencies, 0.50)
				p95 := percentile(latencies, 0.95)
				p99 := percentile(latencies, 0.99)

				var totalRecall float64
				for _, r := range recalls {
					totalRecall += r
				}
				meanRecall := totalRecall / float64(len(recalls))

				t.Logf("N=%d efSearch=%d: p50=%v p95=%v p99=%v recall@%d=%.4f",
					n, ef, p50, p95, p99, k, meanRecall)
			})
		}
	}
}

// TestBenchmarkEfConstructionCompare compares different efConstruction values at 40K scale.
func TestBenchmarkEfConstructionCompare(t *testing.T) {
	n := 40000
	dim := 128
	k := 10
	efSearchValues := []int{200, 400}
	efConstructionValues := []int{128, 200, 256}

	rng := rand.New(rand.NewSource(42))

	vectors := make([][]float32, n)
	for i := range vectors {
		v := make([]float32, dim)
		for j := range v {
			v[j] = rng.Float32()*2 - 1
		}
		vectors[i] = v
	}

	nQueries := 50
	queries := make([][]float32, nQueries)
	for i := range queries {
		q := make([]float32, dim)
		for j := range q {
			q[j] = rng.Float32()*2 - 1
		}
		queries[i] = q
	}

	groundTruth := make([][]int, nQueries)
	for qi, q := range queries {
		type distIdx struct {
			dist float32
			idx  int
		}
		dists := make([]distIdx, n)
		for vi, v := range vectors {
			dists[vi] = distIdx{CosineDistance(q, v), vi}
		}
		sort.Slice(dists, func(a, b int) bool { return dists[a].dist < dists[b].dist })
		gt := make([]int, k)
		for i := 0; i < k && i < len(dists); i++ {
			gt[i] = dists[i].idx
		}
		groundTruth[qi] = gt
	}

	for _, efc := range efConstructionValues {
		store := NewMemNodeStore()
		idx := NewHNSWIndex(store, WithEfConstruction(efc))

		t.Run(fmt.Sprintf("efC=%d/insert", efc), func(t *testing.T) {
			start := time.Now()
			for i, v := range vectors {
				if err := idx.Insert(fmt.Sprintf("%d", i), v); err != nil {
					t.Fatalf("insert %d: %v", i, err)
				}
			}
			elapsed := time.Since(start)
			t.Logf("efConstruction=%d insert 40K: %v (%.2fms/op)", efc, elapsed, float64(elapsed.Milliseconds())/float64(n))
		})

		nodeMapping := buildNodeToBaseIdxMap(store, n, "%d")

		for _, efs := range efSearchValues {
			t.Run(fmt.Sprintf("efC=%d/efS=%d", efc, efs), func(t *testing.T) {
				idx.mu.Lock()
				idx.efSearch = efs
				idx.mu.Unlock()

				latencies := make([]time.Duration, len(queries))
				recalls := make([]float64, len(queries))

				for qi, q := range queries {
					start := time.Now()
					results, err := idx.Search(q, k)
					latencies[qi] = time.Since(start)
					if err != nil {
						t.Fatalf("search %d: %v", qi, err)
					}

					recalls[qi] = recallAtKMapped(groundTruth[qi], results, k, nodeMapping)
				}

				sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
				p50 := percentile(latencies, 0.50)
				p99 := percentile(latencies, 0.99)

				var totalRecall float64
				for _, r := range recalls {
					totalRecall += r
				}
				meanRecall := totalRecall / float64(len(recalls))

				t.Logf("efC=%d efS=%d: p50=%v p99=%v recall@10=%.4f", efc, efs, p50, p99, meanRecall)
			})
		}
	}
}

// TestBenchmarkMCompare compares M=16 vs M=32 at 40K scale.
func TestBenchmarkMCompare(t *testing.T) {
	n := 40000
	dim := 128
	k := 10
	efSearchValues := []int{200, 400, 800}
	mValues := []int{16, 32}

	rng := rand.New(rand.NewSource(42))

	vectors := make([][]float32, n)
	for i := range vectors {
		v := make([]float32, dim)
		for j := range v {
			v[j] = rng.Float32()*2 - 1
		}
		vectors[i] = v
	}

	nQueries := 50
	queries := make([][]float32, nQueries)
	for i := range queries {
		q := make([]float32, dim)
		for j := range q {
			q[j] = rng.Float32()*2 - 1
		}
		queries[i] = q
	}

	groundTruth := make([][]int, nQueries)
	for qi, q := range queries {
		type distIdx struct {
			dist float32
			idx  int
		}
		dists := make([]distIdx, n)
		for vi, v := range vectors {
			dists[vi] = distIdx{CosineDistance(q, v), vi}
		}
		sort.Slice(dists, func(a, b int) bool { return dists[a].dist < dists[b].dist })
		gt := make([]int, k)
		for i := 0; i < k && i < len(dists); i++ {
			gt[i] = dists[i].idx
		}
		groundTruth[qi] = gt
	}

	for _, m := range mValues {
		store := NewMemNodeStore()
		idx := NewHNSWIndex(store, WithM(m))

		t.Run(fmt.Sprintf("M=%d/insert", m), func(t *testing.T) {
			start := time.Now()
			for i, v := range vectors {
				if err := idx.Insert(fmt.Sprintf("%d", i), v); err != nil {
					t.Fatalf("insert %d: %v", i, err)
				}
			}
			elapsed := time.Since(start)
			t.Logf("M=%d insert 40K: %v (%.2fms/op)", m, elapsed, float64(elapsed.Milliseconds())/float64(n))
		})

		nodeMapping := buildNodeToBaseIdxMap(store, n, "%d")

		for _, efs := range efSearchValues {
			t.Run(fmt.Sprintf("M=%d/efS=%d", m, efs), func(t *testing.T) {
				idx.mu.Lock()
				idx.efSearch = efs
				idx.mu.Unlock()

				latencies := make([]time.Duration, len(queries))
				recalls := make([]float64, len(queries))

				for qi, q := range queries {
					start := time.Now()
					results, err := idx.Search(q, k)
					latencies[qi] = time.Since(start)
					if err != nil {
						t.Fatalf("search %d: %v", qi, err)
					}

					recalls[qi] = recallAtKMapped(groundTruth[qi], results, k, nodeMapping)
				}

				sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
				p50 := percentile(latencies, 0.50)
				p99 := percentile(latencies, 0.99)

				var totalRecall float64
				for _, r := range recalls {
					totalRecall += r
				}
				meanRecall := totalRecall / float64(len(recalls))

				t.Logf("M=%d efS=%d: p50=%v p99=%v recall@10=%.4f", m, efs, p50, p99, meanRecall)
			})
		}
	}
}

// loadFvecs reads a .fvecs file: each record is [dim(int32), vec(dim × float32)].
func loadFvecs(path string, limit int) ([][]float32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var vectors [][]float32
	for len(vectors) < limit {
		var dim int32
		if err := binary.Read(f, binary.LittleEndian, &dim); err != nil {
			break
		}
		vec := make([]float32, dim)
		if err := binary.Read(f, binary.LittleEndian, &vec); err != nil {
			return nil, fmt.Errorf("read vector %d: %v", len(vectors), err)
		}
		vectors = append(vectors, vec)
	}
	return vectors, nil
}

// loadIvecs reads a .ivecs file (ground truth): each record is [k(int32), ids(k × int32)].
func loadIvecs(path string, limit int) ([][]int32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var results [][]int32
	for len(results) < limit {
		var k int32
		if err := binary.Read(f, binary.LittleEndian, &k); err != nil {
			break
		}
		ids := make([]int32, k)
		if err := binary.Read(f, binary.LittleEndian, &ids); err != nil {
			return nil, fmt.Errorf("read result %d: %v", len(results), err)
		}
		results = append(results, ids)
	}
	return results, nil
}

// TestBenchmarkSIFT runs HNSW benchmark on real SIFT-128 data.
func TestBenchmarkSIFT(t *testing.T) {
	siftDir := "testdata/sift/sift"
	basePath := filepath.Join(siftDir, "sift_base.fvecs")
	queryPath := filepath.Join(siftDir, "sift_query.fvecs")

	if _, err := os.Stat(basePath); os.IsNotExist(err) {
		t.Skip("SIFT dataset not found — download from ftp://ftp.irisa.fr/local/texmex/corpus/sift.tar.gz")
	}

	nBase := 100000
	t.Logf("Loading %d SIFT base vectors...", nBase)
	base, err := loadFvecs(basePath, nBase)
	if err != nil {
		t.Fatalf("load base: %v", err)
	}
	t.Logf("Loaded %d vectors, dim=%d", len(base), len(base[0]))

	queries, err := loadFvecs(queryPath, 100)
	if err != nil {
		t.Fatalf("load queries: %v", err)
	}
	t.Logf("Loaded %d queries", len(queries))

	k := 10
	t.Logf("Computing brute-force ground truth for %d queries over %d vectors...", len(queries), nBase)
	gt := make([][]int, len(queries))
	for qi, q := range queries {
		type distIdx struct {
			dist float32
			idx  int
		}
		dists := make([]distIdx, len(base))
		for vi, v := range base {
			dists[vi] = distIdx{EuclideanDistance(q, v), vi}
		}
		sort.Slice(dists, func(a, b int) bool { return dists[a].dist < dists[b].dist })
		gtk := make([]int, k)
		for i := 0; i < k; i++ {
			gtk[i] = dists[i].idx
		}
		gt[qi] = gtk
	}
	t.Logf("Ground truth computed")
	efSearchValues := []int{128, 200, 400}

	store := NewMemNodeStore(Euclidean)
	idx := NewHNSWIndex(store)

	t.Run("insert", func(t *testing.T) {
		start := time.Now()
		for i, v := range base {
			if err := idx.Insert(fmt.Sprintf("%d", i), v); err != nil {
				t.Fatalf("insert %d: %v", i, err)
			}
			if (i+1)%10000 == 0 {
				t.Logf("  inserted %d/%d", i+1, nBase)
			}
		}
		elapsed := time.Since(start)
		t.Logf("Insert %d SIFT vectors: %v (%.2fms/op)", nBase, elapsed, float64(elapsed.Milliseconds())/float64(nBase))
	})

	nodeMapping := buildNodeToBaseIdxMap(store, nBase, "%d")

	for _, efs := range efSearchValues {
		t.Run(fmt.Sprintf("efSearch=%d", efs), func(t *testing.T) {
			idx.mu.Lock()
			idx.efSearch = efs
			idx.mu.Unlock()

			latencies := make([]time.Duration, len(queries))
			recalls := make([]float64, len(queries))

			for qi, q := range queries {
				start := time.Now()
				results, err := idx.Search(q, k)
				latencies[qi] = time.Since(start)
				if err != nil {
					t.Fatalf("search %d: %v", qi, err)
				}

				recalls[qi] = recallAtKMapped(gt[qi], results, k, nodeMapping)
			}

			sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
			p50 := percentile(latencies, 0.50)
			p95 := percentile(latencies, 0.95)
			p99 := percentile(latencies, 0.99)

			var totalRecall float64
			for _, r := range recalls {
				totalRecall += r
			}
			meanRecall := totalRecall / float64(len(recalls))

			t.Logf("SIFT 100K efSearch=%d: p50=%v p95=%v p99=%v recall@%d=%.4f",
				efs, p50, p95, p99, k, meanRecall)

			p99Ms := float64(p99.Microseconds()) / 1000.0
			assert.Less(t, p99Ms, 5.0, "p99 search latency should be < 5ms")
		})
	}
}

// BenchmarkMmapStoreGetVector measures mmap read latency.
func BenchmarkMmapStoreGetVector(b *testing.B) {
	store := NewMemNodeStore()
	for i := 0; i < 1000; i++ {
		v := make([]float32, 128)
		for j := range v {
			v[j] = float32(i*128 + j)
		}
		store.PutNode(uint64(i), 0, v)
		store.SetNeighbors(uint64(i), 0, nil)
		store.SetNodeMapping(fmt.Sprintf("doc%d", i), uint64(i))
	}

	dir := b.TempDir()
	ms, err := exportMemStoreToMmap(store, dir, 128, 16)
	if err != nil {
		b.Fatalf("export: %v", err)
	}
	defer ms.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := ms.GetVector(uint64(i % 1000))
		if err != nil {
			b.Fatal(err)
		}
	}
}
