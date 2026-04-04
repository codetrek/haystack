package symbols

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/codetrek/haystack/server/core/storage"
)

const (
	KeyTypeSymbolTable        = storage.KeyTypeSymbol
	KeyTypeSymbolDocFunctions = storage.KeyTypeSymbolDocFunctions
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
