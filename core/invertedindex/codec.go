package invertedindex

import (
	"encoding/binary"
	"encoding/json"
	"slices"
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

	// DefaultKeyTypeForward is the on-disk key-type prefix byte for the
	// per-document forward map (docid -> keyword set) that the index owns. Byte
	// 11 is reused from the documents store's retired doc-words key: the inverted
	// index lives in a physically separate `index` db where 11 is free (the index
	// uses 20/21/22), so there is no collision with the data db's keys.
	DefaultKeyTypeForward = byte(11)

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
	// "<prefix><doccount>|<tick>.<seq>" where tick is the current micros and seq
	// is a per-Index monotonic counter. tick alone is not unique within a single
	// microsecond, so two rows of the same (tableId,keyword,doccount) could get
	// byte-identical keys and overwrite each other; the seq suffix makes every
	// encoded key unique. decode treats everything after the last '|' as the
	// opaque tick, so this does not change the on-disk format contract.
	b := make([]byte, 0, 1+11+1+len(keyword)+1+11+1+19+1+20)
	b = idx.appendInvertedKeyPrefix(b, tableId, keyword)
	b = strconv.AppendInt(b, int64(doccount), 10)
	b = append(b, '|')
	b = strconv.AppendInt(b, time.Now().UnixMicro(), 10)
	b = append(b, '.')
	b = strconv.AppendUint(b, idx.keySeq.Add(1), 10)
	return b
}

func (idx *Index) decodeInvertedKey(key string) (int, string, int, string) {
	if !isKeyType(key, idx.keyTypeRow) {
		return InvalidId, "", 0, ""
	}

	key = key[1:]

	// Key layout: "<tableId>|<keyword>|<doccount>|<tick>". tableId, doccount and
	// tick are numeric and never contain the '|' delimiter; the keyword is
	// arbitrary indexed text that CAN contain '|'. So anchor on the FIRST '|'
	// (end of tableId) and the LAST TWO '|' (start of doccount and tick) and take
	// everything in between as the keyword verbatim. A "exactly 3 separators"
	// parse instead rejects '|'-keywords (returns InvalidId), which makes the
	// background merger regroup them under an empty keyword and rewrite their data
	// under a garbage key — orphaning the postings permanently. Located via
	// IndexByte (no allocation), keeping the merger's hot path alloc-free.
	first := strings.IndexByte(key, '|')
	last := strings.LastIndexByte(key, '|')
	if first < 0 || last <= first {
		return InvalidId, "", 0, ""
	}
	prev := strings.LastIndexByte(key[:last], '|')
	if prev <= first {
		// Fewer than three delimiters: not a well-formed inverted-index key.
		return InvalidId, "", 0, ""
	}

	tableId := parseId(key[:first])
	keyword := key[first+1 : prev]
	doccount, err := strconv.Atoi(key[prev+1 : last])
	if err != nil {
		return InvalidId, "", 0, ""
	}
	tick := key[last+1:]

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

// encodeInvertedValue encodes a posting row's docids as a delta-varint sequence:
// the docids are sorted (by their unsigned 64-bit bit pattern) and written as the
// first id followed by successive gaps, each a base-128 uvarint. Real docids are
// small, densely-allocated idtable ids, so the gaps are tiny and most encode to a
// single byte — far smaller than the previous fixed 8-byte-per-id layout, and
// cheaper to decode than a general-purpose block compressor. Order within a row
// is irrelevant (the docids are a set), so sorting here is free; deduplication
// remains the caller's responsibility. Sorting/subtracting in uint64 space (not
// int64) keeps every gap non-negative and makes the round-trip exact for any
// int64 docid, including negative and max values. This is an on-disk format
// change from the old big-endian layout and requires a reindex.
func encodeInvertedValue(docids []int64) []byte {
	if len(docids) == 0 {
		return []byte{}
	}
	us := make([]uint64, len(docids))
	for i, id := range docids {
		us[i] = uint64(id)
	}
	slices.Sort(us)

	buf := make([]byte, 0, len(us)+len(us)/2) // most ids encode to ~1 byte
	var tmp [binary.MaxVarintLen64]byte
	var prev uint64
	for i, u := range us {
		delta := u
		if i > 0 {
			delta = u - prev // non-negative: us is sorted ascending in uint64 space
		}
		n := binary.PutUvarint(tmp[:], delta)
		buf = append(buf, tmp[:n]...)
		prev = u
	}
	return buf
}

// decodeInvertedValue is the inverse of encodeInvertedValue: it reads successive
// uvarint gaps, accumulating each into the running docid. A truncated trailing
// varint stops the decode; values this package wrote always decode fully.
func decodeInvertedValue(data []byte) []int64 {
	if len(data) == 0 {
		return []int64{}
	}
	ids := make([]int64, 0, len(data)) // upper bound: one byte per id
	var cur uint64
	for i := 0; i < len(data); {
		delta, n := binary.Uvarint(data[i:])
		if n <= 0 {
			break
		}
		cur += delta
		ids = append(ids, int64(cur))
		i += n
	}
	return ids
}

func decodeInvertedValueStr(data string) []int64 {
	// The merger holds row values as strings; copy once into a byte slice and
	// reuse the byte decoder.
	return decodeInvertedValue([]byte(data))
}

// encodeForwardKey builds "<keyTypeForward><tableId>|<docid>" where <docid> is its
// fixed 8-byte big-endian form. The fixed-width 8-byte suffix needs no trailing
// delimiter and no companion decode function — the caller that wrote the key
// already holds the docid; the decimal tableId + '|' keeps table 1's prefix from
// matching table 12's.
func (idx *Index) encodeForwardKey(tableId int, docid int64) []byte {
	b := make([]byte, 0, 1+11+1+8)
	b = append(b, idx.keyTypeForward)
	b = strconv.AppendInt(b, int64(tableId), 10)
	b = append(b, '|')
	var d [8]byte
	binary.BigEndian.PutUint64(d[:], uint64(docid))
	b = append(b, d[:]...)
	return b
}

// encodeForwardKeyPrefix builds "<keyTypeForward><tableId>|", the DeletePrefix
// argument that clears a whole table's forward map. The trailing '|' ensures
// table 5's prefix does not also match table 50's rows.
func (idx *Index) encodeForwardKeyPrefix(tableId int) []byte {
	b := make([]byte, 0, 1+11+1)
	b = append(b, idx.keyTypeForward)
	b = strconv.AppendInt(b, int64(tableId), 10)
	b = append(b, '|')
	return b
}

// encodeForwardValue joins keywords with '|' — the same encoding the retired
// doc-words value used. A keyword containing '|' splits the same lossy way it
// always has (no behavior change vs. the old doc-words value).
func encodeForwardValue(keywords []string) []byte {
	if len(keywords) == 0 {
		return []byte{}
	}
	n := len(keywords) - 1
	for _, k := range keywords {
		n += len(k)
	}
	b := make([]byte, 0, n)
	for i, k := range keywords {
		if i > 0 {
			b = append(b, '|')
		}
		b = append(b, k...)
	}
	return b
}

// decodeForwardValue is the inverse of encodeForwardValue. An empty input decodes
// to a zero-length slice (NOT []string{""}); a plain strings.Split("", "|") would
// mis-yield the latter.
func decodeForwardValue(data []byte) []string {
	if len(data) == 0 {
		return []string{}
	}
	return strings.Split(string(data), "|")
}
