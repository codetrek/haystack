package vectorstore

import (
	"math/rand"
	"testing"
)

// TestAutoMerge_GrowthTriggersOnBuildCompletion red-proofs Phase-4 finding #1
// (the background auto-trigger growth gap): the on-SEAL trigger can never roll up
// the K-th of K full maxSegSize segments, because when the K-th seal fires
// maybeMergeLocked that K-th segment is itself still PENDING — so
// planReclamationLocked sees only K-1 INDEXED tier peers (below fanout) and the
// anti-thrash guard (graphs[id]==nil in planMergeWithCapLocked) skips the tier.
// After the K-th segment's background build COMPLETES (pending→indexed) the OLD
// code did NOT re-evaluate the policy, and no further seal/write is guaranteed to
// follow — so the tier stays at [N N N] forever (the [10 10 10] never folds to
// [30] without an explicit Compact() or another write).
//
// The fix re-evaluates the merge policy when a build completes (buildAndPublish's
// pending→indexed flip), so once the K-th segment finishes building the growth
// driver rolls the tier up — with NO explicit Compact() and NO further Put.
//
// This test isolates the TIERED-GROWTH (fanout) driver: TargetSegCount is set
// high so the count-cap fallback (finding #2, tested separately) never fires, and
// each segment is fully indexed before the next is sealed so all K land in the
// SAME size tier with no build-vs-seal race. The red-proof is the K-th seal: with
// the fix reverted the store quiesces at [N N N]; with the fix it folds to [K*N].
func TestAutoMerge_GrowthTriggersOnBuildCompletion(t *testing.T) {
	s := openTestStore(t, Cosine)
	const segSize = 5
	const fanout = 3
	s.maxSegSize = segSize
	s.mcfg.Fanout = fanout
	s.mcfg.MergeFloor = 0          // disable the delete driver: isolate the growth path
	s.mcfg.TargetSegCount = 1000   // disable the count-cap fallback: isolate the fanout path
	s.mcfg.MaxMergedSize = 1 << 20 // roomy enough to fold all Fanout segments into one

	rng := rand.New(rand.NewSource(71))
	dim := 8
	put := func(i int) { requireNoError(t, s.Put("d-"+itoa(i), randVecN(rng, dim), nil)) }

	// Seal the FIRST K-1 full segments, indexing each before sealing the next so all
	// land in the same size tier and there is no build-vs-seal race. After each of
	// these the on-seal trigger sees < fanout INDEXED tier peers (the just-sealed one
	// is pending; once indexed it is still only the (i+1)-th of K), so NO merge fires.
	idx := 0
	for seg := 0; seg < fanout-1; seg++ {
		for r := 0; r < segSize; r++ {
			put(idx)
			idx++
		}
		requireNoError(t, s.WaitForIndex()) // index this segment; no merge yet
		requireNoError(t, s.WaitForMerge())
	}
	s.mu.RLock()
	beforeK := len(s.sealed)
	s.mu.RUnlock()
	if beforeK != fanout-1 {
		t.Fatalf("setup: nSealed=%d before the K-th seal, want %d (no premature merge)", beforeK, fanout-1)
	}

	// Seal the K-th full segment. Its OWN seal-trigger sees only K-1 INDEXED peers
	// (it is still pending) → no roll-up. The fix's re-trigger must fire when this
	// K-th segment's background build completes, folding all K into one.
	for r := 0; r < segSize; r++ {
		put(idx)
		idx++
	}
	// Quiesce: WaitForIndex drains builds AND the merge a build completion spawns,
	// then that merge's output build, looping until the count stops moving (each
	// growth roll-up strictly reduces the segment count, so this terminates).
	prev := -1
	for {
		requireNoError(t, s.WaitForIndex())
		requireNoError(t, s.WaitForMerge())
		requireNoError(t, s.WaitForIndex())
		s.mu.RLock()
		n := len(s.sealed)
		s.mu.RUnlock()
		if n == prev {
			break
		}
		prev = n
	}

	s.mu.RLock()
	nSealed := len(s.sealed)
	counts := make([]int, len(s.sealed))
	for i, ss := range s.sealed {
		counts[i] = ss.count()
	}
	s.mu.RUnlock()

	if nSealed != 1 {
		t.Fatalf("growth driver never rolled up after the K-th build completed: "+
			"nSealed=%d (counts=%v), want 1 (folded [%d*%d]) WITHOUT Compact/Put",
			nSealed, counts, fanout, segSize)
	}
	if counts[0] != fanout*segSize {
		t.Fatalf("rolled-up segment row count = %d, want %d (no doc lost)", counts[0], fanout*segSize)
	}
	for i := 0; i < fanout*segSize; i++ {
		if _, _, found, _ := s.Get("d-" + itoa(i)); !found {
			t.Fatalf("doc d-%d lost across the build-completion roll-up", i)
		}
	}
}
