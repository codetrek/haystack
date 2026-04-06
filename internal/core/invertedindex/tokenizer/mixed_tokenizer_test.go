package tokenizer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMixedTokenizer_TokenizeForIndex(t *testing.T) {
	mixed := &MixedTokenizer{}
	ascii := &ASCIITokenizer{}

	t.Run("Pure ASCII text matches ASCIITokenizer", func(t *testing.T) {
		inputs := []string{
			"hello world test",
			"CamelCaseString",
			"test_123_value",
			"func handleUpdateDocument(w http.ResponseWriter)",
		}
		for _, input := range inputs {
			expected := ascii.TokenizeForIndex(input)
			result := mixed.TokenizeForIndex(input)
			assert.Equal(t, expected, result, "For input %q, MixedTokenizer should match ASCIITokenizer", input)
		}
	})

	t.Run("Pure CJK text produces CJK tokens", func(t *testing.T) {
		input := "中华人民共和国"
		result := mixed.TokenizeForIndex(input)
		assert.NotEmpty(t, result, "Pure CJK text should produce tokens")
	})

	t.Run("Mixed text produces both ASCII and CJK tokens", func(t *testing.T) {
		input := "hello世界test"
		result := mixed.TokenizeForIndex(input)
		assert.NotEmpty(t, result, "Mixed text should produce tokens")

		// Should contain ASCII tokens
		hasASCII := false
		hasCJK := false
		for _, token := range result {
			if token == "hello" || token == "test" {
				hasASCII = true
			}
			if containsCJK(token) {
				hasCJK = true
			}
		}
		assert.True(t, hasASCII, "Should contain ASCII tokens like 'hello' or 'test'")
		assert.True(t, hasCJK, "Should contain CJK tokens")
	})

	t.Run("English sentence with Chinese produces both", func(t *testing.T) {
		input := "The 中华人民共和国 is great"
		result := mixed.TokenizeForIndex(input)
		assert.NotEmpty(t, result, "Mixed sentence should produce tokens")

		hasASCII := false
		hasCJK := false
		for _, token := range result {
			if token == "the" || token == "great" {
				hasASCII = true
			}
			if containsCJK(token) {
				hasCJK = true
			}
		}
		assert.True(t, hasASCII, "Should contain ASCII tokens")
		assert.True(t, hasCJK, "Should contain CJK tokens")
	})

	t.Run("Results are sorted", func(t *testing.T) {
		input := "hello世界test编程"
		result := mixed.TokenizeForIndex(input)
		assert.NotEmpty(t, result)
		for i := 1; i < len(result); i++ {
			assert.True(t, result[i-1] <= result[i],
				"Results should be sorted: %q should be <= %q", result[i-1], result[i])
		}
	})

	t.Run("Results are deduplicated", func(t *testing.T) {
		input := "hello世界hello世界"
		result := mixed.TokenizeForIndex(input)
		seen := make(map[string]bool)
		for _, token := range result {
			assert.False(t, seen[token], "Token %q should not be duplicated", token)
			seen[token] = true
		}
	})

	t.Run("Empty string returns empty", func(t *testing.T) {
		result := mixed.TokenizeForIndex("")
		assert.Empty(t, result)
	})
}

func TestMixedTokenizer_TokenizeForSearch(t *testing.T) {
	mixed := &MixedTokenizer{}
	ascii := &ASCIITokenizer{}

	t.Run("Pure ASCII text matches ASCIITokenizer", func(t *testing.T) {
		inputs := []string{
			"hello world test",
			"CamelCaseString",
			"test_123_value",
		}
		for _, input := range inputs {
			expectedTokens, expectedWC := ascii.TokenizeForSearch(input, false)
			resultTokens, resultWC := mixed.TokenizeForSearch(input, false)
			assert.ElementsMatch(t, expectedTokens, resultTokens, "For input %q", input)
			assert.ElementsMatch(t, expectedWC, resultWC, "Wildcards for input %q", input)
		}
	})

	t.Run("Pure ASCII exact matching matches ASCIITokenizer", func(t *testing.T) {
		input := "CamelCaseString"
		expectedTokens, _ := ascii.TokenizeForSearch(input, true)
		resultTokens, _ := mixed.TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedTokens, resultTokens)
	})

	t.Run("Pure CJK text produces tokens", func(t *testing.T) {
		input := "中华人民共和国"
		result, wildcards := mixed.TokenizeForSearch(input, false)
		assert.NotEmpty(t, result, "Pure CJK text should produce search tokens")
		assert.Nil(t, wildcards)
	})

	t.Run("Mixed text produces both ASCII and CJK tokens", func(t *testing.T) {
		input := "hello世界test"
		result, _ := mixed.TokenizeForSearch(input, false)
		assert.NotEmpty(t, result, "Mixed text should produce search tokens")

		hasASCII := false
		hasCJK := false
		for _, token := range result {
			if token == "hello" || token == "test" {
				hasASCII = true
			}
			if containsCJK(token) {
				hasCJK = true
			}
		}
		assert.True(t, hasASCII, "Should contain ASCII tokens")
		assert.True(t, hasCJK, "Should contain CJK tokens")
	})

	t.Run("English with Chinese produces both", func(t *testing.T) {
		input := "The 中华人民共和国 is great"
		result, _ := mixed.TokenizeForSearch(input, false)
		assert.NotEmpty(t, result, "Mixed sentence should produce search tokens")

		hasASCII := false
		hasCJK := false
		for _, token := range result {
			if token == "The" || token == "great" {
				hasASCII = true
			}
			if containsCJK(token) {
				hasCJK = true
			}
		}
		assert.True(t, hasASCII, "Should contain ASCII tokens")
		assert.True(t, hasCJK, "Should contain CJK tokens")
	})

	t.Run("Empty string returns empty", func(t *testing.T) {
		result, _ := mixed.TokenizeForSearch("", false)
		assert.Empty(t, result)
	})
}

func TestSplitIntoRuns(t *testing.T) {
	t.Run("Pure ASCII", func(t *testing.T) {
		runs := splitIntoRuns("hello world")
		assert.Len(t, runs, 1)
		assert.False(t, runs[0].isCJK)
		assert.Equal(t, "hello world", runs[0].text)
	})

	t.Run("Pure CJK", func(t *testing.T) {
		runs := splitIntoRuns("中华人民")
		assert.Len(t, runs, 1)
		assert.True(t, runs[0].isCJK)
		assert.Equal(t, "中华人民", runs[0].text)
	})

	t.Run("Mixed ASCII and CJK", func(t *testing.T) {
		runs := splitIntoRuns("hello世界test")
		assert.Len(t, runs, 3)
		assert.False(t, runs[0].isCJK)
		assert.Equal(t, "hello", runs[0].text)
		assert.True(t, runs[1].isCJK)
		assert.Equal(t, "世界", runs[1].text)
		assert.False(t, runs[2].isCJK)
		assert.Equal(t, "test", runs[2].text)
	})

	t.Run("Empty string", func(t *testing.T) {
		runs := splitIntoRuns("")
		assert.Empty(t, runs)
	})

	t.Run("CJK with spaces", func(t *testing.T) {
		runs := splitIntoRuns("The 中华人民共和国 is great")
		// "The " is non-CJK, "中华人民共和国" is CJK, " is great" is non-CJK
		assert.True(t, len(runs) >= 3, "Should have at least 3 runs")
	})
}
