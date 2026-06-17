package vectorstore

import (
	"sort"
	"testing"
)

// TestSearch_HeadLeg_UsesHeadAttr_MatchesOracle RED-proofs that the filtered
// HEAD leg consults the maintained head attr index (s.seg.attr) to compute the
// matching member set S_head and brutes ONLY that subset (architecture §6 head
// "brute-S"), instead of the dead-code full every-live-slot evalPayload scan that
// never touched headAttr.
//
// Two assertions, both required:
//
//  1. WIRING (counter seam): s.headBruteS must increment when the head leg runs a
//     filtered search with a declared attr index present. A pure oracle check
//     cannot prove headAttr ran, because brute-S returns the same set as the full
//     scan; the counter is the seam that distinguishes "used the index" from
//     "bruted everything anyway".
//
//  2. CORRECTNESS: results must stay IDENTICAL to the independent brute oracle
//     (evalPayload over live docs) across selectivities and across Eq / In / Range
//     / And — so wiring the index never changes the answer.
//
// All docs live in the HEAD (no Seal), so the head leg is the only producer.
func TestSearch_HeadLeg_UsesHeadAttr_MatchesOracle(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.CreateAttrIndex("color", Keyword))
	requireNoError(t, s.CreateAttrIndex("n", Numeric))

	// 60 docs entirely in the head; color cycles for ~1/3 selectivity, n = i.
	vecs := map[int64][]float32{}
	pls := map[int64]Payload{}
	colors := []string{"red", "blue", "green"}
	for i := 0; i < 60; i++ {
		v := []float32{float32(i%7) + 1, float32((i+2)%5) + 1, float32((i+4)%3) + 1}
		pl := Payload{"color": StringValue(colors[i%3]), "n": Int64Value(int64(i))}
		requireNoError(t, s.Put("k"+itoa(i), v, pl))
		doc := s.idToDoc["k"+itoa(i)]
		vecs[doc] = v
		pls[doc] = pl
	}

	q := vecs[s.idToDoc["k1"]]
	preds := []struct {
		name string
		p    Predicate
	}{
		{"Eq-selective", Eq("color", StringValue("red"))},
		{"In-broad", In("color", StringValue("red"), StringValue("blue"))},
		{"Range-mid", Range("n", Int64Value(20), Int64Value(40))},
		{"Range-all", Range("n", Int64Value(0), Int64Value(1000))},
		{"And-narrow", And(Eq("color", StringValue("red")), Range("n", Int64Value(0), Int64Value(30)))},
		{"NonDeclared-residual", Eq("color", StringValue("nonexistent"))}, // empty match, still via index path
	}

	for _, tc := range preds {
		before := s.headBruteS.Load()
		got, err := s.Search("default", q, 8, tc.p)
		requireNoError(t, err)
		took := s.headBruteS.Load() - before

		// (1) WIRING: the head leg must have used headAttr (brute-S over S_head).
		if took == 0 {
			t.Fatalf("[%s] head leg did not consult headAttr (headBruteS did not advance) — head attr index is dead code", tc.name)
		}

		// (2) CORRECTNESS: identical to the brute oracle.
		want := bruteOracleFiltered(Cosine, q, vecs, pls, tc.p, 8)
		if !setEqual(got, want) {
			t.Fatalf("[%s] pred result mismatch\n got=%v\nwant=%v", tc.name, ids(got), want)
		}
		// Non-decreasing by distance. Use strict < in the SliceIsSorted comparator so
		// adjacent EQUAL distances (ties, broken by docId in sorted()) are accepted —
		// SliceIsSorted reports unsorted iff comp(i+1,i) is ever true, which with < means
		// got[i+1].Distance < got[i].Distance, i.e. a genuine descending step.
		if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].Distance < got[j].Distance }) {
			t.Fatalf("[%s] results not ascending by distance: %v", tc.name, got)
		}
	}
}
