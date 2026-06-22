package tokenizer

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// collectRealCorpus reads source files under TOKENIZER_BENCH_ROOT (sorted,
// capped) and returns their contents. Returns nil when the env var is unset.
func collectRealCorpus(tb testing.TB) []string {
	root := os.Getenv("TOKENIZER_BENCH_ROOT")
	if root == "" {
		return nil
	}
	const maxFiles = 600
	const maxBytes = 4 << 20 // ~4 MB total
	exts := map[string]bool{".rs": true, ".ts": true, ".py": true, ".js": true, ".go": true}

	var paths []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.Contains(p, "/node_modules/") || strings.Contains(p, "/target/") || strings.Contains(p, "/.git/") {
			return nil
		}
		if exts[filepath.Ext(p)] {
			paths = append(paths, p)
		}
		return nil
	})
	sort.Strings(paths) // deterministic order

	var corpus []string
	total := 0
	for _, p := range paths {
		if len(corpus) >= maxFiles || total >= maxBytes {
			break
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		corpus = append(corpus, string(b))
		total += len(b)
	}
	tb.Logf("real corpus: %d files, %d bytes from %s", len(corpus), total, root)
	return corpus
}

// TestRealRepoTokensMatchRegex proves the hand scanner is byte-for-byte identical
// to reASCII on real source code. Gated on TOKENIZER_BENCH_ROOT.
func TestRealRepoTokensMatchRegex(t *testing.T) {
	corpus := collectRealCorpus(t)
	if corpus == nil {
		t.Skip("set TOKENIZER_BENCH_ROOT to a source tree to diff scanner vs regex on real code")
	}
	tokens := 0
	for fi, content := range corpus {
		got := findASCIITokensInto(nil, content)
		want := reASCII.FindAllString(content, -1)
		if !equalTokens(got, want) {
			// find first divergence for a readable message
			n := len(got)
			if len(want) < n {
				n = len(want)
			}
			for i := 0; i < n; i++ {
				if got[i] != want[i] {
					t.Fatalf("file #%d: token %d differs: got=%q want=%q", fi, i, got[i], want[i])
				}
			}
			t.Fatalf("file #%d: token count differs: got %d want %d", fi, len(got), len(want))
		}
		tokens += len(got)
	}
	t.Logf("real corpus: %d tokens, scanner == reASCII on every file", tokens)
}

// BenchmarkRealRepoTokenize benchmarks ASCIITokenizer.TokenizeForIndex over the
// real corpus (production path; uses the scanner after the optimization, the
// regexp before it). Gated on TOKENIZER_BENCH_ROOT.
func BenchmarkRealRepoTokenize(b *testing.B) {
	corpus := collectRealCorpus(b)
	if corpus == nil {
		b.Skip("set TOKENIZER_BENCH_ROOT to a source tree to benchmark on real code")
	}
	tok := &ASCIITokenizer{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, content := range corpus {
			tok.TokenizeForIndex(content)
		}
	}
}
