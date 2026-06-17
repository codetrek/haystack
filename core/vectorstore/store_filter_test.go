package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// bruteOracleFiltered is the INDEPENDENT oracle: it walks the live docs, applies
// the predicate via evalPayload (NOT the attr index), and returns the exact top-k
// docIds over the filter-MATCHING LIVE set. This must NOT reuse production
// segment eval (anti-tautology, correctness-tdd finding).
func bruteOracleFiltered(m Metric, q []float32, vecs map[int64][]float32, pls map[int64]Payload, pred Predicate, k int) []int64 {
	match := make(map[int64][]float32)
	for doc, raw := range vecs {
		if pred == nil || pred.evalPayload(pls[doc]) {
			match[doc] = raw
		}
	}
	return bruteForceKNN(m, q, match, k)
}

func setEqual(got []SearchResult, want []int64) bool {
	gs := make(map[int64]bool)
	for _, r := range got {
		gs[r.DocID] = true
	}
	if len(gs) != len(want) {
		return false
	}
	for _, d := range want {
		if !gs[d] {
			return false
		}
	}
	return true
}

// buildFilterStore creates a store with N docs across a sealed indexed segment +
// a head, with a declared color(Keyword) + n(Numeric), returning the live vecs +
// payloads for the oracle. selectColor controls per-doc color so selectivity is
// tunable.
func buildFilterStore(t *testing.T, n, sealAt int, color func(i int) string) (*Store, map[int64][]float32, map[int64]Payload) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(7))
	vecs := map[int64][]float32{}
	pls := map[int64]Payload{}
	put := func(i int) {
		v := make([]float32, 8)
		for d := range v {
			v[d] = rng.Float32()
		}
		pl := Payload{"color": StringValue(color(i)), "n": Int64Value(int64(i))}
		requireNoError(t, s.Put("k"+itoa(i), v, pl))
		doc := s.idToDoc["k"+itoa(i)]
		vecs[doc] = v
		pls[doc] = pl
	}
	for i := 0; i < sealAt; i++ {
		put(i)
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	requireNoError(t, s.CreateAttrIndex("color", Keyword))
	requireNoError(t, s.CreateAttrIndex("n", Numeric))
	for i := sealAt; i < n; i++ { // remainder stays in the head
		put(i)
	}
	return s, vecs, pls
}

func TestSearch_Filter_BothBranches_MatchOracle(t *testing.T) {
	// color cycles red/blue/green → ~1/3 selectivity; vary T to force each branch.
	color := func(i int) string { return []string{"red", "blue", "green"}[i%3] }
	for _, tc := range []struct {
		name string
		T    int
	}{
		{"forceBruteS_highT", 1 << 30}, // |S_seg| <= T always → brute-S branch
		{"forceGraphS_lowT", 0},        // |S_seg| > T always → graph∩S branch
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, vecs, pls := buildFilterStore(t, 120, 80, color)
			s.attrSearchT = tc.T
			requireNoError(t, s.WaitForIndex())
			q := vecs[s.idToDoc["k3"]]
			for _, pred := range []Predicate{
				Eq("color", StringValue("red")),
				In("color", StringValue("red"), StringValue("blue")),
				Range("n", Int64Value(50), Int64Value(150)),
				And(Eq("color", StringValue("red")), Range("n", Int64Value(0), Int64Value(120))),
			} {
				before := s.graphSDispatches.Load()
				got, err := s.Search("default", q, 10, pred)
				requireNoError(t, err)
				took := s.graphSDispatches.Load() - before
				// Pin the branch per-case (appendix #25): a high T must NEVER take the
				// graph∩S leg (all brute-S), a T=0 must ALWAYS take it for the indexed
				// segment (|S_seg| > 0). Without this the graph∩S path is an untested
				// correctness path: brute-S is a correct superset answer either way.
				if tc.T == 0 && took == 0 {
					t.Fatalf("[%s] pred=%v expected graph∩S branch to run, but it did not", tc.name, pred)
				}
				if tc.T == 1<<30 && took != 0 {
					t.Fatalf("[%s] pred=%v expected only brute-S, but graph∩S ran %d times", tc.name, pred, took)
				}
				want := bruteOracleFiltered(Cosine, q, vecs, pls, pred, 10)
				if !setEqual(got, want) {
					t.Fatalf("[%s] pred=%v\n got=%v\nwant=%v", tc.name, pred, ids(got), want)
				}
				if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].Distance <= got[j].Distance }) {
					t.Fatalf("results not ascending: %v", got)
				}
			}
		})
	}
}

func TestSearch_Filter_MatchAll_EqualsUnfiltered(t *testing.T) {
	s, vecs, _ := buildFilterStore(t, 120, 80, func(i int) string { return "red" })
	q := vecs[s.idToDoc["k1"]]
	unf, err := s.Search("default", q, 10, nil)
	requireNoError(t, err)
	fil, err := s.Search("default", q, 10, Eq("color", StringValue("red"))) // matches all
	requireNoError(t, err)
	wantIDs := make([]int64, len(unf))
	for i, r := range unf {
		wantIDs[i] = r.DocID
	}
	if !setEqual(fil, wantIDs) {
		t.Fatalf("match-all filter != unfiltered:\n fil=%v\n unf=%v", ids(fil), wantIDs)
	}
}

func TestSearch_Filter_EmptyMatch_NoPanic(t *testing.T) {
	s, vecs, _ := buildFilterStore(t, 60, 40, func(i int) string { return "red" })
	q := vecs[s.idToDoc["k1"]]
	got, err := s.Search("default", q, 10, Eq("color", StringValue("nonexistent")))
	requireNoError(t, err)
	if len(got) != 0 {
		t.Fatalf("empty-match filter returned %d results, want 0", len(got))
	}
}

func TestSearch_Filter_DeletedMatchingDoc_NeverLeaks(t *testing.T) {
	for _, T := range []int{1 << 30, 0} { // both branches
		s, vecs, pls := buildFilterStore(t, 100, 70, func(i int) string {
			if i%2 == 0 {
				return "red"
			}
			return "blue"
		})
		s.attrSearchT = T
		// Delete a matching doc from the SEALED segment (stale value sits in its
		// immutable bitmap; only the tomb AND suppresses it).
		requireNoError(t, s.Delete("k0")) // k0 is "red", in the sealed segment
		delete(vecs, s.idToDoc["k0"])
		delete(pls, s.idToDoc["k0"])
		requireNoError(t, s.WaitForIndex())
		q := vecs[s.idToDoc["k2"]]
		got, err := s.Search("default", q, 10, Eq("color", StringValue("red")))
		requireNoError(t, err)
		for _, r := range got {
			if r.DocID == s.idToDoc["k0"] {
				t.Fatalf("[T=%d] deleted matching doc k0 leaked into filtered results", T)
			}
		}
		want := bruteOracleFiltered(Cosine, q, vecs, pls, Eq("color", StringValue("red")), 10)
		if !setEqual(got, want) {
			t.Fatalf("[T=%d] post-delete filter != oracle\n got=%v\nwant=%v", T, ids(got), want)
		}
	}
}

func TestSearch_Filter_UnsupportedPredicate_Errors(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.Put("a", []float32{1, 0, 0}, Payload{"x": StringValue("y")}))
	// validatePredicate rejects a Range on a declared Keyword field.
	requireNoError(t, s.CreateAttrIndex("x", Keyword))
	if _, err := s.Search("default", []float32{1, 0, 0}, 5, Range("x", Int64Value(1), Int64Value(2))); err == nil {
		t.Fatal("Range on a Keyword field must error from Search")
	}
}

func ids(rs []SearchResult) []int64 {
	out := make([]int64, len(rs))
	for i, r := range rs {
		out[i] = r.DocID
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// TestSearch_Filter_GraphDistantSelective_GraphSBranch red-proofs the graph∩S
// leg: a SELECTIVE filter (1-in-50 "hot") whose matching docs are SPREAD (not
// clustered near the query), so they are graph-distant from any single entry
// region. A naive post-filter over the top-k graph hits would return ~0 matches;
// filter-during-traversal (still expanding THROUGH non-members) must recover them.
// T=0 forces the >T graph∩S branch, and the counter assertion proves that leg ran
// (so this is not silently the brute-S floor under test).
func TestSearch_Filter_GraphDistantSelective_GraphSBranch(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(99))
	dim := 16
	vecs := map[int64][]float32{}
	pls := map[int64]Payload{}
	put := func(id string, v []float32, hot bool) {
		pl := Payload{"hot": BoolValue(hot)}
		requireNoError(t, s.Put(id, v, pl))
		doc := s.idToDoc[id]
		vecs[doc] = v
		pls[doc] = pl
	}
	// 1 in 50 docs is "hot" (selective). Hot docs are NOT clustered near the query
	// — they are spread, so they are graph-distant from any single entry region.
	n := 500
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		put("k"+itoa(i), v, i%50 == 0)
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	requireNoError(t, s.CreateAttrIndex("hot", Keyword))
	s.attrSearchT = 0 // force graph∩S (|S_seg| ~ 10 > 0)
	requireNoError(t, s.WaitForIndex())

	q := make([]float32, dim)
	for d := range q {
		q[d] = rng.Float32()
	}
	pred := Eq("hot", BoolValue(true))
	before := s.graphSDispatches.Load()
	got, err := s.Search("default", q, 5, pred)
	requireNoError(t, err)
	if s.graphSDispatches.Load() == before {
		t.Fatal("expected the graph∩S branch to run (T=0, |S_seg|>0), but it did not")
	}
	want := bruteOracleFiltered(Cosine, q, vecs, pls, pred, 5)
	// filter-during-traversal must recover the in-set neighbors a post-filter misses.
	if r := recallAtK(got, want); r < 0.8 {
		t.Fatalf("graph∩S recall@5 = %.2f over a selective graph-distant filter, want >= 0.8 (post-filter would be ~0)", r)
	}
}

// TestSearch_Filter_AfterMerge_MatchesOracle proves the derived attr index is
// REBUILT (not copied) when a merge repacks live docs into a new bucket with
// renumbered slots, and that a doc deleted before the merge leaves no stale
// posting in the merged segment's attr.dat (member AND live preserved through
// the rewrite). Both adaptive branches are pinned via attrSearchT.
func TestSearch_Filter_AfterMerge_MatchesOracle(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(123))
	dim := 8
	vecs := map[int64][]float32{}
	pls := map[int64]Payload{}
	requireNoError(t, s.CreateAttrIndex("color", Keyword))
	put := func(id string, color string) {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		pl := Payload{"color": StringValue(color)}
		requireNoError(t, s.Put(id, v, pl))
		doc := s.idToDoc[id]
		vecs[doc] = v
		pls[doc] = pl
	}
	// Two sealed segments, each with mixed colors.
	for i := 0; i < 40; i++ {
		put("a"+itoa(i), []string{"red", "blue"}[i%2])
	}
	requireNoError(t, s.Seal())
	for i := 0; i < 40; i++ {
		put("b"+itoa(i), []string{"red", "green"}[i%2])
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	// Snapshot segment "a"'s id so we can assert the repack actually ran (a new
	// segment id replaces it once its live ratio crosses MergeFloor).
	s.mu.RLock()
	preIDs := append([]segID(nil), s.sealedID...)
	s.mu.RUnlock()
	// Delete a MAJORITY of segment "a" so its live ratio drops below MergeFloor
	// (0.5) and Compact's delete-driven repack fires — the live docs are bin-packed
	// into a fresh bucket with RENUMBERED slots, so the merged segment's attr.dat
	// must be REBUILT from the repacked payloads, not copied. (Deleting a single doc
	// would leave liveRatio≈0.975, a no-op merge — plan deviation, see NOTE.)
	for i := 0; i < 25; i++ {
		id := "a" + itoa(i)
		doc := s.idToDoc[id]
		if doc == 0 { // sealed → absent from idToDoc cache; resolve durably
			d, ok, err := s.lookupDocID(id)
			requireNoError(t, err)
			if ok {
				doc = d
			}
		}
		requireNoError(t, s.Delete(id))
		delete(vecs, doc)
		delete(pls, doc)
	}
	requireNoError(t, s.Compact()) // delete-driven repack of segment "a"
	requireNoError(t, s.WaitForIndex())
	// Confirm the repack actually renumbered slots: segment "a" (preIDs[0]) is
	// deflated below MergeFloor, so it is replaced by a fresh repacked segment with
	// a new id; the untouched "b" segment survives.
	aSeg := preIDs[0]
	s.mu.RLock()
	stillThere := false
	for _, cur := range s.sealedID {
		if cur == aSeg {
			stillThere = true
		}
	}
	postIDs := append([]segID(nil), s.sealedID...)
	s.mu.RUnlock()
	if stillThere {
		t.Fatalf("expected delete-driven repack to replace segment %d with renumbered slots; pre=%v post=%v", aSeg, preIDs, postIDs)
	}

	a0Doc, _, _ := s.lookupDocID("a0")
	b1Doc, ok, err := s.lookupDocID("b1")
	requireNoError(t, err)
	if !ok {
		t.Fatal("lookupDocID(b1) not found")
	}
	q := vecs[b1Doc]
	for _, T := range []int{1 << 30, 0} {
		s.attrSearchT = T
		pred := Eq("color", StringValue("red"))
		got, err := s.Search("default", q, 10, pred)
		requireNoError(t, err)
		want := bruteOracleFiltered(Cosine, q, vecs, pls, pred, 10)
		if !setEqual(got, want) {
			t.Fatalf("[T=%d] post-merge filter != oracle\n got=%v\nwant=%v", T, ids(got), want)
		}
		// the deleted "red" a0 must be gone (no stale postings in the merged seg).
		for _, r := range got {
			if r.DocID == a0Doc {
				t.Fatalf("[T=%d] deleted a0 leaked through merged attr index", T)
			}
		}
	}
}

// TestSearch_Filter_RecoversAfterReopen proves the declared attr set survives a
// Close/Open cycle (manifest v3) and the per-segment attr index is loaded (or
// rebuilt from payload) on recovery, so a filtered Search after reopen matches
// the independent oracle.
func TestSearch_Filter_RecoversAfterReopen(t *testing.T) {
	dir := t.TempDir()
	kvs := newTestKV(t)
	s, err := Open(Options{Dir: dir, KV: kvs, Metric: Cosine})
	requireNoError(t, err)
	requireNoError(t, s.CreateAttrIndex("color", Keyword))
	vecs := map[int64][]float32{}
	pls := map[int64]Payload{}
	for i := 0; i < 60; i++ {
		v := []float32{float32(i + 1), float32(i % 7), 1}
		pl := Payload{"color": StringValue([]string{"red", "blue"}[i%2])}
		requireNoError(t, s.Put("k"+itoa(i), v, pl))
		doc := s.idToDoc["k"+itoa(i)]
		vecs[doc] = v
		pls[doc] = pl
	}
	requireNoError(t, s.Seal()) // crosses a seal boundary
	requireNoError(t, s.WaitForIndex())
	requireNoError(t, s.Close())

	s2, err := Open(Options{Dir: dir, KV: kvs, Metric: Cosine})
	requireNoError(t, err)
	defer s2.Close()
	requireNoError(t, s2.WaitForIndex())
	// NOTE (plan deviation): the plan read q := vecs[s2.idToDoc["k2"]], but after
	// reopen the head WAL was truncated at Seal, so a sealed doc's string id is
	// absent from the in-memory idToDoc cache (store.go:493). Resolve the docId via
	// the durable idtable lookup (lookupDocID), which reconstructs the same docId.
	k2Doc, ok, err := s2.lookupDocID("k2")
	requireNoError(t, err)
	if !ok {
		t.Fatal("lookupDocID(k2) not found after reopen")
	}
	q := vecs[k2Doc]
	pred := Eq("color", StringValue("red"))
	got, err := s2.Search("default", q, 10, pred)
	requireNoError(t, err)
	want := bruteOracleFiltered(Cosine, q, vecs, pls, pred, 10)
	if !setEqual(got, want) {
		t.Fatalf("post-reopen filter != oracle\n got=%v\nwant=%v", ids(got), want)
	}
}

// TestSearch_Filter_UpdateCrossSegment_CountedOnce proves that updating a doc
// whose old copy lives in a sealed segment and whose new copy lands in the head
// — both matching the filter — yields the doc EXACTLY once: the old sealed slot
// is tombstoned at Put and removed from S_seg by andLive, the head copy is
// counted by the head leg.
func TestSearch_Filter_UpdateCrossSegment_CountedOnce(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.CreateAttrIndex("color", Keyword))
	for i := 0; i < 40; i++ {
		requireNoError(t, s.Put("k"+itoa(i), []float32{float32(i + 1), 0, 0}, Payload{"color": StringValue("red")}))
	}
	requireNoError(t, s.Seal()) // k* now live in a sealed segment
	requireNoError(t, s.WaitForIndex())
	// Update k0: old "red" copy tombstoned in the sealed segment, new "red" copy in
	// the head — BOTH match the filter. It must appear exactly once.
	requireNoError(t, s.Put("k0", []float32{1, 0, 0}, Payload{"color": StringValue("red")}))
	got, err := s.Search("default", []float32{1, 0, 0}, 40, Eq("color", StringValue("red")))
	requireNoError(t, err)
	seen := 0
	for _, r := range got {
		if r.DocID == s.idToDoc["k0"] {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("updated cross-segment doc k0 appeared %d times, want 1", seen)
	}
}

// TestCreateAttrIndex_ConcurrentWithMerge_Race runs CreateAttrIndex concurrently
// with a Compact-driven merge. Both take s.mu (CreateAttrIndex for its whole
// per-segment scan, mergeAndPublish for the swap+close), so they serialize and no
// input mmap is closed while CreateAttrIndex reads it. The gate runs this under
// -race; the assertion is "no SIGSEGV / no data race", correctness is covered
// elsewhere (appendix #8 planMergeLocked indexed-only guard).
func TestCreateAttrIndex_ConcurrentWithMerge_Race(t *testing.T) {
	s := openTestStore(t, Cosine)
	for i := 0; i < 120; i++ {
		requireNoError(t, s.Put("k"+itoa(i), []float32{float32(i + 1), 0, 0},
			Payload{"color": StringValue([]string{"red", "blue"}[i%2])}))
		if i%40 == 39 {
			requireNoError(t, s.Seal())
		}
	}
	requireNoError(t, s.WaitForIndex())
	done := make(chan error, 1)
	go func() { done <- s.CreateAttrIndex("color", Keyword) }()
	_ = s.Compact()
	requireNoError(t, <-done)
	requireNoError(t, s.WaitForIndex())
	got, err := s.Search("default", []float32{1, 0, 0}, 5, Eq("color", StringValue("red")))
	requireNoError(t, err)
	_ = got // correctness covered elsewhere; this gate is -race clean + no SIGSEGV
}

// TestDropAttrIndex_Unknown_NoOp covers DropAttrIndex's early-return branch for a
// never-declared property (no panic, no error, no file work).
func TestDropAttrIndex_Unknown_NoOp(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.DropAttrIndex("never-declared"))
}

// TestGet_MalformedPayload_Errors covers Get's sealed-path decode-error branch
// (store.go: decodePayload(plBytes) error → surfaced, not a silent empty map). A
// single-doc segment is sealed, then the version byte of slot 0's payload blob is
// flipped on disk so decodePayload rejects it; Get must return that error.
func TestGet_MalformedPayload_Errors(t *testing.T) {
	dir := t.TempDir()
	kvs := newTestKV(t)
	s, err := Open(Options{Dir: dir, KV: kvs, Metric: Cosine})
	requireNoError(t, err)
	requireNoError(t, s.Put("a", []float32{1, 0, 0}, Payload{"k": StringValue("v")}))
	requireNoError(t, s.Seal()) // one-row sealed segment, no attr declared
	requireNoError(t, s.WaitForIndex())
	requireNoError(t, s.Close())

	// payload.dat layout: [segPageSize header][n*4 lens][concatenated blobs]. With
	// n=1 the slot-0 blob begins at segPageSize+4 and its first byte is
	// payloadFmtVersion(1). Flip it to 0xFF so decodePayload rejects the blob.
	matches, _ := filepath.Glob(filepath.Join(dir, "seg-*", "payload.dat"))
	if len(matches) == 0 {
		t.Fatal("expected a payload.dat to corrupt")
	}
	f, err := os.OpenFile(matches[0], os.O_RDWR, 0644)
	requireNoError(t, err)
	if _, err := f.WriteAt([]byte{0xFF}, int64(segPageSize+4)); err != nil {
		t.Fatalf("corrupt write: %v", err)
	}
	requireNoError(t, f.Close())

	// Reopen WITHOUT declaring an attr index (so no attr scan decodes the blob first);
	// the only decodePayload is in Get's sealed path.
	s2, err := Open(Options{Dir: dir, KV: kvs, Metric: Cosine})
	requireNoError(t, err)
	defer s2.Close()
	requireNoError(t, s2.WaitForIndex())
	_, _, _, gerr := s2.Get("a")
	if gerr == nil {
		t.Fatal("Get over a corrupt sealed payload must surface a decode error")
	}
}
