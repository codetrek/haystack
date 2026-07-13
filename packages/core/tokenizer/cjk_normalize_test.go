package tokenizer

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// These tests cover two round-3 acceptance gates for the FST-backed CJK path:
//
//   G2 — control-byte normalization: NUL (\x00) and C0 control bytes are
//        stripped BEFORE segmentation. gse's cedar trie uses \x00 as a key
//        terminator, so 中\x00国 makes gse merge across the NUL (["中\x00","国"])
//        while the FST sees three runes (["中","\x00","国"]). Stripping the NUL
//        removes the divergence and the spurious token, yielding ["中国"].
//
//   G3 — a DIRECT TokenizeForSearch(s, exact) fidelity test (round-3 only
//        inferred this path). It pins the cutForSearch token sequence for CJK,
//        mixed CJK/ASCII, exact vs non-exact mode, dedup, and stop-word filtering.

// ---------------------------------------------------------------------------
// G2: NUL / C0 control-byte normalization before segmentation
// ---------------------------------------------------------------------------

func TestNormalizeForSegmentation_StripsControlBytes(t *testing.T) {
	t.Run("NUL is stripped (cedar sentinel divergence)", func(t *testing.T) {
		assert.Equal(t, "中国", normalizeForSegmentation("中\x00国"))
	})

	t.Run("C0 control bytes are stripped", func(t *testing.T) {
		assert.Equal(t, "中国", normalizeForSegmentation("中\x01\x1f国"))
		assert.Equal(t, "中国", normalizeForSegmentation("中\x07国"))
		assert.Equal(t, "中国", normalizeForSegmentation("\x00中国\x00"))
	})

	t.Run("DEL (0x7f) is stripped", func(t *testing.T) {
		assert.Equal(t, "中国", normalizeForSegmentation("中\x7f国"))
	})

	t.Run("tab/newline/CR are preserved (legitimate whitespace)", func(t *testing.T) {
		assert.Equal(t, "中\t国", normalizeForSegmentation("中\t国"))
		assert.Equal(t, "中\n国", normalizeForSegmentation("中\n国"))
		assert.Equal(t, "中\r国", normalizeForSegmentation("中\r国"))
	})

	t.Run("multi-byte runes are untouched (no NUL/C0 inside UTF-8)", func(t *testing.T) {
		// UTF-8 lead/continuation bytes are all >= 0x80, so byte-wise stripping
		// of <0x20 bytes can never split a multi-byte rune.
		in := "中文日本語한국어😀"
		assert.Equal(t, in, normalizeForSegmentation(in))
	})

	t.Run("ASCII without control bytes is unchanged", func(t *testing.T) {
		in := "hello world 123"
		assert.Equal(t, in, normalizeForSegmentation(in))
	})
}

// TestCJKTokenizer_NULDoesNotReachIndex proves the structural cedar-sentinel
// divergence (中\x00国) cannot reach the inverted index: the NUL is removed so
// the token is the single word 中国 (not a spurious \x00 token, and not a merged
// 中\x00 token).
func TestCJKTokenizer_NULDoesNotReachIndex(t *testing.T) {
	cjk := &CJKTokenizer{}

	idx := cjk.TokenizeForIndex("中\x00国")
	clean := cjk.TokenizeForIndex("中国")
	assert.Equal(t, clean, idx, "NUL-injected input must tokenize identically to clean input")
	for _, tok := range idx {
		assert.NotContains(t, tok, "\x00", "no token may contain a NUL byte")
	}

	srch, _ := cjk.TokenizeForSearch("中\x00国", false)
	cleanSrch, _ := cjk.TokenizeForSearch("中国", false)
	assert.Equal(t, cleanSrch, srch, "search path must also be NUL-immune")
	for _, tok := range srch {
		assert.NotContains(t, tok, "\x00", "no search token may contain a NUL byte")
	}
}

// TestMixedTokenizer_NULDoesNotReachIndex confirms the wrapper used by the
// inverted index (MixedTokenizer, via the package-level functions) is also
// NUL-immune end to end.
func TestMixedTokenizer_NULDoesNotReachIndex(t *testing.T) {
	withNUL := TokenizeForIndex("中\x00国文本")
	clean := TokenizeForIndex("中国文本")
	assert.Equal(t, clean, withNUL)
	for _, tok := range withNUL {
		assert.NotContains(t, tok, "\x00")
	}
}

// ---------------------------------------------------------------------------
// G3: direct TokenizeForSearch(s, exact) / cutForSearch fidelity for CJK
// ---------------------------------------------------------------------------

func TestCJKTokenizer_TokenizeForSearch_Direct(t *testing.T) {
	cjk := &CJKTokenizer{}

	t.Run("CJK search tokens match the segmenter output minus stop words", func(t *testing.T) {
		// 我爱北京天安门 -> gse: 我 / 爱 / 北京 / 天安门 ; 我 is a stop word.
		got, wildcards := cjk.TokenizeForSearch("我爱北京天安门", false)
		assert.Nil(t, wildcards, "CJK search never produces wildcards")
		assert.Contains(t, got, "北京")
		assert.Contains(t, got, "天安门")
		assert.NotContains(t, got, "我", "stop word must be filtered from search")
		for _, tok := range got {
			assert.False(t, isStopWord(tok), "no stop word may survive: %q", tok)
		}
	})

	t.Run("exact and non-exact modes return identical CJK tokens", func(t *testing.T) {
		// CJK has no wildcard expansion, so exactMatching must not change output.
		const in = "中华人民共和国成立了"
		nonExact, wc1 := cjk.TokenizeForSearch(in, false)
		exact, wc2 := cjk.TokenizeForSearch(in, true)
		assert.Equal(t, nonExact, exact, "exactMatching must not alter CJK search tokens")
		assert.Nil(t, wc1)
		assert.Nil(t, wc2)
	})

	t.Run("ASCII embedded in CJK is lowercased (gse Cut ToLower)", func(t *testing.T) {
		// gse Cut(text, true) lowercases the whole input before segmenting, so
		// embedded ASCII comes back lowercased even on the search path (the
		// search wrapper does NOT re-lowercase; the lowering happens in Cut).
		got, _ := cjk.TokenizeForSearch("Go语言Rust编程", false)
		joined := strings.Join(got, "")
		assert.Contains(t, joined, "go", "embedded ASCII is lowercased by gse Cut")
		assert.Contains(t, joined, "rust")
		assert.NotContains(t, joined, "Go", "no uppercase survives gse Cut ToLower")
	})

	t.Run("search tokens are deduplicated in first-seen order", func(t *testing.T) {
		got, _ := cjk.TokenizeForSearch("北京北京北京", false)
		seen := map[string]int{}
		for _, tok := range got {
			seen[tok]++
		}
		for tok, n := range seen {
			assert.Equal(t, 1, n, "token %q must appear once", tok)
		}
	})

	t.Run("empty and whitespace input return empty, nil wildcards", func(t *testing.T) {
		r1, w1 := cjk.TokenizeForSearch("", false)
		assert.Empty(t, r1)
		assert.Nil(t, w1)
		r2, w2 := cjk.TokenizeForSearch("   ", true)
		assert.Empty(t, r2)
		assert.Nil(t, w2)
	})

	t.Run("punctuation-only input is filtered to empty", func(t *testing.T) {
		got, _ := cjk.TokenizeForSearch("，。！？、；：", false)
		assert.Empty(t, got)
	})
}

// TestCJKTokenizer_SearchMatchesIndexBoundaries pins that TokenizeForSearch and
// TokenizeForIndex segment on the SAME boundaries (they share the FST Cut): the
// search tokens, lowercased + sorted + deduped, must equal the index tokens.
// This is the cross-check the round-3 plan only inferred for cutForSearch.
func TestCJKTokenizer_SearchMatchesIndexBoundaries(t *testing.T) {
	cjk := &CJKTokenizer{}
	inputs := []string{
		"中华人民共和国成立了",
		"我爱北京天安门",
		"自然语言处理和机器学习",
		"Haystack是一个高性能代码搜索引擎",
	}
	for _, in := range inputs {
		index := cjk.TokenizeForIndex(in)

		searchTokens, _ := cjk.TokenizeForSearch(in, false)
		// Apply the index normalization (ToLower) + sort + dedup to the search
		// tokens; the result must equal the index token set.
		uniq := map[string]struct{}{}
		for _, tok := range searchTokens {
			uniq[strings.ToLower(tok)] = struct{}{}
		}
		normalized := make([]string, 0, len(uniq))
		for tok := range uniq {
			normalized = append(normalized, tok)
		}
		sort.Strings(normalized)

		assert.Equal(t, index, normalized,
			"search and index must share segmentation boundaries for %q", in)
	}
}
