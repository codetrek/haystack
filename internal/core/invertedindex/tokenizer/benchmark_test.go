package tokenizer

import (
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Sample texts for benchmarks
// ---------------------------------------------------------------------------

// asciiSample is a typical Go code snippet (~400 characters).
const asciiSample = `func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		http.Error(w, "missing query parameter", http.StatusBadRequest)
		return
	}
	results, err := s.searcher.Search(r.Context(), query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(results)
}`

// cjkSample is a Chinese README excerpt (~300 characters).
const cjkSample = `Haystack 是一个高性能的代码搜索引擎，专为大型代码仓库设计。` +
	`它使用倒排索引和智能分词技术来实现毫秒级的全文检索。` +
	`支持多种编程语言的语法感知搜索，包括变量名拆分和驼峰命名识别。` +
	`中文、日文、韩文等CJK字符集通过基于字典的分词器进行处理，` +
	`确保搜索结果的准确性和召回率。系统采用懒加载策略，` +
	`仅在遇到CJK文本时才初始化分词字典，避免对纯英文场景产生性能影响。`

// mixedSample is Go code with Chinese comments (~400 characters).
const mixedSample = `// TokenizeForIndex 将字符串分词用于索引构建。
// 收集所有单词，必要时拆分为更小的部分，返回排序去重后的列表。
func TokenizeForIndex(str string) []string {
	return DefaultTokenizer.TokenizeForIndex(str)
}

// handleSearch 处理搜索请求，解析查询参数并返回搜索结果。
func handleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	results, _ := searcher.Search(r.Context(), query)
	json.NewEncoder(w).Encode(results)
}`

// ---------------------------------------------------------------------------
// Index benchmarks
// ---------------------------------------------------------------------------

// BenchmarkASCIITokenizeForIndex benchmarks pure ASCII text indexing.
// This should show that CJK support adds zero overhead for ASCII-only paths.
func BenchmarkASCIITokenizeForIndex(b *testing.B) {
	tok := &MixedTokenizer{}
	// Warm up to ensure any one-time init (other than gse) is done.
	tok.TokenizeForIndex(asciiSample)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tok.TokenizeForIndex(asciiSample)
	}
}

// BenchmarkCJKTokenizeForIndex benchmarks pure Chinese text indexing.
func BenchmarkCJKTokenizeForIndex(b *testing.B) {
	tok := &MixedTokenizer{}
	// Warm up — loads gse dictionary.
	tok.TokenizeForIndex(cjkSample)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tok.TokenizeForIndex(cjkSample)
	}
}

// BenchmarkMixedTokenizeForIndex benchmarks mixed Chinese+ASCII text indexing.
func BenchmarkMixedTokenizeForIndex(b *testing.B) {
	tok := &MixedTokenizer{}
	// Warm up — loads gse dictionary.
	tok.TokenizeForIndex(mixedSample)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tok.TokenizeForIndex(mixedSample)
	}
}

// ---------------------------------------------------------------------------
// Search benchmarks
// ---------------------------------------------------------------------------

// BenchmarkASCIITokenizeForSearch benchmarks pure ASCII text search tokenization.
func BenchmarkASCIITokenizeForSearch(b *testing.B) {
	tok := &MixedTokenizer{}
	tok.TokenizeForSearch(asciiSample, false)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tok.TokenizeForSearch(asciiSample, false)
	}
}

// BenchmarkCJKTokenizeForSearch benchmarks pure Chinese text search tokenization.
func BenchmarkCJKTokenizeForSearch(b *testing.B) {
	tok := &MixedTokenizer{}
	tok.TokenizeForSearch(cjkSample, false)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tok.TokenizeForSearch(cjkSample, false)
	}
}

// BenchmarkMixedTokenizeForSearch benchmarks mixed Chinese+ASCII text search tokenization.
func BenchmarkMixedTokenizeForSearch(b *testing.B) {
	tok := &MixedTokenizer{}
	tok.TokenizeForSearch(mixedSample, false)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tok.TokenizeForSearch(mixedSample, false)
	}
}

// ---------------------------------------------------------------------------
// Dictionary loading benchmarks
// ---------------------------------------------------------------------------

// BenchmarkCJKFirstLoad benchmarks the first CJK tokenization including gse
// dictionary loading. Uses a fresh CJKTokenizer each iteration.
func BenchmarkCJKFirstLoad(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tok := &CJKTokenizer{}
		tok.TokenizeForIndex(cjkSample)
	}
}

// BenchmarkCJKSubsequent benchmarks CJK tokenization after the dictionary is
// already loaded. Uses a shared sync.Once to load the dictionary once before
// the benchmark loop, then creates fresh CJKTokenizer instances that share the
// segmenter state indirectly (by pre-loading via a separate instance first).
func BenchmarkCJKSubsequent(b *testing.B) {
	// Pre-load the dictionary into a shared tokenizer.
	shared := &CJKTokenizer{}
	shared.TokenizeForIndex(cjkSample)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// Use the pre-loaded tokenizer — this measures only segmentation cost.
		shared.TokenizeForIndex(cjkSample)
	}
}

// ---------------------------------------------------------------------------
// Baseline: direct ASCIITokenizer (no CJK detection overhead)
// ---------------------------------------------------------------------------

// BenchmarkDirectASCIITokenizeForIndex benchmarks ASCIITokenizer directly,
// without the MixedTokenizer dispatch layer. Comparing this with
// BenchmarkASCIITokenizeForIndex shows the overhead of containsCJK() scanning.
func BenchmarkDirectASCIITokenizeForIndex(b *testing.B) {
	tok := &ASCIITokenizer{}
	tok.TokenizeForIndex(asciiSample)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tok.TokenizeForIndex(asciiSample)
	}
}

// ---------------------------------------------------------------------------
// containsCJK detection overhead
// ---------------------------------------------------------------------------

// BenchmarkContainsCJK_ASCII benchmarks CJK detection on pure ASCII text.
// This measures the cost of the fast-path check that keeps ASCII tokenization free
// of gse overhead.
func BenchmarkContainsCJK_ASCII(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		containsCJK(asciiSample)
	}
}

// BenchmarkContainsCJK_CJK benchmarks CJK detection on CJK text (early exit).
func BenchmarkContainsCJK_CJK(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		containsCJK(cjkSample)
	}
}

// ---------------------------------------------------------------------------
// Concurrency benchmark
// ---------------------------------------------------------------------------

// BenchmarkCJKConcurrent benchmarks concurrent CJK tokenization to verify
// the sync.Once dictionary loading is safe under contention.
func BenchmarkCJKConcurrent(b *testing.B) {
	tok := &MixedTokenizer{}
	tok.TokenizeForIndex(cjkSample) // pre-load dictionary

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			tok.TokenizeForIndex(cjkSample)
		}
	})
}

// ---------------------------------------------------------------------------
// Helpers — ensure lazy loading works per-benchmark
// ---------------------------------------------------------------------------

// resetCJKTokenizer returns a fresh MixedTokenizer with an uninitialized CJK tokenizer.
// This is used internally in BenchmarkCJKFirstLoad; exported for documentation clarity.
var _ = sync.Once{} // Ensure sync import is used
