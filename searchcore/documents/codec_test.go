package documents

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// newTestStore returns a minimal *Store with default key-type bytes for codec tests.
func newTestStore() *Store {
	return &Store{
		keyTypeDocCollection: DefaultKeyTypeDocCollection,
		keyTypeDocWords:      DefaultKeyTypeDocWords,
		keyTypeDocMeta:       DefaultKeyTypeDocMeta,
		keyTypeDocPath:       DefaultKeyTypeDocPath,
	}
}

// ---------------------------------------------------------------------------
// parseCollectionID
// ---------------------------------------------------------------------------

func TestParseCollectionID_Valid(t *testing.T) {
	assert.Equal(t, 42, parseCollectionID("42"))
	assert.Equal(t, 0, parseCollectionID("0"))
}

func TestParseCollectionID_Invalid(t *testing.T) {
	assert.Equal(t, invalidCollectionID, parseCollectionID("abc"))
	assert.Equal(t, invalidCollectionID, parseCollectionID(""))
}

// ---------------------------------------------------------------------------
// encodeDocumentPathKey / decodeDocumentPathKey
// ---------------------------------------------------------------------------

func TestDocumentPathKey_RoundTrip(t *testing.T) {
	s := newTestStore()
	key := s.encodeDocumentPathKey(5, "docABC")
	wsid, docid := s.decodeDocumentPathKey(string(key))
	assert.Equal(t, 5, wsid)
	assert.Equal(t, "docABC", docid)
}

func TestDecodeDocumentPathKey_WrongType(t *testing.T) {
	s := newTestStore()
	// key with wrong type byte
	wsid, docid := s.decodeDocumentPathKey("X5|docABC")
	assert.Equal(t, invalidCollectionID, wsid)
	assert.Empty(t, docid)
}

func TestDecodeDocumentPathKey_NoPipe(t *testing.T) {
	s := newTestStore()
	key := []byte{DefaultKeyTypeDocPath}
	key = append(key, []byte("nopipe")...)
	wsid, docid := s.decodeDocumentPathKey(string(key))
	assert.Equal(t, invalidCollectionID, wsid)
	assert.Empty(t, docid)
}

// ---------------------------------------------------------------------------
// encodeDocumentMetaKey / decodeDocumentMetaKey
// ---------------------------------------------------------------------------

func TestDocumentMetaKey_RoundTrip(t *testing.T) {
	s := newTestStore()
	key := s.encodeDocumentMetaKey(10, "meta123")
	wsid, docid := s.decodeDocumentMetaKey(string(key))
	assert.Equal(t, 10, wsid)
	assert.Equal(t, "meta123", docid)
}

func TestDecodeDocumentMetaKey_WrongType(t *testing.T) {
	s := newTestStore()
	wsid, docid := s.decodeDocumentMetaKey("Z10|meta123")
	assert.Equal(t, invalidCollectionID, wsid)
	assert.Empty(t, docid)
}

func TestDecodeDocumentMetaKey_NoPipe(t *testing.T) {
	s := newTestStore()
	key := []byte{DefaultKeyTypeDocMeta}
	key = append(key, []byte("nopipe")...)
	wsid, docid := s.decodeDocumentMetaKey(string(key))
	assert.Equal(t, invalidCollectionID, wsid)
	assert.Empty(t, docid)
}

// ---------------------------------------------------------------------------
// encodeDocumentMetaValue / decodeDocumentMetaValue
// ---------------------------------------------------------------------------

func TestDocumentMetaValue_RoundTrip(t *testing.T) {
	doc := &Document{
		RelPath:      "src/main.go",
		Size:         999,
		Hash:         "sha256abc",
		ModifiedTime: 12345,
		LastSyncTime: 67890,
	}
	data, err := encodeDocumentMetaValue(doc)
	if !assert.NoError(t, err) {
		return
	}

	decoded, err := decodeDocumentMetaValue(data)
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
	_, err := decodeDocumentMetaValue([]byte("{invalid"))
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// encodeDocumentWordsKey / decodeDocumentWordsKey
// ---------------------------------------------------------------------------

func TestDocumentWordsKey_RoundTrip(t *testing.T) {
	s := newTestStore()
	key := s.encodeDocumentWordsKey(7, "wdoc")
	wsid, docid := s.decodeDocumentWordsKey(string(key))
	assert.Equal(t, 7, wsid)
	assert.Equal(t, "wdoc", docid)
}

func TestDecodeDocumentWordsKey_WrongType(t *testing.T) {
	s := newTestStore()
	wsid, docid := s.decodeDocumentWordsKey("Q7|wdoc")
	assert.Equal(t, invalidCollectionID, wsid)
	assert.Empty(t, docid)
}

func TestDecodeDocumentWordsKey_NoPipe(t *testing.T) {
	s := newTestStore()
	key := []byte{DefaultKeyTypeDocWords}
	key = append(key, []byte("nopipe")...)
	wsid, docid := s.decodeDocumentWordsKey(string(key))
	assert.Equal(t, invalidCollectionID, wsid)
	assert.Empty(t, docid)
}

// ---------------------------------------------------------------------------
// encodeDocumentWordsValue / decodeDocumentWordsValue
// ---------------------------------------------------------------------------

func TestDocumentWordsValue_RoundTrip(t *testing.T) {
	words := []string{"alpha", "beta", "gamma"}
	encoded := encodeDocumentWordsValue(words)
	decoded := decodeDocumentWordsValue(string(encoded))
	assert.Equal(t, words, decoded)
}

func TestDecodeDocumentWordsValue_Empty(t *testing.T) {
	decoded := decodeDocumentWordsValue("")
	assert.Empty(t, decoded)
}

func TestDocumentWordsValue_SingleWord(t *testing.T) {
	words := []string{"single"}
	encoded := encodeDocumentWordsValue(words)
	decoded := decodeDocumentWordsValue(string(encoded))
	assert.Equal(t, words, decoded)
}

// ---------------------------------------------------------------------------
// encodeMetaKey
// ---------------------------------------------------------------------------

func TestMetaKey_TypeByte(t *testing.T) {
	s := newTestStore()
	key := s.encodeMetaKey(3)
	assert.True(t, isKeyType(string(key), DefaultKeyTypeDocCollection))
}

// ---------------------------------------------------------------------------
// encodeFTMetaValue / decodeFTMetaValue
// ---------------------------------------------------------------------------

func TestFTMetaValue_RoundTrip(t *testing.T) {
	ws := CollectionInfo{
		CollectionID: 5,
		InvertedId:   10,
		Desc:         "my collection",
	}
	encoded := encodeFTMetaValue(ws)
	decoded, err := decodeFTMetaValue(encoded)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, ws.CollectionID, decoded.CollectionID)
	assert.Equal(t, ws.InvertedId, decoded.InvertedId)
	assert.Equal(t, ws.Desc, decoded.Desc)
}

func TestDecodeFTMetaValue_InvalidJSON(t *testing.T) {
	_, err := decodeFTMetaValue([]byte("{bad"))
	assert.Error(t, err)
}
