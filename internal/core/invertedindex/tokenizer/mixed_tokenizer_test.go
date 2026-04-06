package tokenizer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMixedTokenizer_TokenizeForIndex(t *testing.T) {
	tok := &MixedTokenizer{}

	// --- Pure ASCII tests: behavior must match ASCIITokenizer exactly ---

	t.Run("ASCII special characters", func(t *testing.T) {
		result := tok.TokenizeForIndex("test@123")
		assert.Equal(t, []string{"123", "test"}, result)
	})

	t.Run("ASCII empty string", func(t *testing.T) {
		result := tok.TokenizeForIndex("")
		assert.Equal(t, []string{}, result)
	})

	t.Run("ASCII long string", func(t *testing.T) {
		input := "This is a long string with multiple words and some special characters !@#$%^&*()"
		expected := []string{"and", "characters", "long", "multiple", "some", "special", "string", "this", "with", "words"}
		result := tok.TokenizeForIndex(input)
		assert.Equal(t, expected, result)
	})

	t.Run("ASCII camel case", func(t *testing.T) {
		result := tok.TokenizeForIndex("CamelCaseString")
		assert.ElementsMatch(t, []string{"camelcasestring", "casestring", "string"}, result)
	})

	t.Run("ASCII prefix de-dup", func(t *testing.T) {
		result := tok.TokenizeForIndex("camel case CamelCaseString-")
		assert.ElementsMatch(t, []string{"camelcasestring", "casestring", "string"}, result)
	})

	t.Run("ASCII numbers", func(t *testing.T) {
		result := tok.TokenizeForIndex("test123")
		assert.Equal(t, []string{"test123"}, result)
	})

	t.Run("ASCII underscores", func(t *testing.T) {
		result := tok.TokenizeForIndex("test_123_")
		assert.Equal(t, []string{"123", "test_123"}, result)
	})

	t.Run("ASCII short words", func(t *testing.T) {
		result := tok.TokenizeForIndex("ab")
		assert.Equal(t, []string{}, result)
	})

	t.Run("ASCII long words", func(t *testing.T) {
		input := "a" + strings.Repeat("b", 79)
		expected := []string{"a" + strings.Repeat("b", 79)}
		result := tok.TokenizeForIndex(input)
		assert.Equal(t, expected, result)
	})

	t.Run("ASCII code snippets", func(t *testing.T) {
		result := tok.TokenizeForIndex("if !equalSlices(result, test.expected)")
		assert.ElementsMatch(t, []string{"equalslices", "expected", "result", "slices", "test"}, result)
	})

	t.Run("ASCII dot text", func(t *testing.T) {
		result := tok.TokenizeForIndex("conf.Server.Search.Limit.MaxResultsPerFile")
		assert.ElementsMatch(t, []string{"conf", "server", "search", "limit", "maxresultsperfile", "resultsperfile", "perfile", "file"}, result)
	})

	// --- Mixed CJK + ASCII tests ---

	t.Run("Mixed Chinese and ASCII", func(t *testing.T) {
		result := tok.TokenizeForIndex("Hello世界")
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
		assert.True(t, hasCJK, "should have CJK tokens")
		assert.True(t, hasASCII, "should have ASCII tokens")
	})

	t.Run("Pure Chinese dispatches to CJK path", func(t *testing.T) {
		result := tok.TokenizeForIndex("自然语言处理")
		assert.NotEmpty(t, result)
	})
}

func TestMixedTokenizer_TokenizeForSearch(t *testing.T) {
	tok := &MixedTokenizer{}

	// --- Pure ASCII tests: behavior must match ASCIITokenizer exactly ---

	t.Run("ASCII special characters", func(t *testing.T) {
		result, wildcards := tok.TokenizeForSearch("test@123", false)
		assert.ElementsMatch(t, []string{"test", "123"}, result)
		assert.Empty(t, wildcards)
	})

	t.Run("ASCII empty string", func(t *testing.T) {
		result, _ := tok.TokenizeForSearch("", false)
		assert.Empty(t, result)
	})

	t.Run("ASCII camel case", func(t *testing.T) {
		result, _ := tok.TokenizeForSearch("CamelCaseString", false)
		assert.ElementsMatch(t, []string{"CamelCaseString"}, result)
	})

	t.Run("ASCII code snippets", func(t *testing.T) {
		result, _ := tok.TokenizeForSearch("if !equalSlices(result, test.expected)", false)
		assert.ElementsMatch(t, []string{"equalSlices", "result", "test", "expected"}, result)
	})

	t.Run("ASCII with wildcard", func(t *testing.T) {
		result, wildcards := tok.TokenizeForSearch("test*wildcard", false)
		assert.ElementsMatch(t, []string{"test"}, result)
		assert.ElementsMatch(t, []string{"wildcard"}, wildcards)
	})

	t.Run("ASCII exact matching", func(t *testing.T) {
		result, wildcards := tok.TokenizeForSearch("test*wildcard", true)
		assert.ElementsMatch(t, []string{"test", "wildcard"}, result)
		assert.Empty(t, wildcards)
	})

	// --- Mixed CJK + ASCII tests ---

	t.Run("Mixed Chinese and ASCII for search", func(t *testing.T) {
		result, _ := tok.TokenizeForSearch("Hello世界", false)
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
		assert.True(t, hasCJK, "should have CJK tokens")
		assert.True(t, hasASCII, "should have ASCII tokens")
	})

	t.Run("Pure Chinese for search", func(t *testing.T) {
		result, wildcards := tok.TokenizeForSearch("中华人民共和国", false)
		assert.NotEmpty(t, result)
		assert.Empty(t, wildcards)
	})
}
