package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// randVecN returns a deterministic random dim-vector from rng.
func randVecN(rng *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	for d := range v {
		v[d] = rng.Float32()
	}
	return v
}

// TestMergeCrash_BeforeSwap_OutputSwept forces a crash inside the REAL merge path
// (appendix #4): mergeAndPublish writes + reopens every output bucket, then — via
// the testHookAfterWrite seam, BEFORE writeManifestLocked — aborts as if the
// process died. The output dirs are real (p.outDirs at the real allocated segIds)
// and the manifest never referenced them, so recover() must sweep them while the
// inputs (still manifest-referenced) survive intact with every doc.
func TestMergeCrash_BeforeSwap_OutputSwept(t *testing.T) {
	kvStore := newTestKV(t)
	s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	rng := rand.New(rand.NewSource(11))
	for i := 0; i < 30; i++ {
		requireNoError(t, s.Put("d-"+itoa(i), randVecN(rng, 8), nil))
	}
	requireNoError(t, s.Seal()) // seg-1-0 (the input, committed in the manifest)
	requireNoError(t, s.WaitForIndex())

	// Abort the merge AFTER the outputs are written+fsynced but BEFORE the manifest
	// swap — the exact crash-before-swap window. Capture the real output dirs.
	var outDirs []string
	s.testHookAfterWrite = func(p *mergePlan) bool {
		outDirs = append([]string(nil), p.outDirs...)
		return true // simulate crash: bail before writeManifestLocked
	}
	requireNoError(t, s.mergeNow([]segID{1}))
	requireNoError(t, s.WaitForMerge())

	if len(outDirs) == 0 {
		t.Fatal("seam never fired: no output dirs captured (merge produced no output?)")
	}
	// Pre-reopen sanity: the output dir really exists on disk (proves we wrote it).
	for _, d := range outDirs {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("crash-before-swap: output dir %s not written by the real merge path: %v", d, err)
		}
	}

	s2 := reopenStore(t, s, kvStore)

	// Every real merge output dir must be swept (unreferenced by the manifest).
	for _, d := range outDirs {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Fatalf("crash-before-swap: unreferenced merge output %s not swept", d)
		}
	}
	// The input survived (manifest-referenced) — no data loss.
	if _, err := os.Stat(filepath.Join(s2.dir, "seg-1-0")); err != nil {
		t.Fatalf("crash-before-swap: input seg-1-0 wrongly removed: %v", err)
	}
	for i := 0; i < 30; i++ {
		if _, _, found, _ := s2.Get("d-" + itoa(i)); !found {
			t.Fatalf("crash-before-swap: input doc d-%d lost", i)
		}
	}
}

// TestMergeCrash_AfterSwap_OldInputSwept forces a crash in the REAL merge path
// (appendix #5) AFTER the manifest swap committed but BEFORE the old input dirs are
// deleted: the testHookAfterSwap seam returns true so os.RemoveAll never runs. The
// manifest now references the new output; the old input dir is a leftover orphan
// that recover() must sweep, and the merged output must carry every live doc.
func TestMergeCrash_AfterSwap_OldInputSwept(t *testing.T) {
	kvStore := newTestKV(t)
	s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	rng := rand.New(rand.NewSource(12))
	for i := 0; i < 40; i++ {
		requireNoError(t, s.Put("d-"+itoa(i), randVecN(rng, 8), nil))
	}
	requireNoError(t, s.Seal()) // seg-1-0
	requireNoError(t, s.WaitForIndex())

	// Abort AFTER the manifest swap but BEFORE deleting the old input dirs.
	staleInput := filepath.Join(s.dir, "seg-1-0")
	s.testHookAfterSwap = func(p *mergePlan) bool {
		return true // simulate crash: skip os.RemoveAll of the old inputs
	}
	requireNoError(t, s.mergeNow([]segID{1}))
	requireNoError(t, s.WaitForMerge())

	// Pre-reopen sanity: the swap committed (a new output seg exists) AND the old
	// input dir is still present (the RemoveAll was skipped by the seam).
	if _, err := os.Stat(staleInput); err != nil {
		t.Fatalf("crash-after-swap: seam did not preserve the old input dir: %v", err)
	}

	s2 := reopenStore(t, s, kvStore)

	if _, err := os.Stat(staleInput); !os.IsNotExist(err) {
		t.Fatal("crash-after-swap: stale input seg-1-0 not swept on recovery")
	}
	for i := 0; i < 40; i++ {
		if _, _, found, _ := s2.Get("d-" + itoa(i)); !found {
			t.Fatalf("crash-after-swap: doc d-%d lost", i)
		}
	}
}

// TestMergeCrash_MidBuild_RecoverResumes drives a real merge whose manifest swap
// committed the output as PENDING, then crashes (via testHookAfterSwap) BEFORE the
// background HNSW build is even spawned — so the output is durably pending with no
// graph. recover() must re-spawn the build so the merged output ends up indexed +
// searchable. Using the seam makes the pending state deterministic (otherwise a
// fast background build could index the output before reopen, masking the resume
// path). Outputs are kept at Gen=0, so recover's segDirName(sid, 0) resume path
// resolves correctly (plan §7b).
func TestMergeCrash_MidBuild_RecoverResumes(t *testing.T) {
	kvStore := newTestKV(t)
	s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	rng := rand.New(rand.NewSource(13))
	live := map[int64][]float32{}
	for i := 0; i < 50; i++ {
		v := randVecN(rng, 8)
		requireNoError(t, s.Put("d-"+itoa(i), v, nil))
		live[s.idToDoc["d-"+itoa(i)]] = v
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	// Crash right after the swap, before the build spawns: the output is committed
	// PENDING (graph not yet installed when writeManifestLocked ran).
	s.testHookAfterSwap = func(p *mergePlan) bool { return true }
	requireNoError(t, s.mergeNow([]segID{1}))
	requireNoError(t, s.WaitForMerge()) // swap done; output published pending, no build

	s.mu.RLock()
	outID := s.sealedID[0]
	pendingAtCrash := s.indexes[defaultIndexName].graphs[outID] == nil
	s.mu.RUnlock()
	if !pendingAtCrash {
		t.Fatal("crash-mid-build: output unexpectedly indexed before crash; seam did not force the pending state")
	}

	// Reopen: recover() must resume the pending build for the merged output.
	s2 := reopenStore(t, s, kvStore)
	requireNoError(t, s2.WaitForIndex())
	if !s2.isIndexedForTest(outID) {
		t.Fatalf("crash-mid-build: merged output seg %d not re-indexed on recovery", outID)
	}
	var sum float64
	for it := 0; it < 20; it++ {
		q := randVecN(rng, 8)
		got, err := s2.Search("default", q, 5, nil)
		requireNoError(t, err)
		sum += recallAtK(got, bruteForceKNN(Cosine, q, live, 5))
	}
	if avg := sum / 20; avg < 0.8 {
		t.Fatalf("crash-mid-build: post-recovery recall@5 = %.3f, want >= 0.8", avg)
	}
}

// TestMerge_SurvivesRecovery_EndToEnd: a full churn → seal → merge → restart cycle.
// docToSeg is derived from on-disk slotDoc over live slots, so the merged set must
// reconstruct exactly on reopen (architecture §4.6/§4.8). This is the Task 11
// integration test — no new product code; it locks in that the manifest round-trips
// the merge OUTPUTS (not the pre-merge inputs) across recovery.
func TestMerge_SurvivesRecovery_EndToEnd(t *testing.T) {
	kvStore := newTestKV(t)
	s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	s.maxSegSize = 8
	s.mcfg.Fanout = 2
	s.mcfg.MergeFloor = 0.5
	rng := rand.New(rand.NewSource(51))
	dim := 8
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	live := map[int64][]float32{}
	for i := 0; i < 64; i++ {
		id := "d-" + itoa(i)
		v := randVec()
		requireNoError(t, s.Put(id, v, nil))
		live[s.idToDoc[id]] = v
	}
	// Delete a third to make some segments merge-bait.
	for i := 0; i < 64; i++ {
		if i%3 == 0 {
			requireNoError(t, s.Delete("d-"+itoa(i)))
			delete(live, s.idToDoc["d-"+itoa(i)])
		}
	}
	// Ensure every sealed segment is indexed before Compact: the delete-driven and
	// growth drivers only select INDEXED inputs (a pending segment is skipped to
	// avoid close-during-build, appendix #3/#8), so without this the Compact round
	// could be a partial no-op and the assertion below would be racy.
	requireNoError(t, s.WaitForIndex())
	requireNoError(t, s.Compact())
	requireNoError(t, s.WaitForMerge())
	requireNoError(t, s.WaitForIndex())

	s2 := reopenStore(t, s, kvStore)
	requireNoError(t, s2.WaitForIndex())

	// Every survivor present, every deleted absent.
	for i := 0; i < 64; i++ {
		id := "d-" + itoa(i)
		_, _, found, err := s2.Get(id)
		requireNoError(t, err)
		wantLive := i%3 != 0
		if found != wantLive {
			t.Fatalf("after recovery Get(%s) found=%v, want %v", id, found, wantLive)
		}
	}
	var sum float64
	for it := 0; it < 30; it++ {
		q := randVec()
		got, err := s2.Search("default", q, 5, nil)
		requireNoError(t, err)
		sum += recallAtK(got, bruteForceKNN(Cosine, q, live, 5))
	}
	if avg := sum / 30; avg < 0.8 {
		t.Fatalf("post-recovery (merged) recall@5 = %.3f, want >= 0.8", avg)
	}
}
