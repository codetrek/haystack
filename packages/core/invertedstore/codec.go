package invertedstore

import (
	"github.com/golang/snappy"
	"github.com/klauspost/compress/zstd"
)

const (
	codecNone   = byte(0)
	codecSnappy = byte(1)
	codecZstd   = byte(2)
)

type codec struct {
	id  byte
	enc *zstd.Encoder
	dec *zstd.Decoder
}

func newCodec(id byte) *codec {
	c := &codec{id: id}
	if id == codecZstd {
		c.enc, _ = zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.SpeedFastest),
			zstd.WithEncoderConcurrency(1),
			zstd.WithWindowSize(128*1024))
		c.dec, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	}
	return c
}

func (c *codec) compress(src []byte) []byte {
	switch c.id {
	case codecSnappy:
		return snappy.Encode(nil, src)
	case codecZstd:
		return c.enc.EncodeAll(src, nil)
	default:
		return append([]byte(nil), src...)
	}
}

// onDecompress, when non-nil, is invoked at the start of every codec.decompress. Test-only (F0): a
// test counts data-block decompressions DURING finish() — the genuine red→green discriminator (old
// writeTermDict re-reads+decompresses each [I] block; the inline build decompresses none) that a
// byte-identical oracle cannot provide. nil in production (one predictable branch). A test that
// installs it MUST NOT t.Parallel (same constraint as the merge observers).
var onDecompress func()

func (c *codec) decompress(src []byte, rawLen int) []byte {
	return c.decompressInto(make([]byte, 0, rawLen), src, rawLen)
}

// decompressInto decompresses src into a buffer reusing dst's backing array when it has room for the
// rawLen decompressed bytes (C.2: the mergeCursor hands its previous block buffer so a k-way merge
// over K sources allocates O(K) block buffers, not one per block). dst may be nil. The returned slice
// may alias dst's storage, so a caller that REUSES the same dst across calls MUST NOT retain bytes
// from a previous decompressInto into it (the mergeCursor's blkFirst-copy in segment.go addEntry
// enforces this for the writer; readers that retain bytes already copy out). rawLen is the
// decompressed length; the buffer is sized to it up front so snappy/zstd reuse it in place.
func (c *codec) decompressInto(dst, src []byte, rawLen int) []byte {
	if onDecompress != nil {
		onDecompress()
	}
	// Present a zero-length slice with rawLen capacity so the decoders reuse dst in place (snappy's
	// Decode reuses only when len(dst) >= decodedLen, zstd's DecodeAll appends into dst[:0]).
	if cap(dst) >= rawLen {
		dst = dst[:0]
	} else {
		dst = make([]byte, 0, rawLen)
	}
	switch c.id {
	case codecSnappy:
		d, err := snappy.Decode(dst[:rawLen], src)
		if err != nil {
			panic(err)
		}
		return d
	case codecZstd:
		d, err := c.dec.DecodeAll(src, dst)
		if err != nil {
			panic(err)
		}
		return d
	default:
		return append(dst, src...)
	}
}
