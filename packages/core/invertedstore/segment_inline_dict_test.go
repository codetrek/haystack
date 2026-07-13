package invertedstore

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

var dictKws = []string{"alpha", "beta", "delta", "gamma", "kappa", "omega", "zeta"}

// writeDictSegment builds a small term-id segment: the 7 sorted [I] keys + one [F] record (which must
// NOT enter the dict). blockTarget 64 forces multiple data blocks so the old re-read decompresses >1.
func writeDictSegment(path string, dictChunk int) *segWriter {
	w := newSegWriter(path, newCodec(codecSnappy), newCodec(codecZstd), 64, 1<<16, 1<<10, true, dictChunk)
	tid := uint32(7)
	for _, kw := range dictKws {
		w.addEntry(invertedKey(tid, kw), encodeInvertedValue([]int64{1}, nil))
	}
	w.addEntry(forwardKey(tid, 1), encodeForward([]uint32{0, 1, 2, 3, 4, 5, 6}))
	return w
}

// rereadDictRegion independently reconstructs the expected term-dict region bytes by scanning the
// segment's own [I] data blocks in order — the SAME bytes finish() must produce inline. Oracle: it
// shares no code with the inline builder under test.
func rereadDictRegion(t *testing.T, s *segment, dictChunk int, dict *codec) []byte {
	t.Helper()
	var region, chunk []byte
	var ord, chunkFirst uint32
	flush := func() {
		if len(chunk) == 0 {
			return
		}
		comp := dict.compress(chunk)
		region = appendUvarint(region, uint64(chunkFirst))
		region = appendUvarint(region, uint64(len(chunk)))
		region = appendUvarint(region, uint64(len(comp)))
		region = append(region, comp...)
		chunk = chunk[:0]
	}
	for i := range s.idx {
		scanBlock(s.blockBytes(i), func(key, _ []byte, _ int64, _ int, _ bool) bool {
			if key[0] != ktInverted {
				return true
			}
			if len(chunk) == 0 {
				chunkFirst = ord
			}
			kw := key[5:]
			chunk = appendUvarint(chunk, uint64(len(kw)))
			chunk = append(chunk, kw...)
			ord++
			if len(chunk) >= dictChunk {
				flush()
			}
			return true
		})
	}
	flush()
	return region
}

// THE GENUINE RED: finish() must decompress zero data blocks (no re-read). Fails before F0 (the
// re-read decompresses every [I] block), passes after, and persists (a re-introduced re-read decompresses).
func TestInlineDict_FinishDecompressesNoDataBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seg-000001.dat")
	w := writeDictSegment(path, 8)
	var decompresses int
	onDecompress = func() { decompresses++ }
	t.Cleanup(func() { onDecompress = nil })
	seg := w.finish(path)
	onDecompress = nil // stop before any read-path decompress
	defer seg.close()
	if decompresses != 0 {
		t.Fatalf("finish() decompressed %d data blocks (re-read path); the inline dict build must decompress 0", decompresses)
	}
}

// Correctness net: the on-disk dict region byte-equals the independent oracle, and every ordinal
// round-trips to its keyword. (Passes against both old + new code — it pins format, not behavior.)
func TestInlineDict_RegionByteIdenticalToReread(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seg-000001.dat")
	dictChunk := 8
	w := writeDictSegment(path, dictChunk)
	seg := w.finish(path)
	defer seg.close()

	want := rereadDictRegion(t, seg, dictChunk, seg.dictCodec)
	got := make([]byte, seg.biOff-seg.dictOff)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	mustReadAt(f, got, seg.dictOff)
	if !bytes.Equal(got, want) {
		t.Fatalf("inline dict region (%d B) != oracle (%d B)", len(got), len(want))
	}
	res := seg.resolveOrds(map[uint32]struct{}{0: {}, 1: {}, 2: {}, 3: {}, 4: {}, 5: {}, 6: {}})
	for i, kw := range dictKws {
		if res[uint32(i)] != kw {
			t.Fatalf("ord %d resolved %q, want %q", i, res[uint32(i)], kw)
		}
	}
}
