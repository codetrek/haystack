package vectorstore

import (
	"math/rand"
	"testing"
)

// TestStore_Recover_PreservesNonPrimaryMetric is the permanent regression guard for
// the §3.4/§4.8 load-bearing invariant flagged by the Phase-6 final review: a named
// index whose Metric differs from the store's primary metric must survive close +
// reopen. The manifest must persist the index's metric, and recover must reopen its
// per-segment graph through the reconstruct-raw reindex wrapper — otherwise the
// reopened index would silently score under the PRIMARY metric.
func TestStore_Recover_PreservesNonPrimaryMetric(t *testing.T) {
	kvStore := newTestKV(t)
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, KV: kvStore, Metric: Cosine})
	requireNoError(t, err)

	rng := rand.New(rand.NewSource(202))
	dim := 12
	vecs := make(map[int64][]float32)
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()*4 - 2 // varied magnitudes: cosine and euclidean disagree
		}
		return v
	}
	for i := 0; i < 150; i++ {
		v := randVec()
		requireNoError(t, s.Put("v-"+itoa(i), v, nil))
		vecs[s.idToDoc["v-"+itoa(i)]] = append([]float32(nil), v...)
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.CreateVectorIndex("euclid", VectorIndexConfig{
		Type: "hnsw", Metric: Euclidean, M: 16, EfConstruction: 200, EfSearch: 64,
	}))
	requireNoError(t, s.WaitForIndex())

	// Crash/restart: close, then recover over the same dir + KV.
	s2 := reopenStore(t, s, kvStore)
	requireNoError(t, s2.WaitForIndex())

	q := randVec()
	wantEuc := bruteForceKNN(Euclidean, q, vecs, 10)
	wantCos := bruteForceKNN(Cosine, q, vecs, 10)
	gotEuc, err := s2.Search("euclid", q, 10, nil)
	requireNoError(t, err)

	if r := recallAtK(gotEuc, wantEuc); r < 0.8 {
		t.Fatalf("after recover, euclid index recall = %.2f vs EUCLIDEAN oracle, want >= 0.8 "+
			"(per-index metric not persisted or not reconstructed on reopen)", r)
	}
	// And it must not have collapsed to the primary (cosine) ranking on reopen.
	if r := recallAtK(gotEuc, wantCos); r >= 0.8 {
		t.Fatalf("after recover, euclid result matches the COSINE oracle (%.2f) — "+
			"the index lost its metric on reopen", r)
	}
}
