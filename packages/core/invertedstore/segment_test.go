package invertedstore

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// writeTestSeg writes a segment with [I] keyword records (postings) and [F] forward records
// (term-ids), term-id mode on, then returns the opened segment.
func writeTestSeg(t *testing.T, termid bool) *segment {
	t.Helper()
	path := filepath.Join(t.TempDir(), "seg00001.dat")
	w := newSegWriter(path, newCodec(codecSnappy), newCodec(codecZstd), 32768, 65536, 1024, termid, 4096)
	// term dict (ordinal order) = sorted inverted keywords: alpha=0, beta=1, gamma=2
	terms := []string{"alpha", "beta", "gamma"}
	for i, kw := range terms {
		w.addEntry(invertedKey(1, kw), encodeInvertedValue([]int64{int64(i + 10)}, nil))
	}
	// forward: doc 10 has {alpha(0), gamma(2)}; doc 11 deleted (tombstone)
	if termid {
		w.addEntry(forwardKey(1, 10), encodeForward([]uint32{0, 2}))
	}
	w.addEntry(forwardKey(1, 11), forwardTombstone())
	return w.finish(path)
}

func TestSegmentRoundTrip(t *testing.T) {
	s := writeTestSeg(t, true)
	defer s.close()
	// footer carries both codec ids
	if s.dataCodec.id != codecSnappy || s.dictCodec.id != codecZstd {
		t.Fatalf("footer codec ids wrong: data=%d dict=%d", s.dataCodec.id, s.dictCodec.id)
	}
	// prefix scan for keyword "beta" finds exactly doc 11's posting
	lo := invertedKey(1, "beta")
	hi := prefixUpper(lo)
	var hits []int64
	s.scanPrefix(lo, hi, func(_ []byte, val []byte) {
		ab, _ := splitInvertedValue(val)
		decodeDocs(ab, func(d int64) { hits = append(hits, d) })
	})
	if len(hits) != 1 || hits[0] != 11 {
		t.Fatalf("scanPrefix(beta) = %v, want [11]", hits)
	}
	// forward point lookup + term-id resolve: doc 10 → {alpha, gamma}
	val, ok := s.lookupForward(forwardKey(1, 10))
	if !ok {
		t.Fatal("forward lookup miss for doc 10")
	}
	ords, deleted := decodeForward(val)
	if deleted {
		t.Fatal("doc 10 wrongly read as deleted")
	}
	need := map[uint32]struct{}{}
	for _, o := range ords {
		need[o] = struct{}{}
	}
	got := s.resolveOrds(need) // ord -> keyword via term-dict region
	words := []string{got[0], got[2]}
	sort.Strings(words)
	if words[0] != "alpha" || words[1] != "gamma" {
		t.Fatalf("resolve = %v, want [alpha gamma]", words)
	}
	// doc 11 forward is a tombstone
	tval, _ := s.lookupForward(forwardKey(1, 11))
	if _, del := decodeForward(tval); !del {
		t.Fatal("doc 11 should read as deleted")
	}
}

func TestSegmentGoldenFooter(t *testing.T) {
	s := writeTestSeg(t, true)
	defer s.close()
	fi, err := s.f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	sz := fi.Size()
	foot := make([]byte, 25)
	if _, err := s.f.ReadAt(foot, sz-25); err != nil {
		t.Fatalf("read footer: %v", err)
	}
	if string(foot[18:25]) != "SRSEG\x00\x00" {
		t.Fatalf("footer magic wrong: % x", foot[18:25])
	}
	if foot[16] != codecSnappy {
		t.Fatalf("footer data codec id = %d, want %d", foot[16], codecSnappy)
	}
	if foot[17] != codecZstd {
		t.Fatalf("footer dict codec id = %d, want %d", foot[17], codecZstd)
	}
}

// TestNewSegWriterDefaultsDictChunk: passing dictChunk <= 0 defaults it to blockTarget (the
// term-dict chunk size falls back to the block size when the caller does not size it).
func TestNewSegWriterDefaultsDictChunk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seg-defaultdict.dat")
	const blockTarget = 4096
	w := newSegWriter(path, newCodec(codecSnappy), newCodec(codecZstd), blockTarget, 65536, 1024, true, 0)
	defer func() { _ = w.f.Close() }()
	if w.dictChunk != blockTarget {
		t.Fatalf("dictChunk=%d, want blockTarget %d when dictChunk<=0", w.dictChunk, blockTarget)
	}
}

// TestNewSegWriterPanicsOnUncreatablePath: newSegWriter cannot recover from a failed os.Create (a
// corrupt/unwritable dir is unrecoverable at seal time), so it panics — here the path's parent
// directory does not exist.
func TestNewSegWriterPanicsOnUncreatablePath(t *testing.T) {
	badPath := filepath.Join(t.TempDir(), "no-such-subdir", "seg.dat")
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("newSegWriter should panic when os.Create fails")
		}
	}()
	newSegWriter(badPath, newCodec(codecSnappy), newCodec(codecZstd), 4096, 65536, 1024, true, 4096)
}

// TestMustReadAtPanicsOnShortRead: mustReadAt requires an exact-length read at off; a read past the
// end of the file (nothing there) returns io.EOF, which mustReadAt turns into a panic (a corrupt
// segment is unrecoverable at this layer).
func TestMustReadAtPanicsOnShortRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tiny.bin")
	if err := os.WriteFile(path, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("mustReadAt should panic on a short/EOF read past the file end")
		}
	}()
	// Read 16 bytes starting at offset 100, well past the 2-byte file → io.EOF, zero bytes read.
	mustReadAt(f, make([]byte, 16), 100)
}
