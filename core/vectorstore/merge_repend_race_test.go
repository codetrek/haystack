package vectorstore

import (
	"math/rand"
	"testing"
)

// TestMerge_RebuildRePendsInputDuringMergeWindow is the red-proof for the Task-13
// blocker: RebuildVectorIndex (and CreateVectorIndex) re-pend an already-indexed
// sealed segment and spawn background builders that read its input mmap. If a
// concurrent background merge is in its OFF-LOCK output-write window (holding
// neither buildMu nor s.mu) it has already passed its plan-time fullyIndexedLocked
// gate and trusts "no builder is reading the input mmap"; its swap then munmaps the
// input (s.sealed[i].close()) while the rebuild builder is mid-read in
// getVectorRef → SIGSEGV / use-after-free, AND a -race write/read on the mmap word.
//
// We drive that exact interleave deterministically via testHookInMergeWindow: the
// merge parks after writing+reopening its outputs but BEFORE taking the swap lock;
// the hook runs RebuildVectorIndex("default"), which under buildMu+s.mu clears
// vx.graphs (re-pending segment 1) and spawns builders against the STILL-LIVE input
// mmap, fully completing the re-pend before the hook returns and the swap proceeds.
//
// The fix re-validates fullyIndexedLocked under the swap's buildMu+s.mu BEFORE
// closing any input; finding the input re-pended, it ABORTS the merge (leaving the
// inputs live + untouched) so the rebuild builders finish safely. Run under -race
// with repetition: every doc must survive, the index must reconverge, and there
// must be no fault/data-race.
func TestMerge_RebuildRePendsInputDuringMergeWindow(t *testing.T) {
	for iter := 0; iter < 30; iter++ {
		kvStore := newTestKV(t)
		s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: DotProduct})
		requireNoError(t, err)
		rng := rand.New(rand.NewSource(int64(900 + iter)))
		dim := 8
		const n = 24
		for i := 0; i < n; i++ {
			requireNoError(t, s.Put("d-"+itoa(i), randVecN(rng, dim), nil))
		}
		requireNoError(t, s.Seal())
		requireNoError(t, s.WaitForIndex()) // segment 1 fully indexed in "default"

		// In the off-lock merge window, re-pend the input via RebuildVectorIndex and
		// spawn builders against its still-live mmap, fully completing the re-pend
		// before the swap proceeds. Without the fix, the swap's close() of segment 1
		// munmaps the input mid-build → SIGSEGV + data race; with the fix the swap
		// re-validates fullyIndexedLocked, finds segment 1 re-pended, and aborts.
		s.testHookInMergeWindow = func(p *mergePlan) {
			requireNoError(t, s.RebuildVectorIndex("default"))
		}

		requireNoError(t, s.mergeNow([]segID{1}))
		requireNoError(t, s.WaitForMerge())
		requireNoError(t, s.WaitForIndex()) // rebuild builds (and any re-merge) drain

		// Every doc must still be retrievable: the merge aborted, the inputs are intact.
		for i := 0; i < n; i++ {
			if _, _, found, _ := s.Get("d-" + itoa(i)); !found {
				t.Fatalf("iter %d: doc d-%d lost after re-pend-during-merge", iter, i)
			}
		}
		// The index reconverged and Search returns every doc exactly once. (Get above
		// is the authoritative no-data-loss check; this also exercises the reconverged
		// graph leg. The default index has full recall at this size.)
		q := randVecN(rng, dim)
		got, err := s.Search("default", q, n, nil)
		requireNoError(t, err)
		seen := make(map[int64]int, n)
		for _, r := range got {
			seen[r.DocID]++
		}
		for i := 0; i < n; i++ {
			doc := s.idToDoc["d-"+itoa(i)]
			if seen[doc] != 1 {
				t.Fatalf("iter %d: doc d-%d appeared %d times in Search, want 1", iter, i, seen[doc])
			}
		}
		requireNoError(t, s.Close())
		_ = kvStore.Close()
	}
}

// TestMerge_CreateRePendsInputDuringMergeWindow is the same red-proof for the
// IDENTICAL defect in CreateVectorIndex (Task 4): a freshly created index is born
// pending for EVERY sealed segment and immediately spawns builders reading the
// input mmaps. Driven concurrently with a merge's off-lock window, the merge swap
// must re-validate fullyIndexedLocked (which now requires the NEW index too) and
// abort rather than close() an input a new-index builder is mid-read of.
func TestMerge_CreateRePendsInputDuringMergeWindow(t *testing.T) {
	for iter := 0; iter < 30; iter++ {
		kvStore := newTestKV(t)
		s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: DotProduct})
		requireNoError(t, err)
		rng := rand.New(rand.NewSource(int64(1300 + iter)))
		dim := 8
		const n = 24
		for i := 0; i < n; i++ {
			requireNoError(t, s.Put("d-"+itoa(i), randVecN(rng, dim), nil))
		}
		requireNoError(t, s.Seal())
		requireNoError(t, s.WaitForIndex())

		// Create a new index in the off-lock window: it is pending for segment 1 and
		// spawns a builder against the still-live input mmap. fullyIndexedLocked now
		// requires BOTH "default" and "aux"; the swap must re-validate and abort.
		s.testHookInMergeWindow = func(p *mergePlan) {
			requireNoError(t, s.CreateVectorIndex("aux", VectorIndexConfig{Type: "hnsw", Metric: DotProduct, M: 8}))
		}

		requireNoError(t, s.mergeNow([]segID{1}))
		requireNoError(t, s.WaitForMerge())
		requireNoError(t, s.WaitForIndex())

		for i := 0; i < n; i++ {
			if _, _, found, _ := s.Get("d-" + itoa(i)); !found {
				t.Fatalf("iter %d: doc d-%d lost after create-re-pend-during-merge", iter, i)
			}
		}
		// Both indexes survive the abort and return dup-free results without a fault.
		// (Get above is the authoritative no-data-loss check; the aux index's HNSW
		// recall at M=8/k=n is not asserted here — the fix guarantees integrity and
		// race-freedom, not graph recall.)
		q := randVecN(rng, dim)
		for _, name := range []string{"default", "aux"} {
			got, err := s.Search(name, q, n, nil)
			requireNoError(t, err)
			seen := make(map[int64]bool, len(got))
			for _, r := range got {
				if seen[r.DocID] {
					t.Fatalf("iter %d: index %q returned duplicate doc %d", iter, name, r.DocID)
				}
				seen[r.DocID] = true
			}
		}
		requireNoError(t, s.Close())
		_ = kvStore.Close()
	}
}
