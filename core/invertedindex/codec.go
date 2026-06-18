package invertedindex

import (
	"encoding/json"
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

// appendInvertedKeyPrefix appends "<keyTypeRow><tableId>|<keyword>|" to b. Shared
// by the prefix and full-key encoders; hand-rolled (vs fmt.Sprintf) because it is
// on every Search/GetDocs/write/merge path.
func (idx *Index) appendInvertedKeyPrefix(b []byte, tableId int, keyword string) []byte {
	b = append(b, idx.keyTypeRow)
	b = strconv.AppendInt(b, int64(tableId), 10)
	b = append(b, '|')
	b = append(b, keyword...)
	b = append(b, '|')
	return b
}

func (idx *Index) encodeInvertedSearchKey(tableId int, query string) []byte {
	// "<keyTypeRow><tableId>|<query>" (no trailing '|').
	b := make([]byte, 0, 1+11+1+len(query))
	b = append(b, idx.keyTypeRow)
	b = strconv.AppendInt(b, int64(tableId), 10)
	b = append(b, '|')
	b = append(b, query...)
	return b
}

func (idx *Index) encodeInvertedKeyPrefix(tableId int, keyword string) []byte {
	b := make([]byte, 0, 1+11+1+len(keyword)+1)
	return idx.appendInvertedKeyPrefix(b, tableId, keyword)
}

func (idx *Index) encodeInvertedKey(tableId int, keyword string, doccount int) []byte {
	// "<prefix><doccount>|<tick>" where tick is the current micros.
	b := make([]byte, 0, 1+11+1+len(keyword)+1+11+1+19)
	b = idx.appendInvertedKeyPrefix(b, tableId, keyword)
	b = strconv.AppendInt(b, int64(doccount), 10)
	b = append(b, '|')
	b = strconv.AppendInt(b, time.Now().UnixMicro(), 10)
	return b
}

func (idx *Index) decodeInvertedKey(key string) (int, string, int, string) {
	if !isKeyType(key, idx.keyTypeRow) {
		return InvalidId, "", 0, ""
	}

	key = key[1:]

	// Expect exactly "<tableId>|<keyword>|<doccount>|<tick>" (3 separators, no more).
	// Parse by locating the separators instead of strings.Split, which allocates a
	// []string on every row scanned by the merger.
	i1 := strings.IndexByte(key, '|')
	if i1 < 0 {
		return InvalidId, "", 0, ""
	}
	i2 := strings.IndexByte(key[i1+1:], '|')
	if i2 < 0 {
		return InvalidId, "", 0, ""
	}
	i2 += i1 + 1
	i3 := strings.IndexByte(key[i2+1:], '|')
	if i3 < 0 {
		return InvalidId, "", 0, ""
	}
	i3 += i2 + 1
	if strings.IndexByte(key[i3+1:], '|') >= 0 {
		return InvalidId, "", 0, "" // a 5th part — same rejection as the old len(parts)!=4
	}

	tableId := parseId(key[:i1])
	keyword := key[i1+1 : i2]
	doccount, err := strconv.Atoi(key[i2+1 : i3])
	if err != nil {
		return InvalidId, "", 0, ""
	}
	tick := key[i3+1:]

	return tableId, keyword, doccount, tick
}

func (idx *Index) encodeNextTableIdKey() []byte {
	return []byte{idx.keyTypeNextId}
}

func (idx *Index) encodeTableKey(tableId int) []byte {
	b := make([]byte, 0, 1+11)
	b = append(b, idx.keyTypeTable)
	b = strconv.AppendInt(b, int64(tableId), 10)
	return b
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
	// Each docid is an 8-byte string.
	if len(data) == 0 || len(data)%8 != 0 {
		return []string{}
	}

	const size = 8
	chunks := make([]string, 0, len(data)/size)
	for i := 0; i+size <= len(data); i += size {
		chunks = append(chunks, string(data[i:i+size]))
	}
	return chunks
}

func decodeInvertedValueStr(data string) []string {
	// Each docid is an 8-byte string.
	if len(data) == 0 || len(data)%8 != 0 {
		return []string{}
	}

	const size = 8
	chunks := make([]string, 0, len(data)/size)
	for i := 0; i+size <= len(data); i += size {
		chunks = append(chunks, data[i:i+size])
	}
	return chunks
}
