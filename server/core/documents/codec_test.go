package documents

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// ParseWorkspaceId
// ---------------------------------------------------------------------------

func TestParseWorkspaceId_Valid(t *testing.T) {
	assert.Equal(t, 42, ParseWorkspaceId("42"))
	assert.Equal(t, 0, ParseWorkspaceId("0"))
}

func TestParseWorkspaceId_Invalid(t *testing.T) {
	assert.Equal(t, InvalidWorkspaceId, ParseWorkspaceId("abc"))
	assert.Equal(t, InvalidWorkspaceId, ParseWorkspaceId(""))
}

// ---------------------------------------------------------------------------
// EncodeDocumentPathKey / DecodeDocumentPathKey
// ---------------------------------------------------------------------------

func TestDocumentPathKey_RoundTrip(t *testing.T) {
	key := EncodeDocumentPathKey(5, "docABC")
	wsid, docid := DecodeDocumentPathKey(string(key))
	assert.Equal(t, 5, wsid)
	assert.Equal(t, "docABC", docid)
}

func TestDecodeDocumentPathKey_WrongType(t *testing.T) {
	// key with wrong type byte
	wsid, docid := DecodeDocumentPathKey("X5|docABC")
	assert.Equal(t, InvalidWorkspaceId, wsid)
	assert.Empty(t, docid)
}

func TestDecodeDocumentPathKey_NoPipe(t *testing.T) {
	key := []byte{KeyTypeDocPath}
	key = append(key, []byte("nopipe")...)
	wsid, docid := DecodeDocumentPathKey(string(key))
	assert.Equal(t, InvalidWorkspaceId, wsid)
	assert.Empty(t, docid)
}

// ---------------------------------------------------------------------------
// EncodeDocumentMetaKey / DecodeDocumentMetaKey
// ---------------------------------------------------------------------------

func TestDocumentMetaKey_RoundTrip(t *testing.T) {
	key := EncodeDocumentMetaKey(10, "meta123")
	wsid, docid := DecodeDocumentMetaKey(string(key))
	assert.Equal(t, 10, wsid)
	assert.Equal(t, "meta123", docid)
}

func TestDecodeDocumentMetaKey_WrongType(t *testing.T) {
	wsid, docid := DecodeDocumentMetaKey("Z10|meta123")
	assert.Equal(t, InvalidWorkspaceId, wsid)
	assert.Empty(t, docid)
}

func TestDecodeDocumentMetaKey_NoPipe(t *testing.T) {
	key := []byte{KeyTypeDocMeta}
	key = append(key, []byte("nopipe")...)
	wsid, docid := DecodeDocumentMetaKey(string(key))
	assert.Equal(t, InvalidWorkspaceId, wsid)
	assert.Empty(t, docid)
}

// ---------------------------------------------------------------------------
// EncodeDocumentMetaValue / DecodeDocumentMetaValue
// ---------------------------------------------------------------------------

func TestDocumentMetaValue_RoundTrip(t *testing.T) {
	doc := &Document{
		RelPath:      "src/main.go",
		Size:         999,
		Hash:         "sha256abc",
		ModifiedTime: 12345,
		LastSyncTime: 67890,
	}
	data, err := EncodeDocumentMetaValue(doc)
	if !assert.NoError(t, err) {
		return
	}

	decoded, err := DecodeDocumentMetaValue(data)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, doc.RelPath, decoded.RelPath)
	assert.Equal(t, doc.Size, decoded.Size)
	assert.Equal(t, doc.Hash, decoded.Hash)
	assert.Equal(t, doc.ModifiedTime, decoded.ModifiedTime)
	assert.Equal(t, doc.LastSyncTime, decoded.LastSyncTime)
}

func TestDecodeDocumentMetaValue_InvalidJSON(t *testing.T) {
	_, err := DecodeDocumentMetaValue([]byte("{invalid"))
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// EncodeDocumentWordsKey / DecodeDocumentWordsKey
// ---------------------------------------------------------------------------

func TestDocumentWordsKey_RoundTrip(t *testing.T) {
	key := EncodeDocumentWordsKey(7, "wdoc")
	wsid, docid := DecodeDocumentWordsKey(string(key))
	assert.Equal(t, 7, wsid)
	assert.Equal(t, "wdoc", docid)
}

func TestDecodeDocumentWordsKey_WrongType(t *testing.T) {
	wsid, docid := DecodeDocumentWordsKey("Q7|wdoc")
	assert.Equal(t, InvalidWorkspaceId, wsid)
	assert.Empty(t, docid)
}

func TestDecodeDocumentWordsKey_NoPipe(t *testing.T) {
	key := []byte{KeyTypeDocWords}
	key = append(key, []byte("nopipe")...)
	wsid, docid := DecodeDocumentWordsKey(string(key))
	assert.Equal(t, InvalidWorkspaceId, wsid)
	assert.Empty(t, docid)
}

// ---------------------------------------------------------------------------
// EncodeDocumentWordsValue / DecodeDocumentWordsValue
// ---------------------------------------------------------------------------

func TestDocumentWordsValue_RoundTrip(t *testing.T) {
	words := []string{"alpha", "beta", "gamma"}
	encoded := EncodeDocumentWordsValue(words)
	decoded := DecodeDocumentWordsValue(string(encoded))
	assert.Equal(t, words, decoded)
}

func TestDecodeDocumentWordsValue_Empty(t *testing.T) {
	decoded := DecodeDocumentWordsValue("")
	assert.Empty(t, decoded)
}

func TestDocumentWordsValue_SingleWord(t *testing.T) {
	words := []string{"single"}
	encoded := EncodeDocumentWordsValue(words)
	decoded := DecodeDocumentWordsValue(string(encoded))
	assert.Equal(t, words, decoded)
}

// ---------------------------------------------------------------------------
// EncodeMetaKey / DecodeFTMetaKey
// ---------------------------------------------------------------------------

func TestFTMetaKey_RoundTrip(t *testing.T) {
	key := EncodeMetaKey(3)
	wsid := DecodeFTMetaKey(string(key))
	assert.Equal(t, 3, wsid)
}

func TestDecodeFTMetaKey_WrongType(t *testing.T) {
	wsid := DecodeFTMetaKey("Z3")
	assert.Equal(t, InvalidWorkspaceId, wsid)
}

func TestDecodeFTMetaKey_InvalidNumber(t *testing.T) {
	key := []byte{KeyTypeDocWorkspace}
	key = append(key, []byte("abc")...)
	wsid := DecodeFTMetaKey(string(key))
	assert.Equal(t, InvalidWorkspaceId, wsid)
}

func TestDecodeFTMetaKey_EmptyKey(t *testing.T) {
	wsid := DecodeFTMetaKey("")
	assert.Equal(t, InvalidWorkspaceId, wsid)
}

// ---------------------------------------------------------------------------
// EncodeFTMetaValue / DecodeFTMetaValue
// ---------------------------------------------------------------------------

func TestFTMetaValue_RoundTrip(t *testing.T) {
	ws := Workspace{
		WorkspaceId: 5,
		InvertedId:  10,
		Desc:        "my workspace",
	}
	encoded := EncodeFTMetaValue(ws)
	decoded, err := DecodeFTMetaValue(encoded)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, ws.WorkspaceId, decoded.WorkspaceId)
	assert.Equal(t, ws.InvertedId, decoded.InvertedId)
	assert.Equal(t, ws.Desc, decoded.Desc)
}

func TestDecodeFTMetaValue_InvalidJSON(t *testing.T) {
	_, err := DecodeFTMetaValue([]byte("{bad"))
	assert.Error(t, err)
}
