package indexer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCameSnakeSplit(t *testing.T) {
	// Test cases for camel case splitting
	t.Run("Test with empty string", func(t *testing.T) {
		input := ""
		expected := []string{}
		result := camelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("CamelCase", func(t *testing.T) {
		input := "CamelCase"
		expected := []string{"Camel", "Case"}
		result := camelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("camelCase", func(t *testing.T) {
		input := "camelCase"
		expected := []string{"camel", "Case"}
		result := camelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("camelCaseTest", func(t *testing.T) {
		input := "camelCaseTest"
		expected := []string{"camel", "Case", "Test"}
		result := camelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("testCamelCase", func(t *testing.T) {
		input := "testCamelCase"
		expected := []string{"test", "Camel", "Case"}
		result := camelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("nocamel", func(t *testing.T) {
		input := "nocamel"
		expected := []string{"nocamel"}
		result := camelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("camelCASETest", func(t *testing.T) {
		input := "camelCASETest"
		expected := []string{"camel", "CASE", "Test"}
		result := camelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("CAMELCaseTest", func(t *testing.T) {
		input := "CAMELCaseTest"
		expected := []string{"CAMEL", "Case", "Test"}
		result := camelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("snake_case", func(t *testing.T) {
		input := "snake_case"
		expected := []string{"snake", "case"}
		result := camelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("snake_case_test", func(t *testing.T) {
		input := "snake_case_test"
		expected := []string{"snake", "case", "test"}
		result := camelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("snake_camelCaseTest", func(t *testing.T) {
		input := "snake_camelCaseTest"
		expected := []string{"snake", "camel", "Case", "Test"}
		result := camelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("snake_snake_camelCamelCaseTest", func(t *testing.T) {
		input := "snake_snake_camelCamelCaseTest"
		expected := []string{"snake", "snake", "camel", "Camel", "Case", "Test"}
		result := camelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})
}

func TestParseString(t *testing.T) {
	t.Run("Test with special characters", func(t *testing.T) {
		input := "test@123"
		expected := []string{"123", "test"}
		result := parseString(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with empty string", func(t *testing.T) {
		input := ""
		expected := []string{}
		result := parseString(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with long string", func(t *testing.T) {
		input := "This is a long string with multiple words and some special characters !@#$%^&*()"
		expected := []string{"and", "characters", "long", "multiple", "some", "special", "string", "this", "with", "words"}
		result := parseString(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with camel case", func(t *testing.T) {
		input := "CamelCaseString"
		expected := []string{"camel", "camelcasestring", "case", "string"}
		result := parseString(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with mixed case", func(t *testing.T) {
		input := "MixedCASEString"
		expected := []string{"case", "mixed", "mixedcasestring", "string"}
		result := parseString(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with numbers", func(t *testing.T) {
		input := "test123"
		expected := []string{"test123"}
		result := parseString(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with underscores", func(t *testing.T) {
		input := "test_123"
		expected := []string{"123", "test", "test_123"}
		result := parseString(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with hyphens", func(t *testing.T) {
		input := "test-123"
		expected := []string{"123", "test", "test-123"}
		result := parseString(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with special characters", func(t *testing.T) {
		input := "test@123"
		expected := []string{"123", "test"}
		result := parseString(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with long words", func(t *testing.T) {
		input := "a" + strings.Repeat("b", 79)
		expected := []string{"a" + strings.Repeat("b", 79)}
		result := parseString(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with short words", func(t *testing.T) {
		input := "ab"
		expected := []string{}
		result := parseString(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with invalid characters", func(t *testing.T) {
		input := "test@#$%^&*()"
		expected := []string{"test"}
		result := parseString(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with code snippets", func(t *testing.T) {
		input := "if !equalSlices(result, test.expected)"
		expected := []string{"equal", "equalslices", "expected", "result", "slices", "test"}
		result := parseString(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with function signatures", func(t *testing.T) {
		input := "func handleUpdateDocument(w http.ResponseWriter, r *http.Request) {"
		expected := []string{"document", "func", "handle", "handleupdatedocument", "http", "request", "response",
			"responsewriter", "update", "writer"}
		result := parseString(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with CommonResponse", func(t *testing.T) {
		input := "type CommonResponse struct { \nCode int `json:\"code\"` \nMessage string `json:\"message\"` }"
		expected := []string{"code", "common", "commonresponse", "int", "json", "message", "response", "string", "struct", "type"}
		result := parseString(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with Chinese characters", func(t *testing.T) {
		input := "测试中文字符"
		expected := []string{}
		result := parseString(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Test with mixed characters", func(t *testing.T) {
		input := "test123测试中文字符"
		expected := []string{"test123"}
		result := parseString(input)
		assert.Equal(t, expected, result)
	})
}
