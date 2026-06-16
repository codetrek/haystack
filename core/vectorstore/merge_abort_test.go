package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAbortMerge_WriteFaultRollsBackCleanly RED-proofs the abortMerge off-lock
// write-failure cleanup (merge.go ~338): when a merge's off-lock bucket write fails
// AFTER one or more earlier buckets were already written+reopened, abortMerge must
// close + delete the already-written outputs, leave the INPUTS untouched (still the
// authoritative, manifest-referenced segments), and leave the store recoverable —
// no half-merged state, no orphan, no data loss.
//
// We force a 2-bucket merge (maxSegSize small, two full input segments → 10 live
// docs packed into two ≤5-row buckets), then inject a write fault on the SECOND
// output bucket. mergeAndPublish writes+reopens bucket #1 (seg-3-0), then the write
// of bucket #2 (seg-4-0) fails → abortMerge(p, outSS, upto=1) runs: it closes the
// reopened bucket #1 and os.RemoveAll's its dir. The merge returns the error; the
// manifest was never rewritten, so the two inputs stay authoritative.
func TestAbortMerge_WriteFaultRollsBackCleanly(t *testing.T) {
	kvStore := newTestKV(t)
	s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	s.maxSegSize = 5 // delete-driven repack packs into 5-row buckets
	rng := rand.New(rand.NewSource(91))
	dim := 8

	// Two full count-5 segments (ids 1 and 2), each indexed so the merge can consume
	// them (the merge only picks INDEXED inputs). 10 live docs → two 5-row buckets.
	for i := 0; i < 10; i++ {
		requireNoError(t, s.Put("d-"+itoa(i), randVecN(rng, dim), nil))
	}
	requireNoError(t, s.WaitForIndex())
	requireNoError(t, s.WaitForMerge())

	s.mu.RLock()
	nextSeg := s.nextSeg // outputs will be allocated from here: seg-<nextSeg>, seg-<nextSeg+1>
	s.mu.RUnlock()
	if nextSeg != 3 {
		t.Fatalf("setup: nextSeg=%d, want 3 (two sealed inputs)", nextSeg)
	}
	firstOut := segDirName(nextSeg, 0)    // seg-3-0: bucket #1, must be written then rolled back
	secondOut := segDirName(nextSeg+1, 0) // seg-4-0: bucket #2, its write is faulted

	// Fail the FIRST file (vectors.dat) of the SECOND output bucket only. Bucket #1
	// writes fully + reopens; bucket #2's write fails → abortMerge(upto=1) fires.
	orig := fsCreate
	t.Cleanup(func() { fsCreate = orig })
	fsCreate = func(name string) (osFile, error) {
		if strings.Contains(name, secondOut) {
			return nil, errInjected
		}
		return orig(name)
	}

	// Launch the explicit 2-input merge. mergeNow plans under the lock then runs
	// mergeAndPublish on a tracked goroutine; the injected write fault drives the
	// abortMerge path. WaitForMerge awaits the (failed) merge's mergeDone.
	requireNoError(t, s.mergeNow([]segID{1, 2}))
	requireNoError(t, s.WaitForMerge())
	fsCreate = orig // stop faulting before recovery/asserts

	in1 := filepath.Join(s.dir, "seg-1-0")
	in2 := filepath.Join(s.dir, "seg-2-0")
	out1 := filepath.Join(s.dir, firstOut)
	out2 := filepath.Join(s.dir, secondOut)

	// (a) Inputs untouched: both still on disk and still the live sealed set.
	if _, err := os.Stat(in1); err != nil {
		t.Fatalf("abortMerge: input seg-1-0 wrongly removed: %v", err)
	}
	if _, err := os.Stat(in2); err != nil {
		t.Fatalf("abortMerge: input seg-2-0 wrongly removed: %v", err)
	}
	// (b) The already-written bucket #1 dir was rolled back by abortMerge's RemoveAll.
	if _, err := os.Stat(out1); !os.IsNotExist(err) {
		t.Fatalf("abortMerge: already-written output %s not rolled back (orphan left)", firstOut)
	}
	// (c) The faulted bucket #2 left no usable segment either.
	if _, err := os.Stat(out2); err == nil {
		// A partial dir may exist from MkdirAll, but it must be swept on recover (d).
		t.Logf("note: partial %s present pre-recovery; recover() must sweep it", secondOut)
	}
	// (d) The live sealed set is still exactly the two inputs (merge never committed).
	s.mu.RLock()
	nSealed := len(s.sealed)
	ids := append([]segID(nil), s.sealedID...)
	s.mu.RUnlock()
	if nSealed != 2 {
		t.Fatalf("abortMerge: live sealed count=%d (ids=%v), want 2 (inputs still authoritative)", nSealed, ids)
	}
	// (e) Every doc still present and correct via the un-merged inputs.
	for i := 0; i < 10; i++ {
		if _, _, found, _ := s.Get("d-" + itoa(i)); !found {
			t.Fatalf("abortMerge: doc d-%d lost after aborted merge", i)
		}
	}

	// (f) Recoverable: reopen sweeps any partial faulted-bucket orphan and restores
	// the two inputs with every doc.
	s2 := reopenStore(t, s, kvStore)
	requireNoError(t, s2.WaitForIndex())
	for i := 0; i < 10; i++ {
		if _, _, found, _ := s2.Get("d-" + itoa(i)); !found {
			t.Fatalf("abortMerge: doc d-%d lost after recovery", i)
		}
	}
	// Both partial output dirs must be gone after recovery's orphan sweep.
	if _, err := os.Stat(filepath.Join(s2.dir, secondOut)); !os.IsNotExist(err) {
		t.Fatalf("abortMerge: faulted output %s not swept on recovery", secondOut)
	}
}
