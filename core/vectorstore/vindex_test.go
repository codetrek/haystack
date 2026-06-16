package vectorstore

import "testing"

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
