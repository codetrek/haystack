package tokenizer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTokenizeForIndex(t *testing.T) {
	t.Run("Test with special characters", func(t *testing.T) {
		input := "test@123"
		expected := []string{"123", "test"}
		result := TokenizeForIndex(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with empty string", func(t *testing.T) {
		input := ""
		expected := []string{}
		result := TokenizeForIndex(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with long string", func(t *testing.T) {
		input := "This is a long string with multiple words and some special characters !@#$%^&*()"
		expected := []string{"and", "characters", "long", "multiple", "some", "special", "string", "this", "with", "words"}
		result := TokenizeForIndex(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with camel case", func(t *testing.T) {
		input := "CamelCaseString"
		expected := []string{"camelcasestring", "casestring", "string"}
		result := TokenizeForIndex(input)
		assert.ElementsMatch(t, expected, result)
	})

	t.Run("Test with prefix de-dup", func(t *testing.T) {
		input := "camel case CamelCaseString"
		expected := []string{"camelcasestring", "casestring", "string"}
		result := TokenizeForIndex(input)
		assert.ElementsMatch(t, expected, result)
	})

	t.Run("Test with mixed case", func(t *testing.T) {
		input := "MixedCASEString"
		expected := []string{"mixedcasestring", "casestring", "string"}
		result := TokenizeForIndex(input)
		assert.ElementsMatch(t, expected, result)
	})

	t.Run("Test with numbers", func(t *testing.T) {
		input := "test123"
		expected := []string{"test123"}
		result := TokenizeForIndex(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with underscores", func(t *testing.T) {
		input := "test_123"
		expected := []string{"123", "test_123"}
		result := TokenizeForIndex(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with hyphens", func(t *testing.T) {
		input := "test-123"
		expected := []string{"123", "test"}
		result := TokenizeForIndex(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with special characters", func(t *testing.T) {
		input := "test@123"
		expected := []string{"123", "test"}
		result := TokenizeForIndex(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with long words", func(t *testing.T) {
		input := "a" + strings.Repeat("b", 79)
		expected := []string{"a" + strings.Repeat("b", 79)}
		result := TokenizeForIndex(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with short words", func(t *testing.T) {
		input := "ab"
		expected := []string{}
		result := TokenizeForIndex(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with invalid characters", func(t *testing.T) {
		input := "test@#$%^&*()"
		expected := []string{"test"}
		result := TokenizeForIndex(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with code snippets", func(t *testing.T) {
		input := "if !equalSlices(result, test.expected)"
		expected := []string{"equalslices", "expected", "result", "slices", "test"}
		result := TokenizeForIndex(input)
		assert.ElementsMatch(t, expected, result)
	})

	t.Run("Test with function signatures", func(t *testing.T) {
		input := "func handleUpdateDocument(w http.ResponseWriter, r *http.Request) {"
		expected := []string{"document", "func", "handleupdatedocument", "http", "responsewriter", "request", "updatedocument", "writer"}
		result := TokenizeForIndex(input)
		assert.ElementsMatch(t, expected, result)
	})

	t.Run("Test with CommonResponse", func(t *testing.T) {
		input := "type CommonResponse struct { \nCode int `json:\"code\"` \nMessage string `json:\"message\"` }"
		expected := []string{"code", "commonresponse", "int", "json", "message", "response", "string", "struct", "type"}
		result := TokenizeForIndex(input)
		assert.ElementsMatch(t, expected, result)
	})

	t.Run("Test with Chinese characters", func(t *testing.T) {
		input := "测试中文字符"
		expected := []string{}
		result := TokenizeForIndex(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with mixed characters", func(t *testing.T) {
		input := "test123测试中文字符"
		expected := []string{"test123"}
		result := TokenizeForIndex(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test real function", func(t *testing.T) {
		input := "collab_presence_->IsShowingCollaboratorHoverCard()"
		expected := []string{"collab_presence", "presence", "isshowingcollaboratorhovercard",
			"showingcollaboratorhovercard", "collaboratorhovercard", "hovercard", "card"}
		result := TokenizeForIndex(input)
		assert.ElementsMatch(t, expected, result)
	})

	t.Run("Test text with dot", func(t *testing.T) {
		input := "1.2.3. ab.cd.ef Hello.World"
		expected := []string{"1.2.3", "2.3", "ab.cd.ef", "cd.ef", "hello", "world"}
		result := TokenizeForIndex(input)
		assert.ElementsMatch(t, expected, result)
	})

	t.Run("Test real function", func(t *testing.T) {
		input := "collab_presence_->IsShowingCollaborator()"
		expected := []string{"collab_presence", "presence", "isshowingcollaborator", "showingcollaborator", "collaborator"}
		result := TokenizeForIndex(input)
		assert.ElementsMatch(t, expected, result)
	})

	t.Run("Test dot text", func(t *testing.T) {
		input := "conf.Server.Search.Limit.MaxResultsPerFile"
		expected := []string{"conf", "server", "search", "limit", "maxresultsperfile", "resultsperfile", "perfile", "file"}
		result := TokenizeForIndex(input)
		assert.ElementsMatch(t, expected, result)
	})
}
