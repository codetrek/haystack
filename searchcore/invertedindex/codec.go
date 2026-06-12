package invertedindex

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Default on-disk key-type prefix bytes.  These values MUST NOT change after
// data has been written: they are embedded in every inverted-index key.
const (
	DefaultKeyTypeRow    = byte(20)
	DefaultKeyTypeTable  = byte(21)
	DefaultKeyTypeNextId = byte(22)

	InvalidId = -1
)

func isKeyType(key string, keyType byte) bool {
	if len(key) == 0 {
		return false
	}
	return key[0] == keyType
}

func parseId(key string) int {
	v, err := strconv.Atoi(key)
	if err != nil {
		return InvalidId
	}

	return v
}

func (idx *Index) encodeInvertedSearchKey(tableId int, query string) []byte {
	return []byte(fmt.Sprintf("%c%d|%s", idx.keyTypeRow, tableId, query))
}

func (idx *Index) encodeInvertedKeyPrefix(tableId int, keyword string) []byte {
	return []byte(fmt.Sprintf("%c%d|%s|", idx.keyTypeRow, tableId, keyword))
}

func (idx *Index) encodeInvertedKey(tableId int, keyword string, doccount int) []byte {
	return []byte(fmt.Sprintf("%s%d|%d",
		string(idx.encodeInvertedKeyPrefix(tableId, keyword)), doccount, time.Now().UnixMicro()))
}

func (idx *Index) decodeInvertedKey(key string) (int, string, int, string) {
	if !isKeyType(key, idx.keyTypeRow) {
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

func (idx *Index) encodeNextTableIdKey() []byte {
	return []byte(fmt.Sprintf("%c", idx.keyTypeNextId))
}

func (idx *Index) encodeTableKey(tableId int) []byte {
	return []byte(fmt.Sprintf("%c%d", idx.keyTypeTable, tableId))
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
