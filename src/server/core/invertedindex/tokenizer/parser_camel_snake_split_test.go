package tokenizer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCameSnakeSplit(t *testing.T) {
	// Test cases for camel case splitting
	t.Run("Test with empty string", func(t *testing.T) {
		input := ""
		expected := []string{}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("CamelCase", func(t *testing.T) {
		input := "CamelCase"
		expected := []string{"CamelCase", "Case"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("camelCase", func(t *testing.T) {
		input := "camelCase"
		expected := []string{"camelCase", "Case"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("nocamel", func(t *testing.T) {
		input := "nocamel"
		expected := []string{"nocamel"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("camelCASETest", func(t *testing.T) {
		input := "camelCASETest"
		expected := []string{"camelCASETest", "CASETest", "Test"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("CAMELCaseTest", func(t *testing.T) {
		input := "CAMELCaseTest"
		expected := []string{"CAMELCaseTest", "CaseTest", "Test"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("snake_case", func(t *testing.T) {
		input := "snake_case"
		expected := []string{"snake_case", "case"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("short_end", func(t *testing.T) {
		input := "short_en"
		expected := []string{"short_en"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("snake_ended", func(t *testing.T) {
		input := "short_"
		expected := []string{"short"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("shortEn", func(t *testing.T) {
		input := "shortEn"
		expected := []string{"shortEn"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("UpperCaseEnded", func(t *testing.T) {
		input := "upcaseE"
		expected := []string{"upcaseE"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("snake_case_test", func(t *testing.T) {
		input := "snake_case_test"
		expected := []string{"snake_case_test", "case_test", "test"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("Mixed snake and camel", func(t *testing.T) {
		input := "snake_camelCaseTest"
		expected := []string{"snake_camelCaseTest", "camelCaseTest", "CaseTest", "Test"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("snake_snake_camelCamelCaseTest", func(t *testing.T) {
		input := "snake_snake_camelCamelCaseTest"
		expected := []string{"snake_snake_camelCamelCaseTest", "snake_camelCamelCaseTest", "camelCamelCaseTest", "CamelCaseTest", "CaseTest", "Test"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("IsShowingCollaboratorHoverCard", func(t *testing.T) {
		input := "IsShowingCollaboratorHoverCard"
		expected := []string{"IsShowingCollaboratorHoverCard", "ShowingCollaboratorHoverCard", "CollaboratorHoverCard", "HoverCard", "Card"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("handle-update-document", func(t *testing.T) {
		input := "handle-update-document"
		expected := []string{"handle-update-document", "update-document", "document"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("handle_update_document", func(t *testing.T) {
		input := "handle_update_document"
		expected := []string{"handle_update_document", "update_document", "document"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("beginHandle_Update-Document", func(t *testing.T) {
		input := "beginHandle_Update-Document"
		expected := []string{"beginHandle_Update-Document", "Handle_Update-Document", "Update-Document", "Document"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("beginHANDLEUpdate_document", func(t *testing.T) {
		input := "beginHANDLEUpdate_document"
		expected := []string{"beginHANDLEUpdate_document", "HANDLEUpdate_document", "Update_document", "document"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("AAABbbbCccc_dddd-EEEEf", func(t *testing.T) {
		input := "AAABbbbCccc_dddd-EEEEf"
		expected := []string{"AAABbbbCccc_dddd-EEEEf", "BbbbCccc_dddd-EEEEf", "Cccc_dddd-EEEEf", "dddd-EEEEf", "EEEEf"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("_AAABbbbCccc_dddd", func(t *testing.T) {
		input := "_AAABbbbCccc_dddd"
		expected := []string{"AAABbbbCccc_dddd", "BbbbCccc_dddd", "Cccc_dddd", "dddd"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("abc.def.ghi", func(t *testing.T) {
		input := "abc.def.ghi"
		expected := []string{"abc.def.ghi", "def.ghi", "ghi"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})

	t.Run("abc.DEF.ghi", func(t *testing.T) {
		input := "abc.DEF.ghi"
		expected := []string{"abc.DEF.ghi", "DEF.ghi", "ghi"}
		result := CamelSnakeSplit(input)
		assert.Equal(t, expected, result)
	})
}
