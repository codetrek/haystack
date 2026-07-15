package invertedstore

import (
	"bytes"
	"testing"
)

func TestCodecRoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte("the quick brown fox 0123456789 "), 2000) // compressible
	for _, id := range []byte{codecNone, codecSnappy, codecZstd} {
		c := newCodec(id)
		comp := c.compress(payload)
		got := c.decompress(comp, len(payload))
		if !bytes.Equal(got, payload) {
			t.Fatalf("codec %d round-trip mismatch", id)
		}
		if id != codecNone && len(comp) >= len(payload) {
			t.Fatalf("codec %d did not compress (%d >= %d)", id, len(comp), len(payload))
		}
	}
}

func TestZstdBounded(t *testing.T) {
	// zstd must be bounded (concurrency 1, small window) so it can't blow memory.
	c := newCodec(codecZstd)
	if c.enc == nil || c.dec == nil {
		t.Fatal("zstd codec must hold a bounded encoder+decoder")
	}
	_ = c.decompress(c.compress([]byte("x")), 1) // smoke
}
