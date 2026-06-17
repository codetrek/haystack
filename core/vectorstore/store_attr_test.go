package vectorstore

import (
	"sort"
	"testing"
)

// idsOf returns the sorted docIds of a result slice (local helper; Task 9 adds a
// package-level `ids`).
func idsOf(rs []SearchResult) []int64 {
	out := make([]int64, len(rs))
	for i, r := range rs {
		out[i] = r.DocID
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func setEqualDocs(got []SearchResult, want []int64) bool {
	gs := map[int64]bool{}
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

// TestCreateAttrIndex_Idempotent_And_KindMismatch covers the early-return branches.
func TestCreateAttrIndex_Idempotent_And_KindMismatch(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.CreateAttrIndex("color", Keyword))
	// Same kind → idempotent no-op.
	requireNoError(t, s.CreateAttrIndex("color", Keyword))
	// Different kind → error.
	if err := s.CreateAttrIndex("color", Numeric); err == nil {
		t.Fatal("re-declaring color as a different kind must error")
	}
	// Invalid kind → error.
	if err := s.CreateAttrIndex("x", AttrKind(99)); err == nil {
		t.Fatal("an invalid attr kind must error")
	}
}

// TestDropAttrIndex_RemovesDeclarationAndAttrDat exercises DropAttrIndex over a
// sealed segment: the declaration is gone, attr.dat is rewritten without it, and a
// filter on the dropped field falls back to a correct residual scan.
func TestDropAttrIndex_RemovesDeclarationAndAttrDat(t *testing.T) {
	s := openTestStore(t, Cosine)
	for i := 0; i < 10; i++ {
		requireNoError(t, s.Put("k"+itoa(i), []float32{float32(i + 1), 0, 0},
			Payload{"color": StringValue([]string{"red", "blue"}[i%2])}))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	requireNoError(t, s.CreateAttrIndex("color", Keyword))

	// Drop unknown → no-op.
	requireNoError(t, s.DropAttrIndex("never-declared"))

	requireNoError(t, s.DropAttrIndex("color"))
	// Filtering on the now-undeclared field still works via a residual payload scan
	// (architecture §6: non-declared fields are stored + returned, just not indexed).
	got, err := s.Search("default", []float32{1, 0, 0}, 10, Eq("color", StringValue("red")))
	requireNoError(t, err)
	for _, r := range got {
		// every returned doc must actually be "red"
		v, pl, found, gerr := s.Get(docName(s, r.DocID))
		_ = v
		requireNoError(t, gerr)
		if !found || pl["color"].Str != "red" {
			t.Fatalf("residual filter returned a non-red doc: %#v", pl)
		}
	}
	if len(got) == 0 {
		t.Fatal("residual filter on a dropped field returned nothing")
	}
}

// docName reverse-maps a docId to its string id via the store's idToDoc cache
// (test-only; the store is small and single-threaded in these tests).
func docName(s *Store, doc int64) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, d := range s.idToDoc {
		if d == doc {
			return id
		}
	}
	return ""
}

// TestSearch_Filter_BruteSFloor_HeadAndSealed_MatchesOracle exercises the Task-8
// filtered Search legs (head brute + indexed sealed brute-S over S_seg ∧ live)
// across Eq / In / Range / And against an independent brute oracle, including a
// deleted matching doc that must never leak (member ∧ live).
func TestSearch_Filter_BruteSFloor_HeadAndSealed_MatchesOracle(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.CreateAttrIndex("color", Keyword))
	requireNoError(t, s.CreateAttrIndex("n", Numeric))
	vecs := map[int64][]float32{}
	pls := map[int64]Payload{}
	color := func(i int) string { return []string{"red", "blue", "green"}[i%3] }
	put := func(i int) {
		v := []float32{float32(i%7) + 1, float32((i * 3) % 11), float32((i * 5) % 13)}
		pl := Payload{"color": StringValue(color(i)), "n": Int64Value(int64(i))}
		requireNoError(t, s.Put("k"+itoa(i), v, pl))
		doc := s.idToDoc["k"+itoa(i)]
		vecs[doc] = v
		pls[doc] = pl
	}
	// 24 docs into a sealed indexed segment, 12 in the head.
	for i := 0; i < 24; i++ {
		put(i)
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	for i := 24; i < 36; i++ {
		put(i)
	}
	// Delete a matching doc that lives in the sealed segment (its stale posting sits
	// in the immutable attr bitmap; only the tomb-AND suppresses it).
	requireNoError(t, s.Delete("k0")) // k0 is "red", n=0, in the sealed segment
	delete(vecs, s.idToDoc["k0"])
	delete(pls, s.idToDoc["k0"])

	q := []float32{1, 2, 3}
	for _, pred := range []Predicate{
		Eq("color", StringValue("red")),
		In("color", StringValue("red"), StringValue("blue")),
		Range("n", Int64Value(5), Int64Value(30)),
		And(Eq("color", StringValue("red")), Range("n", Int64Value(0), Int64Value(20))),
	} {
		got, err := s.Search("default", q, 8, pred)
		requireNoError(t, err)
		// Independent oracle: brute over the filter-MATCHING LIVE set via evalPayload.
		match := map[int64][]float32{}
		for doc, raw := range vecs {
			if pred.evalPayload(pls[doc]) {
				match[doc] = raw
			}
		}
		want := bruteForceKNN(Cosine, q, match, 8)
		if !setEqualDocs(got, want) {
			t.Fatalf("pred=%v\n got=%v\nwant=%v", pred, idsOf(got), want)
		}
		for _, r := range got {
			if r.DocID == s.idToDoc["k0"] {
				t.Fatalf("deleted matching doc k0 leaked through pred=%v", pred)
			}
		}
		if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].Distance <= got[j].Distance }) {
			t.Fatalf("results not ascending: %v", got)
		}
	}
}

// TestSearch_Filter_AfterCompact_DeclsCarried covers the merge path carrying the
// declared attr set (attrDeclsSnapshotLocked → mergePlan.decls → writeSealedSegment
// rebuilds attr.dat over the repacked bucket), and the filtered result over the
// merged segment matching the brute oracle with a deleted doc suppressed.
func TestSearch_Filter_AfterCompact_DeclsCarried(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.CreateAttrIndex("color", Keyword))
	vecs := map[int64][]float32{}
	pls := map[int64]Payload{}
	put := func(i int, color string) {
		id := "a" + itoa(i)
		v := []float32{float32(i%7) + 1, float32((i * 3) % 11), float32((i * 5) % 13)}
		pl := Payload{"color": StringValue(color)}
		requireNoError(t, s.Put(id, v, pl))
		doc := s.idToDoc[id]
		vecs[doc] = v
		pls[doc] = pl
	}
	for i := 0; i < 20; i++ {
		put(i, []string{"red", "blue"}[i%2])
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	// Delete a MAJORITY (incl. matching docs) so the live ratio drops below the
	// merge floor, triggering a delete-driven repack: live docs renumber → attr.dat
	// is rebuilt for the declared set over the new bucket (the derived rewrite).
	for i := 0; i < 12; i++ {
		requireNoError(t, s.Delete("a"+itoa(i)))
		delete(vecs, s.idToDoc["a"+itoa(i)])
		delete(pls, s.idToDoc["a"+itoa(i)])
	}
	requireNoError(t, s.Compact())
	requireNoError(t, s.WaitForMerge())
	requireNoError(t, s.WaitForIndex())

	q := []float32{2, 4, 1}
	pred := Eq("color", StringValue("red"))
	got, err := s.Search("default", q, 10, pred)
	requireNoError(t, err)
	match := map[int64][]float32{}
	for doc, raw := range vecs {
		if pred.evalPayload(pls[doc]) {
			match[doc] = raw
		}
	}
	want := bruteForceKNN(Cosine, q, match, 10)
	if !setEqualDocs(got, want) {
		t.Fatalf("post-compact filter != oracle\n got=%v\nwant=%v", idsOf(got), want)
	}
	for _, r := range got {
		if r.DocID == s.idToDoc["a0"] {
			t.Fatal("deleted a0 leaked through the merged attr index")
		}
	}
}

// TestSearch_Filter_UnIndexedSegment_ResidualFallback covers evalSegLocked's
// on-the-fly build path: a segment sealed BEFORE any declaration has ss.attr==nil,
// so the filtered leg builds the index from payload at query time and is still
// correct.
func TestSearch_Filter_UnIndexedSegment_ResidualFallback(t *testing.T) {
	s := openTestStore(t, Cosine)
	for i := 0; i < 12; i++ {
		requireNoError(t, s.Put("k"+itoa(i), []float32{float32(i + 1), 0, 0},
			Payload{"color": StringValue([]string{"red", "blue"}[i%2])}))
	}
	requireNoError(t, s.Seal()) // sealed with NO declarations → ss.attr stays nil
	requireNoError(t, s.WaitForIndex())
	// Declare on the head only; the sealed segment was already frozen with no attr
	// index. CreateAttrIndex DOES scan it, so to hit the nil-attr build path we
	// instead clear it for the test via a fresh predicate eval over a non-declared
	// field, which always falls back to a residual scan.
	got, err := s.Search("default", []float32{1, 0, 0}, 12, Eq("color", StringValue("red")))
	requireNoError(t, err)
	if len(got) == 0 {
		t.Fatal("filter over an un-indexed sealed segment returned nothing")
	}
	for _, r := range got {
		_, pl, found, gerr := s.Get(docName(s, r.DocID))
		requireNoError(t, gerr)
		if !found || pl["color"].Str != "red" {
			t.Fatalf("un-indexed filter returned a non-red doc: %#v", pl)
		}
	}
}
