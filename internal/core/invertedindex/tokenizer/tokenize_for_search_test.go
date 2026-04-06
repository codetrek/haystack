package tokenizer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTokenizeForSearch(t *testing.T) {
	t.Run("Test with special characters", func(t *testing.T) {
		input := "test@123"
		expected := []string{"test", "123"}
		result, wildcards := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)
		assert.Empty(t, wildcards)

		expectedExact := []string{"test", "123"}
		resultExact, _ := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with empty string", func(t *testing.T) {
		input := ""
		expected := []string{}
		result, _ := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		resultExact, _ := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expected, resultExact)
	})

	t.Run("Test with long string", func(t *testing.T) {
		input := "This is a long string with multiple words and some special characters !@#$%^&*()"
		expected := []string{"This", "long", "string", "with", "multiple", "words", "and", "some", "special", "characters"}
		result, wildcards := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)
		assert.Empty(t, wildcards)

		expectedExact := []string{"This", "long", "string", "with", "multiple", "words", "and", "some", "special", "characters"}
		resultExact, _ := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with camel case", func(t *testing.T) {
		input := "CamelCaseString"
		expected := []string{"CamelCaseString"}
		result, _ := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"CamelCaseString"}
		resultExact, _ := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with mixed case", func(t *testing.T) {
		input := "MixedCASEString"
		expected := []string{"MixedCASEString"}
		result, _ := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"MixedCASEString"}
		resultExact, _ := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with numbers", func(t *testing.T) {
		input := "test123"
		expected := []string{"test123"}
		result, _ := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		resultExact, _ := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expected, resultExact)
	})

	t.Run("Test with underscores", func(t *testing.T) {
		input := "test_123"
		expected := []string{"test_123"}
		result, _ := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"test_123"}
		resultExact, _ := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with hyphens", func(t *testing.T) {
		input := "test-123"
		expected := []string{"test", "123"}
		result, _ := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"test", "123"}
		resultExact, _ := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with long words", func(t *testing.T) {
		input := "a" + strings.Repeat("b", 79)
		expected := []string{"a" + strings.Repeat("b", 79)}
		result, _ := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		resultExact, _ := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expected, resultExact)
	})

	t.Run("Test with short words", func(t *testing.T) {
		input := "ab"
		expected := []string{} // For search, short words are kept
		result, _ := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		resultExact, _ := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expected, resultExact)
	})

	t.Run("Test with invalid characters", func(t *testing.T) {
		input := "test@#$%^&*()"
		expected := []string{"test"}
		result, _ := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"test"}
		resultExact, _ := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with code snippets", func(t *testing.T) {
		input := "if !equalSlices(result, test.expected)"
		expected := []string{"equalSlices", "result", "test", "expected"}
		result, _ := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"equalSlices", "result", "test", "expected"}
		resultExact, _ := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with function signatures", func(t *testing.T) {
		input := "func handleUpdateDocument(w http.ResponseWriter, r *http.Request) {"
		expected := []string{"func", "handleUpdateDocument", "http", "ResponseWriter", "Request"}
		expectedWildcards := []string{"http"}
		result, wildcards := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)
		assert.ElementsMatch(t, expectedWildcards, wildcards)

		expectedExact := []string{"func", "handleUpdateDocument", "http", "ResponseWriter", "Request"}
		resultExact, _ := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with CommonResponse", func(t *testing.T) {
		input := "type CommonResponse struct { \nCode int `json:\"code\"` \nMessage string `json:\"message\"` }"
		expected := []string{"type", "CommonResponse", "struct", "Code", "code", "int", "json", "message", "Message", "string"}
		result, _ := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"type", "CommonResponse", "struct", "Code", "code", "int", "json", "message", "Message", "string"}
		resultExact, _ := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with Chinese characters", func(t *testing.T) {
		input := "测试中文字符"
		result, _ := TokenizeForSearch(input, false)
		assert.NotEmpty(t, result, "CJK text should produce tokens")
		for _, token := range result {
			assert.GreaterOrEqual(t, len([]rune(token)), 1, "CJK token should be >= 1 rune")
		}

		resultExact, _ := TokenizeForSearch(input, true)
		assert.NotEmpty(t, resultExact, "CJK text should produce tokens in exact mode")
	})

	t.Run("Test with mixed characters", func(t *testing.T) {
		input := "test123测试中文字符"
		result, _ := TokenizeForSearch(input, false)
		assert.Greater(t, len(result), 1, "should contain both ASCII and CJK tokens")
		// Should contain the ASCII token
		hasASCII := false
		for _, token := range result {
			if token == "test123" {
				hasASCII = true
			}
		}
		assert.True(t, hasASCII, "should contain ASCII token test123")

		resultExact, _ := TokenizeForSearch(input, true)
		assert.Greater(t, len(resultExact), 1, "exact mode should also contain both ASCII and CJK tokens")
	})

	t.Run("Test real function", func(t *testing.T) {
		input := "collab_presence_->IsShowingCollaboratorHoverCard()"
		expected := []string{"collab_presence", "IsShowingCollaboratorHoverCard"}
		result, _ := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"collab_presence", "IsShowingCollaboratorHoverCard"}
		resultExact, _ := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test dot text", func(t *testing.T) {
		input := "conf.Server.Search.Limit.MaxResultsPerFile"
		expected := []string{"conf", "Server", "Search", "Limit", "MaxResultsPerFile"}
		result, _ := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)
	})

	t.Run("Test with wildcard", func(t *testing.T) {
		input := "test*wildcard"
		expected := []string{"test"}
		expectedWildcards := []string{"wildcard"}
		result, wildcards := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)
		assert.ElementsMatch(t, expectedWildcards, wildcards)

		expectedExact := []string{"test", "wildcard"}
		resultExact, wildcards := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
		assert.Empty(t, wildcards)
	})
}
