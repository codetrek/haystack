package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
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
