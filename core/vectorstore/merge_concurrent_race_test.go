package vectorstore

import (
	"math/rand"
	"sync"
	"testing"
)

// TestMerge_ConcurrentDeletePutDuringInFlightMerge is a genuinely-concurrent
// hardening test (Phase-4 finding #4): a Delete and a Put run on a REAL separate
// goroutine WHILE a merge is live in its off-lock build window (outputs written,
// swap not yet taken). The in-window seam blocks the merge mid-flight and signals
// the mutator goroutine, so the Delete (of an input doc) and the Put (re-homing
// another input doc to the head) land AFTER the merge's live-set snapshot but
// BEFORE its atomic swap — the highest-risk reconciliation path (mergeAndPublish
// step 2a). The merge must reconcile both: the deleted doc must NOT resurrect in
// the output, and the re-homed doc must appear EXACTLY once (head copy wins, no
// duplicate live copy from the output). Run under -race with repetition.
// DotProduct metric is used so Get round-trips the exact re-Put vector (identity
// prepare/restore).
func TestMerge_ConcurrentDeletePutDuringInFlightMerge(t *testing.T) {
	for iter := 0; iter < 40; iter++ {
		kvStore := newTestKV(t)
		s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: DotProduct})
		requireNoError(t, err)
		rng := rand.New(rand.NewSource(int64(200 + iter)))
		dim := 8
		const n = 24
		for i := 0; i < n; i++ {
			requireNoError(t, s.Put("d-"+itoa(i), randVecN(rng, dim), nil))
		}
		requireNoError(t, s.Seal())
		requireNoError(t, s.WaitForIndex())

		delDoc := s.idToDoc["d-3"]
		putDoc := s.idToDoc["d-11"]
		newVec := randVecN(rng, dim)

		// The mutator runs on a real concurrent goroutine; the in-window seam releases
		// it (start) and waits for it to finish (done) so the mutations are guaranteed
		// to land in the off-lock window, then the swap reconciles them.
		start := make(chan struct{})
		done := make(chan struct{})
		s.testHookInMergeWindow = func(p *mergePlan) {
			close(start)
			<-done
		}
		var mwg sync.WaitGroup
		mwg.Add(1)
		go func() {
			defer mwg.Done()
			<-start
			requireNoError(t, s.Delete("d-3"))            // tombstone an input doc mid-merge
			requireNoError(t, s.Put("d-11", newVec, nil)) // re-home an input doc to head mid-merge
			close(done)
		}()

		// Launch the merge of the single sealed segment on its background goroutine.
		requireNoError(t, s.mergeNow([]segID{1}))
		requireNoError(t, s.WaitForMerge())
		mwg.Wait()
		requireNoError(t, s.WaitForIndex())

		// (1) The deleted doc must NOT resurrect anywhere.
		if _, _, found, _ := s.Get("d-3"); found {
			t.Fatalf("iter %d: d-3 deleted during the merge window resurrected", iter)
		}
		s.mu.RLock()
		_, delMapped := s.docToSeg[delDoc]
		putOwner := s.docToSeg[putDoc]
		s.mu.RUnlock()
		if delMapped {
			t.Fatalf("iter %d: deleted doc %d still mapped in docToSeg", iter, delDoc)
		}

		// (2) The re-homed doc must live in the HEAD (the Put won), with the new vector.
		if putOwner != headSegID {
			t.Fatalf("iter %d: re-homed d-11 owner=%d, want headSegID", iter, putOwner)
		}
		v, _, found, err := s.Get("d-11")
		requireNoError(t, err)
		if !found {
			t.Fatalf("iter %d: re-homed d-11 lost", iter)
		}
		if !floatsEqual(v, newVec) {
			t.Fatalf("iter %d: d-11 vector = %v, want the re-Put %v (head copy must win)", iter, v, newVec)
		}

		// (3) Search: d-3 absent, d-11 appears EXACTLY once (no duplicate from output).
		q := randVecN(rng, dim)
		got, err := s.Search("default", q, n, nil)
		requireNoError(t, err)
		nDel, nPut := 0, 0
		for _, r := range got {
			if r.DocID == delDoc {
				nDel++
			}
			if r.DocID == putDoc {
				nPut++
			}
		}
		if nDel != 0 {
			t.Fatalf("iter %d: deleted d-3 appeared %d times in Search", iter, nDel)
		}
		if nPut != 1 {
			t.Fatalf("iter %d: re-homed d-11 appeared %d times in Search, want exactly 1", iter, nPut)
		}

		// (4) Every other survivor present exactly once.
		for i := 0; i < n; i++ {
			if i == 3 {
				continue
			}
			if _, _, found, _ := s.Get("d-" + itoa(i)); !found {
				t.Fatalf("iter %d: survivor d-%d lost across the concurrent merge", iter, i)
			}
		}
		requireNoError(t, s.Close())
		_ = kvStore.Close()
	}
}

// floatsEqual reports whether two float32 slices are elementwise equal.
func floatsEqual(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
