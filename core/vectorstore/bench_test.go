package vectorstore

import (
	"math/rand"
	"strconv"
	"testing"

	"github.com/codetrek/haystack/core/kv/pebblekv"
)

// Benchmarks for the vectorstore at a small scale (128-dim, 10k vectors, Cosine).
// Run: go test ./vectorstore/ -run x -bench 'Benchmark(Put|Build|Search)' -benchmem
//
// Default index HNSW params: M=16, efConstruction=200, efSearch=64 (hnsw.go).

const (
	benchDim = 128
	benchN   = 10000
)

func benchOpenStore(b *testing.B, m Metric) *Store {
	b.Helper()
	kvs, err := pebblekv.Open(b.TempDir(), 16<<20)
	if err != nil {
		b.Fatal(err)
	}
	s, err := Open(Options{Dir: b.TempDir(), KV: kvs, Metric: m})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close(); _ = kvs.Close() })
	return s
}

// benchVecs returns n random dim-d vectors in [-1,1] (deterministic per seed).
func benchVecs(n, dim int, seed int64) [][]float32 {
	rng := rand.New(rand.NewSource(seed))
	out := make([][]float32, n)
	for i := range out {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()*2 - 1
		}
		out[i] = v
	}
	return out
}

// BenchmarkPut_128 measures per-vector insert cost into the brute head (idtable
// alloc + head-bucket commit). vec/s = sustained insert throughput.
func BenchmarkPut_128(b *testing.B) {
	s := benchOpenStore(b, Cosine)
	pool := benchVecs(benchN, benchDim, 1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.Put("d-"+strconv.Itoa(i), pool[i%len(pool)], nil); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "vec/s")
}

// BenchmarkBuild_128_10k measures Seal + per-segment HNSW build of a 10k head
// (insert excluded from the timer). ns/op = wall time to build one 10k graph.
func BenchmarkBuild_128_10k(b *testing.B) {
	pool := benchVecs(benchN, benchDim, 2)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		s := benchOpenStore(b, Cosine)
		for j, v := range pool {
			if err := s.Put("d-"+strconv.Itoa(j), v, nil); err != nil {
				b.Fatal(err)
			}
		}
		b.StartTimer()
		if err := s.Seal(); err != nil {
			b.Fatal(err)
		}
		if err := s.WaitForIndex(); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(benchN)*float64(b.N)/b.Elapsed().Seconds(), "vec/s")
}

// BenchmarkSearch_128_10k measures HNSW query latency over a sealed+indexed 10k
// segment, and reports recall@10 vs an exact brute oracle. qps = 1/latency.
func BenchmarkSearch_128_10k(b *testing.B) {
	s := benchOpenStore(b, Cosine)
	pool := benchVecs(benchN, benchDim, 3)
	vecs := make(map[int64][]float32, len(pool))
	for j, v := range pool {
		id := "d-" + strconv.Itoa(j)
		if err := s.Put(id, v, nil); err != nil {
			b.Fatal(err)
		}
		vecs[s.idToDoc[id]] = v
	}
	if err := s.Seal(); err != nil {
		b.Fatal(err)
	}
	if err := s.WaitForIndex(); err != nil {
		b.Fatal(err)
	}

	const k = 10
	queries := benchVecs(1000, benchDim, 99)
	var recallSum float64
	for _, q := range queries {
		got, err := s.Search("default", q, k, nil)
		if err != nil {
			b.Fatal(err)
		}
		recallSum += recallAtK(got, bruteForceKNN(Cosine, q, vecs, k))
	}
	recall := recallSum / float64(len(queries))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Search("default", queries[i%len(queries)], k, nil); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(recall, "recall@10")
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "qps")
}

// BenchmarkSearchBrute_128_10k measures the brute (linear-scan) head leg over 10k
// un-sealed vectors — the baseline the HNSW index must beat.
func BenchmarkSearchBrute_128_10k(b *testing.B) {
	s := benchOpenStore(b, Cosine)
	pool := benchVecs(benchN, benchDim, 4)
	for j, v := range pool {
		if err := s.Put("d-"+strconv.Itoa(j), v, nil); err != nil {
			b.Fatal(err)
		}
	}
	// No Seal: all 10k stay in the brute head.
	queries := benchVecs(1000, benchDim, 98)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Search("default", queries[i%len(queries)], 10, nil); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "qps")
}

// BenchmarkBatchPut100_128 measures per-record insert cost at batch size 100 (one
// control.db commit / fsync per 100 records) — the write-throughput lever vs the
// single-Put-per-commit BenchmarkPut_128.
func BenchmarkBatchPut100_128(b *testing.B) {
	s := benchOpenStore(b, Cosine)
	pool := benchVecs(benchN, benchDim, 1)
	const batchSize = 100
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i += batchSize {
		bt := s.NewBatch()
		for j := 0; j < batchSize && i+j < b.N; j++ {
			bt.Put("d-"+strconv.Itoa(i+j), pool[(i+j)%len(pool)], nil)
		}
		if err := bt.Commit(); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "vec/s")
}
