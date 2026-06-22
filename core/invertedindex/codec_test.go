package invertedindex

import (
	"encoding/json"
	"strings"
	"testing"
)

// testCodecIdx is a minimal *Index with default key-type bytes used by codec tests.
// It avoids the need for a full test environment (database + queue).
var testCodecIdx = &Index{
	keyTypeRow:    DefaultKeyTypeRow,
	keyTypeTable:  DefaultKeyTypeTable,
	keyTypeNextId: DefaultKeyTypeNextId,
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
// encodeInvertedValue / decodeInvertedValue round-trip
// ---------------------------------------------------------------------------
func TestEncodeDecodeInvertedValue(t *testing.T) {
	// Each docid is an 8-byte big-endian int64. These literals are the int64
	// values whose big-endian bytes spell the ASCII labels below, so the encoded
	// buffer is byte-identical to the previous string representation (no reindex).
	docids := []int64{0x6162636465666768 /*abcdefgh*/, 0x3132333435363738 /*12345678*/, 0x5858585858585858 /*XXXXXXXX*/}
	encoded := encodeInvertedValue(docids)

	// Byte-parity with the old string-docid format.
	if string(encoded) != "abcdefgh12345678XXXXXXXX" {
		t.Fatalf("encoded bytes = %q, want %q", string(encoded), "abcdefgh12345678XXXXXXXX")
	}

	decoded := decodeInvertedValue(encoded)
	if len(decoded) != len(docids) {
		t.Fatalf("expected %d docids, got %d", len(docids), len(decoded))
	}
	for i, want := range docids {
		if decoded[i] != want {
			t.Errorf("docids[%d]: got %d, want %d", i, decoded[i], want)
		}
	}
}

func TestDecodeInvertedValueEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want int
	}{
		{"empty data", []byte{}, 0},
		{"not multiple of 8", []byte("abc"), 0},
		{"exactly 8 bytes", []byte("12345678"), 1},
		{"16 bytes", []byte("1234567890abcdef"), 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeInvertedValue(tc.data)
			if len(got) != tc.want {
				t.Errorf("decodeInvertedValue(%q): got %d docids, want %d", tc.data, len(got), tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// decodeInvertedValueStr
// ---------------------------------------------------------------------------
func TestDecodeInvertedValueStr(t *testing.T) {
	// "abcdefgh12345678" → two 8-byte big-endian int64 docids.
	data := "abcdefgh12345678"
	want := []int64{0x6162636465666768, 0x3132333435363738}
	decoded := decodeInvertedValueStr(data)

	if len(decoded) != len(want) {
		t.Fatalf("expected %d docids, got %d", len(want), len(decoded))
	}
	for i, w := range want {
		if decoded[i] != w {
			t.Errorf("docids[%d]: got %d, want %d", i, decoded[i], w)
		}
	}
}

func TestDecodeInvertedValueStrEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		data string
		want int
	}{
		{"empty string", "", 0},
		{"not multiple of 8", "abcde", 0},
		{"exactly 8 chars", "12345678", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeInvertedValueStr(tc.data)
			if len(got) != tc.want {
				t.Errorf("decodeInvertedValueStr(%q): got %d docids, want %d", tc.data, len(got), tc.want)
			}
		})
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
