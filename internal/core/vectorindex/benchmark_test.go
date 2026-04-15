package vectorindex

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
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
// backed by PebbleNodeStore (disk-backed), and measures search latency and
// recall against brute-force ground truth.
//
// PebbleNodeStore is used instead of MemNodeStore because 100K 384-d vectors
// exceed 8GB RAM with the in-memory store. Pebble keeps memory bounded.
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

	// Build HNSW index with PebbleNodeStore (disk-backed).
	db := openTestDB(t)
	defer db.Close()
	store := NewPebbleNodeStore(db, 1)
	idx := NewHNSWIndex(store, CosineDistance,
		WithCosineDistance(),
		WithEfConstruction(200),
		WithEfSearch(128),
	)

	t.Log("Inserting vectors into HNSW index (Pebble-backed)...")
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
	t.Logf("Index built in %v (Pebble insert is slower than in-memory but uses bounded RAM)", insertElapsed)

	// Search and measure latency.
	t.Log("Running search queries...")
	latencies := make([]time.Duration, len(queries))
	recalls := make([]float64, len(queries))

	for qi, q := range queries {
		start := time.Now()
		results, err := idx.Search(q, benchmarkK)
		latencies[qi] = time.Since(start)

		if err != nil {
			t.Fatalf("search query %d: %v", qi, err)
		}

		// Compute recall@10: fraction of ground-truth neighbors found.
		// Node IDs are 1-indexed (NextNodeId starts at 1), so vector index i
		// has node ID i+1.
		gtSet := make(map[uint64]bool, benchmarkK)
		for _, idx := range groundTruth[qi] {
			gtSet[uint64(idx)+1] = true // convert vector index to node ID
		}
		hits := 0
		for _, r := range results {
			if gtSet[r.ID] {
				hits++
			}
		}
		recalls[qi] = float64(hits) / float64(benchmarkK)
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
// generated vectors. No fixture files needed — vectors are produced inline
// from a fixed seed. Useful as a fast sanity check for search quality.
//
//	go test ./internal/core/vectorindex/ -run TestBenchmark10K -v
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
	idx := NewHNSWIndex(store, CosineDistance,
		WithCosineDistance(),
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
	latencies := make([]time.Duration, numQueries)
	recalls := make([]float64, numQueries)

	for qi, q := range queries {
		start := time.Now()
		results, err := idx.Search(q, k)
		latencies[qi] = time.Since(start)
		if err != nil {
			t.Fatalf("search query %d: %v", qi, err)
		}

		// Node IDs are 1-indexed, so vector index i has node ID i+1.
		gtSet := make(map[uint64]bool, k)
		for _, idx := range groundTruth[qi] {
			gtSet[uint64(idx)+1] = true
		}
		hits := 0
		for _, r := range results {
			if gtSet[r.ID] {
				hits++
			}
		}
		recalls[qi] = float64(hits) / float64(k)
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
	idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(), WithRand(rand.New(rand.NewSource(99))))

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
	idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(), WithRand(rand.New(rand.NewSource(99))))

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

func openBenchDB(b *testing.B) *pebble.DB {
	dir := b.TempDir()
	db, err := pebble.Open(filepath.Join(dir, "bench.db"), &pebble.Options{})
	if err != nil {
		b.Fatal(err)
	}
	return db
}

// BenchmarkHNSWInsertPebble measures the throughput of inserting 128-dim vectors
// into an HNSW index backed by PebbleNodeStore (one-at-a-time).
func BenchmarkHNSWInsertPebble(b *testing.B) {
	const dim = 128
	rng := rand.New(rand.NewSource(42))
	vecs := randomVectors(rng, b.N, dim)

	db := openBenchDB(b)
	defer db.Close()
	store := NewPebbleNodeStore(db, 1)
	idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(), WithRand(rand.New(rand.NewSource(99))))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := idx.Insert(fmt.Sprintf("%d", i), vecs[i]); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkHNSWInsertBatchPebble measures the throughput of batched insertion
// of 128-dim vectors into an HNSW index backed by PebbleNodeStore.
func BenchmarkHNSWInsertBatchPebble(b *testing.B) {
	const dim = 128
	rng := rand.New(rand.NewSource(42))
	vecs := randomVectors(rng, b.N, dim)

	db := openBenchDB(b)
	defer db.Close()
	store := NewPebbleNodeStore(db, 1)
	idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(), WithRand(rand.New(rand.NewSource(99))))

	items := make([]InsertItem, b.N)
	for i := range items {
		items[i] = InsertItem{DocId: fmt.Sprintf("%d", i), Vector: vecs[i]}
	}

	b.ResetTimer()
	if err := idx.InsertBatch(items); err != nil {
		b.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Recall@10 test with 1000 vectors
// ---------------------------------------------------------------------------

// TestRecallAt10_1000Vectors inserts 1000 random 128-dim vectors, then searches
// each vector and computes recall@10 using brute-force ground truth.
// Mean recall@10 must be >= 0.95.
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
	idx := NewHNSWIndex(store, CosineDistance,
		WithCosineDistance(),
		WithEfConstruction(200),
		WithEfSearch(128),
		WithRand(rand.New(rand.NewSource(seed))),
	)

	for i, v := range vecs {
		if err := idx.Insert(fmt.Sprintf("%d", i), v); err != nil {
			t.Fatalf("insert vector %d: %v", i, err)
		}
	}

	var totalRecall float64
	for i, v := range vecs {
		results, err := idx.Search(v, k)
		if err != nil {
			t.Fatalf("search vector %d: %v", i, err)
		}

		gt := bruteForceKNN(v, vecs, k, CosineDistance)
		gtSet := make(map[uint64]bool, k)
		for _, idx := range gt {
			gtSet[uint64(idx)+1] = true // node IDs are 1-indexed
		}

		hits := 0
		for _, r := range results {
			if gtSet[r.ID] {
				hits++
			}
		}
		totalRecall += float64(hits) / float64(k)
	}

	meanRecall := totalRecall / float64(n)
	t.Logf("Mean recall@%d over %d vectors: %.4f", k, n, meanRecall)

	if meanRecall < 0.95 {
		t.Errorf("mean recall@%d = %.4f, want >= 0.95", k, meanRecall)
	}
}

// ---------------------------------------------------------------------------
// Persistence test
// ---------------------------------------------------------------------------

// TestPersistenceRecall builds an HNSW index on PebbleNodeStore, closes and
// reopens the database, then verifies recall@10 >= 0.95 against brute-force.
func TestPersistenceRecall(t *testing.T) {
	const (
		n    = 200
		dim  = 128
		k    = 10
		seed = 42
	)

	rng := rand.New(rand.NewSource(seed))
	vecs := randomVectors(rng, n, dim)

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "persistence_recall.db")

	// Phase 1: build the index and close the DB.
	db1, err := pebble.Open(dbPath, &pebble.Options{})
	if err != nil {
		t.Fatalf("open pebble (phase 1): %v", err)
	}

	store1 := NewPebbleNodeStore(db1, 1)
	idx1 := NewHNSWIndex(store1, CosineDistance,
		WithEfConstruction(200),
		WithEfSearch(128),
		WithRand(rand.New(rand.NewSource(seed))),
	)

	for i, v := range vecs {
		if err := idx1.Insert(fmt.Sprintf("%d", i), v); err != nil {
			t.Fatalf("insert vector %d: %v", i, err)
		}
	}

	if err := db1.Close(); err != nil {
		t.Fatalf("close pebble (phase 1): %v", err)
	}

	// Phase 2: reopen the DB and build a new HNSWIndex on the same data.
	db2, err := pebble.Open(dbPath, &pebble.Options{})
	if err != nil {
		t.Fatalf("open pebble (phase 2): %v", err)
	}
	defer db2.Close()

	store2 := NewPebbleNodeStore(db2, 1)
	idx2 := NewHNSWIndex(store2, CosineDistance,
		WithCosineDistance(),
		WithEfConstruction(200),
		WithEfSearch(128),
	)

	// Compute recall@10 using brute-force ground truth.
	numQueries := 50
	queryRng := rand.New(rand.NewSource(seed + 1))
	queries := randomVectors(queryRng, numQueries, dim)

	var totalRecall float64
	for qi, q := range queries {
		results, err := idx2.Search(q, k)
		if err != nil {
			t.Fatalf("search query %d: %v", qi, err)
		}

		gt := bruteForceKNN(q, vecs, k, CosineDistance)
		gtSet := make(map[uint64]bool, k)
		for _, idx := range gt {
			gtSet[uint64(idx)+1] = true // node IDs are 1-indexed
		}

		hits := 0
		for _, r := range results {
			if gtSet[r.ID] {
				hits++
			}
		}
		totalRecall += float64(hits) / float64(k)
	}

	meanRecall := totalRecall / float64(numQueries)
	t.Logf("Persistence recall@%d over %d queries: %.4f", k, numQueries, meanRecall)

	if meanRecall < 0.95 {
		t.Errorf("persistence recall@%d = %.4f, want >= 0.95", k, meanRecall)
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
		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(), WithRand(rand.New(rand.NewSource(42))))

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
		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance())

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
		idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(), WithRand(rand.New(rand.NewSource(42))))

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
