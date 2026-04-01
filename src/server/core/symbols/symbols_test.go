package symbols

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSplitCamelCase(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"handleUpdateDocument", []string{"handle", "Update", "Document"}},
		{"SplitCamelCase", []string{"Split", "Camel", "Case"}},
		{"simple", []string{"simple"}},
		{"ABC", []string{"ABC"}},
		{"parseJSON", []string{"parse", "JSON"}},
		{"std::vector", []string{"std", "vector"}},
		{"my::ns::MyClass", []string{"my", "ns", "My", "Class"}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := SplitCamelCase(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSplitCamelCasePart(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"handleUpdate", []string{"handle", "Update"}},
		{"HTTPServer", []string{"HTTP", "Server"}},
		{"getHTTPResponse", []string{"get", "HTTP", "Response"}},
		{"simple", []string{"simple"}},
		{"ABC", []string{"ABC"}},
		{"snake_case", []string{"snake", "case"}},
		{"num123value", []string{"num", "123", "value"}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := splitCamelCasePart(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetUniqueFunctionNames(t *testing.T) {
	funcs := []Function{
		{Name: "foo", Line: 1},
		{Name: "bar", Line: 5},
		{Name: "foo", Line: 10},
		{Name: "baz", Line: 15},
	}

	names := getUniqueFunctionNames(funcs)
	assert.Contains(t, names, "foo")
	assert.Contains(t, names, "bar")
	assert.Contains(t, names, "baz")
	assert.Len(t, names, 3)
}

func TestGetUniqueFunctionNames_Empty(t *testing.T) {
	names := getUniqueFunctionNames([]Function{})
	assert.Nil(t, names)
}

func TestEncodeDocFunctionsKey(t *testing.T) {
	key := EncodeDocFunctionsKey(1, "abc123")
	assert.NotEmpty(t, key)
}

func TestEncodeDecodeSymbolTableKey(t *testing.T) {
	key := EncodeSymbolTableKey(42)
	assert.NotEmpty(t, key)
}

func TestEncodeSymbolWordsTableKey(t *testing.T) {
	key := EncodeSymbolWordsTableKey(42)
	assert.NotEmpty(t, key)
}

func TestEncodeDecodeSymbolTableValue(t *testing.T) {
	now := time.Now()
	info := SymbolUniversalTable{
		WorkspaceId: 42,
		InvertedId:  99,
		Desc:        "test",
		CreateAt:    &now,
	}
	val := EncodeSymbolTableValue(info)
	decoded, err := DecodeSymbolTableValue(val)
	assert.NoError(t, err)
	assert.Equal(t, 42, decoded.WorkspaceId)
	assert.Equal(t, 99, decoded.InvertedId)
}

func TestParseWorkspaceId(t *testing.T) {
	assert.Equal(t, 7, ParseWorkspaceId("7"))
	assert.Equal(t, InvalidWorkspaceId, ParseWorkspaceId("notanumber"))
	assert.Equal(t, 0, ParseWorkspaceId("0"))
}

func TestDecodeDocFunctionsKey(t *testing.T) {
	key := EncodeDocFunctionsKey(7, "docid")
	wsId, docId := DecodeDocFunctionsKey(string(key))
	assert.Equal(t, 7, wsId)
	assert.Equal(t, "docid", docId)
}

func TestDecodeDocFunctionsKey_Invalid(t *testing.T) {
	wsId, docId := DecodeDocFunctionsKey("invalid")
	assert.Equal(t, InvalidWorkspaceId, wsId)
	assert.Equal(t, "", docId)
}
