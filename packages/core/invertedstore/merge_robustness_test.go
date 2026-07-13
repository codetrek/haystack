package invertedstore

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/codetrek/haystack/packages/core/queue"
)

// merge_robustness_test.go — P8 (design §6 merger; task T6) robustness regressions.
//
// These cover the three background-merger hazards the cross-review surfaced, as behavior-named
// tests (not investigation scratch):
//
//   - a covering merge must NOT crash the worker goroutine when a (corrupt/legacy) surviving forward
//     references a keyword the merge reclaimed; it self-heals to a valid segment;
//   - a covering merge over a public-path edit churn round-trips every live doc's forward (the strong
//     public-path fuzz that proves the reclaim path is sound end-to-end);
//   - installMerge must keep s.man / s.segs / the on-disk MANIFEST consistent when the MANIFEST write
//     fails mid-install (no corruption of the live segment set, the inputs stay live).

// --- the sentinel self-heal: a stale forward must not brick the worker --------

// TestCoveringMerge_StaleForwardSelfHealsNoPanic builds a deliberately INCONSISTENT input the public
// write path cannot produce (a corrupt/legacy segment): doc 1's forward says {alpha} (seg0) while a
// later segment fully tombstones the alpha posting WITHOUT clearing the forward (seg1). A covering
// merge reclaims the fully-tombstoned 'alpha' keyword, so doc 1's forward now references a dropped
// keyword ordinal. The merge runs on the un-recovered mpsc worker, so it MUST NOT panic (a worker
// panic bricks the whole process): it self-heals by dropping the stale term, producing a valid
// reopenable segment, and reports the heal via the test observer.
func TestCoveringMerge_StaleForwardSelfHealsNoPanic(t *testing.T) {
	s, tbl := newMergeStore(t, 100) // high Fanout so only the explicit covering merge fires
	defer s.CloseAndWait()

	// seg0: doc 1 live with forward {alpha} + alpha ADD 1 (the real apply path writes both).
	s.applyForTest(tbl, 1, []string{"alpha"})
	s.forceSpill(tbl)
	// seg1: tombstone the alpha POSTING for doc 1 but leave the forward stale (head-level helper that
	// does NOT touch the forward map) — this is the inverted/forward divergence a corrupt segment has.
	s.tombstoneForTest(tbl, "alpha", 1)
	s.forceSpill(tbl)

	if len(s.segs) != 2 {
		t.Fatalf("expected 2 segments before covering merge, got %d", len(s.segs))
	}

	var heals int
	mergeDroppedForwardTermObserver = func() { heals++ }
	defer func() { mergeDroppedForwardTermObserver = nil }()

	// MUST NOT panic on the worker. coveringMergeForTest fails the test (does not crash) if it errors.
	s.coveringMergeForTest(t)

	if len(s.segs) != 1 {
		t.Fatalf("covering merge must compact to 1 segment, got %d", len(s.segs))
	}
	if heals == 0 {
		t.Fatal("expected the covering merge to self-heal the stale forward term (observer never fired)")
	}
	// alpha is fully tombstoned -> reclaimed, and doc 1's forward (its only keyword reclaimed) drops to
	// a clean miss. The segment is valid and reopenable: search and forward both read empty, no panic.
	if r := s.Search(tbl, "alpha", 0, nil); len(r.DocIds) != 0 {
		t.Errorf("alpha must have no live postings after the covering merge, got %v", r.DocIds)
	}
	got, deleted := s.forwardKeywords(tbl, 1)
	if deleted || len(got) != 0 {
		t.Errorf("doc 1's stale forward must self-heal to empty (a miss), got words=%v deleted=%v", got, deleted)
	}
}

// --- public-path covering fuzz: every live doc's forward round-trips ----------

// TestCoveringMerge_PublicFuzzForwardRoundTrips drives a randomized add/re-add/delete churn through
// the PUBLIC Update path only (tiny CapBytes forcing many spills), then runs a covering merge and
// asserts every doc's surviving keyword set round-trips through the rebuilt, remapped term dict and
// matches an independently-tracked ground truth — and that the public path never trips the
// sentinel self-heal (the inverted del and the forward re-post stay co-located for every docid).
func TestCoveringMerge_PublicFuzzForwardRoundTrips(t *testing.T) {
	const vocab = 24
	kw := func(i int) string { return kwf("w", i) }

	for seed := int64(0); seed < 40; seed++ {
		rng := rand.New(rand.NewSource(seed))
		dir := t.TempDir()
		q := queue.NewMpsc("invcovfuzz")
		q.Start()
		// Tiny CapBytes so the public Update path spills frequently mid-run (many segments to merge).
		s, err := Open(dir, q, Options{CapBytes: 256, Fanout: 1000})
		if err != nil {
			t.Fatal(err)
		}
		tbl, err := s.CreateTable("files")
		if err != nil {
			t.Fatal(err)
		}

		truth := map[int64][]string{} // docid -> current live keyword set ("" deleted => absent)
		var heals int
		mergeDroppedForwardTermObserver = func() { heals++ }

		for op := 0; op < 400; op++ {
			d := int64(rng.Intn(12) + 1)
			if rng.Intn(5) == 0 {
				// delete
				s.Update(tbl, d, nil)
				delete(truth, d)
				continue
			}
			// pick a random non-empty keyword subset
			n := rng.Intn(4) + 1
			set := map[string]struct{}{}
			for k := 0; k < n; k++ {
				set[kw(rng.Intn(vocab))] = struct{}{}
			}
			words := make([]string, 0, len(set))
			for w := range set {
				words = append(words, w)
			}
			s.Update(tbl, d, words)
			truth[d] = words
		}
		s.sync()
		s.coveringMergeForTest(t)

		mergeDroppedForwardTermObserver = nil
		if heals != 0 {
			t.Fatalf("seed %d: public Update path must never need a sentinel self-heal, got %d", seed, heals)
		}
		if len(s.segs) != 1 {
			t.Fatalf("seed %d: covering merge must compact to 1 segment, got %d", seed, len(s.segs))
		}

		// Every live doc's forward round-trips to its exact keyword set; every deleted doc reads empty.
		for d := int64(1); d <= 12; d++ {
			got, deleted := s.forwardKeywords(tbl, d)
			want, live := truth[d]
			if !live {
				if deleted == false && len(got) == 0 {
					continue // a clean miss is the expected post-covering state for a deleted doc
				}
				if len(got) != 0 {
					t.Fatalf("seed %d: deleted doc %d must read empty, got %v", seed, d, got)
				}
				continue
			}
			if deleted {
				t.Fatalf("seed %d: live doc %d unexpectedly deleted after covering merge", seed, d)
			}
			if !sameSet(got, want) {
				t.Fatalf("seed %d: doc %d forward after covering merge = %v, want %v", seed, d, got, want)
			}
			// The inverted side must agree: every live keyword still lists the doc.
			for _, w := range want {
				if r := s.Search(tbl, w, 0, nil); !hasDoc(r, d) {
					t.Fatalf("seed %d: doc %d missing from keyword %q after covering merge, got %v", seed, d, w, r.DocIds)
				}
			}
		}
		s.CloseAndWait()
	}
}

// --- installMerge rollback: a MANIFEST-write failure must not corrupt state ---

// TestInstallMerge_ManifestWriteFailureRollsBack forces the MANIFEST write inside installMerge to
// fail (by pre-creating a DIRECTORY named MANIFEST.tmp so os.Create cannot make the temp file) AFTER
// the merged output segment is written. installMerge must roll back: s.man must still name exactly
// the live INPUT segments (not the deleted output), s.segs must still hold them, the orphan output
// file is removed, and the store stays usable + reopenable. This guards the persist-then-publish
// invariant (never mutate the live segment set before the durable write succeeds).
func TestInstallMerge_ManifestWriteFailureRollsBack(t *testing.T) {
	s, tbl := newMergeStore(t, 100) // high Fanout: drive the merge explicitly
	defer s.CloseAndWait()

	s.Update(tbl, 1, []string{"apple", "mango"})
	s.sync()
	s.forceSpill(tbl)
	s.Update(tbl, 2, []string{"banana", "mango"})
	s.sync()
	s.forceSpill(tbl)

	if len(s.segs) != 2 {
		t.Fatalf("expected 2 input segments, got %d", len(s.segs))
	}
	// Snapshot the pre-merge segment-id set the MANIFEST must still name after the failed install.
	s.mu.RLock()
	wantIds := map[uint64]bool{}
	for _, sm := range s.man.Segments {
		wantIds[sm.Id] = true
	}
	nSegFiles := len(wantIds)
	s.mu.RUnlock()

	// Make MANIFEST.tmp un-creatable as a file (it is an existing directory) so writeManifest fails
	// AFTER mergeSegments has already written + fsync'd the output segment.
	tmpDirBlock := filepath.Join(s.dir, "MANIFEST.tmp")
	if err := os.Mkdir(tmpDirBlock, 0o755); err != nil {
		t.Fatal(err)
	}

	// Run the covering merge on the worker; installMerge's writeManifest must fail and return an error
	// (NOT crash, NOT corrupt state). coveringMerge returns that error.
	err := s.q.RunFunc(func() error { return s.coveringMerge() })
	if err == nil {
		t.Fatal("expected coveringMerge to return the MANIFEST-write error")
	}
	// Unblock MANIFEST.tmp again so later spills/close can write.
	if err := os.Remove(tmpDirBlock); err != nil {
		t.Fatal(err)
	}

	// The in-memory manifest must still name EXACTLY the pre-merge inputs (rolled back), and s.segs
	// must still hold their handles — no divergence, no dangling reference to the deleted output.
	s.mu.RLock()
	gotIds := map[uint64]bool{}
	for _, sm := range s.man.Segments {
		gotIds[sm.Id] = true
	}
	segHandles := len(s.segs)
	s.mu.RUnlock()
	if len(gotIds) != len(wantIds) {
		t.Fatalf("after failed install, manifest names %d segments, want the %d inputs", len(gotIds), len(wantIds))
	}
	for id := range wantIds {
		if !gotIds[id] {
			t.Fatalf("after failed install, manifest dropped live input segment %d (gotIds=%v)", id, gotIds)
		}
	}
	if segHandles != len(wantIds) {
		t.Fatalf("after failed install, s.segs holds %d handles, want the %d inputs", segHandles, len(wantIds))
	}
	// The orphan output segment file must be gone (installMerge removes it on the rollback path).
	dents, _ := os.ReadDir(s.dir)
	segCount := 0
	for _, de := range dents {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		if len(name) >= 4 && name[:4] == "seg-" {
			segCount++
		}
	}
	if segCount != nSegFiles {
		t.Fatalf("after failed install, found %d seg-*.dat files, want %d (orphan output not removed)", segCount, nSegFiles)
	}

	// The store stays usable: data is intact and a covering merge now succeeds.
	if r := s.Search(tbl, "mango", 0, nil); !hasDoc(r, 1) || !hasDoc(r, 2) {
		t.Errorf("data must survive the failed install: mango = %v", r.DocIds)
	}
	s.coveringMergeForTest(t) // now succeeds
	if len(s.segs) != 1 {
		t.Fatalf("after a successful retry the merge must compact to 1 segment, got %d", len(s.segs))
	}
	if r := s.Search(tbl, "mango", 0, nil); !hasDoc(r, 1) || !hasDoc(r, 2) {
		t.Errorf("data must survive the successful retry: mango = %v", r.DocIds)
	}
}
