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
	DefaultKeyTypeDocWorkspace = byte(10)
	DefaultKeyTypeDocWords     = byte(11)
	DefaultKeyTypeDocMeta      = byte(12)
	DefaultKeyTypeDocPath      = byte(13)

	// InvalidWorkspaceId is returned by parse/decode functions when parsing fails.
	InvalidWorkspaceId = -1
)

// isKeyType reports whether key starts with the given keyType byte.
func isKeyType(key string, keyType byte) bool {
	if len(key) == 0 {
		return false
	}
	return key[0] == keyType
}

// ParseWorkspaceId parses a decimal workspace ID string.
// Returns InvalidWorkspaceId if the string is not a valid integer.
func ParseWorkspaceId(key string) int {
	v, err := strconv.Atoi(key)
	if err != nil {
		return InvalidWorkspaceId
	}
	return v
}

// encodeDocumentPathKey encodes the key for a document path entry.
func (s *Store) encodeDocumentPathKey(workspaceid int, docid string) []byte {
	return []byte(fmt.Sprintf("%c%d|%s", s.keyTypeDocPath, workspaceid, docid))
}

// decodeDocumentPathKey decodes a document path key, returning (workspaceId, docid).
// Returns (InvalidWorkspaceId, "") if the key is malformed or has the wrong type byte.
func (s *Store) decodeDocumentPathKey(key string) (int, string) {
	if !isKeyType(key, s.keyTypeDocPath) {
		return InvalidWorkspaceId, ""
	}
	key = key[1:]
	parts := strings.SplitN(key, "|", 2)
	if len(parts) != 2 {
		return InvalidWorkspaceId, ""
	}
	return ParseWorkspaceId(parts[0]), parts[1]
}

// encodeDocumentMetaKey encodes the key for a document metadata entry.
func (s *Store) encodeDocumentMetaKey(workspaceid int, docid string) []byte {
	return []byte(fmt.Sprintf("%c%d|%s", s.keyTypeDocMeta, workspaceid, docid))
}

// decodeDocumentMetaKey decodes a document metadata key, returning (workspaceId, docid).
// Returns (InvalidWorkspaceId, "") if the key is malformed or has the wrong type byte.
func (s *Store) decodeDocumentMetaKey(key string) (int, string) {
	if !isKeyType(key, s.keyTypeDocMeta) {
		return InvalidWorkspaceId, ""
	}
	key = key[1:]
	parts := strings.SplitN(key, "|", 2)
	if len(parts) != 2 {
		return InvalidWorkspaceId, ""
	}
	return ParseWorkspaceId(parts[0]), parts[1]
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
func (s *Store) encodeDocumentWordsKey(workspaceid int, docid string) []byte {
	return []byte(fmt.Sprintf("%c%d|%s", s.keyTypeDocWords, workspaceid, docid))
}

// decodeDocumentWordsKey decodes a document words key, returning (workspaceId, docid).
// Returns (InvalidWorkspaceId, "") if the key is malformed or has the wrong type byte.
func (s *Store) decodeDocumentWordsKey(key string) (int, string) {
	if !isKeyType(key, s.keyTypeDocWords) {
		return InvalidWorkspaceId, ""
	}
	key = key[1:]
	parts := strings.SplitN(key, "|", 2)
	if len(parts) != 2 {
		return InvalidWorkspaceId, ""
	}
	return ParseWorkspaceId(parts[0]), parts[1]
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

// encodeMetaKey encodes the key for a workspace metadata entry.
func (s *Store) encodeMetaKey(workspaceid int) []byte {
	return []byte(fmt.Sprintf("%c%d", s.keyTypeDocWorkspace, workspaceid))
}

// decodeFTMetaKey decodes a workspace metadata key, returning the workspace ID.
// Returns InvalidWorkspaceId if the key is malformed or has the wrong type byte.
func (s *Store) decodeFTMetaKey(key string) int {
	if !isKeyType(key, s.keyTypeDocWorkspace) {
		return InvalidWorkspaceId
	}
	key = key[1:]
	workspaceid, err := strconv.Atoi(key)
	if err != nil {
		return InvalidWorkspaceId
	}
	return workspaceid
}

// encodeFTMetaValue serialises a workspace record as JSON.
func encodeFTMetaValue(info workspace) []byte {
	content, _ := json.Marshal(info)
	return content
}

// decodeFTMetaValue deserialises a workspace record from JSON.
func decodeFTMetaValue(data []byte) (*workspace, error) {
	ft := workspace{}
	if err := json.Unmarshal(data, &ft); err != nil {
		return &workspace{}, err
	}
	return &ft, nil
}
