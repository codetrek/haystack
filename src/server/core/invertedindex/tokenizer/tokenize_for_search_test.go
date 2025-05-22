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
		result := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"test", "123"}
		resultExact := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with empty string", func(t *testing.T) {
		input := ""
		expected := []string{}
		result := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		resultExact := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expected, resultExact)
	})

	t.Run("Test with long string", func(t *testing.T) {
		input := "This is a long string with multiple words and some special characters !@#$%^&*()"
		expected := []string{"This", "long", "string", "with", "multiple", "words", "and", "some", "special", "characters"}
		result := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"This", "long", "string", "with", "multiple", "words", "and", "some", "special", "characters"}
		resultExact := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with camel case", func(t *testing.T) {
		input := "CamelCaseString"
		expected := []string{"CamelCaseString"}
		result := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"CamelCaseString"}
		resultExact := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with mixed case", func(t *testing.T) {
		input := "MixedCASEString"
		expected := []string{"MixedCASEString"}
		result := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"MixedCASEString"}
		resultExact := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with numbers", func(t *testing.T) {
		input := "test123"
		expected := []string{"test123"}
		result := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		resultExact := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expected, resultExact)
	})

	t.Run("Test with underscores", func(t *testing.T) {
		input := "test_123"
		expected := []string{"test_123"}
		result := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"test_123"}
		resultExact := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with hyphens", func(t *testing.T) {
		input := "test-123"
		expected := []string{"test", "123"}
		result := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"test", "123"}
		resultExact := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with long words", func(t *testing.T) {
		input := "a" + strings.Repeat("b", 79)
		expected := []string{"a" + strings.Repeat("b", 79)}
		result := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		resultExact := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expected, resultExact)
	})

	t.Run("Test with short words", func(t *testing.T) {
		input := "ab"
		expected := []string{} // For search, short words are kept
		result := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		resultExact := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expected, resultExact)
	})

	t.Run("Test with invalid characters", func(t *testing.T) {
		input := "test@#$%^&*()"
		expected := []string{"test"}
		result := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"test"}
		resultExact := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with code snippets", func(t *testing.T) {
		input := "if !equalSlices(result, test.expected)"
		expected := []string{"equalSlices", "result", "test", "expected"}
		result := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"equalSlices", "result", "test", "expected"}
		resultExact := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with function signatures", func(t *testing.T) {
		input := "func handleUpdateDocument(w http.ResponseWriter, r *http.Request) {"
		expected := []string{"func", "handleUpdateDocument", "http", "ResponseWriter", "Request"}
		result := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"func", "handleUpdateDocument", "http", "ResponseWriter", "Request"}
		resultExact := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with CommonResponse", func(t *testing.T) {
		input := "type CommonResponse struct { \nCode int `json:\"code\"` \nMessage string `json:\"message\"` }"
		expected := []string{"type", "CommonResponse", "struct", "Code", "code", "int", "json", "message", "Message", "string"}
		result := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"type", "CommonResponse", "struct", "Code", "code", "int", "json", "message", "Message", "string"}
		resultExact := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test with Chinese characters", func(t *testing.T) {
		input := "测试中文字符"
		expected := []string{}
		result := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		resultExact := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expected, resultExact)
	})

	t.Run("Test with mixed characters", func(t *testing.T) {
		input := "test123测试中文字符"
		expected := []string{"test123"}
		result := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"test123"}
		resultExact := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test real function", func(t *testing.T) {
		input := "collab_presence_->IsShowingCollaboratorHoverCard()"
		expected := []string{"collab_presence", "IsShowingCollaboratorHoverCard"}
		result := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"collab_presence", "IsShowingCollaboratorHoverCard"}
		resultExact := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})

	t.Run("Test dot text", func(t *testing.T) {
		input := "conf.Server.Search.Limit.MaxResultsPerFile"
		expected := []string{"conf", "Server", "Search", "Limit", "MaxResultsPerFile"}
		result := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)
	})

	t.Run("Test with wildcard", func(t *testing.T) {
		input := "test*wildcard"
		expected := []string{"test"}
		result := TokenizeForSearch(input, false)
		assert.ElementsMatch(t, expected, result)

		expectedExact := []string{"test", "wildcard"}
		resultExact := TokenizeForSearch(input, true)
		assert.ElementsMatch(t, expectedExact, resultExact)
	})
}
