package documents

import (
	"encoding/json"
	"fmt"
	"strconv"

	"strings"

	"github.com/codetrek/haystack/internal/core/storage"
)

const (
	KeyTypeDocPath      = storage.KeyTypeDocPath
	KeyTypeDocMeta      = storage.KeyTypeDocMeta
	KeyTypeDocWords     = storage.KeyTypeDocWords
	KeyTypeDocWorkspace = storage.KeyTypeDocWorkspace

	InvalidWorkspaceId = -1
)

func ParseWorkspaceId(key string) int {
	v, err := strconv.Atoi(key)
	if err != nil {
		return InvalidWorkspaceId
	}

	return v
}

func EncodeDocumentPathKey(workspaceid int, docid string) []byte {
	return []byte(fmt.Sprintf("%c%d|%s", KeyTypeDocPath, workspaceid, docid))
}

func DecodeDocumentPathKey(key string) (int, string) {
	if !storage.IsKeyType(key, KeyTypeDocPath) {
		return InvalidWorkspaceId, ""
	}

	key = key[1:]

	parts := strings.SplitN(key, "|", 2)
	if len(parts) != 2 {
		return InvalidWorkspaceId, ""
	}

	return ParseWorkspaceId(parts[0]), parts[1]
}

func EncodeDocumentMetaKey(workspaceid int, docid string) []byte {
	return []byte(fmt.Sprintf("%c%d|%s", KeyTypeDocMeta, workspaceid, docid))
}

func DecodeDocumentMetaKey(key string) (int, string) {
	if !storage.IsKeyType(key, KeyTypeDocMeta) {
		return InvalidWorkspaceId, ""
	}

	key = key[1:]

	parts := strings.SplitN(key, "|", 2)
	if len(parts) != 2 {
		return InvalidWorkspaceId, ""
	}

	return ParseWorkspaceId(parts[0]), parts[1]
}

func EncodeDocumentMetaValue(doc *Document) ([]byte, error) {
	return json.Marshal(doc)
}

func DecodeDocumentMetaValue(data []byte) (*Document, error) {
	doc := Document{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}

	return &doc, nil
}

func EncodeDocumentWordsKey(workspaceid int, docid string) []byte {
	return []byte(fmt.Sprintf("%c%d|%s", KeyTypeDocWords, workspaceid, docid))
}

func DecodeDocumentWordsKey(key string) (int, string) {
	if !storage.IsKeyType(key, KeyTypeDocWords) {
		return InvalidWorkspaceId, ""
	}

	key = key[1:]

	parts := strings.SplitN(key, "|", 2)
	if len(parts) != 2 {
		return InvalidWorkspaceId, ""
	}

	workspaceid := ParseWorkspaceId(parts[0])
	docid := parts[1]

	return workspaceid, docid
}

func EncodeDocumentWordsValue(keywords []string) []byte {
	return []byte(strings.Join(keywords, "|"))
}

func DecodeDocumentWordsValue(data string) []string {
	if len(data) == 0 {
		return []string{}
	}

	return strings.Split(data, "|")
}

func EncodeMetaKey(workspaceid int) []byte {
	return []byte(fmt.Sprintf("%c%d", KeyTypeDocWorkspace, workspaceid))
}

func DecodeFTMetaKey(key string) int {
	if !storage.IsKeyType(key, KeyTypeDocWorkspace) {
		return InvalidWorkspaceId
	}

	key = key[1:]

	workspaceid, err := strconv.Atoi(key)
	if err != nil {
		return InvalidWorkspaceId
	}

	return workspaceid
}

func EncodeFTMetaValue(info Workspace) []byte {
	content, _ := json.Marshal(info)
	return content
}

func DecodeFTMetaValue(data []byte) (*Workspace, error) {
	ft := Workspace{}
	if err := json.Unmarshal(data, &ft); err != nil {
		return &Workspace{}, err
	}

	return &ft, nil
}
