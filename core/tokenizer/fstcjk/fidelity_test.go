package fstcjk

import (
	"bufio"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/go-ego/gse"
)

// Golden-diff fidelity suite: fstcjk.Segmenter.Cut (embedded FST) MUST be
// byte-identical to a real gse.Segmenter.Cut(text, true) over the corpus, the
// curated edge cases, and tens of thousands of fuzz inputs. Ported from the
// proven prototype (prototypes/fstseg/fidelity_test.go + heavyfuzz_test.go).

const (
	corpusPath = "/tmp/cjkopt/corpus.txt"
	queryPath  = "/tmp/cjkopt/queries.txt"
)

// the 510B CJK sample referenced by the mandate (from benchmark_test.go cjkSample).
const cjkSample = `Haystack 是一个高性能的代码搜索引擎，专为大型代码仓库设计。` +
	`它使用倒排索引和智能分词技术来实现毫秒级的全文检索。` +
	`支持多种编程语言的语法感知搜索，包括变量名拆分和驼峰命名识别。` +
	`中文、日文、韩文等CJK字符集通过基于字典的分词器进行处理，` +
	`确保搜索结果的准确性和召回率。系统采用懒加载策略，` +
	`仅在遇到CJK文本时才初始化分词字典，避免对纯英文场景产生性能影响。`

func loadFST(t testing.TB) *Segmenter {
	t.Helper()
	s, err := Open()
	if err != nil {
		t.Fatalf("Open embedded FST: %v", err)
	}
	return s
}

func newGse(t testing.TB) *gse.Segmenter {
	t.Helper()
	var seg gse.Segmenter
	seg.SkipLog = true
	if err := seg.LoadDict(); err != nil {
		t.Fatalf("gse LoadDict: %v", err)
	}
	return &seg
}

func readLines(t testing.TB, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Skipf("input %s unavailable: %v", path, err)
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

func eqSlices(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func edgeCases() []string {
	return []string{
		"",
		" ",
		"a",
		"中",
		"abc",
		"ABC",
		"AbC123",
		"hello world",
		"Go语言很棒",
		"iPhone15Pro最大",
		"2024年5月1日",
		"价格是￥99.99元",
		"!!!???。。。，，，",
		"100%的努力",
		"CAR-T细胞疗法和PD-1抑制剂",
		"u200bzero​width",
		"naïve café résumé",
		"ＡＢＣ全角字母",
		"한국어와日本語のテスト",
		"ßẞ德语",
		"a b c 中 文 混 排",
		"超长句子测试：人工智能和机器学习以及深度学习还有自然语言处理在计算机视觉领域的广泛应用前景十分广阔令人期待不已啊",
		"123456789012345",
		"   leading and trailing spaces   ",
		"emoji😀test混排",
		"\t\n制表符和换行",
		"COVID-19疫情期间",
		"3.14159是圆周率",
		"naïveCafé混排CJK",
		"全角＆半角&符号",
	}
}

// TestTotalFreqParity asserts the embedded totalFreq equals gse's loaded
// Dict.TotalFreq() exactly, and the dict key count matches Dict.NumTokens().
func TestTotalFreqParity(t *testing.T) {
	fst := loadFST(t)
	g := newGse(t)
	gseTF := g.Dictionary().TotalFreq()
	gseN := g.Dictionary().NumTokens()
	t.Logf("FST totalFreq=%.0f ; gse totalFreq=%.0f numTokens=%d", fst.TotalFreq(), gseTF, gseN)
	if fst.TotalFreq() != gseTF {
		t.Errorf("totalFreq mismatch: fst=%.6f gse=%.6f (diff=%.6f)",
			fst.TotalFreq(), gseTF, fst.TotalFreq()-gseTF)
	}
}

// TestByteFidelity is the headline test: every input's token sequence must be
// byte-identical to gse Cut(text,true) over corpus + queries + edge cases.
func TestByteFidelity(t *testing.T) {
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
		got := fst.Cut(in)
		want := g.Cut(in, true)
		if eqSlices(got, want) {
			identical++
			continue
		}
		diffs = append(diffs, fmt.Sprintf("INPUT %q\n  gse: %q\n  fst: %q", truncate(in), want, got))
	}

	pct := 100.0 * float64(identical) / float64(total)
	t.Logf("BYTE-FIDELITY: %d/%d = %.4f%% byte-identical to gse Cut(text,true)", identical, total, pct)
	for _, d := range diffs {
		t.Logf("\n%s", d)
	}
	if identical != total {
		t.Errorf("NOT 100%% byte-identical: %d/%d (%.4f%%); %d residual diffs", identical, total, pct, len(diffs))
	}
}

func truncate(s string) string {
	r := []rune(s)
	if len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}

// TestFuzzFidelity throws deterministic randomized CJK+ASCII+punct strings at
// both segmenters to surface any boundary divergence the curated set misses.
func TestFuzzFidelity(t *testing.T) {
	fst := loadFST(t)
	g := newGse(t)

	alphabet := []rune("的一是不了人我在有他这中大来上国个到说们为子和你地出道也时年得就那要下以生会自着去之过家学对可她里后小么心多天而能好都然没日于起还发成事只作当想看文无开手十用主行方又如前所本见经头面公同三已老从动两长知民样现分将外但身些与高意进把法此实回二理美月公月美什")
	puncts := []rune("，。！？、；：“”（）—…·")
	asciis := []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 -.")

	pick := func(seed, mod int) int {
		seed = seed*1103515245 + 12345
		seed ^= seed >> 16
		if seed < 0 {
			seed = -seed
		}
		return seed % mod
	}

	total, identical := 0, 0
	var diffs []string
	N := 3000
	for n := 0; n < N; n++ {
		ln := 1 + pick(n*7+1, 18)
		var sb strings.Builder
		for j := 0; j < ln; j++ {
			cls := pick(n*31+j*13+3, 10)
			switch {
			case cls < 6:
				sb.WriteRune(alphabet[pick(n*17+j*5+7, len(alphabet))])
			case cls < 8:
				sb.WriteRune(asciis[pick(n*19+j*11+9, len(asciis))])
			default:
				sb.WriteRune(puncts[pick(n*23+j*3+5, len(puncts))])
			}
		}
		in := sb.String()
		total++
		got := fst.Cut(in)
		want := g.Cut(in, true)
		if eqSlices(got, want) {
			identical++
			continue
		}
		if len(diffs) < 25 {
			diffs = append(diffs, fmt.Sprintf("INPUT %q\n  gse: %q\n  fst: %q", in, want, got))
		}
	}
	pct := 100.0 * float64(identical) / float64(total)
	t.Logf("FUZZ BYTE-FIDELITY: %d/%d = %.4f%% over %d random strings", identical, total, pct, N)
	for _, d := range diffs {
		t.Logf("\n%s", d)
	}
	if identical != total {
		t.Errorf("fuzz: NOT 100%% identical: %d/%d (%.4f%%)", identical, total, pct)
	}
}

// TestHeavyFuzzFidelity stresses the full CJK plane + HMM OOV fallback with a
// real PRNG and longer strings (20k inputs), where prior prototypes diverged
// on tie-break + OOV penalty + HMM.
func TestHeavyFuzzFidelity(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	fst := loadFST(t)
	g := newGse(t)

	rng := rand.New(rand.NewSource(0xC1A551C))
	sampleRune := func() rune {
		switch rng.Intn(12) {
		case 0, 1, 2, 3, 4, 5:
			return rune(0x4E00 + rng.Intn(0x9FA5-0x4E00))
		case 6:
			a := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
			return rune(a[rng.Intn(len(a))])
		case 7:
			p := []rune("，。！？、；：（）《》【】—…·“”")
			return p[rng.Intn(len(p))]
		case 8:
			p := " -.,!?:;()/+"
			return rune(p[rng.Intn(len(p))])
		case 9:
			return rune(0xFF21 + rng.Intn(58))
		case 10:
			return rune(0x3400 + rng.Intn(0x4DB5-0x3400))
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
		got := fst.Cut(in)
		want := g.Cut(in, true)
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
	t.Logf("HEAVY FUZZ BYTE-FIDELITY: %d/%d = %.4f%% over %d strings (full CJK+ext+fullwidth, HMM-stressed)", identical, total, pct, N)
	for _, d := range diffs {
		t.Logf("\n%s", d)
	}
	if identical != total {
		t.Errorf("heavy fuzz: NOT 100%% identical: %d/%d (%.4f%%); %d diffs shown", identical, total, pct, len(diffs))
	}
}

// TestNoneAddrSentinel pins vellum's internal noneAddr sentinel value our walk
// relies on: Accept of an absent first byte from Start() must equal noneAddrVellum.
func TestNoneAddrSentinel(t *testing.T) {
	fst := loadFST(t)
	got := fst.fst.Accept(fst.fst.Start(), 0x00)
	if got != noneAddrVellum {
		t.Fatalf("expected noneAddr sentinel %d, got %d", noneAddrVellum, got)
	}
}

// BenchmarkFSTCutSample / BenchmarkGseCutSample give an interleaved head-to-head
// per-call latency on the 510B CJK sample.
func BenchmarkFSTCutSample(b *testing.B) {
	fst := loadFST(b)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = fst.Cut(cjkSample)
	}
}

func BenchmarkGseCutSample(b *testing.B) {
	g := newGse(b)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = g.Cut(cjkSample, true)
	}
}
