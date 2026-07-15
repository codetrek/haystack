package vectorstore

import (
	"math/rand"
	"testing"
)

// recallAtK is the fraction of the brute top-k that the graph also returned.
func recallAtK(got []SearchResult, want []int64) float64 {
	set := make(map[int64]bool, len(want))
	for _, d := range want {
		set[d] = true
	}
	hit := 0
	for _, r := range got {
		if set[r.DocID] {
			hit++
		}
	}
	return float64(hit) / float64(len(want))
}

func TestHNSW_MemStore_BuildSearchRecall(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	dim := 16
	n := 300
	vecs := make(map[int64][]float32, n)
	gs := newMemGraphStore(Cosine)
	idx := newHNSWIndex(gs, withGraphM(16), withGraphEfConstruction(100), withGraphRand(rand.New(rand.NewSource(2))))
	b := idx.newBatch()
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		vecs[int64(i)] = v
		b.put(int64(i), v)
	}
	requireNoError(t, b.commit())

	q := make([]float32, dim)
	for d := range q {
		q[d] = rng.Float32()
	}
	got, err := idx.search(q, 10)
	requireNoError(t, err)
	want := bruteForceKNN(Cosine, q, vecs, 10)
	if r := recallAtK(got, want); r < 0.8 {
		t.Fatalf("recall@10 = %.2f, want >= 0.8 (graph copy broken)", r)
	}
}

func TestHNSW_EmptyReturnsNil(t *testing.T) {
	idx := newHNSWIndex(newMemGraphStore(Cosine))
	got, err := idx.search([]float32{1, 0, 0}, 5)
	requireNoError(t, err)
	if got != nil {
		t.Fatalf("empty index search = %v, want nil", got)
	}
}
