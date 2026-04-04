package invertedindex

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"strings"

	"github.com/codetrek/haystack/internal/core/storage"
)

const (
	KeyTypeRow    = storage.KeyTypeInvertedRow
	KeyTypeTable  = storage.KeyTypeInvertedTable
	KeyTypeNextId = storage.KeyTypeInvertedNextTableId

	InvalidId = -1
)

func parseId(key string) int {
	v, err := strconv.Atoi(key)
	if err != nil {
		return InvalidId
	}

	return v
}

func encodeInvertedSearchKey(tableId int, query string) []byte {
	return []byte(fmt.Sprintf("%c%d|%s", KeyTypeRow, tableId, query))
}

func encodeInvertedKeyPrefix(tableId int, keyword string) []byte {
	return []byte(fmt.Sprintf("%c%d|%s|", KeyTypeRow, tableId, keyword))
}

func encodeInvertedKey(tableId int, keyword string, doccount int) []byte {
	return []byte(fmt.Sprintf("%s%d|%d",
		string(encodeInvertedKeyPrefix(tableId, keyword)), doccount, time.Now().UnixMicro()))
}

func decodeInvertedKey(key string) (int, string, int, string) {
	if !storage.IsKeyType(key, KeyTypeRow) {
		return InvalidId, "", 0, ""
	}

	key = key[1:]

	parts := strings.Split(key, "|")
	if len(parts) != 4 {
		return InvalidId, "", 0, ""
	}

	tableId := parseId(parts[0])
	keyword := parts[1]
	doccount, err := strconv.Atoi(parts[2])
	if err != nil {
		return InvalidId, "", 0, ""
	}
	tick := parts[3]

	return tableId, keyword, doccount, tick
}

func encodeNextTableIdKey() []byte {
	return []byte(fmt.Sprintf("%c", KeyTypeNextId))
}

func encodeTableKey(tableId int) []byte {
	return []byte(fmt.Sprintf("%c%d", KeyTypeTable, tableId))
}

func encodeTableValue(info TableInfo) []byte {
	content, _ := json.Marshal(info)
	return content
}

func encodeInvertedValue(docids []string) []byte {
	// Each docid is a 8-byte string
	return []byte(strings.Join(docids, ""))
}

func decodeInvertedValue(data []byte) []string {
	// Each docid is a 8-byte string
	if len(data)%8 != 0 || len(data) == 0 {
		return []string{}
	}

	const size = 8
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

func decodeInvertedValueStr(data string) []string {
	// Each docid is a 8-byte string
	if len(data)%8 != 0 || len(data) == 0 {
		return []string{}
	}

	const size = 8
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
