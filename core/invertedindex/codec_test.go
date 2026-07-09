package invertedindex

import (
	"bytes"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
)

// testCodecIdx is a minimal *Index with default key-type bytes used by codec tests.
// It avoids the need for a full test environment (database + queue).
var testCodecIdx = &Index{
	keyTypeRow:     DefaultKeyTypeRow,
	keyTypeTable:   DefaultKeyTypeTable,
	keyTypeNextId:  DefaultKeyTypeNextId,
	keyTypeForward: DefaultKeyTypeForward,
}

// ---------------------------------------------------------------------------
// parseId
// ---------------------------------------------------------------------------
func TestParseId(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want int
	}{
		{"valid integer", "42", 42},
		{"zero", "0", 0},
		{"negative", "-1", -1},
		{"empty string", "", InvalidId},
		{"non-numeric", "abc", InvalidId},
		{"float", "3.14", InvalidId},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseId(tc.key)
			if got != tc.want {
				t.Errorf("parseId(%q) = %d; want %d", tc.key, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// encodeInvertedSearchKey
// ---------------------------------------------------------------------------
func TestEncodeInvertedSearchKey(t *testing.T) {
	key := testCodecIdx.encodeInvertedSearchKey(5, "hello")
	s := string(key)

	// First byte must be DefaultKeyTypeRow
	if s[0] != DefaultKeyTypeRow {
		t.Fatalf("expected first byte %d, got %d", DefaultKeyTypeRow, s[0])
	}

	// Must contain tableId|query
	if !strings.Contains(s, "5|hello") {
		t.Errorf("expected key to contain '5|hello', got %q", s)
	}
}

// ---------------------------------------------------------------------------
// encodeInvertedKeyPrefix
// ---------------------------------------------------------------------------
func TestEncodeInvertedKeyPrefix(t *testing.T) {
	prefix := testCodecIdx.encodeInvertedKeyPrefix(3, "myword")
	s := string(prefix)

	if s[0] != DefaultKeyTypeRow {
		t.Fatalf("expected first byte %d, got %d", DefaultKeyTypeRow, s[0])
	}

	// Must end with a trailing pipe (separator before doccount)
	if !strings.HasSuffix(s, "3|myword|") {
		t.Errorf("expected key to end with '3|myword|', got %q", s)
	}
}

// ---------------------------------------------------------------------------
// encodeInvertedKey / decodeInvertedKey round-trip
// ---------------------------------------------------------------------------
func TestEncodeDecodeInvertedKeyRoundTrip(t *testing.T) {
	tableId := 7
	keyword := "testing"
	doccount := 42

	key := testCodecIdx.encodeInvertedKey(tableId, keyword, doccount)
	s := string(key)

	// First byte is DefaultKeyTypeRow
	if s[0] != DefaultKeyTypeRow {
		t.Fatalf("expected first byte %d, got %d", DefaultKeyTypeRow, s[0])
	}

	// Decode and verify
	gotTable, gotKeyword, gotDoccount, gotTick := testCodecIdx.decodeInvertedKey(s)
	if gotTable != tableId {
		t.Errorf("tableId: got %d, want %d", gotTable, tableId)
	}
	if gotKeyword != keyword {
		t.Errorf("keyword: got %q, want %q", gotKeyword, keyword)
	}
	if gotDoccount != doccount {
		t.Errorf("doccount: got %d, want %d", gotDoccount, doccount)
	}
	if gotTick == "" {
		t.Error("expected non-empty tick (timestamp)")
	}
}

// ---------------------------------------------------------------------------
// decodeInvertedKey — error cases
// ---------------------------------------------------------------------------
func TestDecodeInvertedKeyErrors(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"empty key", ""},
		{"wrong key type prefix", string([]byte{byte(11)}) + "1|word|5|1234"},
		{"too few parts", string([]byte{DefaultKeyTypeRow}) + "1|word"},
		// NOTE: a key with "extra" '|' (e.g. "1|word|5|1234|extra") is NOT an
		// error — it is a valid row for the keyword "word|5" (keywords may contain
		// the '|' delimiter). That round-trip is covered by
		// TestDecodeInvertedKey_KeywordWithDelimiter.
		{"non-numeric doccount", string([]byte{DefaultKeyTypeRow}) + "1|word|abc|1234"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tableId, keyword, doccount, tick := testCodecIdx.decodeInvertedKey(tc.key)
			if tableId != InvalidId {
				t.Errorf("expected tableId = InvalidId, got %d", tableId)
			}
			if keyword != "" {
				t.Errorf("expected empty keyword, got %q", keyword)
			}
			if doccount != 0 {
				t.Errorf("expected doccount = 0, got %d", doccount)
			}
			if tick != "" {
				t.Errorf("expected empty tick, got %q", tick)
			}
		})
	}
}

// TestDecodeInvertedKeyNonNumericTableId verifies that a non-numeric tableId
// returns InvalidId for the tableId while still parsing the other fields.
func TestDecodeInvertedKeyNonNumericTableId(t *testing.T) {
	key := string([]byte{DefaultKeyTypeRow}) + "abc|word|5|1234"
	tableId, keyword, doccount, tick := testCodecIdx.decodeInvertedKey(key)
	if tableId != InvalidId {
		t.Errorf("expected tableId = InvalidId, got %d", tableId)
	}
	// The function still parses the remaining fields even with a bad tableId
	if keyword != "word" {
		t.Errorf("expected keyword 'word', got %q", keyword)
	}
	if doccount != 5 {
		t.Errorf("expected doccount 5, got %d", doccount)
	}
	if tick != "1234" {
		t.Errorf("expected tick '1234', got %q", tick)
	}
}

// ---------------------------------------------------------------------------
// encodeNextTableIdKey
// ---------------------------------------------------------------------------
func TestEncodeNextTableIdKey(t *testing.T) {
	key := testCodecIdx.encodeNextTableIdKey()
	if len(key) != 1 {
		t.Fatalf("expected 1 byte, got %d", len(key))
	}
	if key[0] != DefaultKeyTypeNextId {
		t.Errorf("expected byte %d, got %d", DefaultKeyTypeNextId, key[0])
	}
}

// ---------------------------------------------------------------------------
// encodeTableKey
// ---------------------------------------------------------------------------
func TestEncodeTableKey(t *testing.T) {
	key := testCodecIdx.encodeTableKey(9)
	s := string(key)
	if s[0] != DefaultKeyTypeTable {
		t.Fatalf("expected first byte %d, got %d", DefaultKeyTypeTable, s[0])
	}
	if !strings.Contains(s, "9") {
		t.Errorf("expected key to contain '9', got %q", s)
	}
}

// ---------------------------------------------------------------------------
// encodeTableValue
// ---------------------------------------------------------------------------
func TestEncodeTableValue(t *testing.T) {
	info := TableInfo{
		Id:          3,
		Description: "test table",
	}
	data := encodeTableValue(info)

	var decoded TableInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal table value: %v", err)
	}
	if decoded.Id != info.Id {
		t.Errorf("Id: got %d, want %d", decoded.Id, info.Id)
	}
	if decoded.Description != info.Description {
		t.Errorf("Description: got %q, want %q", decoded.Description, info.Description)
	}
}

// ---------------------------------------------------------------------------
// encodeInvertedValue / decodeInvertedValue round-trip (delta-varint)
// ---------------------------------------------------------------------------
func TestEncodeDecodeInvertedValue(t *testing.T) {
	// Unsorted, realistic densely-allocated docids. encode sorts and delta-varint
	// encodes them; decode returns the ascending set.
	docids := []int64{100, 5, 5000, 6, 101, 9}
	want := []int64{5, 6, 9, 100, 101, 5000}
	encoded := encodeInvertedValue(docids)

	// Delta-varint must be far smaller than the old fixed 8-byte-per-id layout.
	if len(encoded) >= len(docids)*8 {
		t.Errorf("delta-varint did not shrink value: %d bytes for %d ids", len(encoded), len(docids))
	}

	decoded := decodeInvertedValue(encoded)
	if !slices.Equal(decoded, want) {
		t.Fatalf("round-trip mismatch: got %v, want %v", decoded, want)
	}
}

func TestDecodeInvertedValueEdgeCases(t *testing.T) {
	// Round-trip cases: encode then decode reproduces the ascending docid set.
	roundTrips := []struct {
		name string
		ids  []int64
		want []int64
	}{
		{"empty", nil, []int64{}},
		{"single", []int64{42}, []int64{42}},
		{"adjacent", []int64{7, 8, 9}, []int64{7, 8, 9}},
		{"large gaps", []int64{0, 1 << 50}, []int64{0, 1 << 50}},
	}
	for _, tc := range roundTrips {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeInvertedValue(encodeInvertedValue(tc.ids))
			if !slices.Equal(got, tc.want) {
				t.Errorf("round-trip(%v): got %v, want %v", tc.ids, got, tc.want)
			}
		})
	}

	// A truncated trailing varint (a dangling continuation byte) stops the decode
	// after the valid prefix instead of corrupting it.
	valid := encodeInvertedValue([]int64{3, 70000}) // 70000 needs a multi-byte varint
	truncated := valid[:len(valid)-1]
	if got := decodeInvertedValue(truncated); len(got) == 0 || got[0] != 3 {
		t.Errorf("truncated decode: got %v, want the valid prefix [3 ...]", got)
	}
}

// ---------------------------------------------------------------------------
// decodeInvertedValueStr
// ---------------------------------------------------------------------------
func TestDecodeInvertedValueStr(t *testing.T) {
	want := []int64{2, 4, 8, 4096}
	data := string(encodeInvertedValue(want))
	decoded := decodeInvertedValueStr(data)
	if !slices.Equal(decoded, want) {
		t.Fatalf("got %v, want %v", decoded, want)
	}
}

func TestDecodeInvertedValueStrEdgeCases(t *testing.T) {
	if got := decodeInvertedValueStr(""); len(got) != 0 {
		t.Errorf("empty string: got %v, want none", got)
	}
	if got := decodeInvertedValueStr(string(encodeInvertedValue([]int64{99}))); !slices.Equal(got, []int64{99}) {
		t.Errorf("single: got %v, want [99]", got)
	}
}

// ---------------------------------------------------------------------------
// removeDuplicatesEfficiently
// ---------------------------------------------------------------------------
func TestRemoveDuplicatesEfficiently(t *testing.T) {
	tests := []struct {
		name  string
		input []int64
		want  int
	}{
		{"empty", []int64{}, 0},
		{"single", []int64{1}, 1},
		{"no duplicates", []int64{1, 2, 3}, 3},
		{"with duplicates", []int64{1, 2, 1, 3, 2}, 3},
		{"all same", []int64{9, 9, 9}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := removeDuplicatesEfficiently(tc.input)
			if len(got) != tc.want {
				t.Errorf("removeDuplicatesEfficiently(%v): got %d unique, want %d", tc.input, len(got), tc.want)
			}
		})
	}
}

// Verify ordering is preserved (first occurrence wins)
func TestRemoveDuplicatesPreservesOrder(t *testing.T) {
	input := []int64{2, 1, 3, 1, 2}
	got := removeDuplicatesEfficiently(input)
	expected := []int64{2, 1, 3}
	if len(got) != len(expected) {
		t.Fatalf("expected len %d, got %d", len(expected), len(got))
	}
	for i, v := range expected {
		if got[i] != v {
			t.Errorf("index %d: got %d, want %d", i, got[i], v)
		}
	}
}

// ---------------------------------------------------------------------------
// encodeInvertedValue — set-dedup + byte-identity (I1)
// ---------------------------------------------------------------------------

// sortedUniqueUint64Space returns ids as the ascending-in-uint64-space set with
// duplicates removed. It is an INDEPENDENT oracle for encodeInvertedValue's
// sort+dedup contract — it never calls the function under test.
func sortedUniqueUint64Space(ids []int64) []int64 {
	us := make([]uint64, len(ids))
	for i, id := range ids {
		us[i] = uint64(id)
	}
	slices.Sort(us)
	us = slices.Compact(us)
	out := make([]int64, len(us))
	for i, u := range us {
		out[i] = int64(u)
	}
	return out
}

// TestEncodeInvertedValueGoldenBytes pins byte-identity against HAND-TYPED
// delta-varint constants (never derived by calling encodeInvertedValue). The
// encoder sorts the docids in uint64 space, drops duplicates, then writes the
// first id followed by successive gaps as base-128 uvarints. This is the
// load-bearing dedup guard: without slices.Compact the duplicate inputs encode
// extra zero-gap varints and fail these goldens.
func TestEncodeInvertedValueGoldenBytes(t *testing.T) {
	tests := []struct {
		name string
		ids  []int64
		want []byte
	}{
		// sort {1,1,2,3,3} -> unique {1,2,3} -> deltas 1,1,1
		{"dups and unsorted", []int64{3, 1, 2, 1, 3}, []byte{0x01, 0x01, 0x01}},
		// all identical -> {9} -> single absolute varint
		{"all duplicates", []int64{9, 9, 9}, []byte{0x09}},
		// already sorted, no dups -> deltas 1,1,2
		{"sorted no dups", []int64{1, 2, 4}, []byte{0x01, 0x01, 0x02}},
		// empty -> empty
		{"empty", []int64{}, []byte{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := encodeInvertedValue(tc.ids)
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("encodeInvertedValue(%v) = %v, want %v", tc.ids, got, tc.want)
			}
		})
	}
}

// TestEncodeInvertedValueRoundTripFullIntRange proves the encode/decode round
// trip is exact for the full int64 range (negatives, MinInt64, MaxInt64) AND
// that duplicates are dropped, comparing against the independent sorted-unique
// oracle rather than hand-typed 10-byte varints.
func TestEncodeInvertedValueRoundTripFullIntRange(t *testing.T) {
	tests := [][]int64{
		{math.MinInt64},
		{-1},
		{math.MaxInt64},
		{5, -1, 5, math.MaxInt64},
	}
	for _, ids := range tests {
		want := sortedUniqueUint64Space(ids)
		got := decodeInvertedValue(encodeInvertedValue(ids))
		if !slices.Equal(got, want) {
			t.Fatalf("round-trip(%v): got %v, want %v", ids, got, want)
		}
	}
}
