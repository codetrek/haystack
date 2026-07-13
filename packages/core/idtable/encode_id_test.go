package idtable

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/codetrek/haystack/packages/core/kv/pebblekv"
	"github.com/stretchr/testify/assert"
)

// TestEncodeDecodeId_RoundTrip pins the EncodeId/DecodeId pair as exact inverses
// across edge values, and confirms EncodeId always yields the fixed 8-byte form.
func TestEncodeDecodeId_RoundTrip(t *testing.T) {
	cases := []int64{0, 1, -1, 255, 256, 1 << 40, 9223372036854775807 /* max int64 */}
	for _, id := range cases {
		enc := EncodeId(id)
		if len(enc) != 8 {
			t.Errorf("EncodeId(%d) length = %d, want 8", id, len(enc))
		}
		if got := DecodeId(enc); got != id {
			t.Errorf("DecodeId(EncodeId(%d)) = %d, want %d", id, got, id)
		}
	}
}

// TestEncodeId_BigEndianBytes pins the exact on-disk byte layout (big-endian),
// which the documents store and inverted index depend on for key ordering and
// value chunking.
func TestEncodeId_BigEndianBytes(t *testing.T) {
	// 0x0102030405060708 big-endian → bytes 01 02 03 04 05 06 07 08.
	enc := EncodeId(0x0102030405060708)
	want := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	if string(enc) != string(want) {
		t.Errorf("EncodeId big-endian bytes = % x, want % x", enc, want)
	}
}

// TestDecodeId_ShortStringIsInvalid pins that a too-short string (never a real
// docid) decodes to InvalidId rather than panicking.
func TestDecodeId_ShortStringIsInvalid(t *testing.T) {
	for _, s := range []string{"", "abc", "1234567" /* 7 bytes */} {
		if got := DecodeId(s); got != InvalidId {
			t.Errorf("DecodeId(%q) = %d, want InvalidId(%d)", s, got, InvalidId)
		}
	}
}

// TestEncodeId_MatchesGetId confirms EncodeId reproduces the exact string GetId
// hands out for the same underlying counter value, so the int64 form and the
// string form are interchangeable at storage boundaries.
func TestEncodeId_MatchesGetId(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-idtable-encode-*")
	assert.NoError(t, err)
	defer os.RemoveAll(tempDir)

	store, err := pebblekv.Open(filepath.Join(tempDir, "data"), 0)
	assert.NoError(t, err)
	defer store.Close()

	alloc, err := New(store, Options{})
	assert.NoError(t, err)
	defer alloc.Close()

	// The first allocated id corresponds to nextId == 1 (New seeds nextId = 1).
	got, err := alloc.GetId([]byte("some/path.go"))
	assert.NoError(t, err)
	assert.Equal(t, EncodeId(1), got)
	// And DecodeId recovers that counter value.
	assert.Equal(t, int64(1), DecodeId(got))
}
