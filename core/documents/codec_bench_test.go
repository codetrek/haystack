package documents

import "testing"

// TestEncodeDocKeyGolden locks the exact on-disk key bytes, proving the hand-rolled
// builders are byte-identical to the previous fmt.Sprintf("%c%d|%s", ...) form
// (default prefixes 10/13 are <128, so "%c" emitted the raw byte).
func TestEncodeDocKeyGolden(t *testing.T) {
	s := benchStore()
	if got, want := string(s.encodeDocumentPathKey(42, "src/a.go")), "\x0d42|src/a.go"; got != want {
		t.Errorf("path key = %q, want %q", got, want)
	}
	if got, want := string(s.encodeDocumentMetaKey(-3, "x")), "\x0c-3|x"; got != want {
		t.Errorf("meta key = %q, want %q", got, want)
	}
	if got, want := string(s.encodeMetaKey(7)), "\x0a7"; got != want {
		t.Errorf("meta key = %q, want %q", got, want)
	}
}

func benchStore() *Store {
	return &Store{
		keyTypeDocCollection: DefaultKeyTypeDocCollection,
		keyTypeDocMeta:       DefaultKeyTypeDocMeta,
		keyTypeDocPath:       DefaultKeyTypeDocPath,
	}
}

func BenchmarkEncodeDocumentPathKey(b *testing.B) {
	s := benchStore()
	b.ReportAllocs()
	var sink []byte
	for i := 0; i < b.N; i++ {
		sink = s.encodeDocumentPathKey(42, "src/server/handler.go")
	}
	_ = sink
}

func BenchmarkEncodeMetaKey(b *testing.B) {
	s := benchStore()
	b.ReportAllocs()
	var sink []byte
	for i := 0; i < b.N; i++ {
		sink = s.encodeMetaKey(42)
	}
	_ = sink
}
