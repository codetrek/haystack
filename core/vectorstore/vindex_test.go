package vectorstore

import (
	"math/rand"
	"testing"
)

func TestVectorIndexConfig_GraphConfigFromCfg_Defaults(t *testing.T) {
	// A zero-valued cfg fills HNSW params from the package defaults (parity with
	// graphConfig{}.withDefaults()), and carries the metric through unchanged.
	cfg := VectorIndexConfig{Type: "hnsw", Metric: Cosine}
	gc := graphConfigFromCfg(cfg)
	if gc.M != defaultGraphM || gc.EfConstruction != defaultGraphEfConstruction || gc.EfSearch != defaultGraphEfSearch {
		t.Fatalf("defaults not applied: %+v", gc)
	}

	// Explicit params are preserved.
	cfg2 := VectorIndexConfig{Type: "hnsw", Metric: Euclidean, M: 24, EfConstruction: 111, EfSearch: 40}
	gc2 := graphConfigFromCfg(cfg2)
	if gc2.M != 24 || gc2.EfConstruction != 111 || gc2.EfSearch != 40 {
		t.Fatalf("explicit params lost: %+v", gc2)
	}
}

func TestVindex_NewVindex_EmptyGraphsIsPending(t *testing.T) {
	// A freshly created vindex has an empty graphs map: every (index, segment) is
	// pending (no graph) until the builder fills it (§4.7 "新建索引 = 对所有段 pending").
	vx := newVindex(VectorIndexConfig{Type: "hnsw", Metric: Cosine})
	if vx.metric != Cosine {
		t.Fatalf("metric = %v, want Cosine", vx.metric)
	}
	if vx.graphs == nil {
		t.Fatal("graphs map must be initialized (non-nil), not lazily nil")
	}
	if len(vx.graphs) != 0 {
		t.Fatalf("new vindex must have zero graphs (all pending), got %d", len(vx.graphs))
	}
	if _, ok := vx.graphs[segID(1)]; ok {
		t.Fatal("segment 1 must be pending (absent) in a new index")
	}
}

func TestStore_DefaultIndex_ExistsAndSearchCorrect(t *testing.T) {
	s := openTestStore(t, Cosine)
	// The store is born with exactly one index named "default", carrying the store
	// metric (Phases 1-5 behavior, migrated to a named index).
	names := s.ListVectorIndexes()
	if len(names) != 1 || names[0].Name != "default" {
		t.Fatalf("expected one index named default, got %+v", names)
	}
	if names[0].Metric != Cosine {
		t.Fatalf("default index metric = %v, want Cosine", names[0].Metric)
	}

	rng := rand.New(rand.NewSource(7))
	dim := 16
	vecs := make(map[int64][]float32)
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	for i := 0; i < 120; i++ {
		v := randVec()
		requireNoError(t, s.Put("d-"+itoa(i), v, nil))
		vecs[s.idToDoc["d-"+itoa(i)]] = append([]float32(nil), v...)
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	q := randVec()
	// The named-index Search("default", …) reproduces the legacy default-path
	// behavior: against an exact brute-force oracle it must return the correct
	// top-k (no recall regression on the migration to a named index). The whole
	// Phase-1-5 suite — all migrated to Search("default", …) — is the byte-identical
	// guard; this asserts the named dispatch is itself correct.
	got, err := s.Search("default", q, 10, nil)
	requireNoError(t, err)
	want := bruteForceKNN(Cosine, q, vecs, 10)
	if r := recallAtK(got, want); r < 0.8 {
		t.Fatalf("default-index Search recall@10 = %.2f, want >= 0.8", r)
	}
}

func TestStore_Search_UnknownIndexErrors(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.Put("a", []float32{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, nil))
	if _, err := s.Search("nope", []float32{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, 5, nil); err == nil {
		t.Fatal("Search on an unknown index must error")
	}
}
