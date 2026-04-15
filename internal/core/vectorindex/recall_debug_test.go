package vectorindex

import (
	"fmt"
	"math/rand"
	"testing"
)

func TestHNSWRecallDebug10K(t *testing.T) {
	const n = 10000
	const dim = 384
	const k = 10
	const numQueries = 20
	const seed = 99

	rng := rand.New(rand.NewSource(seed))
	vecs := make([][]float32, n)
	for i := range vecs {
		v := make([]float32, dim)
		for j := range v {
			v[j] = rng.Float32()*2 - 1
		}
		vecs[i] = v
	}

	// Build the index once and reuse it for all efSearch values.
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance, WithCosineDistance(), WithRand(rand.New(rand.NewSource(seed))))

	for i, v := range vecs {
		if err := idx.Insert(fmt.Sprintf("doc-%d", i), v); err != nil {
			t.Fatalf("Insert doc-%d: %v", i, err)
		}
	}

	for _, efS := range []int{64, 200, 500, 1000} {
		idx.efSearch = efS

		queryRng := rand.New(rand.NewSource(seed + 2))
		totalRecall := 0.0
		for qi := 0; qi < numQueries; qi++ {
			q := make([]float32, dim)
			for j := range q {
				q[j] = queryRng.Float32()*2 - 1
			}

			hnswResults, err := idx.Search(q, k)
			if err != nil {
				t.Fatalf("Search %d: %v", qi, err)
			}

			bfResults := bruteForceKNN(q, vecs, k, CosineDistance)
			bfSet := make(map[uint64]bool, k)
			for _, bfIdx := range bfResults {
				bfSet[uint64(bfIdx+1)] = true
			}

			hits := 0
			for _, r := range hnswResults {
				if bfSet[r.ID] {
					hits++
				}
			}
			totalRecall += float64(hits) / float64(k)
		}
		t.Logf("efSearch=%d  Recall@%d on %d vectors: %.4f", efS, k, n, totalRecall/float64(numQueries))
	}
}
