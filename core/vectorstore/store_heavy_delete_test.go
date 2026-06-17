package vectorstore

import (
	"math/rand"
	"testing"
)

// TestStore_Search_HeavyDeleteRecall is the red-proof for the indexed-leg recall
// collapse under post-seal deletes. The indexed leg fetched exactly k from each
// indexed sealed segment's HNSW then post-filtered tombstones, so a heavily-
// deleted segment under-returns: with delete fraction f, roughly f of the top-k
// graph hits are tombstoned and dropped, leaving recall@k ≈ 1-f. At f=0.3 that is
// ~0.69, below the 0.8 graph-recall gate. The fix over-fetches per indexed
// segment (inflate k by the live-tombstone count) so k LIVE hits survive the
// post-filter.
func TestStore_Search_HeavyDeleteRecall(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(7))
	dim := 16
	n := 400

	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}

	ids := make([]string, n)
	for i := 0; i < n; i++ {
		id := "d-" + itoa(i)
		ids[i] = id
		requireNoError(t, s.Put(id, randVec(), nil))
	}
	// Seal into a single sealed segment and build its HNSW (indexed leg).
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	// Delete 30% of the segment's docs (tombstoned in the immutable indexed graph).
	live := make(map[int64][]float32, n)
	for i, id := range ids {
		doc := s.idToDoc[id]
		if i%10 < 3 { // 30% deleted
			requireNoError(t, s.Delete(id))
			continue
		}
		// Reconstruct the live doc's vector from its sealed slot for the oracle.
		v, _, found, err := s.Get(id)
		requireNoError(t, err)
		if !found {
			t.Fatalf("expected live doc %q to be found", id)
		}
		live[doc] = v
	}

	// Average recall@10 across many queries vs a brute-force oracle over LIVE docs.
	const iters = 30
	var sum float64
	for it := 0; it < iters; it++ {
		q := randVec()
		got, err := s.Search("default", q, 10, nil)
		requireNoError(t, err)
		want := bruteForceKNN(Cosine, q, live, 10)
		sum += recallAtK(got, want)
	}
	avg := sum / iters
	if avg < 0.8 {
		t.Fatalf("indexed-leg recall@10 under 30%% deletes = %.3f, want >= 0.8 (over-fetch missing)", avg)
	}
}
