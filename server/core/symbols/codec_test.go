package symbols

import (
	"testing"
	"time"

	"github.com/codetrek/haystack/server/core/storage"
	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// ParseWorkspaceId
// ---------------------------------------------------------------------------

func TestParseWorkspaceId_Valid(t *testing.T) {
	assert.Equal(t, 42, ParseWorkspaceId("42"))
	assert.Equal(t, 0, ParseWorkspaceId("0"))
	assert.Equal(t, 999, ParseWorkspaceId("999"))
}

func TestParseWorkspaceId_Invalid(t *testing.T) {
	assert.Equal(t, InvalidWorkspaceId, ParseWorkspaceId("abc"))
	assert.Equal(t, InvalidWorkspaceId, ParseWorkspaceId(""))
	assert.Equal(t, InvalidWorkspaceId, ParseWorkspaceId("12.5"))
}

// ---------------------------------------------------------------------------
// EncodeDocFunctionsKey / DecodeDocFunctionsKey
// ---------------------------------------------------------------------------

func TestDocFunctionsKey_RoundTrip(t *testing.T) {
	key := EncodeDocFunctionsKey(5, "docABC")
	wsid, docid := DecodeDocFunctionsKey(string(key))
	assert.Equal(t, 5, wsid)
	assert.Equal(t, "docABC", docid)
}

func TestDocFunctionsKey_RoundTrip_LargeId(t *testing.T) {
	key := EncodeDocFunctionsKey(99999, "path/to/file.go")
	wsid, docid := DecodeDocFunctionsKey(string(key))
	assert.Equal(t, 99999, wsid)
	assert.Equal(t, "path/to/file.go", docid)
}

func TestDecodeDocFunctionsKey_WrongType(t *testing.T) {
	wsid, docid := DecodeDocFunctionsKey("X5|docABC")
	assert.Equal(t, InvalidWorkspaceId, wsid)
	assert.Empty(t, docid)
}

func TestDecodeDocFunctionsKey_NoPipe(t *testing.T) {
	key := []byte{KeyTypeSymbolDocFunctions}
	key = append(key, []byte("nopipe")...)
	wsid, docid := DecodeDocFunctionsKey(string(key))
	assert.Equal(t, InvalidWorkspaceId, wsid)
	assert.Empty(t, docid)
}

func TestDecodeDocFunctionsKey_EmptyKey(t *testing.T) {
	wsid, docid := DecodeDocFunctionsKey("")
	assert.Equal(t, InvalidWorkspaceId, wsid)
	assert.Empty(t, docid)
}

// ---------------------------------------------------------------------------
// EncodeSymbolTableKey
// ---------------------------------------------------------------------------

func TestEncodeSymbolTableKey(t *testing.T) {
	key := EncodeSymbolTableKey(10)
	// Verify key starts with correct type byte
	assert.True(t, storage.IsKeyType(string(key), KeyTypeSymbolTable))
}

func TestEncodeSymbolTableKey_DifferentIds(t *testing.T) {
	key1 := EncodeSymbolTableKey(1)
	key2 := EncodeSymbolTableKey(2)
	assert.NotEqual(t, key1, key2, "different workspace IDs should produce different keys")
}

// ---------------------------------------------------------------------------
// EncodeSymbolWordsTableKey
// ---------------------------------------------------------------------------

func TestEncodeSymbolWordsTableKey(t *testing.T) {
	key := EncodeSymbolWordsTableKey(10)
	// Verify key starts with correct type byte
	assert.True(t, storage.IsKeyType(string(key), KeyTypeSymbolWordsTable))
}

func TestEncodeSymbolWordsTableKey_DifferentIds(t *testing.T) {
	key1 := EncodeSymbolWordsTableKey(1)
	key2 := EncodeSymbolWordsTableKey(2)
	assert.NotEqual(t, key1, key2, "different workspace IDs should produce different keys")
}

// ---------------------------------------------------------------------------
// EncodeSymbolTableKey vs EncodeSymbolWordsTableKey
// ---------------------------------------------------------------------------

func TestSymbolTableKeys_DifferentTypes(t *testing.T) {
	symbolKey := EncodeSymbolTableKey(1)
	wordsKey := EncodeSymbolWordsTableKey(1)
	assert.NotEqual(t, symbolKey, wordsKey, "symbol table and words table keys should differ")
}

// ---------------------------------------------------------------------------
// EncodeSymbolTableValue / DecodeSymbolTableValue
// ---------------------------------------------------------------------------

func TestSymbolTableValue_RoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	info := SymbolUniversalTable{
		WorkspaceId: 5,
		InvertedId:  10,
		Desc:        "my workspace",
		CreateAt:    &now,
	}
	encoded := EncodeSymbolTableValue(info)
	decoded, err := DecodeSymbolTableValue(encoded)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, info.WorkspaceId, decoded.WorkspaceId)
	assert.Equal(t, info.InvertedId, decoded.InvertedId)
	assert.Equal(t, info.Desc, decoded.Desc)
	assert.NotNil(t, decoded.CreateAt)
}

func TestSymbolTableValue_RoundTrip_NilTime(t *testing.T) {
	info := SymbolUniversalTable{
		WorkspaceId: 3,
		InvertedId:  7,
		Desc:        "no-time",
		CreateAt:    nil,
	}
	encoded := EncodeSymbolTableValue(info)
	decoded, err := DecodeSymbolTableValue(encoded)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, info.WorkspaceId, decoded.WorkspaceId)
	assert.Equal(t, info.InvertedId, decoded.InvertedId)
	assert.Equal(t, info.Desc, decoded.Desc)
	assert.Nil(t, decoded.CreateAt)
}

func TestDecodeSymbolTableValue_InvalidJSON(t *testing.T) {
	_, err := DecodeSymbolTableValue([]byte("{bad"))
	assert.Error(t, err)
}

func TestDecodeSymbolTableValue_EmptyBytes(t *testing.T) {
	_, err := DecodeSymbolTableValue([]byte{})
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// SplitCamelCase
// ---------------------------------------------------------------------------

func TestSplitCamelCase_Simple(t *testing.T) {
	result := SplitCamelCase("getFunctionName")
	assert.Equal(t, []string{"get", "Function", "Name"}, result)
}

func TestSplitCamelCase_WithNamespace(t *testing.T) {
	result := SplitCamelCase("MyClass::getFunctionName")
	assert.Equal(t, []string{"My", "Class", "get", "Function", "Name"}, result)
}

func TestSplitCamelCase_SingleWord(t *testing.T) {
	result := SplitCamelCase("name")
	assert.Equal(t, []string{"name"}, result)
}

func TestSplitCamelCase_AllUppercase(t *testing.T) {
	result := SplitCamelCase("URL")
	assert.Equal(t, []string{"URL"}, result)
}

func TestSplitCamelCase_WithDigits(t *testing.T) {
	result := SplitCamelCase("get2ndItem")
	assert.Equal(t, []string{"get", "2", "nd", "Item"}, result)
}

func TestSplitCamelCase_SnakeCase(t *testing.T) {
	result := SplitCamelCase("get_function_name")
	assert.Equal(t, []string{"get", "function", "name"}, result)
}

func TestSplitCamelCase_MixedSnakeAndCamel(t *testing.T) {
	result := SplitCamelCase("getFunction_name")
	assert.Equal(t, []string{"get", "Function", "name"}, result)
}

func TestSplitCamelCase_Empty(t *testing.T) {
	result := SplitCamelCase("")
	assert.Empty(t, result)
}

func TestSplitCamelCase_DoubleNamespace(t *testing.T) {
	result := SplitCamelCase("std::vector::pushBack")
	assert.Equal(t, []string{"std", "vector", "push", "Back"}, result)
}

func TestSplitCamelCase_LeadingUpper(t *testing.T) {
	result := SplitCamelCase("HTTPServer")
	assert.Equal(t, []string{"HTTP", "Server"}, result)
}

// ---------------------------------------------------------------------------
// getUniqueFunctionNames (internal helper, same package)
// ---------------------------------------------------------------------------

func TestGetUniqueFunctionNames(t *testing.T) {
	funcs := []Function{
		{Name: "foo", Line: 1},
		{Name: "bar", Line: 2},
		{Name: "foo", Line: 3}, // duplicate name
	}
	names := getUniqueFunctionNames(funcs)
	assert.Equal(t, []string{"foo", "bar"}, names)
}

func TestGetUniqueFunctionNames_Empty(t *testing.T) {
	names := getUniqueFunctionNames([]Function{})
	assert.Empty(t, names)
}

func TestGetUniqueFunctionNames_AllUnique(t *testing.T) {
	funcs := []Function{
		{Name: "alpha", Line: 1},
		{Name: "beta", Line: 2},
		{Name: "gamma", Line: 3},
	}
	names := getUniqueFunctionNames(funcs)
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, names)
}
