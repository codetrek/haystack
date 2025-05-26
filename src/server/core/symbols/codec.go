package symbols

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ai-microsoft/haystack/server/core/storage"
)

const (
	KeyTypeSymbolTable        = storage.KeyTypeSymbol
	KeyTypeSymbolDocFunctions = storage.KeyTypeSymbolDocFunctions
	KeyTypeEmbeddingFuncFlag  = storage.KeyTypeEmbeddingFuncFlag
	KeyTypeSymbolWordsTable   = storage.KeyTypeSymbolWords
	InvalidWorkspaceId        = -1
)

func ParseWorkspaceId(key string) int {
	v, err := strconv.Atoi(key)
	if err != nil {
		return InvalidWorkspaceId
	}

	return v
}

func EncodeDocFunctionsKey(workspaceid int, docid string) []byte {
	return []byte(fmt.Sprintf("%c%d|%s", KeyTypeSymbolDocFunctions, workspaceid, docid))
}

func DecodeDocFunctionsKey(key string) (int, string) {
	if !storage.IsKeyType(key, KeyTypeSymbolDocFunctions) {
		return InvalidWorkspaceId, ""
	}

	key = key[1:]

	parts := strings.SplitN(key, "|", 2)
	if len(parts) != 2 {
		return InvalidWorkspaceId, ""
	}

	return ParseWorkspaceId(parts[0]), parts[1]
}

func EncodeEmbeddingFuncFlagKey(workspaceid int, functionName string, done int) []byte {
	return []byte(fmt.Sprintf("%c%d|%d|%s", KeyTypeEmbeddingFuncFlag, done, workspaceid, functionName))
}

func DecodeEmbeddingFuncFlagKey(key string) (int, int, string) {
	if !storage.IsKeyType(key, KeyTypeEmbeddingFuncFlag) {
		return InvalidWorkspaceId, 0, ""
	}

	key = key[1:]

	parts := strings.SplitN(key, "|", 3)
	done, _ := strconv.Atoi(parts[0])
	if len(parts) != 3 {
		return InvalidWorkspaceId, 0, ""
	}
	workspaceid := ParseWorkspaceId(parts[1])

	return workspaceid, done, parts[2]
}

func EncodeEmbeddingFuncFlagPrefix(done int) []byte {
	return []byte(fmt.Sprintf("%c%d|", KeyTypeEmbeddingFuncFlag, done))
}

func EncodeSymbolTableKey(workspaceid int) []byte {
	return []byte(fmt.Sprintf("%c%d", KeyTypeSymbolTable, workspaceid))
}

func EncodeSymbolWordsTableKey(workspaceid int) []byte {
	return []byte(fmt.Sprintf("%c%d", KeyTypeSymbolWordsTable, workspaceid))
}

func EncodeSymbolTableValue(info SymbolUniversalTable) []byte {
	content, _ := json.Marshal(info)
	return content
}

func DecodeSymbolTableValue(data []byte) (SymbolUniversalTable, error) {
	ft := SymbolUniversalTable{}
	if err := json.Unmarshal(data, &ft); err != nil {
		return SymbolUniversalTable{}, err
	}

	return ft, nil
}
