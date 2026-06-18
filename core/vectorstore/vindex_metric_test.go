package vectorstore

import (
	"math/rand"
	"testing"
)

func TestStore_PerIndexMetric_EachReturnsItsOwnTopK(t *testing.T) {
	// Primary (records) metric = Cosine. A second index uses Euclidean. For the SAME
	// query, the two indexes must return DIFFERENT, each-metric-correct top-k.
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(123))
	dim := 12
	vecs := make(map[int64][]float32)
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()*4 - 2 // varied magnitudes so cosine != euclidean ordering
		}
		return v
	}
	batchPutVecs(t, s, "v-", 150, randVec, vecs)
	requireNoError(t, s.Seal())
	requireNoError(t, s.CreateVectorIndex("euclid", VectorIndexConfig{
		Type: "hnsw", Metric: Euclidean, M: 16, EfConstruction: 200, EfSearch: 64,
	}))
	requireNoError(t, s.WaitForIndex())

	q := randVec()
	wantCos := bruteForceKNN(Cosine, q, vecs, 10)
	wantEuc := bruteForceKNN(Euclidean, q, vecs, 10)

	gotCos, err := s.Search("default", q, 10, nil)
	requireNoError(t, err)
	gotEuc, err := s.Search("euclid", q, 10, nil)
	requireNoError(t, err)

	if r := recallAtK(gotCos, wantCos); r < 0.8 {
		t.Fatalf("default(cosine) recall = %.2f vs cosine oracle, want >= 0.8", r)
	}
	if r := recallAtK(gotEuc, wantEuc); r < 0.8 {
		t.Fatalf("euclid index recall = %.2f vs EUCLIDEAN oracle, want >= 0.8", r)
	}
	// The euclid index must NOT just echo the cosine ranking: its recall against the
	// cosine oracle should be materially lower (the metrics order these vectors
	// differently). This proves the per-index metric actually drove the search.
	if r := recallAtK(gotEuc, wantCos); r >= 0.8 {
		t.Fatalf("euclid result matches the COSINE oracle (%.2f) — per-index metric not applied", r)
	}
}

// TestStore_PerIndexMetric_FilteredEachLeg covers the four brute distance legs the
// plan's nil-filter test misses (adversarial appendix #8/#9/#13/#15/#17): a filtered
// Search on a non-primary-metric (Euclidean) index over a Cosine-primary store must
// reconstruct raw in (1) the inline filtered head brute-S leg (declared attr on the
// head), (2) the pending sealed brute leg, and (3) the indexed sealed brute-S leg
// (|S_seg| <= attrSearchT). Each must rank under the INDEX metric, not the primary.
// We oracle against a Euclidean brute over only the filter-matching live docs.
func TestStore_PerIndexMetric_FilteredEachLeg(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.CreateAttrIndex("grp", Keyword)) // head + sealed get a declared attr index
	rng := rand.New(rand.NewSource(321))
	dim := 12
	// Track vecs partitioned by group so the oracle restricts to the matching subset.
	vecsByGroup := map[string]map[int64][]float32{"x": {}, "y": {}}
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()*4 - 2
		}
		return v
	}
	// putBatch Puts n group-tagged vectors in one batch commit, then resolves the
	// grouped docId-keyed oracle AFTER commit (idToDoc is populated by Commit).
	putBatch := func(prefix string, n int) {
		b := s.NewBatch()
		keys := make([]string, n)
		gs := make([]string, n)
		vs := make([][]float32, n)
		for i := 0; i < n; i++ {
			grp := "x"
			if i%2 == 0 {
				grp = "y"
			}
			keys[i] = prefix + itoa(i)
			gs[i] = grp
			vs[i] = randVec()
			b.Put(keys[i], vs[i], Payload{"grp": StringValue(grp)})
		}
		requireNoError(t, b.Commit())
		for i := 0; i < n; i++ {
			vecsByGroup[gs[i]][s.idToDoc[keys[i]]] = append([]float32(nil), vs[i]...)
		}
	}
	// Sealed segment with both groups (exercises the indexed sealed legs after build).
	putBatch("s-", 120)
	requireNoError(t, s.Seal())
	requireNoError(t, s.CreateVectorIndex("euclid", VectorIndexConfig{
		Type: "hnsw", Metric: Euclidean, M: 16, EfConstruction: 200, EfSearch: 64,
	}))
	requireNoError(t, s.WaitForIndex())
	// Head docs (un-sealed) so the filtered head brute-S leg runs.
	putBatch("h-", 30)

	q := randVec()
	filter := Eq("grp", StringValue("x"))
	// Oracle: euclidean nearest among ONLY the group-x live docs (head + sealed).
	wantEuc := bruteForceKNN(Euclidean, q, vecsByGroup["x"], 10)

	got, err := s.Search("euclid", q, 10, filter)
	requireNoError(t, err)
	if r := recallAtK(got, wantEuc); r < 0.8 {
		t.Fatalf("filtered euclid recall vs euclidean oracle = %.2f, want >= 0.8 "+
			"(a brute leg not reconstructing raw computes the wrong-metric distance)", r)
	}
	// Every returned doc must be in group x (filter honored, no leak from group y).
	for _, h := range got {
		if _, ok := vecsByGroup["x"][h.DocID]; !ok {
			t.Fatalf("filtered euclid returned a non-matching doc %d (filter not honored)", h.DocID)
		}
	}
	// And it must NOT be the cosine ranking: the euclidean filtered result should
	// recall the cosine-over-group-x oracle materially less (per-index metric drove it).
	wantCos := bruteForceKNN(Cosine, q, vecsByGroup["x"], 10)
	if r := recallAtK(got, wantCos); r >= 0.8 {
		t.Fatalf("filtered euclid matches the COSINE oracle (%.2f) — per-index metric not applied to a brute leg", r)
	}
}
