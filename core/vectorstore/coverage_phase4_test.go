package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// --- Phase-4 final-review coverage backfill (clearing the go-cov 80% floor) ---

// TestLiveRatio_ZeroCountIsOne covers segLiveStats.liveRatio's count==0 guard: a
// zero-row segment has no reclaimable space, so its live ratio is defined as 1
// (never delete-driven bait). The driver tests only ever see non-empty segments,
// leaving this branch uncovered.
func TestLiveRatio_ZeroCountIsOne(t *testing.T) {
	if r := (segLiveStats{count: 0, live: 0}).liveRatio(); r != 1 {
		t.Fatalf("liveRatio(count=0) = %v, want 1", r)
	}
	// And the normal ratio path for contrast.
	if r := (segLiveStats{count: 4, live: 1}).liveRatio(); r != 0.25 {
		t.Fatalf("liveRatio(1/4) = %v, want 0.25", r)
	}
}

// TestMergeNow_NoOpWhenClosing covers mergeNow's s.closing early return: once the
// store is closing, no new merge may launch (a mergeBegin must not race Close's
// quiescence drain). The call is a no-op that returns nil.
func TestMergeNow_NoOpWhenClosing(t *testing.T) {
	s := openTestStore(t, Cosine)
	for i := 0; i < 6; i++ {
		requireNoError(t, s.Put("d-"+itoa(i), []float32{float32(i), 1, 0, 0, 0, 0, 0, 0}, nil))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	s.mu.Lock()
	s.closing = true // simulate Close having begun
	s.mu.Unlock()

	if err := s.mergeNow([]segID{1}); err != nil {
		t.Fatalf("mergeNow while closing = %v, want nil (no-op)", err)
	}
	// Nothing launched: the sealed set is unchanged.
	s.mu.RLock()
	n := len(s.sealed)
	s.mu.RUnlock()
	if n != 1 {
		t.Fatalf("mergeNow while closing mutated the set: nSealed=%d, want 1", n)
	}

	// Reset closing so the test-store Cleanup Close() can drain normally.
	s.mu.Lock()
	s.closing = false
	s.mu.Unlock()
}

// TestMergeNow_NoOpWhenInputPending covers mergeNow's p==nil early return: a merge
// of a still-PENDING (unbuilt) segment plans to nil (planMergeLocked skips an
// un-indexed input to avoid close-during-build), so mergeNow returns nil without
// launching anything.
func TestMergeNow_NoOpWhenInputPending(t *testing.T) {
	s := openTestStore(t, Cosine)
	// A segId that does not exist plans to nil (sealedByID == nil).
	if err := s.mergeNow([]segID{12345}); err != nil {
		t.Fatalf("mergeNow(unknown id) = %v, want nil", err)
	}
	requireNoError(t, s.WaitForMerge())
	s.mu.RLock()
	n := len(s.sealed)
	s.mu.RUnlock()
	if n != 0 {
		t.Fatalf("mergeNow(unknown id) launched a merge: nSealed=%d, want 0", n)
	}
}

// TestSealLocked_WriteFaultPropagates covers sealLocked's writeSealedSegment error
// branch: a write fault on the head dump aborts the seal with the error surfaced,
// leaving the head intact (nothing published).
func TestSealLocked_WriteFaultPropagates(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(101))
	for i := 0; i < 6; i++ {
		requireNoError(t, s.Put("d-"+itoa(i), randVecN(rng, 8), nil))
	}
	withCreateFault(t, func(f *faultFile) { f.failWrite = true })
	if err := s.Seal(); err == nil {
		t.Fatal("Seal should surface a writeSealedSegment write fault")
	}
	// Head not published: still no sealed segments, head still holds the 6 docs.
	s.mu.RLock()
	n := len(s.sealed)
	headRows := len(s.seg.slotDoc)
	s.mu.RUnlock()
	if n != 0 || headRows != 6 {
		t.Fatalf("after faulted Seal: nSealed=%d headRows=%d, want 0 / 6 (seal aborted, head intact)", n, headRows)
	}
}

// TestCommitSealLocked_ReconcileErrorRollsBack covers commitSealLocked's in-txn
// error branch: when reconcileControlTx fails inside the seal write-txn, the whole
// bbolt commit rolls back — neither the new segment row NOR the head-bucket clear
// is persisted (the seal's atomicity guarantee). Seal surfaces the error; the
// sealed flat files written before the commit become a crash-before-commit orphan.
// A crash-reopen must recover all docs from the still-intact head bucket, proving
// the head clear did NOT commit, and must sweep the orphaned segment dir.
func TestCommitSealLocked_ReconcileErrorRollsBack(t *testing.T) {
	kvStore := newTestKV(t)
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	rng := rand.New(rand.NewSource(909))
	for i := 0; i < 6; i++ {
		requireNoError(t, s.Put("d-"+itoa(i), randVecN(rng, 8), nil))
	}
	testHookReconcileErr = errInjected
	if err := s.Seal(); err == nil {
		testHookReconcileErr = nil
		t.Fatal("Seal should surface a reconcile error from the seal commit")
	}
	testHookReconcileErr = nil

	// Crash-reopen over the same dir + KV: the seal commit rolled back, so the head
	// bucket still holds all 6 docs and the uncommitted seg dir is swept as an orphan.
	s2 := reopenUnclean(t, s, kvStore)
	for i := 0; i < 6; i++ {
		if _, _, found, _ := s2.Get("d-" + itoa(i)); !found {
			t.Fatalf("doc d-%d lost after a rolled-back seal commit (head bucket clear must not have committed)", i)
		}
	}
	s2.mu.RLock()
	nSealed := len(s2.sealed)
	s2.mu.RUnlock()
	if nSealed != 0 {
		t.Fatalf("sealed segments after rolled-back seal = %d, want 0 (orphan dir must be swept)", nSealed)
	}
}

// TestCommitMergeLocked_ReconcileErrorRollsBack covers commitMergeLocked's in-txn
// error branch (and mergeAndPublish's swap-commit error return): when
// reconcileControlTx fails inside the merge swap write-txn, the whole bbolt commit
// rolls back — neither the output's segment/docseg/tomb rows NOR the input retirement
// is persisted (the merge's atomicity guarantee). The merge surfaces the error; the
// inputs stay live and intact, and the freshly written outputs become a
// crash-before-commit orphan a later recover sweeps. We drive a real merge to the
// swap with testHookReconcileErr armed, then confirm every doc survives.
func TestCommitMergeLocked_ReconcileErrorRollsBack(t *testing.T) {
	kvStore := newTestKV(t)
	s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	defer s.Close() // release output mmaps so t.TempDir cleanup works on Windows
	rng := rand.New(rand.NewSource(808))
	for i := 0; i < 12; i++ {
		requireNoError(t, s.Put("d-"+itoa(i), randVecN(rng, 8), nil))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex()) // no pending builds → the hook only hits the merge commit

	// Arm the in-txn reconcile error so the merge swap commit (commitMergeLocked)
	// fails and rolls back. mergeNow plans under the lock (fine) and the commit runs
	// off-lock during WaitForMerge.
	testHookReconcileErr = errInjected
	requireNoError(t, s.mergeNow([]segID{1}))
	requireNoError(t, s.WaitForMerge())
	testHookReconcileErr = nil

	// The merge rolled back: the input is still live (one sealed segment) and every
	// doc is readable.
	s.mu.RLock()
	nSealed := len(s.sealed)
	s.mu.RUnlock()
	if nSealed != 1 {
		t.Fatalf("sealed segments after a rolled-back merge = %d, want 1 (input must stay live)", nSealed)
	}
	for i := 0; i < 12; i++ {
		if _, _, found, _ := s.Get("d-" + itoa(i)); !found {
			t.Fatalf("doc d-%d lost after a rolled-back merge commit", i)
		}
	}
}

// TestSealLocked_NoBuildSpawnWhenClosing covers sealLocked's s.closing branch (the
// late return that skips the background build spawn): the segment is durably sealed
// + pending, but no builder goroutine is launched (recover would resume it). We
// seal directly via sealLocked under the lock with closing set, then clear closing.
func TestSealLocked_NoBuildSpawnWhenClosing(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(102))
	for i := 0; i < 6; i++ {
		requireNoError(t, s.Put("d-"+itoa(i), randVecN(rng, 8), nil))
	}
	s.mu.Lock()
	s.closing = true
	err := s.sealLocked() // seals durably but skips the build spawn (and maybeMerge)
	s.mu.Unlock()
	requireNoError(t, err)

	// The segment is sealed + pending (no graph installed, no builder spawned).
	s.mu.RLock()
	n := len(s.sealed)
	pending := n == 1 && s.indexes[defaultIndexName].graphs[s.sealedID[0]] == nil
	inflight := s.nInflightBuilds
	s.mu.RUnlock()
	if !pending {
		t.Fatalf("sealLocked(closing) did not publish a pending segment: nSealed=%d", n)
	}
	if inflight != 0 {
		t.Fatalf("sealLocked(closing) spawned a build: nInflightBuilds=%d, want 0", inflight)
	}
	// The records-segment is durable on disk.
	if _, err := os.Stat(filepath.Join(s.dir, "seg-1-0")); err != nil {
		t.Fatalf("sealLocked(closing): segment not durable on disk: %v", err)
	}

	// Clear closing so Cleanup Close() drains cleanly.
	s.mu.Lock()
	s.closing = false
	s.mu.Unlock()
}

// TestSortStatsByCountAsc_StableAndSorted covers sortStatsByCountAsc across both
// the swap and no-swap inner-loop branches (already-sorted prefix vs out-of-order
// tail), which the integration paths do not deterministically exercise.
func TestSortStatsByCountAsc_StableAndSorted(t *testing.T) {
	// Already sorted (no swap ever) + a reversed run (swap every step) + a tie.
	a := []segLiveStats{
		{id: 1, count: 1},
		{id: 2, count: 5},
		{id: 3, count: 5}, // tie with id 2 — stable order must keep 2 before 3
		{id: 4, count: 3}, // out of order → triggers swaps back past the 5s
		{id: 5, count: 2},
	}
	sortStatsByCountAsc(a)
	wantCounts := []int{1, 2, 3, 5, 5}
	for i, st := range a {
		if st.count != wantCounts[i] {
			t.Fatalf("sortStatsByCountAsc => counts %v at %d, want %v", st.count, i, wantCounts)
		}
	}
	// Stable: the two count-5 entries keep input order (id 2 before id 3).
	var firstFive, secondFive segID
	seen := false
	for _, st := range a {
		if st.count == 5 {
			if !seen {
				firstFive = st.id
				seen = true
			} else {
				secondFive = st.id
			}
		}
	}
	if firstFive != 2 || secondFive != 3 {
		t.Fatalf("sortStatsByCountAsc not stable on ties: 5s came out %d,%d, want 2,3", firstFive, secondFive)
	}

	// A single-element and empty slice must be no-ops (no inner-loop iterations).
	sortStatsByCountAsc(nil)
	one := []segLiveStats{{id: 9, count: 7}}
	sortStatsByCountAsc(one)
	if one[0].id != 9 {
		t.Fatal("sortStatsByCountAsc mutated a single-element slice")
	}
}

// TestSortIntsAsc_SwapAndNoSwap covers sortIntsAsc's inner swap and no-swap
// branches (an already-sorted run takes the loop-condition-false path; a reversed
// run swaps each step).
func TestSortIntsAsc_SwapAndNoSwap(t *testing.T) {
	a := []int{3, 1, 2, 2, 5, 4}
	sortIntsAsc(a)
	want := []int{1, 2, 2, 3, 4, 5}
	for i := range want {
		if a[i] != want[i] {
			t.Fatalf("sortIntsAsc = %v, want %v", a, want)
		}
	}
	// Already sorted: no swaps.
	b := []int{1, 2, 3}
	sortIntsAsc(b)
	if b[0] != 1 || b[1] != 2 || b[2] != 3 {
		t.Fatalf("sortIntsAsc(sorted) = %v, want [1 2 3]", b)
	}
}
