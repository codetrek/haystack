package fulltext

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"strings"

	"github.com/ai-microsoft/haystack/server/core/storage"
)

const (
	KeyTypeDocPath  = storage.KeyTypeDocPath
	KeyTypeDocMeta  = storage.KeyTypeDocMeta
	KeyTypeDocWords = storage.KeyTypeDocWords
	KeyTypeKeyword  = storage.KeyTypeKeyword

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

func EncodeInvertedSearchKey(workspaceid int, query string) []byte {
	return []byte(fmt.Sprintf("%c%d|%s", KeyTypeKeyword, workspaceid, query))
}

func EncodeInvertedKeyPrefix(workspaceid int, keyword string) []byte {
	return []byte(fmt.Sprintf("%c%d|%s|", KeyTypeKeyword, workspaceid, keyword))
}

func EncodeInvertedKey(workspaceid int, keyword string, doccount int) []byte {
	return []byte(fmt.Sprintf("%s%d|%d",
		string(EncodeInvertedKeyPrefix(workspaceid, keyword)), doccount, time.Now().UnixMicro()))
}

func DecodeInvertedKey(key string) (int, string, int, string) {
	if !storage.IsKeyType(key, KeyTypeKeyword) {
		return InvalidWorkspaceId, "", 0, ""
	}

	key = key[1:]

	parts := strings.Split(key, "|")
	if len(parts) != 4 {
		return InvalidWorkspaceId, "", 0, ""
	}

	workspaceid := ParseWorkspaceId(parts[0])
	keyword := parts[1]
	doccount, err := strconv.Atoi(parts[2])
	if err != nil {
		return InvalidWorkspaceId, "", 0, ""
	}
	tick := parts[3]

	return workspaceid, keyword, doccount, tick
}

func EncodeInvertedValue(docids []string) []byte {
	// Each docid is a 16-byte string
	return []byte(strings.Join(docids, ""))
}

func DecodeInvertedValue(data []byte) []string {
	// Each docid is a 16-byte string
	if len(data)%16 != 0 || len(data) == 0 {
		return []string{}
	}

	const size = 16
	var chunks []string
	for i := 0; i < len(data); i += size {
		end := i + size
		if end > len(data) {
			end = len(data)
		}
		chunks = append(chunks, string(data[i:end]))
	}
	return chunks
}

func DecodeInvertedValueStr(data string) []string {
	// Each docid is a 16-byte string
	if len(data)%16 != 0 || len(data) == 0 {
		return []string{}
	}

	const size = 16
	var chunks []string
	for i := 0; i < len(data); i += size {
		end := i + size
		if end > len(data) {
			end = len(data)
		}
		chunks = append(chunks, data[i:end])
	}

	return chunks
}
