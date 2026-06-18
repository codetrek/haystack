package fstcjk

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// G3 — DIRECT cutForSearch / n-gram fidelity.
//
// gse's search mode (CutSearch(str, true) -> dag.go:352 cutForSearch) is NOT on
// the production TokenizeForSearch path (the wrapper segments with plain
// Cut(text,true) and applies no n-gram expansion — see cjk_fidelity_test.go in
// package tokenizer for that golden-diff). Round 3 only *inferred* that the FST
// could reproduce gse's n-gram search expansion if it were ever wired in. This
// test discharges that inference DIRECTLY: it drives gse's exact cutForSearch
// loop with the FST's own Cut + Find and asserts the emitted token stream is
// byte-identical to gse.CutSearch(str, true) over the corpus, edge cases, and a
// fuzz set.
//
// gse cutForSearch (dag.go:352, verbatim):
//
//	ws := seg.Cut(str, true)                 // == fst.Cut(str)  (proven elsewhere)
//	for _, word := range ws {
//	    runes := []rune(word)
//	    for _, incr := range []int{2, 3} {
//	        if len(runes) <= incr { continue }
//	        for i := 0; i < len(runes)-incr+1; i++ {
//	            gram := string(runes[i : i+incr])
//	            v, _, ok := seg.Find(gram)   // == fst.findFreq([]rune(gram))
//	            if ok && v > 0 { result = append(result, gram) }
//	        }
//	    }
//	    result = append(result, word)
//	}
//
// fst.findFreq returns (freq, ok) with gse Find semantics exactly (ok=true iff
// the byte path exists; freq is the final value or 0 for a prefix-but-not-word
// node), so the gram filter ok && v>0 is reproduced bit-for-bit.

// fstCutForSearch is gse cutForSearch (dag.go:352) re-expressed over the FST.
// The Cut(str) and findFreq() it calls are the same primitives the byte-fidelity
// suite already pins against gse, so any divergence here would be a search-mode
// (n-gram) bug, not a base-segmentation bug.
func (s *Segmenter) fstCutForSearch(str string) []string {
	ws := s.Cut(str)
	result := make([]string, 0, len(str)/3+1)
	for _, word := range ws {
		runes := []rune(word)
		for _, incr := range []int{2, 3} {
			if len(runes) <= incr {
				continue
			}
			for i := 0; i < len(runes)-incr+1; i++ {
				gram := string(runes[i : i+incr])
				v, ok := s.findFreq([]rune(gram))
				if ok && v > 0 {
					result = append(result, gram)
				}
			}
		}
		result = append(result, word)
	}
	return result
}

// TestCutForSearchFidelity is the DIRECT G3 golden-diff: the FST-driven
// cutForSearch must be byte-identical to gse.CutSearch(str, true) over the
// corpus, queries, curated edge cases, and a fuzz set. Proves the n-gram search
// path (round-3 only inferred it) is faithful, not merely the base Cut.
func TestCutForSearchFidelity(t *testing.T) {
	fst := loadFST(t)
	g := newGse(t)

	var inputs []string
	inputs = append(inputs, readLines(t, corpusPath)...)
	inputs = append(inputs, readLines(t, queryPath)...)
	inputs = append(inputs, edgeCases()...)
	inputs = append(inputs, cjkSample)

	total, identical := 0, 0
	var diffs []string
	for _, in := range inputs {
		total++
		got := fst.fstCutForSearch(in)
		want := g.CutSearch(in, true)
		if eqSlices(got, want) {
			identical++
			continue
		}
		diffs = append(diffs, fmt.Sprintf("INPUT %q\n  gse: %q\n  fst: %q", truncate(in), want, got))
	}

	pct := 100.0 * float64(identical) / float64(total)
	t.Logf("CUTFORSEARCH FIDELITY: %d/%d = %.4f%% byte-identical to gse CutSearch(str,true)",
		identical, total, pct)
	for _, d := range diffs {
		t.Logf("\n%s", d)
	}
	if identical != total {
		t.Errorf("cutForSearch NOT 100%% byte-identical: %d/%d (%.4f%%); %d diffs",
			identical, total, pct, len(diffs))
	}
}

// TestCutForSearchFuzzFidelity stresses the n-gram path with deterministic
// random CJK+ext-A+fullwidth+ASCII+punct strings (lengths 1-30, fixed seed),
// where n-gram emission ordering and the ok && v>0 gram filter are exercised
// hardest. This is the same generator the base TestHeavyFuzzFidelity uses, so
// the two suites share a corpus distribution.
func TestCutForSearchFuzzFidelity(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	fst := loadFST(t)
	g := newGse(t)

	rng := rand.New(rand.NewSource(0x5EA2C4)) // distinct seed from the base suite
	sampleRune := func() rune {
		switch rng.Intn(12) {
		case 0, 1, 2, 3, 4, 5:
			return rune(0x4E00 + rng.Intn(0x9FA5-0x4E00)) // CJK Unified
		case 6:
			a := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
			return rune(a[rng.Intn(len(a))]) // ASCII
		case 7:
			p := []rune("，。！？、；：（）《》【】—…·“”")
			return p[rng.Intn(len(p))] // CJK punct
		case 8:
			p := " -.,!?:;()/+"
			return rune(p[rng.Intn(len(p))]) // ASCII punct/space
		case 9:
			return rune(0xFF21 + rng.Intn(58)) // fullwidth
		case 10:
			return rune(0x3400 + rng.Intn(0x4DB5-0x3400)) // CJK ext-A
		default:
			return rune(0x4E00 + rng.Intn(0x9FA5-0x4E00))
		}
	}

	total, identical := 0, 0
	var diffs []string
	N := 20000
	for n := 0; n < N; n++ {
		ln := 1 + rng.Intn(30)
		var sb strings.Builder
		for j := 0; j < ln; j++ {
			sb.WriteRune(sampleRune())
		}
		in := sb.String()
		total++
		got := fst.fstCutForSearch(in)
		want := g.CutSearch(in, true)
		if eqSlices(got, want) {
			identical++
			continue
		}
		if len(diffs) < 40 {
			diffs = append(diffs, "INPUT "+strings.ToValidUTF8(in, "?")+
				"\n  gse: "+strings.Join(want, "|")+"\n  fst: "+strings.Join(got, "|"))
		}
	}
	pct := 100.0 * float64(identical) / float64(total)
	t.Logf("CUTFORSEARCH FUZZ FIDELITY: %d/%d = %.4f%% over %d strings (CJK+ext-A+fullwidth+ASCII+punct, n-gram path)",
		identical, total, pct, N)
	for _, d := range diffs {
		t.Logf("\n%s", d)
	}
	if identical != total {
		t.Errorf("cutForSearch fuzz NOT 100%% identical: %d/%d (%.4f%%); %d diffs shown",
			identical, total, pct, len(diffs))
	}
}
