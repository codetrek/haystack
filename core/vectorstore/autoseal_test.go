package vectorstore

import (
	"math/rand"
	"testing"
)

func TestStore_AutoSealAtMaxSegSize(t *testing.T) {
	s := openTestStore(t, Cosine)
	s.maxSegSize = 50 // small, deterministic threshold for the test
	rng := rand.New(rand.NewSource(61))
	dim := 8
	put := func(id string) {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		requireNoError(t, s.Put(id, v, nil))
	}
	for i := 0; i < 120; i++ {
		put("d-" + itoa(i))
	}
	requireNoError(t, s.WaitForIndex())

	// 120 puts with maxSegSize 50 → 2 sealed segments (at 50 and 100), 20 in head.
	s.mu.RLock()
	nSealed := len(s.sealed)
	headLive := 0
	s.seg.eachLive(func(int, int64, []float32, float32) { headLive++ })
	s.mu.RUnlock()
	if nSealed != 2 {
		t.Fatalf("sealed segments = %d, want 2", nSealed)
	}
	if headLive != 20 {
		t.Fatalf("head live = %d, want 20", headLive)
	}
	// Everything still searchable.
	q := make([]float32, dim)
	for d := range q {
		q[d] = rng.Float32()
	}
	got, err := s.Search(q, 10)
	requireNoError(t, err)
	if len(got) != 10 {
		t.Fatalf("search returned %d, want 10", len(got))
	}
}
