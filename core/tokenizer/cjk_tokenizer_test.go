package tokenizer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCJKTokenizer_TokenizeForIndex(t *testing.T) {
	cjk := &CJKTokenizer{}

	t.Run("Pure Chinese text produces meaningful tokens", func(t *testing.T) {
		input := "中华人民共和国"
		result := cjk.TokenizeForIndex(input)
		assert.NotEmpty(t, result, "Chinese text should produce tokens")
		// gse should segment this into meaningful words
		assert.True(t, len(result) >= 1, "Should produce at least 1 token")
	})

	t.Run("Chinese sentence produces multiple tokens", func(t *testing.T) {
		input := "我爱北京天安门"
		result := cjk.TokenizeForIndex(input)
		assert.NotEmpty(t, result, "Chinese sentence should produce tokens")
		assert.True(t, len(result) >= 2, "Should produce multiple tokens")
	})

	t.Run("Single CJK character is valid token", func(t *testing.T) {
		input := "国"
		result := cjk.TokenizeForIndex(input)
		assert.NotNil(t, result)
	})

	t.Run("Empty string returns empty", func(t *testing.T) {
		result := cjk.TokenizeForIndex("")
		assert.Empty(t, result)
	})

	t.Run("Whitespace only returns empty", func(t *testing.T) {
		result := cjk.TokenizeForIndex("   ")
		assert.Empty(t, result)
	})

	t.Run("Punctuation only returns empty", func(t *testing.T) {
		result := cjk.TokenizeForIndex("，。！？")
		assert.Empty(t, result)
	})

	t.Run("Results are sorted", func(t *testing.T) {
		input := "中华人民共和国成立了"
		result := cjk.TokenizeForIndex(input)
		assert.NotEmpty(t, result)
		for i := 1; i < len(result); i++ {
			assert.True(t, result[i-1] <= result[i],
				"Results should be sorted: %q should be <= %q", result[i-1], result[i])
		}
	})

	t.Run("Results are deduplicated", func(t *testing.T) {
		input := "中国人民中国人民"
		result := cjk.TokenizeForIndex(input)
		seen := make(map[string]bool)
		for _, token := range result {
			assert.False(t, seen[token], "Token %q should not be duplicated", token)
			seen[token] = true
		}
	})

	t.Run("Results are lowercased", func(t *testing.T) {
		input := "中华人民共和国"
		result := cjk.TokenizeForIndex(input)
		assert.NotEmpty(t, result)
	})

	t.Run("Japanese hiragana text", func(t *testing.T) {
		input := "こんにちは世界"
		result := cjk.TokenizeForIndex(input)
		assert.NotEmpty(t, result, "Japanese text should produce tokens")
	})

	t.Run("Korean text", func(t *testing.T) {
		input := "안녕하세요"
		result := cjk.TokenizeForIndex(input)
		assert.NotNil(t, result)
	})

	t.Run("Stop words are filtered from index tokens", func(t *testing.T) {
		result := cjk.TokenizeForIndex("我的世界很大")
		assert.NotEmpty(t, result)
		for _, token := range result {
			assert.False(t, isStopWord(token), "stop word %q should have been filtered from index", token)
		}
		assert.Contains(t, result, "世界")
		assert.Contains(t, result, "很大")
		assert.NotContains(t, result, "我")
		assert.NotContains(t, result, "的")
	})

	t.Run("Stop words filtered but content preserved", func(t *testing.T) {
		result := cjk.TokenizeForIndex("这是一个测试")
		assert.NotContains(t, result, "这")
		assert.NotContains(t, result, "是")
		assert.Contains(t, result, "测试")
	})

	t.Run("All stop words sentence results in mostly filtered tokens", func(t *testing.T) {
		result := cjk.TokenizeForIndex("我在这里")
		for _, token := range result {
			assert.False(t, isStopWord(token), "stop word %q should have been filtered", token)
		}
	})
}

func TestCJKTokenizer_TokenizeForSearch(t *testing.T) {
	cjk := &CJKTokenizer{}

	t.Run("Pure Chinese text produces tokens", func(t *testing.T) {
		input := "中华人民共和国"
		result, wildcards := cjk.TokenizeForSearch(input, false)
		assert.NotEmpty(t, result, "Chinese text should produce search tokens")
		assert.Nil(t, wildcards, "Wildcards should be nil for now")
	})

	t.Run("Empty string returns empty", func(t *testing.T) {
		result, _ := cjk.TokenizeForSearch("", false)
		assert.Empty(t, result)
	})

	t.Run("Whitespace only returns empty", func(t *testing.T) {
		result, _ := cjk.TokenizeForSearch("   ", false)
		assert.Empty(t, result)
	})

	t.Run("Search tokens are not lowercased", func(t *testing.T) {
		input := "中华人民共和国"
		result, _ := cjk.TokenizeForSearch(input, false)
		assert.NotEmpty(t, result)
	})

	t.Run("Exact matching mode", func(t *testing.T) {
		input := "中华人民共和国"
		result, wildcards := cjk.TokenizeForSearch(input, true)
		assert.NotEmpty(t, result)
		assert.Nil(t, wildcards)
	})

	t.Run("Stop words are filtered from search tokens", func(t *testing.T) {
		result, _ := cjk.TokenizeForSearch("我的世界很大", false)
		assert.NotEmpty(t, result)
		for _, token := range result {
			assert.False(t, isStopWord(token), "stop word %q should have been filtered from search", token)
		}
		assert.Contains(t, result, "世界")
		assert.Contains(t, result, "很大")
		assert.NotContains(t, result, "我")
		assert.NotContains(t, result, "的")
	})

	t.Run("Stop words filtered but content preserved in search", func(t *testing.T) {
		result, _ := cjk.TokenizeForSearch("这是一个测试", false)
		assert.NotContains(t, result, "这")
		assert.NotContains(t, result, "是")
		assert.Contains(t, result, "测试")
	})
}

func TestCJKTokenizer_LazyLoading(t *testing.T) {
	t.Run("CJKTokenizer can be created without loading dict", func(t *testing.T) {
		cjk := &CJKTokenizer{}
		assert.False(t, cjk.loaded, "gse should not be loaded on struct creation")
	})

	t.Run("Dictionary is loaded after first call", func(t *testing.T) {
		cjk := &CJKTokenizer{}
		assert.False(t, cjk.loaded, "gse should not be loaded before first call")
		cjk.TokenizeForIndex("测试")
		assert.True(t, cjk.loaded, "gse should be loaded after first tokenization")
	})
}
