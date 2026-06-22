package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// TestRecover_ReopenWriteDoesNotRaceResumedBuild pins the recover() ordering
// invariant: the unlocked Phase-A reopen-write `vx.graphs[sid] = newBuiltIndex`
// for an INDEXED segment must complete before any Phase-B builder goroutine for a
// PENDING segment of the SAME index is spawned. The buggy single-loop recover
// interleaved the two: an indexed segment's unlocked map write could overlap a
// same-index pending segment's builder write under s.mu (no happens-before edge
// joins an unlocked write to a locked one), which the race detector flags and the
// Go runtime turns into a fatal "concurrent map writes" panic.
//
// The scenario forces N indexed segments + 1 pending segment, ALL on the default
// index, with merge fully suppressed so the segments stay distinct across recover.
// A test seam widens each Phase-A reopen-write so the dangerous overlap window —
// the one the buggy code hit only by timing luck — is wide. Run under `go test
// -race`: with the two-phase recover, no builder exists while any reopen-write
// runs, so the widen cannot produce a race. (Doc id: Phase-6 Task-8 recover race.)
func TestRecover_ReopenWriteDoesNotRaceResumedBuild(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, Metric: Cosine})
	requireNoError(t, err)

	// Suppress every merge so each Seal yields a distinct, surviving segment: a
	// roll-up would fold them into one and erase the multi-segment interleaving the
	// race needs. Huge Fanout/TargetSegCount + zero delete-floor disables both
	// drivers; tiny maxSegSize is irrelevant here (we Seal explicitly).
	s.mu.Lock()
	s.mcfg.Fanout = 1 << 20
	s.mcfg.TargetSegCount = 1 << 20
	s.mcfg.MergeFloor = 0
	s.mu.Unlock()

	rng := rand.New(rand.NewSource(914))
	dim := 12
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}

	// Seal N segments and let all of them index (graph-default.dat on disk,
	// IndexSegs state = segIndexed). These become the Phase-A reopen-writes.
	const nIndexed = 6
	const perSeg = 16
	doc := 0
	for seg := 0; seg < nIndexed; seg++ {
		for i := 0; i < perSeg; i++ {
			requireNoError(t, s.Put("d-"+itoa(doc), randVec(), nil))
			doc++
		}
		requireNoError(t, s.Seal())
		requireNoError(t, s.WaitForIndex())
	}
	requireNoError(t, s.Close())

	// Demote a MIDDLE segment to pending: drop its graph file and flip its default-
	// index IndexSegs state to segPending. With the slice-ordered per-segment loop
	// (segIds 1..N), a buggy single-loop recover spawns this pending segment's
	// Phase-B builder mid-loop, then keeps going and performs UNLOCKED reopen-writes
	// for the still-indexed higher-id segments on the SAME default-index graphs map —
	// concurrently with the builder's locked write to it. Putting the pending segment
	// LAST would let every reopen-write finish before the spawn and hide the race, so
	// it must sit in the middle. The pending segment shares the default index with the
	// indexed ones, so they all contend on one vx.graphs map.
	pendingSeg := segID(2) // segIds are 1..nIndexed; segs 3..nIndexed reopen AFTER its spawn
	pendingDir := filepath.Join(dir, segDirName(pendingSeg, 0))
	requireNoError(t, os.Remove(filepath.Join(pendingDir, "graph-default.dat")))
	cs, err := openControlStore(dir)
	requireNoError(t, err)
	flipped := false
	requireNoError(t, cs.update(func(tx *bolt.Tx) error {
		is, ok, gerr := getIndexSeg(tx, defaultIndexName, pendingSeg)
		if gerr != nil {
			return gerr
		}
		if ok {
			is.State = segPending
			if perr := putIndexSeg(tx, is); perr != nil {
				return perr
			}
			flipped = true
		}
		ver, head, met, _, merr := getMeta(tx)
		if merr != nil {
			return merr
		}
		return putMeta(tx, ver+1, head, met)
	}))
	requireNoError(t, cs.Close())
	if !flipped {
		t.Fatalf("did not find IndexSegs entry for (default, seg %d) to demote", pendingSeg)
	}

	// Widen every Phase-A reopen-write. Under the buggy single-loop recover this
	// delay holds the unlocked map write open long enough for the same-index
	// pending segment's builder (spawned mid-loop) to acquire s.mu and write the
	// same map concurrently → -race / concurrent-map-write panic. The two-phase
	// recover spawns no builder until all reopen-writes are done, so this is safe.
	testHookRecoverBeforeReopenWrite = func() { time.Sleep(8 * time.Millisecond) }
	t.Cleanup(func() { testHookRecoverBeforeReopenWrite = nil })

	s2, err := Open(Options{Dir: dir, Metric: Cosine})
	requireNoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })

	// Recover must converge: the demoted segment's build resumes and every segment
	// ends indexed, with no lost data.
	requireNoError(t, s2.WaitForIndex())
	for seg := segID(1); seg <= segID(nIndexed); seg++ {
		if !s2.isIndexedForTest(seg) {
			t.Fatalf("segment %d not indexed after recover+WaitForIndex", seg)
		}
	}
	for i := 0; i < doc; i++ {
		if _, _, found, _ := s2.Get("d-" + itoa(i)); !found {
			t.Fatalf("doc d-%d lost after recover", i)
		}
	}
	if _, err := os.Stat(filepath.Join(pendingDir, "graph-default.dat")); err != nil {
		t.Fatalf("resumed build did not rebuild graph-default.dat for the pending segment: %v", err)
	}
}
