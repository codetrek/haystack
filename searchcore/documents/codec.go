package documents

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Default on-disk key-type prefix bytes. These values MUST NOT change after
// data has been written: they are embedded in every key stored on disk.
//
// Byte 0 (NUL) is reserved as the zero-value sentinel meaning "use default";
// it cannot be selected as a custom prefix.
const (
	DefaultKeyTypeDocCollection = byte(10)
	DefaultKeyTypeDocWords      = byte(11)
	DefaultKeyTypeDocMeta       = byte(12)
	DefaultKeyTypeDocPath       = byte(13)

	// invalidCollectionID is returned by parse/decode functions when parsing fails.
	invalidCollectionID = -1
)

// isKeyType reports whether key starts with the given keyType byte.
func isKeyType(key string, keyType byte) bool {
	if len(key) == 0 {
		return false
	}
	return key[0] == keyType
}

// parseCollectionID parses a decimal collection ID string.
// Returns invalidCollectionID if the string is not a valid integer.
func parseCollectionID(key string) int {
	v, err := strconv.Atoi(key)
	if err != nil {
		return invalidCollectionID
	}
	return v
}

// encodeDocumentPathKey encodes the key for a document path entry.
func (s *Store) encodeDocumentPathKey(collectionID int, docid string) []byte {
	return []byte(fmt.Sprintf("%c%d|%s", s.keyTypeDocPath, collectionID, docid))
}

// decodeDocumentPathKey decodes a document path key, returning (collectionID, docid).
// Returns (invalidCollectionID, "") if the key is malformed or has the wrong type byte.
func (s *Store) decodeDocumentPathKey(key string) (int, string) {
	if !isKeyType(key, s.keyTypeDocPath) {
		return invalidCollectionID, ""
	}
	key = key[1:]
	parts := strings.SplitN(key, "|", 2)
	if len(parts) != 2 {
		return invalidCollectionID, ""
	}
	return parseCollectionID(parts[0]), parts[1]
}

// encodeDocumentMetaKey encodes the key for a document metadata entry.
func (s *Store) encodeDocumentMetaKey(collectionID int, docid string) []byte {
	return []byte(fmt.Sprintf("%c%d|%s", s.keyTypeDocMeta, collectionID, docid))
}

// decodeDocumentMetaKey decodes a document metadata key, returning (collectionID, docid).
// Returns (invalidCollectionID, "") if the key is malformed or has the wrong type byte.
func (s *Store) decodeDocumentMetaKey(key string) (int, string) {
	if !isKeyType(key, s.keyTypeDocMeta) {
		return invalidCollectionID, ""
	}
	key = key[1:]
	parts := strings.SplitN(key, "|", 2)
	if len(parts) != 2 {
		return invalidCollectionID, ""
	}
	return parseCollectionID(parts[0]), parts[1]
}

// encodeDocumentMetaValue serialises a Document as JSON.
func encodeDocumentMetaValue(doc *Document) ([]byte, error) {
	return json.Marshal(doc)
}

// decodeDocumentMetaValue deserialises a Document from JSON.
func decodeDocumentMetaValue(data []byte) (*Document, error) {
	doc := Document{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// encodeDocumentWordsKey encodes the key for a document words entry.
func (s *Store) encodeDocumentWordsKey(collectionID int, docid string) []byte {
	return []byte(fmt.Sprintf("%c%d|%s", s.keyTypeDocWords, collectionID, docid))
}

// decodeDocumentWordsKey decodes a document words key, returning (collectionID, docid).
// Returns (invalidCollectionID, "") if the key is malformed or has the wrong type byte.
func (s *Store) decodeDocumentWordsKey(key string) (int, string) {
	if !isKeyType(key, s.keyTypeDocWords) {
		return invalidCollectionID, ""
	}
	key = key[1:]
	parts := strings.SplitN(key, "|", 2)
	if len(parts) != 2 {
		return invalidCollectionID, ""
	}
	return parseCollectionID(parts[0]), parts[1]
}

// encodeDocumentWordsValue encodes a slice of keywords as a pipe-separated byte slice.
func encodeDocumentWordsValue(keywords []string) []byte {
	return []byte(strings.Join(keywords, "|"))
}

// decodeDocumentWordsValue decodes a pipe-separated keywords string.
func decodeDocumentWordsValue(data string) []string {
	if len(data) == 0 {
		return []string{}
	}
	return strings.Split(data, "|")
}

// encodeMetaKey encodes the key for a collection metadata entry.
func (s *Store) encodeMetaKey(collectionID int) []byte {
	return []byte(fmt.Sprintf("%c%d", s.keyTypeDocCollection, collectionID))
}

// encodeFTMetaValue serialises a collection record as JSON.
// CollectionInfo has only JSON-safe fields, so Marshal cannot fail.
func encodeFTMetaValue(info CollectionInfo) []byte {
	content, _ := json.Marshal(info)
	return content
}

// decodeFTMetaValue deserialises a collection record from JSON.
func decodeFTMetaValue(data []byte) (*CollectionInfo, error) {
	ft := CollectionInfo{}
	if err := json.Unmarshal(data, &ft); err != nil {
		return &CollectionInfo{}, err
	}
	return &ft, nil
}
