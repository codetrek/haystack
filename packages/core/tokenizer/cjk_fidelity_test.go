package tokenizer

import (
	"bufio"
	"math/rand"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/go-ego/gse"
)

// Golden-diff fidelity suite for the PUBLIC CJK tokenizer interface.
//
// FIDELITY IS THE ENTIRE NO-REINDEX GUARANTEE. The FST-backed CJKTokenizer
// replaces gse's runtime cedar trie, but the inverted index (BM25) was built
// against gse's token boundaries. If a single boundary shifts, already-indexed
// documents stop matching the new tokens and the corpus would need a full
// reindex. These tests pin every public entry point byte-for-byte against a
// REAL gse.Segmenter, so the swap is provably boundary-preserving.
//
// Two gates are discharged here directly (round 3 only inferred them):
//
//	G1  CJKTokenizer.TokenizeForIndex(str)        == gse-driven reference
//	G3  CJKTokenizer.TokenizeForSearch(s, exact)  == gse-driven reference
//	    for BOTH exact=true AND exact=false.
//
// The reference re-implements the EXACT post-processing in cjk_tokenizer.go
// (TrimSpace, drop punct/space, drop CJK stop words, dedup, ToLower+sort for the
// index path) on top of gse.Cut(text, true) — the same segmentation the FST
// reproduces. The G2 NUL/C0 normalization (normalizeForSegmentation) is applied
// to the gse input too, so the comparison is apples-to-apples on real text and
// the cedar \x00-sentinel divergence is neutralized identically on both sides.
//
// The complementary segmenter-level byte-fidelity (fst.Cut == gse.Cut) and the
// direct n-gram cutForSearch fidelity live in package fstcjk
// (fidelity_test.go, search_fidelity_test.go).

const (
	cjkCorpusPath = "/tmp/cjkopt/corpus.txt"
	cjkQueryPath  = "/tmp/cjkopt/queries.txt"
)

// newGseRef builds the golden reference: a real gse.Segmenter loaded with the
// same s_1+t_1 dictionary the FST was built from. The dict (8.6MB) loads ONCE
// per test binary via sync.OnceValue — it is a read-only reference shared across
// the sequential fidelity tests, so per-test reloading (~1.6s each) is avoided.
var sharedGseRef = sync.OnceValue(func() *gse.Segmenter {
	var seg gse.Segmenter
	seg.SkipLog = true
	if err := seg.LoadDict(); err != nil {
		panic("gse LoadDict: " + err.Error())
	}
	return &seg
})

func newGseRef(t testing.TB) *gse.Segmenter {
	t.Helper()
	skipUnlessFidelity(t)
	return sharedGseRef()
}

// skipUnlessFidelity skips the live-gse golden-fidelity tests unless
// HAYSTACK_FIDELITY=1. dict.fst is committed and gse is pinned, so FST-vs-gse
// equivalence only changes when dict.fst, the segmenter, or the gse version
// changes; a dedicated CI step runs this suite on those changes. Normal runs use
// the gse-free golden smoke (cjk_smoke_test.go) for coverage.
func skipUnlessFidelity(t testing.TB) {
	t.Helper()
	if os.Getenv("HAYSTACK_FIDELITY") == "" {
		t.Skip("set HAYSTACK_FIDELITY=1 to run the live-gse golden-fidelity suite")
	}
}

// fidelityFull returns the exhaustive fuzz count when HAYSTACK_FIDELITY_FULL is
// set, else a smaller smoke count.
func fidelityFull(full int) int {
	if os.Getenv("HAYSTACK_FIDELITY_FULL") != "" {
		return full
	}
	return 2000
}

// refTokenizeForIndex mirrors CJKTokenizer.TokenizeForIndex's post-processing
// EXACTLY, parameterized by the raw segment slice, so the only thing under test
// is the segmentation source (gse vs FST).
func refTokenizeForIndex(str string, segments []string) []string {
	if strings.TrimSpace(str) == "" {
		return []string{}
	}
	uniq := make(map[string]struct{}, len(segments))
	for _, seg := range segments {
		token := strings.TrimSpace(seg)
		if len(token) == 0 || isPunctOrSpace(token) || isStopWord(token) {
			continue
		}
		uniq[strings.ToLower(token)] = struct{}{}
	}
	out := make([]string, 0, len(uniq))
	for tok := range uniq {
		out = append(out, tok)
	}
	if len(out) == 0 {
		return []string{}
	}
	sort.Strings(out)
	return out
}

// refTokenizeForSearch mirrors CJKTokenizer.TokenizeForSearch's post-processing
// EXACTLY (first-seen dedup, no lowercasing, no wildcards), parameterized by the
// raw segment slice.
func refTokenizeForSearch(str string, segments []string) []string {
	if strings.TrimSpace(str) == "" {
		return []string{}
	}
	exists := make(map[string]struct{}, len(segments))
	result := []string{}
	for _, seg := range segments {
		token := strings.TrimSpace(seg)
		if len(token) == 0 || isPunctOrSpace(token) || isStopWord(token) {
			continue
		}
		if _, ok := exists[token]; ok {
			continue
		}
		exists[token] = struct{}{}
		result = append(result, token)
	}
	return result
}

// gseSegments returns gse.Cut(normalize(str), true): the golden segmentation the
// FST claims to reproduce, with the same G2 NUL/C0 normalization the public
// wrapper applies before segmenting.
func gseSegments(g *gse.Segmenter, str string) []string {
	return g.Cut(normalizeForSegmentation(str), true)
}

func cjkReadLines(t testing.TB, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Skipf("input %s unavailable: %v", path, err)
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func cjkEqual(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// cjkFidelityInputs is the curated edge-case set, biased to CJK + mixed CJK/ASCII
// since CJKTokenizer is only invoked on CJK-containing runs by MixedTokenizer.
func cjkFidelityInputs() []string {
	return []string{
		"中",
		"中国",
		"中华人民共和国",
		"我爱北京天安门",
		"中华人民共和国成立了",
		"自然语言处理和机器学习以及深度学习",
		"我的世界很大",
		"这是一个测试",
		"我在这里",
		"Go语言很棒",
		"iPhone15Pro最大",
		"2024年5月1日",
		"价格是￥99.99元",
		"100%的努力",
		"CAR-T细胞疗法和PD-1抑制剂",
		"naïve café résumé混排CJK",
		"ＡＢＣ全角字母",
		"한국어와日本語のテスト",
		"こんにちは世界",
		"안녕하세요世界",
		"超长句子测试：人工智能和机器学习以及深度学习还有自然语言处理在计算机视觉领域的广泛应用前景十分广阔令人期待不已啊",
		"emoji😀test混排",
		"\t\n制表符和换行",
		"COVID-19疫情期间研究",
		"3.14159是圆周率的近似值",
		"全角＆半角&符号混排测试",
		"中\x00国",          // G2: NUL injected
		"中\x01\x1f国文本",    // G2: C0 controls injected
		"北京北京北京",          // dedup
		"Go语言Rust编程C++开发", // embedded ASCII lowercasing
		"，。！？、；：",         // punctuation only -> empty
		"我的了着过之所",         // all stop words -> empty
		cjkSample,
	}
}

// TestCJKFidelity_TokenizeForIndex is the headline G1 golden-diff: the public
// CJKTokenizer.TokenizeForIndex output is byte-identical to the gse-driven
// reference over the corpus, queries, curated edge cases, and a fuzz set.
func TestCJKFidelity_TokenizeForIndex(t *testing.T) {
	cjk := &CJKTokenizer{}
	g := newGseRef(t)

	var inputs []string
	inputs = append(inputs, cjkReadLines(t, cjkCorpusPath)...)
	inputs = append(inputs, cjkReadLines(t, cjkQueryPath)...)
	inputs = append(inputs, cjkFidelityInputs()...)
	inputs = append(inputs, cjkFuzzInputs()...)

	total, identical, diffs := runFidelity(t, inputs, func(in string) (got, want []string) {
		return cjk.TokenizeForIndex(in), refTokenizeForIndex(in, gseSegments(g, in))
	})
	t.Logf("G1 TokenizeForIndex FIDELITY: %d/%d = %.4f%% byte-identical to gse over %d inputs",
		identical, total, 100*float64(identical)/float64(total), total)
	reportDiffs(t, "TokenizeForIndex", identical, total, diffs)
}

// TestCJKFidelity_TokenizeForSearch is the G3 golden-diff: the public
// CJKTokenizer.TokenizeForSearch output is byte-identical to the gse-driven
// reference for BOTH exact=true and exact=false. CJK has no wildcard expansion,
// so the reference is identical for both modes; the test pins that exactMatching
// does not perturb the CJK token stream (and that wildcards are always nil).
func TestCJKFidelity_TokenizeForSearch(t *testing.T) {
	cjk := &CJKTokenizer{}
	g := newGseRef(t)

	var inputs []string
	inputs = append(inputs, cjkReadLines(t, cjkCorpusPath)...)
	inputs = append(inputs, cjkReadLines(t, cjkQueryPath)...)
	inputs = append(inputs, cjkFidelityInputs()...)
	inputs = append(inputs, cjkFuzzInputs()...)

	for _, exact := range []bool{false, true} {
		want := func(in string) []string { return refTokenizeForSearch(in, gseSegments(g, in)) }
		total, identical, diffs := runFidelity(t, inputs, func(in string) ([]string, []string) {
			got, wc := cjk.TokenizeForSearch(in, exact)
			if wc != nil {
				t.Errorf("TokenizeForSearch(%.20q, %v): wildcards must be nil for CJK, got %q", in, exact, wc)
			}
			return got, want(in)
		})
		t.Logf("G3 TokenizeForSearch(exact=%v) FIDELITY: %d/%d = %.4f%% byte-identical to gse over %d inputs",
			exact, identical, total, 100*float64(identical)/float64(total), total)
		reportDiffs(t, "TokenizeForSearch(exact)", identical, total, diffs)
	}
}

// runFidelity applies pair(in) -> (got, want) over inputs and tallies identity.
func runFidelity(t *testing.T, inputs []string, pair func(string) (got, want []string)) (total, identical int, diffs []string) {
	t.Helper()
	for _, in := range inputs {
		total++
		got, want := pair(in)
		if cjkEqual(got, want) {
			identical++
			continue
		}
		if len(diffs) < 30 {
			diffs = append(diffs, "INPUT "+strings.ToValidUTF8(in, "?")+
				"\n  gse: "+strings.Join(want, "|")+"\n  cjk: "+strings.Join(got, "|"))
		}
	}
	return total, identical, diffs
}

func reportDiffs(t *testing.T, label string, identical, total int, diffs []string) {
	t.Helper()
	for _, d := range diffs {
		t.Logf("\n%s", d)
	}
	if identical != total {
		t.Errorf("%s NOT 100%% byte-identical to gse: %d/%d (%d diffs shown)",
			label, identical, total, len(diffs))
	}
}

// cjkFuzzInputs generates deterministic random CJK-heavy strings (wide CJK
// Unified + ext-A + fullwidth + ASCII + punct, lengths 1-30, fixed PRNG seed)
// for the public-interface golden-diff. Mirrors the fstcjk heavy-fuzz generator
// so the two suites cover the same distribution.
func cjkFuzzInputs() []string {
	rng := rand.New(rand.NewSource(0xF1DE11)) // fixed seed: reproducible
	sampleRune := func() rune {
		switch rng.Intn(12) {
		case 0, 1, 2, 3, 4, 5:
			return rune(0x4E00 + rng.Intn(0x9FA5-0x4E00)) // CJK Unified
		case 6:
			a := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
			return rune(a[rng.Intn(len(a))]) // ASCII alnum
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
	n := fidelityFull(20000)
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ln := 1 + rng.Intn(30)
		var sb strings.Builder
		for j := 0; j < ln; j++ {
			sb.WriteRune(sampleRune())
		}
		out = append(out, sb.String())
	}
	return out
}
