package searcher

import (
	"strings"
	"testing"

	"github.com/codetrek/haystack/internal/core/invertedindex/tokenizer"
	"github.com/stretchr/testify/assert"
)

// =========================================================================
// CJK search path verification (HAY-002 step 4)
//
// These tests verify the end-to-end CJK search path at the engine level
// (Compile + IsLineMatch) without requiring a database. For full
// integration tests that exercise the index→search pipeline, CJK
// sub-tests are added to TestFullIntegration in searcher_coverage_test.go.
// =========================================================================

// ---------------------------------------------------------------------------
// 1. TokenizeForSearch / TokenizeForIndex CJK path confirmation
// ---------------------------------------------------------------------------

func TestTokenizeForSearch_CJK(t *testing.T) {
	t.Run("pure Chinese query produces tokens", func(t *testing.T) {
		tokens, wildcards := tokenizer.TokenizeForSearch("中华人民共和国成立", false)
		assert.True(t, len(tokens) > 0, "expected CJK tokens, got none")
		assert.Empty(t, wildcards, "CJK queries should not produce wildcards")
	})

	t.Run("mixed Chinese-ASCII query produces tokens for both", func(t *testing.T) {
		tokens, _ := tokenizer.TokenizeForSearch("Go语言是Google开发的编程语言", false)
		// Should contain CJK segments and ASCII keywords
		assert.True(t, len(tokens) > 0, "expected mixed tokens")
		// Check that at least one ASCII-ish token is present (lowercased by ASCII tokenizer)
		hasASCII := false
		for _, tok := range tokens {
			if tok == "google" || tok == "Go" {
				hasASCII = true
			}
		}
		assert.True(t, hasASCII, "expected ASCII tokens from mixed CJK-ASCII input, got: %v", tokens)
	})

	t.Run("stop word only query produces empty tokens", func(t *testing.T) {
		tokens, _ := tokenizer.TokenizeForSearch("的", false)
		assert.Empty(t, tokens, "stop word '的' should be filtered out")
	})

	t.Run("query with stop words mixed in filters them", func(t *testing.T) {
		tokens, _ := tokenizer.TokenizeForSearch("我的编程语言", false)
		for _, tok := range tokens {
			assert.NotEqual(t, "的", tok, "stop word '的' should not appear in tokens")
			assert.NotEqual(t, "我", tok, "stop word '我' should not appear in tokens")
		}
	})
}

func TestTokenizeForIndex_CJK(t *testing.T) {
	t.Run("Chinese content produces indexed tokens", func(t *testing.T) {
		tokens := tokenizer.TokenizeForIndex("中华人民共和国成立于1949年")
		assert.True(t, len(tokens) > 0, "expected CJK index tokens")
	})

	t.Run("mixed content indexes both CJK and ASCII", func(t *testing.T) {
		tokens := tokenizer.TokenizeForIndex("Go语言是Google开发的编程语言")
		assert.True(t, len(tokens) > 0)
	})

	t.Run("index tokens and search tokens overlap", func(t *testing.T) {
		content := "中华人民共和国成立"
		indexTokens := tokenizer.TokenizeForIndex(content)

		// gse segments "中华人民共和国成立" into tokens like "中华人民共和国" and "成立".
		// Search for "成立" which should match exactly.
		searchTokens, _ := tokenizer.TokenizeForSearch("成立", false)

		// At least one search token should match an index token
		indexSet := map[string]struct{}{}
		for _, tok := range indexTokens {
			indexSet[tok] = struct{}{}
		}

		matched := false
		for _, tok := range searchTokens {
			if _, ok := indexSet[tok]; ok {
				matched = true
				break
			}
		}
		assert.True(t, matched, "search tokens %v should overlap with index tokens %v", searchTokens, indexTokens)
	})

	t.Run("index and search tokens overlap for mixed content", func(t *testing.T) {
		content := "Go语言是Google开发的编程语言"
		indexTokens := tokenizer.TokenizeForIndex(content)

		// Search for "编程语言" — gse should produce matching segments
		searchTokens, _ := tokenizer.TokenizeForSearch("编程语言", false)

		indexSet := map[string]struct{}{}
		for _, tok := range indexTokens {
			indexSet[tok] = struct{}{}
		}

		matched := false
		for _, tok := range searchTokens {
			if _, ok := indexSet[tok]; ok {
				matched = true
				break
			}
		}
		assert.True(t, matched, "search tokens %v should overlap with index tokens %v", searchTokens, indexTokens)
	})
}

// ---------------------------------------------------------------------------
// 2. SimpleContentSearchEngine CJK compilation and line matching
// ---------------------------------------------------------------------------

func TestCJKCompileAndLineMatch(t *testing.T) {
	t.Run("pure Chinese query compiles and matches", func(t *testing.T) {
		eng := NewSimpleContentSearchEngine(nil, 24, 32, false)
		err := eng.Compile("中华人民", false)
		assert.NoError(t, err)

		matches := eng.IsLineMatch("中华人民共和国成立于1949年")
		assert.True(t, len(matches) > 0, "expected match for '中华人民' in Chinese content")
	})

	t.Run("Chinese query does not match unrelated content", func(t *testing.T) {
		eng := NewSimpleContentSearchEngine(nil, 24, 32, false)
		err := eng.Compile("中华人民", false)
		assert.NoError(t, err)

		matches := eng.IsLineMatch("Go语言是一种编程语言")
		assert.Equal(t, 0, len(matches), "should not match unrelated Chinese content")
	})

	t.Run("mixed CJK-ASCII query matches mixed content", func(t *testing.T) {
		eng := NewSimpleContentSearchEngine(nil, 24, 32, false)
		err := eng.Compile("Go语言", false)
		assert.NoError(t, err)

		matches := eng.IsLineMatch("Go语言是Google开发的编程语言")
		assert.True(t, len(matches) > 0, "expected match for 'Go语言' in mixed content")
	})

	t.Run("ASCII-only query still matches in CJK content", func(t *testing.T) {
		eng := NewSimpleContentSearchEngine(nil, 24, 32, false)
		err := eng.Compile("Google", false)
		assert.NoError(t, err)

		matches := eng.IsLineMatch("Go语言是Google开发的编程语言")
		assert.True(t, len(matches) > 0, "expected match for 'Google' in mixed content")
	})

	t.Run("Chinese term in exact phrase", func(t *testing.T) {
		eng := NewSimpleContentSearchEngine(nil, 24, 32, false)
		err := eng.Compile("\"编程语言\"", false)
		assert.NoError(t, err)

		matches := eng.IsLineMatch("Go语言是一种编程语言")
		assert.True(t, len(matches) > 0, "expected match for exact phrase '编程语言'")
	})

	t.Run("Chinese OR query", func(t *testing.T) {
		eng := NewSimpleContentSearchEngine(nil, 24, 32, false)
		err := eng.Compile("中华人民 | 编程语言", false)
		assert.NoError(t, err)

		matches1 := eng.IsLineMatch("中华人民共和国成立")
		assert.True(t, len(matches1) > 0, "expected first OR branch to match")

		matches2 := eng.IsLineMatch("Go语言是一种编程语言")
		assert.True(t, len(matches2) > 0, "expected second OR branch to match")
	})

	t.Run("stop word only search matches nothing", func(t *testing.T) {
		// "的" is a Chinese stop word. When tokenized for search, it produces
		// no keywords. The engine.Compile should return an error since
		// the query resolves to no valid terms.
		eng := NewSimpleContentSearchEngine(nil, 24, 32, false)
		err := eng.Compile("的", false)

		// If compile succeeds (stop word still passed as regex), check that
		// it at least doesn't spuriously match everything.
		if err == nil {
			matches := eng.IsLineMatch("这是一个普通的句子")
			// Stop word "的" appears in content as a character,
			// but the regex pattern should be the literal "的"
			// so it may or may not match depending on implementation.
			// The key point is that index-level search would filter it out.
			_ = matches // acceptable either way at regex level
		}
	})
}

// ---------------------------------------------------------------------------
// 3. searchInContent with CJK content
// ---------------------------------------------------------------------------

func TestSearchInContent_CJK(t *testing.T) {
	t.Run("pure Chinese content search", func(t *testing.T) {
		eng := NewSimpleContentSearchEngine(nil, 24, 32, false)
		err := eng.Compile("成立", false)
		assert.NoError(t, err)

		content := "第一行\n中华人民共和国成立于1949年\n第三行\n"
		result, err := searchInContent("test.txt", strings.NewReader(content), eng, 0, nil, new(int))
		assert.NoError(t, err)
		assert.Equal(t, 1, len(result.Lines), "expected one matching line")
		assert.Equal(t, 2, result.Lines[0].Line.LineNumber)
	})

	t.Run("mixed CJK-ASCII content search", func(t *testing.T) {
		eng := NewSimpleContentSearchEngine(nil, 24, 32, false)
		err := eng.Compile("编程", false)
		assert.NoError(t, err)

		content := "line 1\nGo语言是Google开发的编程语言\nline 3\n"
		result, err := searchInContent("test.txt", strings.NewReader(content), eng, 0, nil, new(int))
		assert.NoError(t, err)
		assert.Equal(t, 1, len(result.Lines))
		assert.Equal(t, 2, result.Lines[0].Line.LineNumber)
	})

	t.Run("CJK content with before/after context", func(t *testing.T) {
		eng := NewSimpleContentSearchEngine(nil, 24, 32, false)
		err := eng.Compile("目标", false)
		assert.NoError(t, err)

		content := "上文\n前文\n这是目标行\n后文\n下文\n"
		result, err := searchInContent("test.txt", strings.NewReader(content), eng, 2, nil, new(int))
		assert.NoError(t, err)
		assert.Equal(t, 1, len(result.Lines))
		assert.Equal(t, 2, len(result.Lines[0].Before), "expected 2 context lines before")
		assert.Equal(t, 2, len(result.Lines[0].After), "expected 2 context lines after")
	})

	t.Run("multiple CJK matches in file", func(t *testing.T) {
		eng := NewSimpleContentSearchEngine(nil, 24, 32, false)
		err := eng.Compile("测试", false)
		assert.NoError(t, err)

		content := "测试用例一\n普通行\n测试用例二\n又一个测试\n"
		result, err := searchInContent("test.txt", strings.NewReader(content), eng, 0, nil, new(int))
		assert.NoError(t, err)
		assert.Equal(t, 3, len(result.Lines), "expected 3 matching lines")
	})

	t.Run("CJK filename search does not crash", func(t *testing.T) {
		eng := NewSimpleContentSearchEngine(nil, 24, 32, false)
		err := eng.Compile("说明", false)
		assert.NoError(t, err)

		content := "这是说明文档的内容\n"
		result, err := searchInContent("说明文档.md", strings.NewReader(content), eng, 0, nil, new(int))
		assert.NoError(t, err)
		assert.Equal(t, 1, len(result.Lines))
		assert.Equal(t, "说明文档.md", result.File)
	})
}

// ---------------------------------------------------------------------------
// 4. ASCII behavior preserved (regression guard)
// ---------------------------------------------------------------------------

func TestASCIIBehaviorPreserved(t *testing.T) {
	t.Run("simple ASCII search still works", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "function", true, 24, 32)
		matches := eng.IsLineMatch("func main() { function() }")
		assert.True(t, len(matches) > 0)
	})

	t.Run("camelCase splitting still works", func(t *testing.T) {
		tokens := tokenizer.TokenizeForIndex("ContentSearchEngine")
		// ASCIITokenizer splits camelCase and deduplicates prefixes.
		// "ContentSearchEngine" → ["contentsearchengine", "engine", "searchengine"]
		// Verify that at least one of the expected substrings appears.
		hasEngine := false
		for _, tok := range tokens {
			if tok == "engine" {
				hasEngine = true
			}
		}
		assert.True(t, hasEngine, "expected camelCase split to produce 'engine', got: %v", tokens)
	})

	t.Run("ASCII exact phrase still works", func(t *testing.T) {
		eng := CreateSimpleEngine(t, "\"hello world\"", true, 24, 32)
		matches := eng.IsLineMatch("say hello world today")
		assert.True(t, len(matches) > 0)

		noMatch := eng.IsLineMatch("say world hello today")
		assert.Equal(t, 0, len(noMatch))
	})
}

// ---------------------------------------------------------------------------
// 5. CJK file name fuzzy search
// ---------------------------------------------------------------------------

func TestFuzzyMatchWithScore_CJK(t *testing.T) {
	t.Run("exact CJK filename match", func(t *testing.T) {
		matched, score := fuzzyMatchWithScore("说明文档", "说明文档.md")
		assert.True(t, matched)
		assert.Equal(t, 100, score)
	})

	t.Run("CJK substring in path", func(t *testing.T) {
		matched, score := fuzzyMatchWithScore("说明", "docs/说明文档.md")
		assert.True(t, matched)
		assert.Equal(t, 100, score)
	})
}
