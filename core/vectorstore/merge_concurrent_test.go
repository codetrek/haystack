package vectorstore

import (
	"math/rand"
	"testing"
)

// TestMerge_DeleteDuringWindow_NotResurrected proves the reconciliation gate
// (mergeAndPublish step 2a): a doc deleted AFTER the merge's live-set snapshot but
// BEFORE the atomic swap must not come back to life in the output segment. We open
// the window explicitly: plan (snapshot) under the lock, release, Delete two docs,
// then publish. The output must tombstone those docs at swap time (docToSeg is the
// liveness oracle).
func TestMerge_DeleteDuringWindow_NotResurrected(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(21))
	dim := 8
	randVec := func() []float32 { return randVecN(rng, dim) }
	live := map[int64][]float32{}
	for i := 0; i < 30; i++ {
		id := "d-" + itoa(i)
		v := randVec()
		requireNoError(t, s.Put(id, v, nil))
		live[s.idToDoc[id]] = v
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	// (1) Snapshot the merge plan under the lock (docs d-0..d-29 all live).
	s.mu.Lock()
	p, err := s.planMergeLocked([]segID{1})
	requireNoError(t, err)
	if p == nil {
		s.mu.Unlock()
		t.Fatal("planMergeLocked returned nil (input not indexed?)")
	}
	s.mergeBeginLocked(1)
	s.mu.Unlock()

	// (2) In the OFF-LOCK window, delete two docs that the plan captured as live.
	requireNoError(t, s.Delete("d-7"))
	requireNoError(t, s.Delete("d-13"))
	delete(live, s.idToDoc["d-7"])
	delete(live, s.idToDoc["d-13"])

	// (3) Publish: the swap must reconcile the two late deletes.
	requireNoError(t, s.mergeAndPublish(p))
	requireNoError(t, s.WaitForIndex())

	// The deleted docs must be gone everywhere.
	if _, _, found, _ := s.Get("d-7"); found {
		t.Fatal("d-7 deleted during merge window resurrected in the output")
	}
	if _, _, found, _ := s.Get("d-13"); found {
		t.Fatal("d-13 deleted during merge window resurrected in the output")
	}
	// They must not appear in Search results either.
	for it := 0; it < 30; it++ {
		got, err := s.Search("default", randVec(), 30, nil)
		requireNoError(t, err)
		for _, r := range got {
			if r.DocID == s.idToDoc["d-7"] || r.DocID == s.idToDoc["d-13"] {
				t.Fatalf("deleted-during-merge doc %d appeared in Search", r.DocID)
			}
		}
	}
	// Survivors intact + recall holds.
	var sum float64
	for it := 0; it < 20; it++ {
		q := randVec()
		got, err := s.Search("default", q, 5, nil)
		requireNoError(t, err)
		sum += recallAtK(got, bruteForceKNN(Cosine, q, live, 5))
	}
	if avg := sum / 20; avg < 0.8 {
		t.Fatalf("post-merge recall@5 = %.3f, want >= 0.8", avg)
	}
}

// TestMerge_PutRehomeDuringWindow_NotDuplicated: a concurrent Put re-homes an input
// doc to the head DURING the merge window. The merged output must NOT also claim it
// live (docToSeg now points at head, not the input) — no duplicate live copy, and
// Get returns the new head vector.
func TestMerge_PutRehomeDuringWindow_NotDuplicated(t *testing.T) {
	s := openTestStore(t, DotProduct)
	for i := 0; i < 10; i++ {
		requireNoError(t, s.Put("d-"+itoa(i), []float32{float32(i), 1, 0}, nil))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	s.mu.Lock()
	p, err := s.planMergeLocked([]segID{1})
	requireNoError(t, err)
	if p == nil {
		s.mu.Unlock()
		t.Fatal("planMergeLocked returned nil (input not indexed?)")
	}
	s.mergeBeginLocked(1)
	s.mu.Unlock()

	// Re-Put d-4 with a new vector during the window → rehomed to head.
	requireNoError(t, s.Put("d-4", []float32{99, 99, 99}, nil))
	requireNoError(t, s.mergeAndPublish(p))
	requireNoError(t, s.WaitForIndex())

	d4 := s.idToDoc["d-4"]
	s.mu.RLock()
	owner := s.docToSeg[d4]
	s.mu.RUnlock()
	if owner != headSegID {
		t.Fatalf("d-4 owner after merge = %d, want headSegID (rehomed by concurrent Put)", owner)
	}
	v, _, found, err := s.Get("d-4")
	requireNoError(t, err)
	if !found || len(v) != 3 || v[0] != 99 {
		t.Fatalf("Get(d-4) = (%v, found=%v), want new head vector {99,99,99}", v, found)
	}
	// d-4 must appear exactly once across Search (no duplicate from the output).
	got, err := s.Search("default", []float32{99, 99, 99}, 20, nil)
	requireNoError(t, err)
	count := 0
	for _, r := range got {
		if r.DocID == d4 {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("d-4 appears %d times in Search, want exactly 1", count)
	}
}

// TestMerge_DeleteDuringWindow_DurableAcrossRestart proves the reconcile tombstone
// is DURABLE, not merely in-memory (appendix #7 — the highest-risk durability path
// the in-process tests do not cover). We open the merge window, Delete an input
// doc, then drive mergeAndPublish to JUST PAST the control-store swap and crash there
// (testHookAfterSwap), so the output is committed but its background build never
// ran. step 2a tombstoned the deleted doc in the output, and commitMergeLocked wrote
// that reconcile tombstone into the bbolt tomb bucket IN THE SAME swap txn (incr 3 —
// no separate tomb.dat msync) — so on reopen the doc must stay dead (its liveness is
// reconstructed from the durable docseg + tomb buckets, not an on-disk bitmap rescan).
func TestMerge_DeleteDuringWindow_DurableAcrossRestart(t *testing.T) {
	kvStore := newTestKV(t)
	s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	rng := rand.New(rand.NewSource(22))
	live := map[int64][]float32{}
	for i := 0; i < 30; i++ {
		id := "d-" + itoa(i)
		v := randVecN(rng, 8)
		requireNoError(t, s.Put(id, v, nil))
		live[s.idToDoc[id]] = v
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	// Crash right after the swap commits, before os.RemoveAll / build spawn.
	s.testHookAfterSwap = func(p *mergePlan) bool { return true }

	// (1) Snapshot under the lock, (2) Delete in the off-lock window, (3) publish.
	s.mu.Lock()
	p, err := s.planMergeLocked([]segID{1})
	requireNoError(t, err)
	if p == nil {
		s.mu.Unlock()
		t.Fatal("planMergeLocked returned nil (input not indexed?)")
	}
	s.mergeBeginLocked(1)
	s.mu.Unlock()

	requireNoError(t, s.Delete("d-9"))
	delete(live, s.idToDoc["d-9"])

	requireNoError(t, s.mergeAndPublish(p)) // commits the swap, then aborts at the seam

	// Reopen: docToSeg is rebuilt from the durable docseg bucket and each segment's
	// tomb bucket, so the reconcile tombstone (+ the deleted input's docseg removal)
	// MUST be durable in the control store or d-9 resurrects.
	s2 := reopenStore(t, s, kvStore)
	requireNoError(t, s2.WaitForIndex())

	if _, _, found, _ := s2.Get("d-9"); found {
		t.Fatal("crash-after-reconcile: d-9 deleted in the merge window resurrected after restart (tombstone not durable in the bbolt tomb bucket)")
	}
	// Every survivor present.
	for i := 0; i < 30; i++ {
		if i == 9 {
			continue
		}
		if _, _, found, _ := s2.Get("d-" + itoa(i)); !found {
			t.Fatalf("crash-after-reconcile: survivor d-%d lost after restart", i)
		}
	}
	// d-9 absent from Search too, recall holds over the survivor set.
	d9 := s.idToDoc["d-9"]
	var sum float64
	for it := 0; it < 20; it++ {
		q := randVecN(rng, 8)
		got, err := s2.Search("default", q, 5, nil)
		requireNoError(t, err)
		for _, r := range got {
			if r.DocID == d9 {
				t.Fatal("crash-after-reconcile: d-9 appeared in Search after restart")
			}
		}
		sum += recallAtK(got, bruteForceKNN(Cosine, q, live, 5))
	}
	if avg := sum / 20; avg < 0.8 {
		t.Fatalf("crash-after-reconcile: post-restart recall@5 = %.3f, want >= 0.8", avg)
	}
}
