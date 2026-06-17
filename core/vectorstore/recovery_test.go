package vectorstore

import (
	"math/rand"
	"testing"

	"github.com/codetrek/haystack/core/kv"
)

// reopenStore closes s and reopens a fresh Store over the SAME dir + KV (recovery).
func reopenStore(t *testing.T, s *Store, kvStore kv.Store) *Store {
	t.Helper()
	dir := s.dir
	requireNoError(t, s.Close())
	s2, err := Open(Options{Dir: dir, KV: kvStore, Metric: s.metric})
	requireNoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	return s2
}

func TestRecovery_SealedSegmentsAndHeadWAL(t *testing.T) {
	kvStore := newTestKV(t)
	s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
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
	// Sealed (indexed) batch.
	for i := 0; i < 100; i++ {
		put("s-"+itoa(i), randVec())
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	// Head batch (only in WAL).
	for i := 0; i < 30; i++ {
		put("h-"+itoa(i), randVec())
	}
	// Delete one sealed doc (persisted in tomb.dat) and one head doc (WAL).
	requireNoError(t, s.Delete("s-0"))
	delete(vecs, s.idToDoc["s-0"])
	requireNoError(t, s.Delete("h-0"))
	delete(vecs, s.idToDoc["h-0"])

	q := randVec()

	s2 := reopenStore(t, s, kvStore)

	// All recovered: search recall holds, deleted docs stay gone.
	got, err := s2.Search("default", q, 10, nil)
	requireNoError(t, err)
	want := bruteForceKNN(Cosine, q, vecs, 10)
	if r := recallAtK(got, want); r < 0.8 {
		t.Fatalf("post-recovery recall@10 = %.2f, want >= 0.8", r)
	}
	// Sealed delete survived (persisted tombstone).
	if _, _, found, _ := s2.Get("s-0"); found {
		t.Fatal("deleted sealed doc s-0 resurrected after recovery")
	}
	// Head delete survived (WAL replay).
	if _, _, found, _ := s2.Get("h-0"); found {
		t.Fatal("deleted head doc h-0 resurrected after recovery")
	}
	// A surviving head doc is readable.
	if _, _, found, _ := s2.Get("h-5"); !found {
		t.Fatal("head doc h-5 lost after recovery")
	}
	// A surviving sealed doc is readable.
	if _, _, found, _ := s2.Get("s-5"); !found {
		t.Fatal("sealed doc s-5 lost after recovery")
	}
}
