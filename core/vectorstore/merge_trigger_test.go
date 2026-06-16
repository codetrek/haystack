package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestCompact_ReclaimsHeavyTombstoneSegment: Compact() finds the heavy-tombstone
// segment via the delete driver and reclaims it with no explicit id list.
func TestCompact_ReclaimsHeavyTombstoneSegment(t *testing.T) {
	s := openTestStore(t, Cosine)
	s.mcfg.MergeFloor = 0.5
	rng := rand.New(rand.NewSource(31))
	dim := 8
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	for i := 0; i < 40; i++ {
		requireNoError(t, s.Put("d-"+itoa(i), randVec(), nil))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	oldDir := filepath.Join(s.dir, "seg-1-0")
	for i := 0; i < 40; i++ {
		if i%5 < 3 { // 60% deleted → liveRatio 0.4 < 0.5
			requireNoError(t, s.Delete("d-"+itoa(i)))
		}
	}

	requireNoError(t, s.Compact())
	requireNoError(t, s.WaitForMerge())

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatal("Compact did not reclaim the heavy-tombstone segment")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.sealed) != 1 || s.sealed[0].tombCount() != 0 {
		t.Fatalf("after Compact: nSealed=%d tomb=%d, want 1 seg / 0 tomb", len(s.sealed), s.sealed[0].tombCount())
	}
}

// TestCompact_NoOpWhenHealthy: nothing to reclaim → Compact is a no-op (segId stable).
func TestCompact_NoOpWhenHealthy(t *testing.T) {
	s := openTestStore(t, Cosine)
	for i := 0; i < 20; i++ {
		requireNoError(t, s.Put("d-"+itoa(i), []float32{float32(i), 1, 0, 0, 0, 0, 0, 0}, nil))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	requireNoError(t, s.Compact())
	requireNoError(t, s.WaitForMerge())

	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.sealed) != 1 || s.sealedID[0] != segID(1) {
		t.Fatalf("healthy Compact mutated the set: nSealed=%d id=%d", len(s.sealed), s.sealedID[0])
	}
}

// TestAutoMerge_GrowthTriggersOnSeal: with a small fanout, sealing enough segments
// auto-triggers a growth-driven merge in the background — no manual Compact call.
// The trigger only picks INDEXED inputs (a pending just-sealed segment is never a
// merge input — appendix #3/#8 prevents discarding an in-flight build and the
// close-during-build SIGSEGV). So the three count-5 tier segments are rolled up by
// the auto-trigger of a LATER seal, once all three are indexed. No Compact() call.
func TestAutoMerge_GrowthTriggersOnSeal(t *testing.T) {
	s := openTestStore(t, Cosine)
	s.maxSegSize = 5      // tiny segments
	s.mcfg.Fanout = 3     // a tier of 3 same-sized segments triggers a roll-up
	s.mcfg.MergeFloor = 0 // disable delete driver for this test
	rng := rand.New(rand.NewSource(41))
	dim := 8
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	put := func(i int) { requireNoError(t, s.Put("d-"+itoa(i), randVec(), nil)) }

	// Seal three count-5 (tier-2) segments, indexing each so it is an eligible merge
	// input. The trigger on each seal sees fewer than fanout=3 INDEXED tier peers
	// (the just-sealed one is still pending), so no merge fires yet.
	for i := 0; i < 15; i++ {
		put(i)
		if (i+1)%5 == 0 {
			requireNoError(t, s.WaitForIndex()) // index the seg auto-sealed at row 5
			requireNoError(t, s.WaitForMerge())
		}
	}
	s.mu.RLock()
	before := len(s.sealed)
	s.mu.RUnlock()
	if before != 3 {
		t.Fatalf("before re-trigger: nSealed=%d, want 3 (no premature merge)", before)
	}

	// All three tier-2 segments are now indexed. Force ONE more seal of a smaller
	// (tier-0) head: its auto-trigger re-evaluates the policy and — finding three
	// INDEXED count-5 peers at fanout — launches the growth roll-up. The tier-0
	// segment is a different tier, so it is not swept into the roll-up.
	put(15)
	requireNoError(t, s.Seal()) // seg 4 (count 1, tier 0); trigger fires here
	requireNoError(t, s.WaitForIndex())
	requireNoError(t, s.WaitForMerge())

	// The three count-5 segments rolled up into one; the lone tier-0 seg remains.
	s.mu.RLock()
	defer s.mu.RUnlock()
	var rolled *sealedSegment
	for _, ss := range s.sealed {
		if ss.count() == 15 {
			rolled = ss
		}
	}
	if rolled == nil {
		counts := make([]int, len(s.sealed))
		for i, ss := range s.sealed {
			counts[i] = ss.count()
		}
		t.Fatalf("auto-trigger did not roll up the count-5 tier: seg counts=%v, want a 15-row merge output", counts)
	}
	if len(s.sealed) != 2 {
		t.Fatalf("after auto-merge: nSealed=%d, want 2 (15-row roll-up + the tier-0 seg)", len(s.sealed))
	}
}

// TestAutoMerge_CloseRaceNoPanic red-proofs appendix #1: a Close() racing a merge
// launch must NOT panic with "sync: WaitGroup misuse: Add called concurrently with
// Wait". Close sets s.closing under s.mu before merges.Wait(); every launch site
// (Compact/maybeMergeLocked/mergeNow) checks s.closing under s.mu before
// merges.Add, so a merges.Add and the zero-counter merges.Wait can never
// interleave. We set up a store with real merge candidates, then fire many
// concurrent Compact() launches against a single Close(). The Compact callers stop
// issuing once Close begins via the same s.closing gate, so this isolates the
// WaitGroup discipline (no idtable/WAL teardown racing a Put). -race + repetition
// is the real gate.
func TestAutoMerge_CloseRaceNoPanic(t *testing.T) {
	for iter := 0; iter < 80; iter++ {
		kvStore := newTestKV(t)
		s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: Cosine})
		requireNoError(t, err)
		s.maxSegSize = 3
		s.mcfg.Fanout = 2
		s.mcfg.MergeFloor = 0.99 // almost everything is delete-driven bait

		// Build a few indexed sealed segments so Compact has real merge candidates.
		for i := 0; i < 12; i++ {
			requireNoError(t, s.Put("d-"+itoa(i), []float32{float32(i), 1, 0, 0, 0, 0, 0, 0}, nil))
		}
		requireNoError(t, s.WaitForIndex())

		// Fire concurrent Compact() launches (each may Add to merges) against Close()
		// (which Waits). The s.closing gate must keep the Add from racing the Wait.
		var wg sync.WaitGroup
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for c := 0; c < 25; c++ {
					_ = s.Compact() // no-op once closing; never Adds after Close started
				}
			}()
		}
		_ = s.Close()
		wg.Wait()
	}
}
