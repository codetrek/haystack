package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
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

func TestStore_CreateVectorIndex_BruteThenConverges(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(13))
	dim := 16
	vecs := make(map[int64][]float32)
	put := func(id string, v []float32) {
		requireNoError(t, s.Put(id, v, nil))
		vecs[s.idToDoc[id]] = append([]float32(nil), v...)
	}
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	// Two sealed segments + a head, all under the default index.
	for i := 0; i < 80; i++ {
		put("a-"+itoa(i), randVec())
	}
	requireNoError(t, s.Seal())
	for i := 0; i < 80; i++ {
		put("b-"+itoa(i), randVec())
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	for i := 0; i < 20; i++ {
		put("h-"+itoa(i), randVec()) // head
	}

	// Create a SECOND index (same metric, different params). It is born pending for
	// every existing sealed segment → immediately queryable via brute fallback.
	requireNoError(t, s.CreateVectorIndex("fast", VectorIndexConfig{
		Type: "hnsw", Metric: Cosine, M: 8, EfConstruction: 80, EfSearch: 32,
	}))

	q := randVec()
	want := bruteForceKNN(Cosine, q, vecs, 10)

	// BEFORE the background builds finish, the new index is all-pending → every leg
	// is brute → it returns EXACT top-k (recall 1.0 on the brute legs).
	gotPending, err := s.Search("fast", q, 10, nil)
	requireNoError(t, err)
	if r := recallAtK(gotPending, want); r < 0.9 {
		t.Fatalf("new index pending (brute) recall = %.2f, want ~1.0", r)
	}

	// After convergence, the new index's graphs are built (pending→indexed) and it
	// still returns correct top-k under its own params.
	requireNoError(t, s.WaitForIndex())
	info := indexInfoByName(t, s, "fast")
	if info.Indexed != info.Segments || info.Segments != 2 {
		t.Fatalf("fast index did not converge: %+v", info)
	}
	gotIndexed, err := s.Search("fast", q, 10, nil)
	requireNoError(t, err)
	if r := recallAtK(gotIndexed, want); r < 0.8 {
		t.Fatalf("new index graph recall = %.2f, want >= 0.8", r)
	}
}

func TestStore_CreateVectorIndex_DuplicateAndBadType(t *testing.T) {
	s := openTestStore(t, Cosine)
	if err := s.CreateVectorIndex("x", VectorIndexConfig{Type: "ivfpq", Metric: Cosine}); err == nil {
		t.Fatal("non-hnsw Type must error in v1")
	}
	requireNoError(t, s.CreateVectorIndex("x", VectorIndexConfig{Type: "hnsw", Metric: Cosine, M: 8}))
	// Idempotent on the SAME config; conflicting config errors.
	requireNoError(t, s.CreateVectorIndex("x", VectorIndexConfig{Type: "hnsw", Metric: Cosine, M: 8}))
	if err := s.CreateVectorIndex("x", VectorIndexConfig{Type: "hnsw", Metric: Cosine, M: 16}); err == nil {
		t.Fatal("re-create with a different config must error")
	}
	if err := s.CreateVectorIndex("default", VectorIndexConfig{Type: "hnsw", Metric: Cosine}); err == nil {
		t.Fatal("re-creating the reserved default index must error")
	}
}

func indexInfoByName(t *testing.T, s *Store, name string) VectorIndexInfo {
	t.Helper()
	for _, in := range s.ListVectorIndexes() {
		if in.Name == name {
			return in
		}
	}
	t.Fatalf("index %q not found", name)
	return VectorIndexInfo{}
}

func TestStore_IndexLag_CountsPendingSegments(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(21))
	dim := 8
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	for i := 0; i < 40; i++ {
		requireNoError(t, s.Put("a-"+itoa(i), randVec(), nil))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	// default index: fully indexed → zero lag.
	lag := s.IndexLag("default")
	if lag.PendingSegments != 0 || lag.PendingVectors != 0 {
		t.Fatalf("default lag should be zero, got %+v", lag)
	}

	// A brand-new index is pending for the one sealed segment until builds finish.
	requireNoError(t, s.CreateVectorIndex("slow", VectorIndexConfig{Type: "hnsw", Metric: Cosine}))
	lagNew := s.IndexLag("slow")
	if lagNew.PendingSegments != 1 || lagNew.PendingVectors != 40 {
		t.Fatalf("new index lag = %+v, want 1 segment / 40 vectors pending", lagNew)
	}
	requireNoError(t, s.WaitForIndex())
	if l := s.IndexLag("slow"); l.PendingSegments != 0 {
		t.Fatalf("slow index should be fully built, got %+v", l)
	}

	// Unknown index → Exists=false.
	if l := s.IndexLag("ghost"); l.Exists {
		t.Fatalf("unknown index must report Exists=false, got %+v", l)
	}
}

func TestStore_DropVectorIndex_LeavesOthersIntact(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(41))
	dim := 16
	vecs := make(map[int64][]float32)
	put := func(id string, v []float32) {
		requireNoError(t, s.Put(id, v, nil))
		vecs[s.idToDoc[id]] = append([]float32(nil), v...)
	}
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	for i := 0; i < 100; i++ {
		put("d-"+itoa(i), randVec())
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.CreateVectorIndex("aux", VectorIndexConfig{Type: "hnsw", Metric: Cosine, M: 8}))
	requireNoError(t, s.WaitForIndex())

	// Sanity: aux's graph file exists on disk.
	segDir := filepath.Join(s.dir, segDirName(segID(1), 0))
	if _, err := os.Stat(filepath.Join(segDir, "graph-aux.dat")); err != nil {
		t.Fatalf("graph-aux.dat should exist before drop: %v", err)
	}

	requireNoError(t, s.DropVectorIndex("aux"))

	// aux is gone from the map and its graph file is deleted; the default index's
	// file and records are untouched.
	if _, err := os.Stat(filepath.Join(segDir, "graph-aux.dat")); !os.IsNotExist(err) {
		t.Fatalf("graph-aux.dat must be deleted, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(segDir, "graph-default.dat")); err != nil {
		t.Fatalf("graph-default.dat must survive a sibling drop: %v", err)
	}
	names := s.ListVectorIndexes()
	if len(names) != 1 || names[0].Name != "default" {
		t.Fatalf("after drop, only default should remain, got %+v", names)
	}
	if _, err := s.Search("aux", randVec(), 5, nil); err == nil {
		t.Fatal("Search on the dropped index must error")
	}

	// The surviving default index still returns correct top-k.
	q := randVec()
	got, err := s.Search("default", q, 10, nil)
	requireNoError(t, err)
	want := bruteForceKNN(Cosine, q, vecs, 10)
	if r := recallAtK(got, want); r < 0.8 {
		t.Fatalf("default index recall after sibling drop = %.2f, want >= 0.8", r)
	}

	// Records intact: a doc is still Gettable.
	if _, _, found, _ := s.Get("d-0"); !found {
		t.Fatal("records must survive DropVectorIndex")
	}
}

func TestStore_DropVectorIndex_DefaultRefusedUnknownNoop(t *testing.T) {
	s := openTestStore(t, Cosine)
	if err := s.DropVectorIndex("default"); err == nil {
		t.Fatal("dropping the default index must be refused")
	}
	requireNoError(t, s.DropVectorIndex("never-existed")) // no-op
}

func TestStore_Recover_RestoresAllIndexesAndResumesPending(t *testing.T) {
	dir := t.TempDir()
	kvs := newTestKV(t)
	rng := rand.New(rand.NewSource(61))
	dim := 16
	vecs := make(map[int64][]float32)
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}

	{
		s, err := Open(Options{Dir: dir, KV: kvs, Metric: Cosine})
		requireNoError(t, err)
		put := func(id string, v []float32) {
			requireNoError(t, s.Put(id, v, nil))
			vecs[s.idToDoc[id]] = append([]float32(nil), v...)
		}
		for i := 0; i < 60; i++ {
			put("a-"+itoa(i), randVec())
		}
		requireNoError(t, s.Seal()) // seg 1 (default builds N=1 graph)
		requireNoError(t, s.CreateVectorIndex("aux", VectorIndexConfig{Type: "hnsw", Metric: Cosine, M: 8}))
		requireNoError(t, s.WaitForIndex()) // both indexes built for seg 1
		requireNoError(t, s.Close())
	}

	// Reopen: both indexes' configs + states load from the manifest; indexed graphs
	// reopen from disk, no rebuild needed.
	s2, err := Open(Options{Dir: dir, KV: kvs, Metric: Cosine})
	requireNoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	requireNoError(t, s2.WaitForIndex())

	names := s2.ListVectorIndexes()
	if len(names) != 2 {
		t.Fatalf("recover lost an index, got %+v", names)
	}
	for _, in := range names {
		if in.Indexed != in.Segments || in.Segments != 1 {
			t.Fatalf("index %q not fully indexed after recover: %+v", in.Name, in)
		}
	}

	q := randVec()
	want := bruteForceKNN(Cosine, q, vecs, 10)
	for _, name := range []string{"default", "aux"} {
		got, err := s2.Search(name, q, 10, nil)
		requireNoError(t, err)
		if r := recallAtK(got, want); r < 0.8 {
			t.Fatalf("index %q recall after recover = %.2f, want >= 0.8", name, r)
		}
	}
}
