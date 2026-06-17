package vectorstore

import (
	"math/rand"
	"runtime"
	"testing"

	"github.com/codetrek/haystack/core/kv"
)

// reopenUnclean simulates a crash: it opens a NEW Store over the SAME dir + KV
// WITHOUT closing s first. The original WAL handle stays open (the OS would have
// closed it on a real crash) but the new Store opens its own handle on the same
// (already-Reset) file and reads idtable state straight from the durable KV. This
// is the unclean-crash pattern: nothing in s's process memory or its lazy idtable
// batch is flushed — only what was fsync'd to disk/KV survives.
func reopenUnclean(t *testing.T, s *Store, kvStore kv.Store) *Store {
	t.Helper()
	s2, err := Open(Options{Dir: s.dir, KV: kvStore, Metric: s.metric})
	requireNoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	return s2
}

// TestSeal_IdtableDurableBeforeWALReset is the red-proof for the committed-data
// loss BLOCKER. sealLocked truncates the head WAL — the sole source of truth for
// string→docId — but the idtable Allocator commits its key→id batch + nextId
// counter only lazily (5s tick / Close). A crash between Seal and the next idtable
// commit therefore loses every sealed doc's mapping: the sealed records survive on
// disk, but their string ids no longer resolve to a docId, so Get returns
// not-found (phantom docs) and a fresh Put re-allocates a colliding docId.
func TestSeal_IdtableDurableBeforeWALReset(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("in-process crash sim reopens over the same KV with the old store's idtable commit goroutine still live; that POSIX-timing assumption is unreliable on Windows. A real Windows crash kills the process (no live goroutine) and pebble's committed state is durable cross-platform, so the property itself holds — it is exercised on Linux/macOS.")
	}
	kvStore := newTestKV(t)
	s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	rng := rand.New(rand.NewSource(4242))
	dim := 16
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}

	// Put a batch of docs, capturing the docId each string id was assigned.
	docIDOf := make(map[string]int64)
	for i := 0; i < 40; i++ {
		id := "d-" + itoa(i)
		requireNoError(t, s.Put(id, randVec(), nil))
		docIDOf[id] = s.idToDoc[id]
	}
	// Seal: dumps the records durably, but (pre-fix) leaves the idtable batch
	// uncommitted while truncating the WAL that carried the id→docId records.
	requireNoError(t, s.Seal())

	// Crash here: reopen over the same dir + KV WITHOUT Close (no idtable commit).
	s2 := reopenUnclean(t, s, kvStore)

	// (a) Every sealed doc must still resolve and be readable. Pre-fix the mapping
	// was lost (WAL truncated, KV never committed) → Get returns not-found.
	for id := range docIDOf {
		_, _, found, err := s2.Get(id)
		requireNoError(t, err)
		if !found {
			t.Fatalf("sealed doc %q lost after crash-reopen (idtable mapping not durable before WAL reset)", id)
		}
	}

	// (b) A freshly Put new id must NOT collide with any sealed doc's docId. Pre-fix
	// the allocator's nextId counter was never persisted, so it restarts low and
	// re-hands an already-used docId — a collision.
	requireNoError(t, s2.Put("fresh", randVec(), nil))
	newDoc := s2.idToDoc["fresh"]
	for id, d := range docIDOf {
		if d == newDoc {
			t.Fatalf("re-Put docId %d collides with sealed doc %q (idtable nextId not persisted before WAL reset)", newDoc, id)
		}
	}
}
