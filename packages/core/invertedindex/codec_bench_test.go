package invertedindex

import (
	"fmt"
	"testing"
)

func benchIdx() *Index {
	return &Index{
		keyTypeRow:    DefaultKeyTypeRow,
		keyTypeTable:  DefaultKeyTypeTable,
		keyTypeNextId: DefaultKeyTypeNextId,
	}
}

func benchValue(n int) []byte {
	buf := make([]byte, 0, n*8)
	for i := 0; i < n; i++ {
		buf = append(buf, []byte(fmt.Sprintf("%08d", i))...)
	}
	return buf
}

// BenchmarkEncodeInvertedKey covers the per-row write/merge key encoder (the
// double-fmt.Sprintf path).
func BenchmarkEncodeInvertedKey(b *testing.B) {
	idx := benchIdx()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = idx.encodeInvertedKey(7, "handleRequest", 42, 0)
	}
}

// BenchmarkEncodeInvertedSearchKey covers the per-Search key encoder.
func BenchmarkEncodeInvertedSearchKey(b *testing.B) {
	idx := benchIdx()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = idx.encodeInvertedSearchKey(7, "handlerequest")
	}
}

// BenchmarkEncodeInvertedKeyPrefix covers the per-Search/GetDocs/remove prefix encoder.
func BenchmarkEncodeInvertedKeyPrefix(b *testing.B) {
	idx := benchIdx()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = idx.encodeInvertedKeyPrefix(7, "handleRequest")
	}
}

// BenchmarkDecodeInvertedKey covers the per-row merge-scan key decoder (strings.Split).
func BenchmarkDecodeInvertedKey(b *testing.B) {
	idx := benchIdx()
	key := string(idx.encodeInvertedKey(7, "handleRequest", 42, 0))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = idx.decodeInvertedKey(key)
	}
}

// BenchmarkDecodeInvertedValue covers the value decoder (50 docids).
func BenchmarkDecodeInvertedValue(b *testing.B) {
	v := benchValue(50)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = decodeInvertedValue(v)
	}
}
