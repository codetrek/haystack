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
	if onDecompress != nil {
		onDecompress()
	}
	switch c.id {
	case codecSnappy:
		d, err := snappy.Decode(make([]byte, 0, rawLen), src)
		if err != nil {
			panic(err)
		}
		return d
	case codecZstd:
		d, err := c.dec.DecodeAll(src, make([]byte, 0, rawLen))
		if err != nil {
			panic(err)
		}
		return d
	default:
		return src
	}
}
