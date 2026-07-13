package invertedstore

import (
	"strconv"
	"testing"

	"github.com/codetrek/haystack/packages/core/queue"
)

// merge_test.go — P8 (design §6 merger + §8 remap; task T6) acceptance tests.
//
// These are the correctness cases the spike's narrow (globally-unique-word) edit workload never
// exercised: per-(keyword,docid) newest-wins reconciliation (add->del->add), forward-tombstone
// survival across a merge, forward round-trip after the ord->ord remap, covering-merge reclamation,
// and the bounded (int-array, not string-map) merge memory.

// --- test seams: drive the real worker-side merge synchronously --------------

// newMergeStore opens a store with an explicit Fanout (so a test can force a tiered merge with a
// known number of segments) and AutoMerge OFF (the test drives merges itself, deterministically).
func newMergeStore(t *testing.T, fanout int) (*Store, int) {
	t.Helper()
	dir := t.TempDir()
	q := queue.NewMpsc("invmerge")
	q.Start()
	s, err := Open(dir, q, Options{Fanout: fanout})
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}
	return s, tbl
}

// mergeOneLevelForTest runs one tiered merge pass on the worker (synchronous). Returns whether a
// level qualified and merged.
func (s *Store) mergeOneLevelForTest(t *testing.T) bool {
	t.Helper()
	var merged bool
	err := s.q.RunFunc(func() error {
		var e error
		merged, e = s.mergeOneLevel()
		return e
	})
	if err != nil {
		t.Fatalf("mergeOneLevel: %v", err)
	}
	return merged
}

// coveringMergeForTest runs a full covering merge on the worker (synchronous).
func (s *Store) coveringMergeForTest(t *testing.T) {
	t.Helper()
	if err := s.q.RunFunc(func() error { return s.coveringMerge() }); err != nil {
		t.Fatalf("coveringMerge: %v", err)
	}
}

// segInvRecords reads every [I] record of segment seg for tableId tbl, returning per keyword the
// decoded adds and dels — used to assert post-merge segment contents (dels reclaimed, keys dropped).
func segInvRecords(seg *segment, tbl int) map[string]struct {
	adds, dels []int64
} {
	out := map[string]struct {
		adds, dels []int64
	}{}
	lo := invertedKey(uint32(tbl), "")
	hi := []byte{ktForward}
	seg.scanPrefix(lo, hi, func(key, value []byte) {
		if key[0] != ktInverted {
			return
		}
		kw := string(key[5:])
		ab, db := splitInvertedValue(value)
		var adds, dels []int64
		decodeDocs(ab, func(d int64) { adds = append(adds, d) })
		decodeDocs(db, func(d int64) { dels = append(dels, d) })
		out[kw] = struct {
			adds, dels []int64
		}{adds, dels}
	})
	return out
}

// --- MUST-PASS 1: add -> del -> add, then a merge resolves PRESENT -----------

// TestMerge_AddDelAddResolvesPresent builds three L0 segments for one (keyword,docid): add, then
// del, then add. A tiered merge of all three must reconcile newest-wins (the final ADD survives),
// so Search finds the doc — the case the spike's concat-not-reconcile merge got WRONG.
func TestMerge_AddDelAddResolvesPresent(t *testing.T) {
	s, tbl := newMergeStore(t, 3) // Fanout 3 so three L0 segments trigger one tiered merge
	defer s.CloseAndWait()

	s.addPostingForTest(tbl, "alpha", 10)
	s.forceSpill(tbl) // seg0: alpha ADD 10
	s.tombstoneForTest(tbl, "alpha", 10)
	s.forceSpill(tbl) // seg1: alpha DEL 10
	s.addPostingForTest(tbl, "alpha", 10)
	s.forceSpill(tbl) // seg2: alpha ADD 10  (newest)

	if len(s.segs) != 3 {
		t.Fatalf("expected 3 L0 segments before merge, got %d", len(s.segs))
	}
	// Sanity: even BEFORE the merge, read-time newest-wins already says PRESENT.
	if r := s.Search(tbl, "alpha", 0, nil); !hasDoc(r, 10) {
		t.Fatalf("pre-merge read-time newest-wins should already be PRESENT: %v", r.DocIds)
	}

	if !s.mergeOneLevelForTest(t) {
		t.Fatal("expected a tiered merge to fire with 3 L0 segments at Fanout 3")
	}
	if len(s.segs) != 1 {
		t.Fatalf("after merging 3 L0 segments expected 1 segment, got %d", len(s.segs))
	}

	// The merged segment must resolve the (alpha,10) reconciliation to PRESENT.
	if r := s.Search(tbl, "alpha", 0, nil); !hasDoc(r, 10) {
		t.Fatalf("add->del->add must resolve PRESENT after merge, got %v", r.DocIds)
	}
	// The single merged segment must carry alpha as a LIVE add (newest add wins over the older del).
	recs := segInvRecords(s.segs[0], tbl)
	rec, ok := recs["alpha"]
	if !ok {
		t.Fatal("merged segment must keep the alpha key")
	}
	if len(rec.adds) != 1 || rec.adds[0] != 10 {
		t.Fatalf("merged alpha adds = %v, want [10]", rec.adds)
	}
	// The del was co-located with its newest add inside this merge, so it is reconciled away.
	if len(rec.dels) != 0 {
		t.Fatalf("merged alpha dels = %v, want [] (add->del->add reconciled to a live add)", rec.dels)
	}
}

// TestMerge_AddDelResolvesAbsent is the symmetric case: add then del (newest), merged, must resolve
// ABSENT (the del is the latest action). With the add co-located, the merged inverted record is
// empty (no live add) so Search must not return the doc.
func TestMerge_AddDelResolvesAbsent(t *testing.T) {
	s, tbl := newMergeStore(t, 2)
	defer s.CloseAndWait()

	s.addPostingForTest(tbl, "alpha", 10)
	s.forceSpill(tbl) // seg0: alpha ADD 10
	s.tombstoneForTest(tbl, "alpha", 10)
	s.forceSpill(tbl) // seg1: alpha DEL 10 (newest)

	if !s.mergeOneLevelForTest(t) {
		t.Fatal("expected a tiered merge to fire")
	}
	if r := s.Search(tbl, "alpha", 0, nil); hasDoc(r, 10) {
		t.Fatalf("add->del must resolve ABSENT after merge, got %v", r.DocIds)
	}
}

// --- MUST-PASS 2: a forward-tombstone survives a merge spanning the delete + an older record ----

// TestMerge_ForwardTombstoneSurvives builds seg0 with doc 10 live (forward {alpha}) and seg1 with
// doc 10 DELETED (forward-tombstone nKw=0 + alpha per-keyword tombstone). A tiered merge spanning
// both must carry the forward-tombstone through (newest wins) so the doc still reads EMPTY — the
// older non-empty forward must NOT win and resurrect the doc.
func TestMerge_ForwardTombstoneSurvives(t *testing.T) {
	s, tbl := newMergeStore(t, 2)
	defer s.CloseAndWait()

	s.Update(tbl, 10, []string{"alpha"})
	s.sync()
	s.forceSpill(tbl)      // seg0: doc 10 live, forward {alpha}, alpha ADD 10
	s.Update(tbl, 10, nil) // DELETE
	s.sync()
	s.forceSpill(tbl) // seg1: forward-tombstone + alpha DEL 10 (newest)

	if !s.mergeOneLevelForTest(t) {
		t.Fatal("expected a tiered merge to fire")
	}

	// Forward of the deleted doc must still report deleted/empty after the merge.
	words, deleted := s.forwardKeywords(tbl, 10)
	if !deleted || len(words) != 0 {
		t.Fatalf("forward-tombstone must survive the merge: words=%v deleted=%v", words, deleted)
	}
	// And the doc must be absent from Search.
	if r := s.Search(tbl, "alpha", 0, nil); hasDoc(r, 10) {
		t.Fatalf("deleted doc 10 must stay absent after the merge, got %v", r.DocIds)
	}
}

// --- MUST-PASS 3: forward round-trip correct after merge (ord->ord remap) ----

// TestMerge_ForwardRoundTripAfterMerge builds several live docs whose keyword sets span two
// segments with DIFFERENT per-segment ordinals (so the remap is exercised: the same keyword has a
// different ordinal in each source). After a tiered merge, every doc's forward must resolve to its
// exact keyword set through the rebuilt term-dict region.
func TestMerge_ForwardRoundTripAfterMerge(t *testing.T) {
	s, tbl := newMergeStore(t, 2)
	defer s.CloseAndWait()

	// Two cold-build batches sealed into two segments, with overlapping AND distinct keywords so the
	// ordinals differ between segments. doc->keywords ground truth.
	truth := map[int64][]string{
		1: {"apple", "mango"},
		2: {"banana"},
		3: {"apple", "banana", "cherry"},
		4: {"date", "mango"},
	}
	s.Update(tbl, 1, truth[1])
	s.Update(tbl, 2, truth[2])
	s.sync()
	s.forceSpill(tbl) // seg0: docs 1,2
	s.Update(tbl, 3, truth[3])
	s.Update(tbl, 4, truth[4])
	s.sync()
	s.forceSpill(tbl) // seg1: docs 3,4

	if !s.mergeOneLevelForTest(t) {
		t.Fatal("expected a tiered merge to fire")
	}
	if len(s.segs) != 1 {
		t.Fatalf("expected 1 merged segment, got %d", len(s.segs))
	}

	for d, want := range truth {
		got, deleted := s.forwardKeywords(tbl, d)
		if deleted {
			t.Fatalf("doc %d unexpectedly deleted after merge", d)
		}
		if !sameSet(got, want) {
			t.Fatalf("doc %d forward after merge = %v, want %v (remap broken)", d, got, want)
		}
	}
	// And the inverted side stays searchable for every keyword.
	for _, kw := range []string{"apple", "banana", "cherry", "date", "mango"} {
		if r := s.Search(tbl, kw, 0, nil); len(r.DocIds) == 0 {
			t.Errorf("keyword %q lost all postings after merge", kw)
		}
	}
}

// --- MUST-PASS 4: covering merge bounds tombstone / duplicate growth ---------

// TestMerge_CoveringReclaimsTombstonesAndDuplicates builds a long edit run for ONE doc that churns a
// keyword (add, remove, re-add ...) across many segments plus a doc that ends DELETED, so the bottom
// carries dangling tombstones, a fully-tombstoned key, and duplicate adds. A covering merge must
// reclaim them: the result has NO dels (dangling tombstones gone), NO fully-tombstoned key, and a
// deleted doc's forward-tombstone is dropped — while every LIVE result is preserved.
func TestMerge_CoveringReclaimsTombstonesAndDuplicates(t *testing.T) {
	s, tbl := newMergeStore(t, 100) // high Fanout so only the EXPLICIT covering merge fires
	defer s.CloseAndWait()

	// doc 10: alpha churned add/remove/add across 4 segments; ends with alpha LIVE + beta LIVE.
	s.Update(tbl, 10, []string{"alpha"})
	s.sync()
	s.forceSpill(tbl)
	s.Update(tbl, 10, []string{"beta"}) // drop alpha (alpha tombstone), add beta
	s.sync()
	s.forceSpill(tbl)
	s.Update(tbl, 10, []string{"alpha", "beta"}) // re-add alpha
	s.sync()
	s.forceSpill(tbl)

	// doc 20: only ever had keyword "ghost", then DELETED -> ghost becomes fully tombstoned.
	s.Update(tbl, 20, []string{"ghost"})
	s.sync()
	s.forceSpill(tbl)
	s.Update(tbl, 20, nil) // delete -> forward-tombstone + ghost tombstone
	s.sync()
	s.forceSpill(tbl)

	preSegs := len(s.segs)
	if preSegs < 5 {
		t.Fatalf("expected >=5 segments before the covering merge, got %d", preSegs)
	}

	s.coveringMergeForTest(t)

	if len(s.segs) != 1 {
		t.Fatalf("covering merge must compact to 1 segment, got %d", len(s.segs))
	}
	merged := s.segs[0]
	recs := segInvRecords(merged, tbl)

	// alpha + beta are LIVE for doc 10 and must survive with NO dels (dangling tombstones reclaimed).
	for _, kw := range []string{"alpha", "beta"} {
		rec, ok := recs[kw]
		if !ok {
			t.Fatalf("covering merge dropped live keyword %q", kw)
		}
		if len(rec.dels) != 0 {
			t.Errorf("covering merge must reclaim ALL dels, %q kept dels=%v", kw, rec.dels)
		}
		found := false
		for _, d := range rec.adds {
			if d == 10 {
				found = true
			}
		}
		if !found {
			t.Errorf("live keyword %q must still contain doc 10, adds=%v", kw, rec.adds)
		}
	}
	// ghost is fully tombstoned -> the covering merge must DROP the key entirely.
	if _, ok := recs["ghost"]; ok {
		t.Errorf("covering merge must drop the fully-tombstoned key 'ghost', got %v", recs["ghost"])
	}
	// The deleted doc 20 reads empty; its forward-tombstone need no longer exist (nothing to suppress).
	if _, deleted := s.forwardKeywords(tbl, 20); deleted {
		// deleted==true would mean a tombstone record is still present; covering merge drops it, so a
		// plain miss (deleted==false, empty) is the expected post-covering state.
		t.Errorf("covering merge should drop the forward-tombstone of doc 20 (a miss, not a tombstone)")
	}
	if r := s.Search(tbl, "ghost", 0, nil); len(r.DocIds) != 0 {
		t.Errorf("ghost must have no live postings after the covering merge, got %v", r.DocIds)
	}
	// Live doc 10 still searchable + its forward round-trips.
	if r := s.Search(tbl, "alpha", 0, nil); !hasDoc(r, 10) {
		t.Errorf("doc 10 must stay present under alpha after covering merge, got %v", r.DocIds)
	}
	got, _ := s.forwardKeywords(tbl, 10)
	if !sameSet(got, []string{"alpha", "beta"}) {
		t.Errorf("doc 10 forward after covering merge = %v, want {alpha,beta}", got)
	}
}

// TestMerge_CoveringDropsDeadTableKeys: a covering merge drops [I]/[F] keys for a tableId no longer
// in the catalog (DeleteTable scheduled it). After the merge the dead table's bytes are gone and a
// surviving table is untouched.
func TestMerge_CoveringDropsDeadTableKeys(t *testing.T) {
	dir := t.TempDir()
	q := queue.NewMpsc("invmergedt")
	q.Start()
	s, err := Open(dir, q, Options{Fanout: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer s.CloseAndWait()
	keep, _ := s.CreateTable("keep")
	drop, _ := s.CreateTable("drop")

	s.Update(keep, 1, []string{"alpha"})
	s.Update(drop, 2, []string{"beta"})
	s.sync()
	s.forceSpill(keep)
	s.forceSpill(drop)

	// Drop the table from the catalog (no AutoMerge, so no auto-scheduled covering merge).
	if err := s.DeleteTable(drop); err != nil {
		t.Fatal(err)
	}
	s.coveringMergeForTest(t)

	if len(s.segs) != 1 {
		t.Fatalf("covering merge must compact to 1 segment, got %d", len(s.segs))
	}
	// The dead table's keyword must be gone from the merged segment.
	if recs := segInvRecords(s.segs[0], drop); len(recs) != 0 {
		t.Errorf("covering merge must drop dead-table keys, found %v", recs)
	}
	// The surviving table is untouched.
	if r := s.Search(keep, "alpha", 0, nil); !hasDoc(r, 1) {
		t.Errorf("surviving table 'keep' must still contain doc 1 under alpha, got %v", r.DocIds)
	}
}

// --- MUST-PASS 5: merge memory ~= Sum source term counts (int arrays) --------

// TestMerge_RemapMemoryIsIntArrays asserts the merge's remap is per-source INT arrays whose total
// length equals the sum of the source segments' [I] (term) counts — NOT a string map. We instrument
// the merge with a test hook that reports the realized remap sizes, then compare to the independently
// counted source term totals.
func TestMerge_RemapMemoryIsIntArrays(t *testing.T) {
	s, tbl := newMergeStore(t, 3)
	defer s.CloseAndWait()

	// Three segments with overlapping + distinct keywords.
	s.Update(tbl, 1, []string{"apple", "banana"})
	s.sync()
	s.forceSpill(tbl)
	s.Update(tbl, 2, []string{"banana", "cherry"})
	s.sync()
	s.forceSpill(tbl)
	s.Update(tbl, 3, []string{"apple", "date"})
	s.sync()
	s.forceSpill(tbl)

	// Independently count each source segment's [I] term count (the spill order == oldest->newest).
	wantPerSource := make([]int, 0, len(s.segs))
	wantTotal := 0
	for _, seg := range s.segs {
		n := len(segInvRecords(seg, tbl))
		wantPerSource = append(wantPerSource, n)
		wantTotal += n
	}

	var gotPerSource []int
	mergeRemapObserver = func(remap [][]uint32) {
		gotPerSource = make([]int, len(remap))
		for i, r := range remap {
			gotPerSource[i] = len(r)
		}
	}
	defer func() { mergeRemapObserver = nil }()

	if !s.mergeOneLevelForTest(t) {
		t.Fatal("expected a tiered merge to fire")
	}

	if len(gotPerSource) != len(wantPerSource) {
		t.Fatalf("remap source count = %d, want %d", len(gotPerSource), len(wantPerSource))
	}
	gotTotal := 0
	for i := range gotPerSource {
		if gotPerSource[i] != wantPerSource[i] {
			t.Errorf("source %d remap length = %d, want = its [I] term count %d", i, gotPerSource[i], wantPerSource[i])
		}
		gotTotal += gotPerSource[i]
	}
	if gotTotal != wantTotal {
		t.Fatalf("total remap entries = %d, want Sum(source term counts) = %d", gotTotal, wantTotal)
	}
}

// TestMerge_CoveringEmptyResultIsValid: a covering merge that drops EVERYTHING (the only table is
// deleted) must still produce a well-formed, reopenable segment (empty blocks + footer), not panic.
func TestMerge_CoveringEmptyResultIsValid(t *testing.T) {
	dir := t.TempDir()
	q := queue.NewMpsc("invmergeempty")
	q.Start()
	s, err := Open(dir, q, Options{Fanout: 100})
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := s.CreateTable("files")
	s.Update(tbl, 1, []string{"alpha"})
	s.sync()
	s.forceSpill(tbl)
	s.Update(tbl, 2, []string{"beta"})
	s.sync()
	s.forceSpill(tbl)

	if err := s.DeleteTable(tbl); err != nil {
		t.Fatal(err)
	}
	s.coveringMergeForTest(t) // drops both tables' keys -> empty merged segment
	if len(s.segs) != 1 {
		t.Fatalf("covering merge must still produce exactly one (empty) segment, got %d", len(s.segs))
	}
	s.CloseAndWait()

	// Reopen: the empty merged segment parses cleanly.
	s2 := openTestStore(t, dir)
	defer s2.CloseAndWait()
	if len(s2.segs) != 1 {
		t.Fatalf("after reopen expected 1 segment, got %d", len(s2.segs))
	}
}

// --- crash-safe MANIFEST swap: merged segment set survives a reopen ----------

// TestMerge_ManifestSwapSurvivesReopen merges, closes, and REOPENS the store: the merged segment
// must be the only live segment (inputs unlinked), and all data (search + forward) intact. This
// exercises the crash-safe MANIFEST swap + input deletion (a reopen reads only what the swapped
// MANIFEST names; orphan inputs are gone).
func TestMerge_ManifestSwapSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	q := queue.NewMpsc("invmergereopen")
	q.Start()
	s, err := Open(dir, q, Options{Fanout: 2})
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := s.CreateTable("files")

	s.Update(tbl, 1, []string{"apple", "mango"})
	s.sync()
	s.forceSpill(tbl)
	s.Update(tbl, 2, []string{"banana", "mango"})
	s.sync()
	s.forceSpill(tbl)
	if !s.mergeOneLevelForTest(t) {
		t.Fatal("expected a tiered merge to fire")
	}
	if len(s.segs) != 1 {
		t.Fatalf("expected 1 merged segment, got %d", len(s.segs))
	}
	s.CloseAndWait()

	// Reopen: the MANIFEST names exactly the merged segment; the input files are gone.
	s2 := openTestStore(t, dir)
	defer s2.CloseAndWait()
	if len(s2.segs) != 1 {
		t.Fatalf("after reopen expected exactly the merged segment, got %d", len(s2.segs))
	}
	if r := s2.Search(tbl, "mango", 0, nil); !hasDoc(r, 1) || !hasDoc(r, 2) {
		t.Errorf("merged mango postings lost across reopen: %v", r.DocIds)
	}
	got, _ := s2.forwardKeywords(tbl, 1)
	if !sameSet(got, []string{"apple", "mango"}) {
		t.Errorf("doc 1 forward lost across reopen: %v", got)
	}
}

// --- AutoMerge: the background tiered merger fires automatically --------------

// TestMerge_AutoMergeBackgroundFires turns AutoMerge ON and spills Fanout segments; the worker must
// auto-enqueue the tiered merge so the live segment count drops below Fanout, with search intact.
func TestMerge_AutoMergeBackgroundFires(t *testing.T) {
	dir := t.TempDir()
	q := queue.NewMpsc("invautomerge")
	q.Start()
	s, err := Open(dir, q, Options{Fanout: 3, AutoMerge: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.CloseAndWait()
	tbl, _ := s.CreateTable("files")

	for d := int64(1); d <= 3; d++ {
		s.Update(tbl, d, []string{"alpha"})
		s.sync()
		s.forceSpill(tbl)
	}
	// The spill that reached Fanout raised a background merge trigger (P9: merges run on their own
	// goroutine now, not as a re-enqueued worker task). waitMergeIdle blocks until the merger settles.
	s.waitMergeIdle()

	if len(s.segs) >= 3 {
		t.Fatalf("AutoMerge should have collapsed the 3 L0 segments below Fanout, got %d segments", len(s.segs))
	}
	r := s.Search(tbl, "alpha", 0, nil)
	for d := int64(1); d <= 3; d++ {
		if !hasDoc(r, d) {
			t.Errorf("doc %d lost after auto-merge, got %v", d, r.DocIds)
		}
	}
}

// --- streaming merge over many keywords + block boundaries -------------------

// TestMerge_StreamingManyKeywordsAcrossBlocks merges two segments each holding many keywords (so the
// merge crosses multiple data blocks per source and the streaming cursor advances across block
// boundaries), with overlapping keywords (reconciled) and distinct ones. Every keyword's union must
// be preserved and every doc's forward must round-trip — a port of the spike's k-way merge over real
// block geometry, in production shape.
func TestMerge_StreamingManyKeywordsAcrossBlocks(t *testing.T) {
	// Small blocks so a few hundred keywords span several blocks per segment.
	s, tbl := newMergeStoreOpts(t, Options{Fanout: 2, BlockTarget: 256, DictChunkBytes: 128})
	defer s.CloseAndWait()

	// seg0: docs 1..50, each with 3 keywords drawn from a shared vocabulary (overlap across docs).
	truth := map[int64][]string{}
	for d := int64(1); d <= 50; d++ {
		kws := []string{kwf("k", int(d%17)), kwf("k", int(d%23)), kwf("u0_", int(d))}
		truth[d] = kws
		s.Update(tbl, d, kws)
	}
	s.sync()
	s.forceSpill(tbl)
	// seg1: docs 51..100, overlapping the k* vocabulary so the merge reconciles shared keywords.
	for d := int64(51); d <= 100; d++ {
		kws := []string{kwf("k", int(d%17)), kwf("k", int(d%29)), kwf("u1_", int(d))}
		truth[d] = kws
		s.Update(tbl, d, kws)
	}
	s.sync()
	s.forceSpill(tbl)

	if !s.mergeOneLevelForTest(t) {
		t.Fatal("expected a tiered merge to fire")
	}
	if len(s.segs) != 1 {
		t.Fatalf("expected 1 merged segment, got %d", len(s.segs))
	}

	// Every doc's forward round-trips through the rebuilt (remapped) term dict.
	for d, want := range truth {
		got, deleted := s.forwardKeywords(tbl, d)
		if deleted {
			t.Fatalf("doc %d unexpectedly deleted after streaming merge", d)
		}
		if !sameSet(got, want) {
			t.Fatalf("doc %d forward after streaming merge = %v, want %v", d, got, want)
		}
	}
	// A shared keyword unions docs from BOTH source segments (reconciled, not lost).
	r := s.Search(tbl, kwf("k", 0), 0, nil) // k0 appears for docs where d%17==0 or d%23==0 or d%29==0
	if len(r.DocIds) == 0 {
		t.Fatal("shared keyword k0 lost all postings after the streaming merge")
	}
}

// kwf builds a deterministic keyword "prefixN".
func kwf(prefix string, n int) string {
	return prefix + strconv.Itoa(n)
}

// newMergeStoreOpts is newMergeStore with explicit Options (block geometry, codecs, ...).
func newMergeStoreOpts(t *testing.T, opts Options) (*Store, int) {
	t.Helper()
	dir := t.TempDir()
	q := queue.NewMpsc("invmergeopts")
	q.Start()
	s, err := Open(dir, q, opts)
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}
	return s, tbl
}

// sameSet reports whether two keyword slices hold the same SET of strings (order-independent).
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]struct{}, len(a))
	for _, x := range a {
		m[x] = struct{}{}
	}
	for _, y := range b {
		if _, ok := m[y]; !ok {
			return false
		}
	}
	return true
}
