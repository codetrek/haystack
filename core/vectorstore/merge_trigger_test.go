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
	b := s.NewBatch()
	for i := 0; i < 20; i++ {
		b.Put("d-"+itoa(i), []float32{float32(i), 1, 0, 0, 0, 0, 0, 0}, nil)
	}
	requireNoError(t, b.Commit())
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

// TestAutoMerge_GrowthTriggersOnSeal: with a small fanout, sealing enough full
// same-tier segments auto-triggers a growth-driven merge in the background — no
// manual Compact call. The trigger only picks INDEXED inputs (a pending just-
// sealed segment is never a merge input — appendix #3/#8 prevents discarding an
// in-flight build and the close-during-build SIGSEGV). The K-th segment's own
// seal-trigger sees only K-1 indexed peers (it is still pending), so the roll-up
// fires on the K-th segment's BUILD completion instead (finding #1's re-trigger):
// once all three count-5 segments are indexed they fold into one — with no extra
// seal and no Compact() call.
func TestAutoMerge_GrowthTriggersOnSeal(t *testing.T) {
	s := openTestStore(t, Cosine)
	s.maxSegSize = 5      // tiny segments
	s.mcfg.Fanout = 3     // a tier of 3 same-sized segments triggers a roll-up
	s.mcfg.MergeFloor = 0 // disable delete driver for this test
	s.mcfg.TargetSegCount = 1000
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

	// Seal two count-5 (tier-2) segments, indexing each so it is an eligible merge
	// input. The trigger on each seal/build sees fewer than fanout=3 INDEXED tier
	// peers, so no merge fires yet.
	for i := 0; i < 10; i++ {
		put(i)
		if (i+1)%5 == 0 {
			requireNoError(t, s.WaitForIndex())
			requireNoError(t, s.WaitForMerge())
		}
	}
	s.mu.RLock()
	before := len(s.sealed)
	s.mu.RUnlock()
	if before != 2 {
		t.Fatalf("before the K-th seal: nSealed=%d, want 2 (no premature merge)", before)
	}

	// Seal the THIRD count-5 segment. Its seal-trigger sees only 2 INDEXED peers (it
	// is still pending), so no roll-up there. The fix's re-trigger fires when this
	// third segment's background build COMPLETES — folding all three into one.
	for i := 10; i < 15; i++ {
		put(i)
	}
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

	// The three count-5 segments rolled up into one 15-row segment.
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.sealed) != 1 {
		counts := make([]int, len(s.sealed))
		for i, ss := range s.sealed {
			counts[i] = ss.count()
		}
		t.Fatalf("auto-trigger did not roll up the count-5 tier: seg counts=%v, want one 15-row merge output", counts)
	}
	if s.sealed[0].count() != 15 {
		t.Fatalf("rolled-up segment row count = %d, want 15", s.sealed[0].count())
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
// WaitGroup discipline (no idtable/control-store teardown racing a Put). -race +
// repetition is the real gate.
func TestAutoMerge_CloseRaceNoPanic(t *testing.T) {
	// This is a probabilistic race (no deterministic seam) — it needs many rounds
	// to surface a Close-vs-Compact Add-after-Wait. Full count runs on Linux; under
	// -short (macOS/Windows CI, where each store lifecycle pays a heavy FS/AV tax) a
	// reduced count keeps decent coverage without dominating the run.
	iters := 80
	if testing.Short() {
		iters = 20
	}
	for iter := 0; iter < iters; iter++ {
		kvStore := newTestKV(t)
		s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: Cosine})
		requireNoError(t, err)
		s.maxSegSize = 3
		s.mcfg.Fanout = 2
		s.mcfg.MergeFloor = 0.99 // almost everything is delete-driven bait

		// Build a couple of indexed sealed segments so Compact has real merge
		// candidates: 6 docs / maxSegSize=3 → 2 segments, which is exactly Fanout=2,
		// and MergeFloor=0.99 makes them delete-driven bait — enough for Compact to
		// launch a merge (and Add to the WaitGroup) so the Close race is exercised.
		// Kept minimal per iteration: the race coverage is the iteration COUNT, not the
		// per-iteration data volume (each store lifecycle pays a heavy FS tax on CI).
		for i := 0; i < 6; i++ {
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
