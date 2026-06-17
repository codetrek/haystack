package vectorstore

import (
	"math/rand"
	"runtime"
	"testing"

	"github.com/codetrek/haystack/core/kv"
)

// reopenUnclean simulates a crash: it opens a NEW Store over the SAME dir + KV
// WITHOUT closing s first. A real process kill releases every OS-held file lock,
// so before reopening we drop only s's open file handle the OS would have
// dropped — the bbolt control-DB lock — WITHOUT running the orderly Close()
// (which would commit the idtable batch + quiesce builds, masking the crash). The
// new Store opens its own handle on the same control DB (the durable head bucket
// is the head's source of truth) and reads idtable state straight from the durable
// KV. This is the unclean-crash pattern: nothing in s's process memory or its lazy
// idtable batch is flushed — only what was committed to the control DB (and to the
// KV) survives.
func reopenUnclean(t *testing.T, s *Store, kvStore kv.Store) *Store {
	t.Helper()
	crashRelease(t, s)
	s2, err := Open(Options{Dir: s.dir, KV: kvStore, Metric: s.metric})
	requireNoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	return s2
}

// crashRelease drops the OS-held file handle a process kill would release — the
// bbolt control-DB flock — so a same-process reopen can take it. It does NOT
// commit the idtable or quiesce in-flight builds: that is the whole point of a
// crash sim (lazy/in-memory state must be lost). Safe to call when the control
// store is already closed (a test that closed it by hand).
func crashRelease(t *testing.T, s *Store) {
	t.Helper()
	_ = s.cs.Close()
}

// TestSeal_IdtableDurableBeforeHeadClear is the red-proof for the committed-data
// loss BLOCKER. sealLocked clears the head bucket — the sole source of truth for
// string→docId of head docs — but the idtable Allocator commits its key→id batch +
// nextId counter only lazily (5s tick / Close). A crash between Seal and the next
// idtable commit therefore loses every sealed doc's mapping: the sealed records
// survive on disk, but their string ids no longer resolve to a docId, so Get
// returns not-found (phantom docs) and a fresh Put re-allocates a colliding docId.
func TestSeal_IdtableDurableBeforeHeadClear(t *testing.T) {
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
	// uncommitted while clearing the head bucket that carried the id→docId records.
	requireNoError(t, s.Seal())

	// Crash here: reopen over the same dir + KV WITHOUT Close (no idtable commit).
	s2 := reopenUnclean(t, s, kvStore)

	// (a) Every sealed doc must still resolve and be readable. Pre-fix the mapping
	// was lost (head bucket cleared, KV never committed) → Get returns not-found.
	for id := range docIDOf {
		_, _, found, err := s2.Get(id)
		requireNoError(t, err)
		if !found {
			t.Fatalf("sealed doc %q lost after crash-reopen (idtable mapping not durable before head clear)", id)
		}
	}

	// (b) A freshly Put new id must NOT collide with any sealed doc's docId. Pre-fix
	// the allocator's nextId counter was never persisted, so it restarts low and
	// re-hands an already-used docId — a collision.
	requireNoError(t, s2.Put("fresh", randVec(), nil))
	newDoc := s2.idToDoc["fresh"]
	for id, d := range docIDOf {
		if d == newDoc {
			t.Fatalf("re-Put docId %d collides with sealed doc %q (idtable nextId not persisted before head clear)", newDoc, id)
		}
	}
}
