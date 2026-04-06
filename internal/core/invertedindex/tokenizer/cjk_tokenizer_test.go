package tokenizer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCJKTokenizer_TokenizeForIndex(t *testing.T) {
	tok := &CJKTokenizer{}

	t.Run("Pure Chinese text", func(t *testing.T) {
		result := tok.TokenizeForIndex("中华人民共和国")
		assert.NotEmpty(t, result)
		// gse should segment this into meaningful words
		for _, token := range result {
			assert.GreaterOrEqual(t, len([]rune(token)), 1, "CJK token should be >= 1 rune: %q", token)
		}
	})

	t.Run("Chinese sentence", func(t *testing.T) {
		result := tok.TokenizeForIndex("我爱自然语言处理")
		assert.NotEmpty(t, result)
		// Should contain meaningful Chinese segments
		for _, token := range result {
			assert.GreaterOrEqual(t, len([]rune(token)), 1)
		}
	})

	t.Run("Chinese-English mixed text", func(t *testing.T) {
		result := tok.TokenizeForIndex("Go语言是Google开发的编程语言")
		assert.NotEmpty(t, result)
		// Should contain both CJK and ASCII tokens
		hasCJK := false
		hasASCII := false
		for _, token := range result {
			if containsCJK(token) {
				hasCJK = true
			} else {
				hasASCII = true
			}
		}
		assert.True(t, hasCJK, "should contain CJK tokens")
		assert.True(t, hasASCII, "should contain ASCII tokens")
	})

	t.Run("Japanese text with kanji hiragana katakana", func(t *testing.T) {
		result := tok.TokenizeForIndex("東京タワーはとても高い")
		assert.NotEmpty(t, result)
		for _, token := range result {
			assert.GreaterOrEqual(t, len([]rune(token)), 1)
		}
	})

	t.Run("Korean text", func(t *testing.T) {
		result := tok.TokenizeForIndex("한국어 처리 테스트")
		assert.NotEmpty(t, result)
		for _, token := range result {
			assert.GreaterOrEqual(t, len([]rune(token)), 1)
		}
	})

	t.Run("CJK token minimum length is 1 rune", func(t *testing.T) {
		// Single character should be a valid CJK token
		result := tok.TokenizeForIndex("我")
		assert.NotEmpty(t, result)
		assert.Contains(t, result, "我")
	})

	t.Run("Results are sorted and deduplicated", func(t *testing.T) {
		result := tok.TokenizeForIndex("测试测试数据")
		// Should be sorted
		for i := 1; i < len(result); i++ {
			assert.LessOrEqual(t, result[i-1], result[i], "results should be sorted")
		}
		// Should be deduplicated
		seen := make(map[string]struct{})
		for _, token := range result {
			_, exists := seen[token]
			assert.False(t, exists, "duplicate token found: %q", token)
			seen[token] = struct{}{}
		}
	})

	t.Run("ASCII tokens are lowercased", func(t *testing.T) {
		result := tok.TokenizeForIndex("Hello世界World")
		for _, token := range result {
			if !containsCJK(token) {
				for _, r := range token {
					if r >= 'A' && r <= 'Z' {
						t.Errorf("ASCII token should be lowercase: %q", token)
					}
				}
			}
		}
	})

	t.Run("Empty string", func(t *testing.T) {
		result := tok.TokenizeForIndex("")
		assert.Empty(t, result)
	})
}

func TestCJKTokenizer_TokenizeForSearch(t *testing.T) {
	tok := &CJKTokenizer{}

	t.Run("Pure Chinese text", func(t *testing.T) {
		result, wildcards := tok.TokenizeForSearch("中华人民共和国", false)
		assert.NotEmpty(t, result)
		assert.Empty(t, wildcards)
		for _, token := range result {
			assert.GreaterOrEqual(t, len([]rune(token)), 1)
		}
	})

	t.Run("Chinese-English mixed text for search", func(t *testing.T) {
		result, _ := tok.TokenizeForSearch("Go语言是Google开发的编程语言", false)
		assert.NotEmpty(t, result)
		hasCJK := false
		hasASCII := false
		for _, token := range result {
			if containsCJK(token) {
				hasCJK = true
			} else {
				hasASCII = true
			}
		}
		assert.True(t, hasCJK, "should contain CJK tokens")
		assert.True(t, hasASCII, "should contain ASCII tokens")
	})

	t.Run("Search results are not duplicated", func(t *testing.T) {
		result, _ := tok.TokenizeForSearch("测试测试数据", false)
		seen := make(map[string]struct{})
		for _, token := range result {
			_, exists := seen[token]
			assert.False(t, exists, "duplicate token found: %q", token)
			seen[token] = struct{}{}
		}
	})

	t.Run("Empty string", func(t *testing.T) {
		result, wildcards := tok.TokenizeForSearch("", false)
		assert.Empty(t, result)
		assert.Empty(t, wildcards)
	})
}

func TestCJKTokenizer_LazyLoading(t *testing.T) {
	t.Run("Pure ASCII does not trigger gse loading via MixedTokenizer", func(t *testing.T) {
		// Create a fresh MixedTokenizer
		mixed := &MixedTokenizer{}
		// Tokenize pure ASCII — should use ASCIITokenizer path
		result := mixed.TokenizeForIndex("hello world test")
		assert.NotEmpty(t, result)
		// The CJK tokenizer's once should NOT have been triggered
		// (we can't directly observe this, but we verify the result
		// matches what ASCIITokenizer would produce)
		ascii := &ASCIITokenizer{}
		expected := ascii.TokenizeForIndex("hello world test")
		assert.Equal(t, expected, result)
	})
}
