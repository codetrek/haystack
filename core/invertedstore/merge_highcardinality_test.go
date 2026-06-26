package invertedstore

import (
	"sort"
	"testing"
)

// merge_highcardinality_test.go — characterization of the merge reconciliation under the
// "one very high-cardinality keyword adjacent to many tiny keywords" shape (spec
// invertedstore-merge-mapreuse-regression-fix-spec.md §7.2; task T1).
//
// This is a CORRECTNESS / coverage case, explicitly NOT a perf-regression guard: the C.4 fix
// changes only WHERE the adds/dels reconciliation maps are allocated (hoisted+clear()-reused vs
// fresh-per-key), never the merged OUTPUT, so this test passes byte-identically on the buggy
// (clear()-reuse) and fixed (fresh-map) tree. Its job is to (a) fill a real coverage gap — no
// other test builds a single giant posting list flanked by a long tail of tiny keywords, the exact
// map-population shape the fix touches — and (b) document that the revert preserves behavior.
//
// Two INDEPENDENT sub-cases / stores: a covering merge compacts everything to ONE segment, so a
// tiered merge cannot be run after a covering one in the same store.
//
//   - Tiered  (mergeOneLevelForTest):   keeps BOTH adds and dels, never drops a key.
//   - Covering (coveringMergeForTest):  drops ALL dels and drops any key with zero surviving adds.
//
// The assertion seam is segInvRecords(seg, tbl) (the read-back of the sealed merged segment), NOT a
// Search-only presence check — Search would hide the del side a tiered merge must preserve.

// bigKeyword is the single high-cardinality term whose posting list dominates the reconciliation
// maps; bigCard keeps the unit test fast while still building a map far larger than the tiny ones
// (the regression's "huge map then tiny maps" drain shape).
const (
	bigKeyword = "thebigterm"
	bigCard    = 20000
)

// refModel is an independent newest-wins reference for one keyword across the merge sources. It
// mirrors merge.go:296-325 EXACTLY: per source, process adds THEN dels (a del overrides an add for
// the same docid within one source); across sources, the LATER (newer / higher-id) source wins.
// applySource is called once per source in OLDEST -> NEWEST order.
type refModel struct {
	adds map[int64]struct{}
	dels map[int64]struct{}
}

func newRefModel() *refModel {
	return &refModel{adds: map[int64]struct{}{}, dels: map[int64]struct{}{}}
}

// applySource folds one source's resolved adds/dels into the running newest-wins state. Within the
// source adds are processed THEN dels (so a del overrides an add for the same docid); a later
// applySource call (a newer source) overrides an earlier one for the same docid. This is the exact
// rule of merge.go's `decodeDocs(ab, ...)` then `decodeDocs(db, ...)` over hit in oldest->newest
// cursor order.
func (m *refModel) applySource(adds, dels []int64) {
	for _, d := range adds {
		delete(m.dels, d)
		m.adds[d] = struct{}{}
	}
	for _, d := range dels {
		delete(m.adds, d)
		m.dels[d] = struct{}{}
	}
}

// tieredResult is the reference for a TIERED merge: keep BOTH adds and dels (a del must still
// suppress an older add in a segment outside this merge), and NEVER drop the key.
func (m *refModel) tieredResult() (adds, dels []int64) {
	return sortedKeys(m.adds), sortedKeys(m.dels)
}

// coveringResult is the reference for a COVERING merge: drop ALL dels (nothing older survives to be
// suppressed) and drop the key entirely when no add survives. keep=false => the key must be GONE
// from the merged segment.
func (m *refModel) coveringResult() (adds []int64, keep bool) {
	adds = sortedKeys(m.adds)
	return adds, len(adds) > 0
}

func sortedKeys(m map[int64]struct{}) []int64 {
	out := make([]int64, 0, len(m))
	for d := range m {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sortedInt64Copy(s []int64) []int64 {
	out := append([]int64(nil), s...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func equalInt64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestMerge_HighCardinality_TieredPreservesReconciliation builds Fanout L0 segments where ONE
// keyword (bigKeyword) carries a huge posting list and MANY tiny keywords (1-2 docids each) flank it
// on BOTH sides of the term-dict key order, so the merge drain hits a huge map and then tiny maps.
// Cross-source re-adds AND tombstones on the big keyword exercise newest-wins + the del side. After a
// tiered merge, every keyword's adds/dels in the single merged segment must equal the independent
// reference (which keeps both adds and dels and never drops a key).
func TestMerge_HighCardinality_TieredPreservesReconciliation(t *testing.T) {
	const fanout = 4
	s, tbl := newMergeStore(t, fanout)
	defer s.CloseAndWait()

	// Per-keyword reference, folded source-by-source in oldest->newest (== spill) order.
	ref := map[string]*refModel{}
	refFor := func(kw string) *refModel {
		m := ref[kw]
		if m == nil {
			m = newRefModel()
			ref[kw] = m
		}
		return m
	}
	// Build one source segment: issue the recorded ops into the head, fold them into the reference
	// (resolving the source's own ops latest-wins per docid so the recorded adds/dels match what the
	// sealed segment stores), then forceSpill to seal it.
	type op struct {
		kw    string
		docid int64
		add   bool
	}
	buildSource := func(ops []op) {
		// resolve this source's ops latest-wins per (kw,docid): the LAST op for a docid decides
		// add-vs-del, exactly as resolveOps does at spill time (the sealed segment value holds at most
		// one action per (kw,docid)).
		last := map[string]map[int64]bool{} // kw -> docid -> isAdd (latest)
		order := []string{}                 // first-seen keyword order (deterministic)
		seen := map[string]bool{}
		for _, o := range ops {
			if last[o.kw] == nil {
				last[o.kw] = map[int64]bool{}
			}
			last[o.kw][o.docid] = o.add
			if !seen[o.kw] {
				seen[o.kw] = true
				order = append(order, o.kw)
			}
			if o.add {
				s.addPostingForTest(tbl, o.kw, o.docid)
			} else {
				s.tombstoneForTest(tbl, o.kw, o.docid)
			}
		}
		for _, kw := range order {
			var adds, dels []int64
			for d, isAdd := range last[kw] {
				if isAdd {
					adds = append(adds, d)
				} else {
					dels = append(dels, d)
				}
			}
			refFor(kw).applySource(sortedInt64Copy(adds), sortedInt64Copy(dels))
		}
		s.forceSpill(tbl)
	}

	// Tiny keywords are split into a band that sorts BEFORE bigKeyword and a band that sorts AFTER it
	// in the [I] key order (segInvRecords / the merge walk both order by keyword bytes). "aa.." sorts
	// before "thebigterm"; "zz.." sorts after — so the big map is flanked by tiny maps on both sides.
	loKw := func(n int) string { return kwf("aa_tiny_", n) }
	hiKw := func(n int) string { return kwf("zz_tiny_", n) }

	// --- Source 0 (oldest): big keyword gets the first half of its posting list; tiny flankers. ---
	var src0 []op
	for _, n := range []int{0, 1, 2, 3} {
		src0 = append(src0, op{loKw(n), int64(100000 + n), true}) // tiny, before big
		src0 = append(src0, op{hiKw(n), int64(200000 + n), true}) // tiny, after big
	}
	for d := int64(0); d < bigCard/2; d++ {
		src0 = append(src0, op{bigKeyword, d, true})
	}
	// a tiny keyword that will be re-added live in a later source after a tombstone here
	src0 = append(src0, op{loKw(99), 100099, true})
	buildSource(src0)

	// --- Source 1: big keyword gets the second half + a few tombstones on docids it added in src0
	// (cross-source del on the big keyword); plus more tiny flankers + a cross-source re-add. ---
	var src1 []op
	for d := int64(bigCard / 2); d < bigCard; d++ {
		src1 = append(src1, op{bigKeyword, d, true})
	}
	// tombstone three docids the big keyword added in src0 (newest-wins: these become DEL in tiered)
	for _, d := range []int64{5, 10, 15} {
		src1 = append(src1, op{bigKeyword, d, false})
	}
	for _, n := range []int{4, 5, 6} {
		src1 = append(src1, op{loKw(n), int64(100000 + n), true})
		src1 = append(src1, op{hiKw(n), int64(200000 + n), true})
	}
	// cross-source re-add: loKw(99) doc 100099 was added in src0; tombstone then re-add it here so the
	// newest action (add) wins within src1, and the whole keyword stays a live add overall.
	src1 = append(src1, op{loKw(99), 100099, false})
	src1 = append(src1, op{loKw(99), 100099, true})
	buildSource(src1)

	// --- Source 2: a cross-source re-add of two big-keyword docids tombstoned in src1 (so newest add
	// wins again -> they go back to live adds), plus more tiny flankers. ---
	var src2 []op
	src2 = append(src2, op{bigKeyword, 5, true})  // re-add (src1 deleted it) -> live
	src2 = append(src2, op{bigKeyword, 10, true}) // re-add -> live; doc 15 stays deleted
	for _, n := range []int{7, 8} {
		src2 = append(src2, op{loKw(n), int64(100000 + n), true})
		src2 = append(src2, op{hiKw(n), int64(200000 + n), true})
	}
	buildSource(src2)

	// --- Source 3 (newest): a cross-source tombstone on a big-keyword docid + tiny flankers, to fill
	// out Fanout segments so the tiered merge fires. ---
	var src3 []op
	src3 = append(src3, op{bigKeyword, 7, false}) // delete doc 7 (added live in src0) -> newest=del
	for _, n := range []int{9, 10} {
		src3 = append(src3, op{loKw(n), int64(100000 + n), true})
		src3 = append(src3, op{hiKw(n), int64(200000 + n), true})
	}
	buildSource(src3)

	if len(s.segs) != fanout {
		t.Fatalf("expected %d L0 segments before merge, got %d", fanout, len(s.segs))
	}

	if !s.mergeOneLevelForTest(t) {
		t.Fatalf("expected a tiered merge to fire with %d L0 segments at Fanout %d", fanout, fanout)
	}
	if len(s.segs) != 1 {
		t.Fatalf("after merging %d L0 segments expected 1 segment, got %d", fanout, len(s.segs))
	}

	got := segInvRecords(s.segs[0], tbl)

	// Tiered keeps EVERY keyword (never drops a key) and BOTH its adds and dels must equal the model.
	if len(got) != len(ref) {
		t.Fatalf("tiered merged keyword count = %d, want %d (tiered must drop no key)", len(got), len(ref))
	}
	for kw, m := range ref {
		rec, ok := got[kw]
		if !ok {
			t.Fatalf("tiered merge dropped keyword %q (tiered must never drop a key)", kw)
		}
		wantAdds, wantDels := m.tieredResult()
		gotAdds := sortedInt64Copy(rec.adds)
		gotDels := sortedInt64Copy(rec.dels)
		if !equalInt64(gotAdds, wantAdds) {
			if kw == bigKeyword {
				t.Fatalf("tiered merge %q adds mismatch: got %d adds, want %d adds (first diff at the docid level)",
					kw, len(gotAdds), len(wantAdds))
			}
			t.Fatalf("tiered merge %q adds = %v, want %v", kw, gotAdds, wantAdds)
		}
		if !equalInt64(gotDels, wantDels) {
			t.Fatalf("tiered merge %q dels = %v, want %v", kw, gotDels, wantDels)
		}
	}

	// Spot-check the load-bearing reconciliation on the big keyword: docs 5 and 10 were add(src0) ->
	// del(src1) -> add(src2) => live adds; doc 7 add(src0) -> del(src3) => del; doc 15 add -> del =>
	// del. These prove the across-source newest-wins + the kept-del side under the huge map.
	big := got[bigKeyword]
	bigAdds := map[int64]bool{}
	for _, d := range big.adds {
		bigAdds[d] = true
	}
	bigDels := map[int64]bool{}
	for _, d := range big.dels {
		bigDels[d] = true
	}
	for _, d := range []int64{5, 10} {
		if !bigAdds[d] || bigDels[d] {
			t.Errorf("big keyword doc %d: add->del->add must reconcile to a LIVE add (adds=%v dels=%v)", d, bigAdds[d], bigDels[d])
		}
	}
	for _, d := range []int64{7, 15} {
		if bigAdds[d] || !bigDels[d] {
			t.Errorf("big keyword doc %d: add->del (newest) must reconcile to a DEL (adds=%v dels=%v)", d, bigAdds[d], bigDels[d])
		}
	}
}

// TestMerge_HighCardinality_CoveringReclaimsTombstones mirrors
// TestMerge_CoveringReclaimsTombstonesAndDuplicates under the high-cardinality shape: ONE big
// keyword with a huge posting list (some docids tombstoned), MANY tiny flanking keywords, AND a
// fully-tombstoned tiny keyword (every add cancelled by a later del). A covering merge drops ALL
// dels, keeps adds-only, and drops any key with zero surviving adds — so the merged segment's adds
// must equal the reference's covering result and the fully-tombstoned key must be GONE.
func TestMerge_HighCardinality_CoveringReclaimsTombstones(t *testing.T) {
	// High Fanout so ONLY the explicit covering merge fires (no tiered merge in between).
	s, tbl := newMergeStore(t, 100)
	defer s.CloseAndWait()

	ref := map[string]*refModel{}
	refFor := func(kw string) *refModel {
		m := ref[kw]
		if m == nil {
			m = newRefModel()
			ref[kw] = m
		}
		return m
	}
	type op struct {
		kw    string
		docid int64
		add   bool
	}
	buildSource := func(ops []op) {
		last := map[string]map[int64]bool{}
		order := []string{}
		seen := map[string]bool{}
		for _, o := range ops {
			if last[o.kw] == nil {
				last[o.kw] = map[int64]bool{}
			}
			last[o.kw][o.docid] = o.add
			if !seen[o.kw] {
				seen[o.kw] = true
				order = append(order, o.kw)
			}
			if o.add {
				s.addPostingForTest(tbl, o.kw, o.docid)
			} else {
				s.tombstoneForTest(tbl, o.kw, o.docid)
			}
		}
		for _, kw := range order {
			var adds, dels []int64
			for d, isAdd := range last[kw] {
				if isAdd {
					adds = append(adds, d)
				} else {
					dels = append(dels, d)
				}
			}
			refFor(kw).applySource(sortedInt64Copy(adds), sortedInt64Copy(dels))
		}
		s.forceSpill(tbl)
	}

	loKw := func(n int) string { return kwf("aa_tiny_", n) }
	hiKw := func(n int) string { return kwf("zz_tiny_", n) }
	const fullyTombstoned = "mm_doomed" // a tiny key whose single add is cancelled by a later del

	// --- Source 0: big keyword first half + tiny flankers + the doomed key gets an add (to be
	// cancelled later) + a tiny keyword whose only docid is tombstoned in a later source. ---
	var src0 []op
	for _, n := range []int{0, 1, 2} {
		src0 = append(src0, op{loKw(n), int64(100000 + n), true})
		src0 = append(src0, op{hiKw(n), int64(200000 + n), true})
	}
	for d := int64(0); d < bigCard/2; d++ {
		src0 = append(src0, op{bigKeyword, d, true})
	}
	src0 = append(src0, op{fullyTombstoned, 300000, true}) // the only add the doomed key ever gets
	buildSource(src0)

	// --- Source 1: big keyword second half + a tombstone on a big-keyword docid (dangling tombstone
	// the covering merge must reclaim) + tiny flankers + the doomed key's cancelling tombstone. ---
	var src1 []op
	for d := int64(bigCard / 2); d < bigCard; d++ {
		src1 = append(src1, op{bigKeyword, d, true})
	}
	for _, d := range []int64{3, 8} {
		src1 = append(src1, op{bigKeyword, d, false}) // tombstone -> dangling under covering
	}
	for _, n := range []int{3, 4} {
		src1 = append(src1, op{loKw(n), int64(100000 + n), true})
		src1 = append(src1, op{hiKw(n), int64(200000 + n), true})
	}
	src1 = append(src1, op{fullyTombstoned, 300000, false}) // cancels the doomed key's only add
	buildSource(src1)

	// --- Source 2 (newest): a cross-source re-add of one tombstoned big-keyword docid (so it is live
	// again, surviving the covering merge) + tiny flankers. ---
	var src2 []op
	src2 = append(src2, op{bigKeyword, 3, true}) // re-add (src1 deleted it) -> live; doc 8 stays del
	for _, n := range []int{5, 6} {
		src2 = append(src2, op{loKw(n), int64(100000 + n), true})
		src2 = append(src2, op{hiKw(n), int64(200000 + n), true})
	}
	buildSource(src2)

	preSegs := len(s.segs)
	if preSegs < 3 {
		t.Fatalf("expected >=3 segments before the covering merge, got %d", preSegs)
	}

	s.coveringMergeForTest(t)

	if len(s.segs) != 1 {
		t.Fatalf("covering merge must compact to 1 segment, got %d", len(s.segs))
	}
	got := segInvRecords(s.segs[0], tbl)

	// Build the covering reference: every keyword with a surviving add must be present with EXACTLY
	// those adds and ZERO dels; a keyword with no surviving add must be ABSENT.
	wantKeys := 0
	for kw, m := range ref {
		wantAdds, keep := m.coveringResult()
		if !keep {
			if _, ok := got[kw]; ok {
				t.Errorf("covering merge must drop zero-add key %q, got %v", kw, got[kw])
			}
			continue
		}
		wantKeys++
		rec, ok := got[kw]
		if !ok {
			t.Fatalf("covering merge dropped live keyword %q (it has surviving adds %v)", kw, wantAdds)
		}
		if len(rec.dels) != 0 {
			t.Errorf("covering merge must reclaim ALL dels, %q kept dels=%v", kw, rec.dels)
		}
		gotAdds := sortedInt64Copy(rec.adds)
		if !equalInt64(gotAdds, wantAdds) {
			if kw == bigKeyword {
				t.Errorf("covering merge %q adds count = %d, want %d", kw, len(gotAdds), len(wantAdds))
			} else {
				t.Errorf("covering merge %q adds = %v, want %v", kw, gotAdds, wantAdds)
			}
		}
	}
	if len(got) != wantKeys {
		t.Errorf("covering merged keyword count = %d, want %d (covering drops zero-add keys)", len(got), wantKeys)
	}

	// Load-bearing: the fully-tombstoned key is GONE (its single add was cancelled by a later del, and
	// covering drops all dels -> zero surviving adds -> key dropped). Mirror the "ghost" assertion of
	// TestMerge_CoveringReclaimsTombstonesAndDuplicates.
	if _, ok := got[fullyTombstoned]; ok {
		t.Errorf("covering merge must drop the fully-tombstoned key %q, got %v", fullyTombstoned, got[fullyTombstoned])
	}
	// Load-bearing: the big keyword kept NO dels (the two tombstoned docids are reclaimed; doc 3 came
	// back live via the src2 re-add, doc 8 stays gone as a clean miss — not a del).
	big, ok := got[bigKeyword]
	if !ok {
		t.Fatalf("covering merge dropped the big keyword (it has thousands of surviving adds)")
	}
	if len(big.dels) != 0 {
		t.Errorf("covering merge must reclaim the big keyword's dels, kept %v", big.dels)
	}
	bigAdds := map[int64]bool{}
	for _, d := range big.adds {
		bigAdds[d] = true
	}
	if !bigAdds[3] {
		t.Errorf("big keyword doc 3 (del then re-added newest) must survive the covering merge as a live add")
	}
	if bigAdds[8] {
		t.Errorf("big keyword doc 8 (tombstoned, never re-added) must be reclaimed (absent), not a live add")
	}
}
