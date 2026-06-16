package vectorstore

import (
	"math/rand"
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
				got, err := s.Search(q, 10, pred)
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
	unf, err := s.Search(q, 10, nil)
	requireNoError(t, err)
	fil, err := s.Search(q, 10, Eq("color", StringValue("red"))) // matches all
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
	got, err := s.Search(q, 10, Eq("color", StringValue("nonexistent")))
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
		got, err := s.Search(q, 10, Eq("color", StringValue("red")))
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
	if _, err := s.Search([]float32{1, 0, 0}, 5, Range("x", Int64Value(1), Int64Value(2))); err == nil {
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
