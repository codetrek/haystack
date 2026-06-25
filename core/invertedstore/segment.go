package invertedstore

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"sort"
	"sync"
	"sync/atomic"
)

// ---- key prefix helpers (port spike main.go:213-234, verbatim) -------------

func prefixUpper(p []byte) []byte {
	u := make([]byte, len(p))
	copy(u, p)
	for i := len(u) - 1; i >= 0; i-- {
		if u[i] != 0xff {
			u[i]++
			return u[:i+1]
		}
	}
	return nil
}

func hasPrefixBytes(b, p []byte) bool {
	if len(b) < len(p) {
		return false
	}
	for i := range p {
		if b[i] != p[i] {
			return false
		}
	}
	return true
}

// ---- segment writer (packed blocks; inline small / external large) ---------
//
// Standard SSTable-style format: a segment is a sequence of DATA BLOCKS; each block
// packs N records (key+value) and is compressed AS ONE UNIT. A record's value is INLINE
// when small (≤ threshold bytes) and EXTERNAL (≤chunk compressed chunks, the record holds
// a pointer) when large. Two key-types share the keyspace: [I] keyword->postings and
// [F] docid->forward. Keys are full []byte (keyType + 4B BE tableId + keyword|docid).

type blockEntry struct {
	firstKey []byte
	off      int64
}

type segWriter struct {
	f                    *os.File
	bw                   *bufio.Writer
	off                  int64
	dataCodec, dictCodec *codec
	blockTarget, chunk   int
	threshold, dictChunk int
	termid               bool
	idx                  []blockEntry
	blkRaw               []byte // current block's packed records
	blkFirst             []byte
	blkHave              bool

	// inline term-dict accumulation (F0): built as [I] keys are added, written at finish — no
	// re-read of own blocks. dictRaw is the current chunk; dictRegion is the compressed chunks so far.
	dictRaw        []byte
	dictRegion     []byte
	dictOrd        uint32
	dictChunkFirst uint32
}

func newSegWriter(path string, data, dict *codec, blockTarget, chunk, threshold int, termid bool, dictChunk int) *segWriter {
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	if dictChunk <= 0 {
		dictChunk = blockTarget
	}
	return &segWriter{f: f, bw: bufio.NewWriterSize(f, 1<<20), dataCodec: data, dictCodec: dict,
		blockTarget: blockTarget, chunk: chunk, threshold: threshold, termid: termid, dictChunk: dictChunk}
}

// writeExternalValue writes a large value as ≤chunk compressed chunks and returns
// (offset, totalCompLen). chunk := uvarint(rawLen) uvarint(compLen) bytes.
// (port spike main.go:597-615; codec is dataCodec.)
func (w *segWriter) writeExternalValue(raw []byte) (int64, int) {
	start := w.off
	for p := 0; p < len(raw); {
		end := p + w.chunk
		if end > len(raw) {
			end = len(raw)
		}
		piece := raw[p:end]
		comp := w.dataCodec.compress(piece)
		var hdr []byte
		hdr = appendUvarint(hdr, uint64(len(piece)))
		hdr = appendUvarint(hdr, uint64(len(comp)))
		w.bw.Write(hdr)
		w.bw.Write(comp)
		w.off += int64(len(hdr) + len(comp))
		p = end
	}
	return start, int(w.off - start)
}

// addEntry packs a record (key+value) into the current block. Small values are
// inline; large values are written externally now and the record holds a pointer.
//
//	record := uvarint(klen) key flag(1)
//	         flag==0 inline:   uvarint(vlen) value
//	         flag==1 external: uvarint(off) uvarint(compLen)
//
// (port spike main.go:622-644; key is now []byte.)
func (w *segWriter) addEntry(key []byte, value []byte) {
	if !w.blkHave {
		w.blkFirst, w.blkHave = key, true
	}
	w.blkRaw = appendUvarint(w.blkRaw, uint64(len(key)))
	w.blkRaw = append(w.blkRaw, key...)
	if len(value) <= w.threshold {
		w.blkRaw = append(w.blkRaw, 0)
		w.blkRaw = appendUvarint(w.blkRaw, uint64(len(value)))
		w.blkRaw = append(w.blkRaw, value...)
	} else {
		vOff, vLen := w.writeExternalValue(value)
		w.blkRaw = append(w.blkRaw, 1)
		w.blkRaw = appendUvarint(w.blkRaw, uint64(vOff))
		w.blkRaw = appendUvarint(w.blkRaw, uint64(vLen))
	}
	if len(w.blkRaw) >= w.blockTarget {
		w.flushBlock()
	}
	if w.termid && key[0] == ktInverted {
		if len(w.dictRaw) == 0 {
			w.dictChunkFirst = w.dictOrd
		}
		kw := key[5:] // keyType(1) + tableId(4 BE) then keyword
		w.dictRaw = appendUvarint(w.dictRaw, uint64(len(kw)))
		w.dictRaw = append(w.dictRaw, kw...)
		w.dictOrd++
		if len(w.dictRaw) >= w.dictChunk {
			w.flushDictChunk()
		}
	}
}

// flushBlock compresses the current packed block and appends it. (port spike main.go:646-660.)
func (w *segWriter) flushBlock() {
	if len(w.blkRaw) == 0 {
		return
	}
	comp := w.dataCodec.compress(w.blkRaw)
	w.idx = append(w.idx, blockEntry{w.blkFirst, w.off})
	var hdr []byte
	hdr = appendUvarint(hdr, uint64(len(w.blkRaw)))
	hdr = appendUvarint(hdr, uint64(len(comp)))
	w.bw.Write(hdr)
	w.bw.Write(comp)
	w.off += int64(len(hdr) + len(comp))
	w.blkRaw = w.blkRaw[:0]
	w.blkHave = false
}

// finish writes the term-dict region (term-id only), the block index, and the 25-byte
// footer (both codec ids), then opens and returns the segment reader.
func (w *segWriter) finish(path string) *segment {
	w.flushBlock()
	var dictOff int64
	if w.termid {
		w.flushDictChunk() // flush the final partial chunk
		dictOff = w.off    // == biOff when the region is empty (forward-only segment), as before
		w.bw.Write(w.dictRegion)
		w.off += int64(len(w.dictRegion))
	}
	biOff := w.off
	var bi []byte
	bi = appendUvarint(bi, uint64(len(w.idx)))
	for _, e := range w.idx {
		bi = appendUvarint(bi, uint64(len(e.firstKey)))
		bi = append(bi, e.firstKey...)
		bi = appendUvarint(bi, uint64(e.off))
	}
	w.bw.Write(bi)
	w.off += int64(len(bi))
	var foot [25]byte
	binary.BigEndian.PutUint64(foot[0:8], uint64(biOff))
	binary.BigEndian.PutUint64(foot[8:16], uint64(dictOff)) // 0 ⇒ no term-dict region
	foot[16] = w.dataCodec.id
	foot[17] = w.dictCodec.id
	copy(foot[18:], "SRSEG\x00\x00")
	w.bw.Write(foot[:])
	w.bw.Flush()
	w.f.Sync()
	w.f.Close()
	return openSegment(path)
}

// flushDictChunk compresses the current inline dict chunk and appends it to dictRegion (the same
// uvarint(chunkFirst) uvarint(rawLen) uvarint(compLen) comp layout writeTermDict produced).
func (w *segWriter) flushDictChunk() {
	if len(w.dictRaw) == 0 {
		return
	}
	comp := w.dictCodec.compress(w.dictRaw)
	w.dictRegion = appendUvarint(w.dictRegion, uint64(w.dictChunkFirst))
	w.dictRegion = appendUvarint(w.dictRegion, uint64(len(w.dictRaw)))
	w.dictRegion = appendUvarint(w.dictRegion, uint64(len(comp)))
	w.dictRegion = append(w.dictRegion, comp...)
	w.dictRaw = w.dictRaw[:0]
}

// ---- segment reader --------------------------------------------------------

type segment struct {
	f                    *os.File
	id                   uint64 // seal-sequence id (== seg-%06d.dat); the chunk-LRU key (P5)
	dataCodec, dictCodec *codec
	idx                  []blockEntry
	biOff, dictOff       int64
	path                 string
	minDocid, maxDocid   int64       // forward-record docid span (B); set from segMeta on Open / at seal
	dictChunks           []dictChunk // built lazily for resolve (P3 index mode)
	dictOnce             sync.Once   // guards the one-time, build-once-read-only dictChunks init

	// Concurrency (P9/T8, concurrency.go): the live snapshot holds one PUBLISHED ref per segment;
	// a reader bumps an extra ref for its scan. retired is set when a merge drops the segment from
	// the live set (or Close drops the whole set); when refs reaches zero on a retired segment it is
	// torn down (close fd, and — UNLESS keepFile is set — unlink the file). keepFile distinguishes a
	// Close-retire (just close the fd; the file must survive for the next Open) from a merge-retire
	// (close + unlink the merged-away file). tornDown makes teardown idempotent under a reader/worker
	// race to the final decref.
	refs     atomic.Int64
	retired  atomic.Bool
	keepFile atomic.Bool
	tornDown atomic.Bool
}

// emptyDocidRange is the inverted "no forward records" span: min > max, so coversDocid is always
// false and forwardKeywords always skips the segment (spec §4 item B).
func emptyDocidRange() (min, max int64) { return math.MaxInt64, math.MinInt64 }

// coversDocid reports whether a forward record for docid could exist in this segment.
func (s *segment) coversDocid(docid int64) bool { return docid >= s.minDocid && docid <= s.maxDocid }

// dictChunk locates one compressed term-dict chunk for on-demand (index-mode) resolution.
type dictChunk struct {
	firstOrd uint32
	compOff  int64
	compLen  int
	rawLen   int
}

// openSegment opens a finished segment file and parses the 25-byte footer + block index.
// (port spike main.go:770-797: 25-byte footer with BOTH codec ids; firstKey is []byte.)
func openSegment(path string) *segment {
	f, _ := os.Open(path)
	fi, _ := f.Stat()
	sz := fi.Size()
	foot := make([]byte, 25)
	mustReadAt(f, foot, sz-25)
	biOff := int64(binary.BigEndian.Uint64(foot[0:8]))
	dictOff := int64(binary.BigEndian.Uint64(foot[8:16]))
	s := &segment{f: f, dataCodec: newCodec(foot[16]), dictCodec: newCodec(foot[17]),
		biOff: biOff, dictOff: dictOff, path: path}
	// parse block index [biOff, sz-25)
	bi := make([]byte, sz-25-biOff)
	mustReadAt(f, bi, biOff)
	p := 0
	nb, n := binary.Uvarint(bi[p:])
	p += n
	s.idx = make([]blockEntry, 0, nb)
	for i := uint64(0); i < nb; i++ {
		fkl, n := binary.Uvarint(bi[p:])
		p += n
		fk := append([]byte(nil), bi[p:p+int(fkl)]...)
		p += int(fkl)
		off, n := binary.Uvarint(bi[p:])
		p += n
		s.idx = append(s.idx, blockEntry{fk, int64(off)})
	}
	return s
}

func (s *segment) close() { s.f.Close() }

// blockBytes reads & decompresses data block i. (port spike main.go:799-807.)
func (s *segment) blockBytes(i int) []byte {
	hdr := make([]byte, 20)
	s.f.ReadAt(hdr, s.idx[i].off)
	rl, n := binary.Uvarint(hdr)
	cl, n2 := binary.Uvarint(hdr[n:])
	comp := make([]byte, cl)
	mustReadAt(s.f, comp, s.idx[i].off+int64(n+n2))
	return s.dataCodec.decompress(comp, int(rl))
}

// blockDiskSize returns the on-disk (compressed) size of data block i. (port spike main.go:808-814.)
func (s *segment) blockDiskSize(i int) int64 {
	hdr := make([]byte, 20)
	s.f.ReadAt(hdr, s.idx[i].off)
	_, n := binary.Uvarint(hdr)
	cl, n2 := binary.Uvarint(hdr[n:])
	return int64(n+n2) + int64(cl)
}

// readExternal reads & decompresses an external value's chunks. (port spike main.go:817-830.)
func (s *segment) readExternal(off int64, compLen int) []byte {
	buf := make([]byte, compLen)
	mustReadAt(s.f, buf, off)
	var raw []byte
	for p := 0; p < len(buf); {
		rl, n := binary.Uvarint(buf[p:])
		p += n
		cl, n2 := binary.Uvarint(buf[p:])
		p += n2
		raw = append(raw, s.dataCodec.decompress(buf[p:p+int(cl)], int(rl))...)
		p += int(cl)
	}
	return raw
}

// scanBlock parses records of a decompressed block. fn gets (key, inlineValue OR external
// pointer). Returning false stops. (port spike main.go:834-860.)
func scanBlock(blk []byte, fn func(key, inline []byte, extOff int64, extLen int, external bool) bool) {
	for p := 0; p < len(blk); {
		kl, n := binary.Uvarint(blk[p:])
		p += n
		key := blk[p : p+int(kl)]
		p += int(kl)
		flag := blk[p]
		p++
		if flag == 0 {
			vl, n2 := binary.Uvarint(blk[p:])
			p += n2
			val := blk[p : p+int(vl)]
			p += int(vl)
			if !fn(key, val, 0, 0, false) {
				return
			}
		} else {
			off, n2 := binary.Uvarint(blk[p:])
			p += n2
			cl, n3 := binary.Uvarint(blk[p:])
			p += n3
			if !fn(key, nil, int64(off), int(cl), true) {
				return
			}
		}
	}
}

// value resolves a record to its value bytes. (port spike main.go:863-868.)
func (s *segment) value(inline []byte, extOff int64, extLen int, external bool) []byte {
	if external {
		return s.readExternal(extOff, extLen)
	}
	return inline
}

// scanPrefix visits records whose key has the prefix [lo, hi). (port spike main.go:871-896;
// firstKey/key comparisons are now byte-wise.)
func (s *segment) scanPrefix(lo, hi []byte, fn func(key, value []byte)) {
	start := sort.Search(len(s.idx), func(i int) bool { return bytes.Compare(s.idx[i].firstKey, lo) > 0 }) - 1
	if start < 0 {
		start = 0
	}
	for bi := start; bi < len(s.idx); bi++ {
		if hi != nil && bytes.Compare(s.idx[bi].firstKey, hi) >= 0 {
			break
		}
		blk := s.blockBytes(bi)
		stop := false
		scanBlock(blk, func(key, inline []byte, extOff int64, extLen int, external bool) bool {
			if hi != nil && bytes.Compare(key, hi) >= 0 {
				stop = true
				return false
			}
			if hasPrefixBytes(key, lo) {
				fn(key, s.value(inline, extOff, extLen, external))
			}
			return true
		})
		if stop {
			break
		}
	}
}

// lookupForward point-reads the forward value for a forward key (one record), if present.
// (port spike main.go:899-909; takes the full []byte forward key.)
func (s *segment) lookupForward(key []byte) ([]byte, bool) {
	lo := key
	hi := prefixUpper(lo)
	var out []byte
	found := false
	s.scanPrefix(lo, hi, func(key, value []byte) {
		out = append([]byte(nil), value...)
		found = true
	})
	return out, found
}

// ensureDictIndex (index mode) scans only the term-dict chunk HEADERS (firstOrd, offset,
// lengths) — tiny, no strings held — so a resolve decompresses just the chunks holding the
// requested ordinals. Bounded memory. (port spike main.go:960-979.)
//
// The build runs exactly once under a sync.Once: dictChunks is append-once here and read-only
// thereafter, so concurrent resolves (P5 forwardKeywords, and the concurrent readers T8 will
// publish) all observe a fully-built, immutable slice. Once.Do establishes the happens-before
// that makes the populated dictChunks visible to every caller before Do returns.
func (s *segment) ensureDictIndex() {
	if s.dictOff == 0 {
		return
	}
	s.dictOnce.Do(func() {
		hdr := make([]byte, 30)
		for pos := s.dictOff; pos < s.biOff; {
			mustReadAt(s.f, hdr, pos)
			p := 0
			fo, a := binary.Uvarint(hdr[p:])
			p += a
			rl, b := binary.Uvarint(hdr[p:])
			p += b
			cl, c := binary.Uvarint(hdr[p:])
			p += c
			compOff := pos + int64(p)
			s.dictChunks = append(s.dictChunks, dictChunk{uint32(fo), compOff, int(cl), int(rl)})
			pos = compOff + int64(cl)
		}
	})
}

// resolveOrds maps requested term-id ordinals -> keyword strings via the term-dict chunk
// index (decompress only the chunks holding requested ordinals, with the dictCodec). This
// is the no-cache resolve; the Store-level chunk LRU is a later task (design T3) that wraps
// it. (port spike main.go:981-1013 resolveOrdsIndex, renamed resolveOrds.)
func (s *segment) resolveOrds(need map[uint32]struct{}) map[uint32]string {
	s.ensureDictIndex()
	res := make(map[uint32]string, len(need))
	byChunk := map[int][]uint32{}
	for o := range need {
		i := sort.Search(len(s.dictChunks), func(i int) bool { return s.dictChunks[i].firstOrd > o }) - 1
		if i < 0 {
			continue
		}
		byChunk[i] = append(byChunk[i], o)
	}
	for ci, ords := range byChunk {
		c := s.dictChunks[ci]
		comp := make([]byte, c.compLen)
		mustReadAt(s.f, comp, c.compOff)
		raw := s.dictCodec.decompress(comp, c.rawLen)
		want := make(map[uint32]struct{}, len(ords))
		for _, o := range ords {
			want[o] = struct{}{}
		}
		cur := c.firstOrd
		for q := 0; q < len(raw); {
			kl, m := binary.Uvarint(raw[q:])
			q += m
			if _, ok := want[cur]; ok {
				res[cur] = string(raw[q : q+int(kl)])
			}
			q += int(kl)
			cur++
		}
	}
	return res
}

// mustReadAt reads exactly len(b) bytes at off, panicking on a short/failed read (a corrupt
// segment is unrecoverable here; the reader API gains error returns in a later task).
func mustReadAt(f *os.File, b []byte, off int64) {
	if _, err := f.ReadAt(b, off); err != nil {
		panic(err)
	}
}
