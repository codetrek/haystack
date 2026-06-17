//go:build benchmark

package vectorindex

import (
	"math/rand"
	"sort"
	"testing"
)

// benchVecsVS replicates vectorstore's benchVecs EXACTLY (same RNG source + range)
// so the recall measured here is directly comparable to vectorstore's
// BenchmarkSearch_128_10k (same vectors, same queries, same params, same metric).
func benchVecsVS(n, dim int, seed int64) [][]float32 {
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

func bruteCosineTopK(q []float32, vecs [][]float32, k int) []int64 {
	type hit struct {
		id int64
		d  float32
	}
	hits := make([]hit, len(vecs))
	for i, v := range vecs {
		hits[i] = hit{int64(i), CosineDistance(q, v)}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].d != hits[j].d {
			return hits[i].d < hits[j].d
		}
		return hits[i].id < hits[j].id
	})
	if k > len(hits) {
		k = len(hits)
	}
	out := make([]int64, k)
	for i := 0; i < k; i++ {
		out[i] = hits[i].id
	}
	return out
}

func recallOverlap(got []SearchResult, want []int64) float64 {
	w := make(map[int64]bool, len(want))
	for _, id := range want {
		w[id] = true
	}
	hit := 0
	for _, r := range got {
		if w[r.DocID] {
			hit++
		}
	}
	return float64(hit) / float64(len(want))
}

// BenchmarkHNSWRecall_128_10k builds a 10k/128 cosine HNSW (M=16, efC=200, efS=64 —
// vectorstore's defaults) over the SAME random data as vectorstore's
// BenchmarkSearch_128_10k and reports recall@10 vs an exact cosine oracle, isolating
// whether 0.72 is the shared HNSW core / random data or a vectorstore-wrapper issue.
func BenchmarkHNSWRecall_128_10k(b *testing.B) {
	const (
		dim = 128
		n   = 10000
		k   = 10
	)
	data := benchVecsVS(n, dim, 3)
	queries := benchVecsVS(1000, dim, 99)

	store := NewMemNodeStore(Cosine)
	idx := NewHNSWIndex(store, WithRand(rand.New(rand.NewSource(99))))
	for i, v := range data {
		if err := idx.Insert(int64(i), v); err != nil {
			b.Fatal(err)
		}
	}

	var recallSum float64
	for _, q := range queries {
		got, err := idx.Search(q, k)
		if err != nil {
			b.Fatal(err)
		}
		recallSum += recallOverlap(got, bruteCosineTopK(q, data, k))
	}
	recall := recallSum / float64(len(queries))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := idx.Search(queries[i%len(queries)], k); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(recall, "recall@10")
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "qps")
}
