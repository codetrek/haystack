package vectorstore

import (
	"math/rand"
	"sync"
	"testing"
)

func TestStore_Seal_BuildsInBackground_PendingThenIndexed(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(51))
	dim := 16
	put := func(id string, v []float32) { requireNoError(t, s.Put(id, v, nil)) }
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	for i := 0; i < 100; i++ {
		put("s-"+itoa(i), randVec())
	}
	requireNoError(t, s.Seal())

	// Right after Seal returns, the segment is pending (no graph yet) but the
	// store is immediately writable (fresh head) and searchable (brute leg).
	put("hot-after-seal", randVec()) // must not block / error
	got, err := s.Search(randVec(), 5, nil)
	requireNoError(t, err)
	if len(got) == 0 {
		t.Fatal("search returned nothing while build pending (brute leg missing)")
	}

	// After WaitForIndex, the segment must be indexed (graph installed).
	requireNoError(t, s.WaitForIndex())
	if !s.isIndexedForTest(1) {
		t.Fatal("segment 1 not indexed after WaitForIndex")
	}
}

// TestStore_DeleteDuringPendingBuild_NoRace deletes docs of a freshly-sealed
// (pending) segment WHILE its HNSW is being built in the background. The builder
// reads the segment's tomb bitmap via eachLive off the store lock; Delete writes
// the same bitmap under the store lock. This exercises the appendix #16/#18 race
// the per-segment tomb mutex guards — it must be clean under `go test -race`.
func TestStore_DeleteDuringPendingBuild_NoRace(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(91))
	dim := 16
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	for i := 0; i < 200; i++ {
		requireNoError(t, s.Put("d-"+itoa(i), randVec(), nil))
	}
	requireNoError(t, s.Seal()) // publishes pending, spawns the background build

	// Hammer Delete on the just-sealed segment concurrently with the build.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = s.Delete("d-" + itoa(i))
		}
	}()
	// And search concurrently (touches the pending brute leg + the segment tomb).
	wg.Add(1)
	go func() {
		defer wg.Done()
		srng := rand.New(rand.NewSource(92))
		sv := func() []float32 {
			v := make([]float32, dim)
			for d := range v {
				v[d] = srng.Float32()
			}
			return v
		}
		for i := 0; i < 50; i++ {
			_, _ = s.Search(sv(), 5, nil)
		}
	}()
	wg.Wait()
	requireNoError(t, s.WaitForIndex())

	// The deleted docs must stay gone after the build flips the segment to indexed.
	if _, _, found, _ := s.Get("d-0"); found {
		t.Fatal("deleted doc d-0 resurrected after concurrent build")
	}
}
