package documents

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseWorkspaceId(t *testing.T) {
	assert.Equal(t, 42, ParseWorkspaceId("42"))
	assert.Equal(t, 0, ParseWorkspaceId("0"))
	assert.Equal(t, InvalidWorkspaceId, ParseWorkspaceId("bad"))
	assert.Equal(t, InvalidWorkspaceId, ParseWorkspaceId(""))
}

func TestEncodeDecodeDocumentPathKey(t *testing.T) {
	key := EncodeDocumentPathKey(5, "doc123")
	wsId, docId := DecodeDocumentPathKey(string(key))
	assert.Equal(t, 5, wsId)
	assert.Equal(t, "doc123", docId)
}

func TestDecodeDocumentPathKey_Invalid(t *testing.T) {
	wsId, docId := DecodeDocumentPathKey("invalid")
	assert.Equal(t, InvalidWorkspaceId, wsId)
	assert.Equal(t, "", docId)
}

func TestEncodeDecodeDocumentMetaKey(t *testing.T) {
	key := EncodeDocumentMetaKey(3, "meta456")
	wsId, docId := DecodeDocumentMetaKey(string(key))
	assert.Equal(t, 3, wsId)
	assert.Equal(t, "meta456", docId)
}

func TestDecodeDocumentMetaKey_Invalid(t *testing.T) {
	wsId, docId := DecodeDocumentMetaKey("bad")
	assert.Equal(t, InvalidWorkspaceId, wsId)
	assert.Equal(t, "", docId)
}

func TestEncodeDecodeDocumentMetaValue(t *testing.T) {
	doc := &Document{
		RelPath:      "src/main.go",
		Size:         1024,
		Hash:         "abc",
		ModifiedTime: 123456,
		LastSyncTime: 789012,
	}

	data, err := EncodeDocumentMetaValue(doc)
	assert.NoError(t, err)

	decoded, err := DecodeDocumentMetaValue(data)
	assert.NoError(t, err)
	assert.Equal(t, "src/main.go", decoded.RelPath)
	assert.Equal(t, int64(1024), decoded.Size)
	assert.Equal(t, "abc", decoded.Hash)
}

func TestDecodeDocumentMetaValue_Invalid(t *testing.T) {
	_, err := DecodeDocumentMetaValue([]byte("not json"))
	assert.Error(t, err)
}

func TestEncodeDecodeDocumentWordsKey(t *testing.T) {
	key := EncodeDocumentWordsKey(7, "words789")
	wsId, docId := DecodeDocumentWordsKey(string(key))
	assert.Equal(t, 7, wsId)
	assert.Equal(t, "words789", docId)
}

func TestDecodeDocumentWordsKey_Invalid(t *testing.T) {
	wsId, docId := DecodeDocumentWordsKey("bad")
	assert.Equal(t, InvalidWorkspaceId, wsId)
	assert.Equal(t, "", docId)
}

func TestEncodeDecodeDocumentWordsValue(t *testing.T) {
	words := []string{"hello", "world", "test"}
	encoded := EncodeDocumentWordsValue(words)
	decoded := DecodeDocumentWordsValue(string(encoded))
	assert.Equal(t, words, decoded)
}

func TestDecodeDocumentWordsValue_EmptyString(t *testing.T) {
	decoded := DecodeDocumentWordsValue("")
	assert.Equal(t, []string{}, decoded)
}

func TestEncodeDecodeMetaKey(t *testing.T) {
	key := EncodeMetaKey(10)
	wsId := DecodeFTMetaKey(string(key))
	assert.Equal(t, 10, wsId)
}

func TestDecodeFTMetaKey_Invalid(t *testing.T) {
	assert.Equal(t, InvalidWorkspaceId, DecodeFTMetaKey("bad"))
}

func TestEncodeDecodeFTMetaValue(t *testing.T) {
	ws := Workspace{
		WorkspaceId: 42,
		InvertedId:  99,
	}
	encoded := EncodeFTMetaValue(ws)
	decoded, err := DecodeFTMetaValue(encoded)
	assert.NoError(t, err)
	assert.Equal(t, 42, decoded.WorkspaceId)
	assert.Equal(t, 99, decoded.InvertedId)
}

func TestDecodeFTMetaValue_Invalid(t *testing.T) {
	_, err := DecodeFTMetaValue([]byte("bad json"))
	assert.Error(t, err)
}
