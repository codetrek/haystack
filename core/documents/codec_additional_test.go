package documents

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseCollectionID(t *testing.T) {
	assert.Equal(t, 42, parseCollectionID("42"))
	assert.Equal(t, 0, parseCollectionID("0"))
	assert.Equal(t, invalidCollectionID, parseCollectionID("bad"))
	assert.Equal(t, invalidCollectionID, parseCollectionID(""))
}

func TestEncodeDecodeDocumentPathKey(t *testing.T) {
	s := newTestStore()
	key := s.encodeDocumentPathKey(5, "doc123")
	wsId, docId := s.decodeDocumentPathKey(string(key))
	assert.Equal(t, 5, wsId)
	assert.Equal(t, "doc123", docId)
}

func TestDecodeDocumentPathKey_Invalid(t *testing.T) {
	s := newTestStore()
	wsId, docId := s.decodeDocumentPathKey("invalid")
	assert.Equal(t, invalidCollectionID, wsId)
	assert.Equal(t, "", docId)
}

func TestEncodeDecodeDocumentMetaKey(t *testing.T) {
	s := newTestStore()
	key := s.encodeDocumentMetaKey(3, "meta456")
	wsId, docId := s.decodeDocumentMetaKey(string(key))
	assert.Equal(t, 3, wsId)
	assert.Equal(t, "meta456", docId)
}

func TestDecodeDocumentMetaKey_Invalid(t *testing.T) {
	s := newTestStore()
	wsId, docId := s.decodeDocumentMetaKey("bad")
	assert.Equal(t, invalidCollectionID, wsId)
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

	data, err := encodeDocumentMetaValue(doc)
	assert.NoError(t, err)

	decoded, err := decodeDocumentMetaValue(data)
	assert.NoError(t, err)
	assert.Equal(t, "src/main.go", decoded.RelPath)
	assert.Equal(t, int64(1024), decoded.Size)
	assert.Equal(t, "abc", decoded.Hash)
}

func TestDecodeDocumentMetaValue_Invalid(t *testing.T) {
	_, err := decodeDocumentMetaValue([]byte("not json"))
	assert.Error(t, err)
}

func TestEncodeDecodeDocumentWordsKey(t *testing.T) {
	s := newTestStore()
	key := s.encodeDocumentWordsKey(7, "words789")
	wsId, docId := s.decodeDocumentWordsKey(string(key))
	assert.Equal(t, 7, wsId)
	assert.Equal(t, "words789", docId)
}

func TestDecodeDocumentWordsKey_Invalid(t *testing.T) {
	s := newTestStore()
	wsId, docId := s.decodeDocumentWordsKey("bad")
	assert.Equal(t, invalidCollectionID, wsId)
	assert.Equal(t, "", docId)
}

func TestEncodeDecodeDocumentWordsValue(t *testing.T) {
	words := []string{"hello", "world", "test"}
	encoded := encodeDocumentWordsValue(words)
	decoded := decodeDocumentWordsValue(string(encoded))
	assert.Equal(t, words, decoded)
}

func TestDecodeDocumentWordsValue_EmptyString(t *testing.T) {
	decoded := decodeDocumentWordsValue("")
	assert.Equal(t, []string{}, decoded)
}

func TestEncodeMetaKey(t *testing.T) {
	s := newTestStore()
	key := s.encodeMetaKey(10)
	assert.True(t, isKeyType(string(key), DefaultKeyTypeDocCollection))
}

func TestEncodeDecodeFTMetaValue(t *testing.T) {
	ws := CollectionInfo{
		CollectionID: 42,
		InvertedId:   99,
	}
	encoded := encodeFTMetaValue(ws)
	decoded, err := decodeFTMetaValue(encoded)
	assert.NoError(t, err)
	assert.Equal(t, 42, decoded.CollectionID)
	assert.Equal(t, 99, decoded.InvertedId)
}

func TestDecodeFTMetaValue_Invalid(t *testing.T) {
	_, err := decodeFTMetaValue([]byte("bad json"))
	assert.Error(t, err)
}
