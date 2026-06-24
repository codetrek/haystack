# invertedstore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended)
> or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax
> for tracking. This plan elaborates the design ([invertedstore-design.md](invertedstore-design.md)) and
> task breakdown ([invertedstore-tasks.md](invertedstore-tasks.md)) into bite-sized TDD steps. It is
> written **incrementally**: the foundational format tasks (P1–P4 = design T1) are detailed here;
> later tasks are detailed just-in-time once their dependencies' real interfaces exist.

**Goal:** A pebble-free, segment-based inverted index `core/invertedstore` that replaces
`core/invertedindex` — builds fast at bounded memory, ~25% smaller on disk via segment-local term-id
forward map, search ~1.8× faster than pebble.

**Architecture:** Write-once sorted runs + tiered background merge. A byte-capped in-memory head spills
immutable SSTable-style segments (data blocks of packed records + a redundant ordinal-ordered term-dict
region); a background merger reconciles newest-wins and remaps term-ids. See the design doc for the full
contract; this plan ports the validated `core/cmd/sortbench/main.go` spike into a production package and
fills the gaps the spike never exercised (tableId, int64, delete, recovery, concurrency).

**Tech Stack:** Go (module `github.com/codetrek/haystack/core`), `encoding/binary` varints,
`github.com/golang/snappy`, `github.com/klauspost/compress/zstd`, `container/list` (LRU). Tests: standard
`go test` — real round-trip/differential tests, **no mocks of the format**.

**Spike reference:** `core/cmd/sortbench/main.go` has working (int32, no-tableId) implementations of every
algorithm here; each step cites the function to port and the exact production adaptation.

---

## File Structure (`core/invertedstore/`)

| File | Responsibility |
| --- | --- |
| `keys.go` | key encode/decode (`keyType`,`tableId`,`keyword`/`docid`), `invertedValue`, `forwardValue`, delta-varint postings |
| `keys_test.go` | round-trip + golden tests for all encodings |
| `codec.go` | `snappy`/bounded-`zstd` codecs (`dataCodec`,`dictCodec`), codec ids |
| `segment.go` | `segWriter` (blocks, inline/external, term-dict region, footer), `segment` reader, `scanPrefix`, ord→string resolve |
| `segment_test.go` | segment write→read round-trip, golden footer, term-dict resolve |
| `manifest.go` | versioned MANIFEST encode/decode, table catalog |
| `store.go` | `Store`, `Open`, head buffer, spill, `Update`/`Batch`, `CreateTable`/`DeleteTable`, `Search`/`GetDocs` |
| `merge.go` | tiered merger, newest-wins reconciliation, ord→ord remap, term-dict rebuild, covering merge |
| `dictcache.go` | Store-level chunk LRU |
| `*_test.go` | per-file tests; plus `differential_test.go` vs `invertedindex` |

> **At execution time** create an isolated worktree off `main` (superpowers:using-git-worktrees) — do
> NOT build on the spike branch. Run tests from the `core/` module dir: `go test ./invertedstore/ -v`.

---

## P1 — Key & value encoding (design T1, §5)

The byte-layout contract. Everything else depends on these exact bytes.

**Files:**
- Create: `core/invertedstore/keys.go`
- Test: `core/invertedstore/keys_test.go`

- [ ] **Step 1: Write the failing test** — `core/invertedstore/keys_test.go`

```go
package invertedstore

import (
	"sort"
	"testing"
)

func TestKeyEncoding(t *testing.T) {
	// [I] keyType(1) tableId(4 BE) keyword
	ik := invertedKey(7, "return")
	if ik[0] != ktInverted || len(ik) != 5+len("return") {
		t.Fatalf("inverted key shape: % x", ik)
	}
	// [F] keyType(1) tableId(4 BE) docid(8 BE int64); [I] (0x01) must sort before [F] (0x02)
	fk := forwardKey(7, 1<<40) // a docid > 2^31 to prove int64 width
	if fk[0] != ktForward || len(fk) != 13 {
		t.Fatalf("forward key shape: % x", fk)
	}
	if string(invertedKey(7, "")) >= string(fk) {
		t.Fatal("[I] must sort before [F]")
	}
	// tableId is fixed-width so 2 vs 10 sort numerically and prefixes are unambiguous
	if string(invertedKey(2, "z")) >= string(invertedKey(10, "a")) {
		t.Fatal("fixed-width tableId mis-sorts")
	}
}

func TestPostingsRoundTrip(t *testing.T) {
	in := []int64{5, 1<<40, 1, 1, 9} // unsorted, dup, and > 2^31
	var got []int64
	decodeDocs(encodeDocs(in), func(d int64) { got = append(got, d) })
	want := []int64{1, 5, 9, 1 << 40} // sorted + deduped
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestInvertedValueRoundTrip(t *testing.T) {
	adds, dels := []int64{1, 4, 9}, []int64{4}
	ab, db := splitInvertedValue(encodeInvertedValue(adds, dels))
	var ga, gd []int64
	decodeDocs(ab, func(d int64) { ga = append(ga, d) })
	decodeDocs(db, func(d int64) { gd = append(gd, d) })
	if len(ga) != 3 || len(gd) != 1 || gd[0] != 4 {
		t.Fatalf("inverted value split wrong: adds=%v dels=%v", ga, gd)
	}
}

func TestForwardTombstoneNoAlias(t *testing.T) {
	// The blocker: a single-keyword doc whose only term-id is ordinal 0 must NOT
	// look like a delete. forwardValue = uvarint(nKw) delta-varint(ords); tombstone = nKw 0.
	live := encodeForward([]uint32{0}) // nKw=1, ord 0  → bytes 0x01 0x00
	if ords, deleted := decodeForward(live); deleted || len(ords) != 1 || ords[0] != 0 {
		t.Fatalf("single-ord-0 doc misread: ords=%v deleted=%v bytes=% x", ords, deleted, live)
	}
	tomb := forwardTombstone()
	if len(tomb) != 1 || tomb[0] != 0x00 {
		t.Fatalf("tombstone must be a single 0x00: % x", tomb)
	}
	if _, deleted := decodeForward(tomb); !deleted {
		t.Fatal("tombstone not detected as delete")
	}
	// round-trip a multi-keyword doc, order-independent
	in := []uint32{9, 0, 4}
	ords, deleted := decodeForward(encodeForward(in))
	sort.Slice(ords, func(i, j int) bool { return ords[i] < ords[j] })
	if deleted || len(ords) != 3 || ords[0] != 0 || ords[1] != 4 || ords[2] != 9 {
		t.Fatalf("forward round-trip wrong: %v", ords)
	}
}
```

- [ ] **Step 2: Run the tests, verify they fail to compile** (symbols undefined)

Run: `cd core && go test ./invertedstore/ -run 'TestKey|TestPostings|TestInverted|TestForward' -v`
Expected: FAIL — `undefined: invertedKey` etc.

- [ ] **Step 3: Write `core/invertedstore/keys.go`**

Port the spike's `encodeDocs`/`decodeDocsInto`/`encodeInvertedValue`/`splitInvertedValue`
(`main.go:443-486`) to **int64**, and the keys (`main.go:529-535`) with a **4-byte BE tableId** +
**8-byte int64 docid**; add the **nKw-prefixed** forward value (the spike's `encodeTermIds` had no
prefix — that was the aliasing bug).

```go
package invertedstore

import (
	"encoding/binary"
	"sort"
)

const (
	ktInverted = byte(0x01) // [I] tableId keyword -> invertedValue  (sorts BEFORE forward)
	ktForward  = byte(0x02) // [F] tableId docid   -> forwardValue
)

func appendUvarint(b []byte, v uint64) []byte {
	var t [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(t[:], v)
	return append(b, t[:n]...)
}

func invertedKey(tableId uint32, keyword string) []byte {
	b := make([]byte, 5+len(keyword))
	b[0] = ktInverted
	binary.BigEndian.PutUint32(b[1:5], tableId)
	copy(b[5:], keyword)
	return b
}

func forwardKey(tableId uint32, docid int64) []byte {
	b := make([]byte, 13)
	b[0] = ktForward
	binary.BigEndian.PutUint32(b[1:5], tableId)
	binary.BigEndian.PutUint64(b[5:13], uint64(docid))
	return b
}

// encodeDocs: sort + dedup + delta-varint (gaps are non-negative). int64 (production docid).
func encodeDocs(docs []int64) []byte {
	sort.Slice(docs, func(i, j int) bool { return docs[i] < docs[j] })
	buf := make([]byte, 0, len(docs)+len(docs)/2)
	var prev int64
	first := true
	for _, d := range docs {
		if !first && d == prev {
			continue
		}
		delta := d
		if !first {
			delta = d - prev
		}
		buf = appendUvarint(buf, uint64(delta))
		prev, first = d, false
	}
	return buf
}

func decodeDocs(b []byte, fn func(int64)) {
	var cur uint64
	for i := 0; i < len(b); {
		d, n := binary.Uvarint(b[i:])
		if n <= 0 {
			return
		}
		cur += d
		fn(int64(cur))
		i += n
	}
}

// invertedValue := uvarint(addsByteLen) delta-varint(adds) delta-varint(dels)  (dels run to end)
func encodeInvertedValue(adds, dels []int64) []byte {
	ab := encodeDocs(adds)
	out := appendUvarint(nil, uint64(len(ab)))
	out = append(out, ab...)
	out = append(out, encodeDocs(dels)...)
	return out
}

func splitInvertedValue(v []byte) (adds, dels []byte) {
	al, n := binary.Uvarint(v)
	return v[n : n+int(al)], v[n+int(al):]
}

// forwardValue := uvarint(nKw) delta-varint(sorted term-ids); nKw==0 (single 0x00) ⇒ tombstone.
// A live doc has nKw>=1, so it can never alias the tombstone (even term-id 0 ⇒ 0x01 0x00).
func encodeForward(ords []uint32) []byte {
	cp := append([]uint32(nil), ords...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	out := appendUvarint(nil, uint64(len(cp)))
	var prev uint32
	first := true
	for _, o := range cp {
		delta := uint64(o)
		if !first {
			delta = uint64(o - prev)
		}
		out = appendUvarint(out, delta)
		prev, first = o, false
	}
	return out
}

func forwardTombstone() []byte { return []byte{0x00} } // nKw==0

func decodeForward(v []byte) (ords []uint32, deleted bool) {
	n, p := binary.Uvarint(v)
	if n == 0 {
		return nil, true
	}
	ords = make([]uint32, 0, n)
	var cur uint64
	for i := uint64(0); i < n; i++ {
		d, m := binary.Uvarint(v[p:])
		p += m
		cur += d
		ords = append(ords, uint32(cur))
	}
	return ords, false
}
```

- [ ] **Step 4: Run the tests, verify they pass**

Run: `cd core && go test ./invertedstore/ -run 'TestKey|TestPostings|TestInverted|TestForward' -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Add a golden byte fixture test** (lock the wire format)

Append to `keys_test.go`:

```go
func TestForwardGoldenBytes(t *testing.T) {
	// nKw=1 then ord 0 → exactly 0x01 0x00 (the anti-alias guarantee, frozen)
	got := encodeForward([]uint32{0})
	if len(got) != 2 || got[0] != 0x01 || got[1] != 0x00 {
		t.Fatalf("forward golden changed: % x", got)
	}
}
```

Run: `cd core && go test ./invertedstore/ -run TestForwardGolden -v` → PASS.

- [ ] **Step 6: Commit**

```bash
git add core/invertedstore/keys.go core/invertedstore/keys_test.go
git commit -m "feat(invertedstore): key & value encoding (int64, 4B tableId, nKw forward)"
```

---

## P2 — Codecs (design T1, §7)

Pluggable block codec: `none`/`snappy`/bounded-`zstd`. Each segment persists its `dataCodecId` and
`dictCodecId` so a reader of mixed L0(snappy)/merged(zstd)/dict(zstd) segments never guesses.

**Files:**
- Create: `core/invertedstore/codec.go`
- Test: `core/invertedstore/codec_test.go`

- [ ] **Step 1: Write the failing test** — `core/invertedstore/codec_test.go`

```go
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
```

- [ ] **Step 2: Run, verify fail** — `cd core && go test ./invertedstore/ -run TestCodec -v` → FAIL (undefined).

- [ ] **Step 3: Write `core/invertedstore/codec.go`** — port spike `main.go:389-439`, unchanged except
naming (`codecNone/Snappy/Zstd` constants), keeping the **bounded** zstd (`WithEncoderConcurrency(1)` +
128 KiB window — the spike proved the default spins up GOMAXPROCS encoders → 766 MiB).

```go
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

func (c *codec) decompress(src []byte, rawLen int) []byte {
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
```

> Note: the spike `panic`s on codec errors via `must`; production should return errors up the segment
> reader. Keep `panic` for P2 (a corrupt segment is unrecoverable) and revisit in P3 if the reader API
> returns errors.

- [ ] **Step 4: Run, verify pass** — `cd core && go test ./invertedstore/ -run TestCodec -v` → PASS.

- [ ] **Step 5: Commit**

```bash
git add core/invertedstore/codec.go core/invertedstore/codec_test.go
git commit -m "feat(invertedstore): bounded snappy/zstd block codecs"
```

---

## P3 — Segment writer/reader + term-dict region (design T1, §5)

The immutable segment: data blocks of packed records (inline-small / external-large values), the
ordinal-ordered term-dict region, block index, **25-byte footer with BOTH codec ids**. Plus the
ord→string resolve via the term-dict chunk index (no cache yet — the Store-level LRU is a later task,
design T3).

**Files:**
- Create: `core/invertedstore/segment.go`
- Test: `core/invertedstore/segment_test.go`

- [ ] **Step 1: Write the failing test** — `core/invertedstore/segment_test.go`

```go
package invertedstore

import (
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
```

- [ ] **Step 2: Run, verify fail** — `cd core && go test ./invertedstore/ -run TestSegment -v` → FAIL (undefined).

- [ ] **Step 3: Port the writer/reader mechanics from the spike** into `core/invertedstore/segment.go`.
Port these spike functions **verbatim except keys are now `[]byte` (not `string`)**:
`writeExternalValue`, `addEntry`, `flushBlock`, `blockBytes`, `blockDiskSize`, `readExternal`,
`scanBlock`, `value`, `scanPrefix`, `lookupForward`, `prefixUpper`, `hasPrefixBytes`
(`main.go:566-863`). They are unchanged in logic — the records already carry the full key bytes.

- [ ] **Step 4: Write the CHANGED pieces** — the `segWriter`/`segment` structs (two codecs), `finish`
(term-dict region + **25-byte** footer), `writeTermDict` (uses `dictCodec`, `firstOrd` headers),
`openSegment` (reads both codec ids), and the chunk-index resolve. Add to `segment.go`:

```go
type segWriter struct {
	f                     *os.File
	bw                    *bufio.Writer
	off                   int64
	dataCodec, dictCodec  *codec
	blockTarget, chunk    int
	threshold, dictChunk  int
	termid                bool
	idx                   []blockEntry
	blkRaw                []byte
	blkFirst              []byte
	blkHave               bool
}
type blockEntry struct {
	firstKey []byte
	off      int64
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

func (w *segWriter) finish(path string) *segment {
	w.flushBlock()
	var dictOff int64
	if w.termid {
		w.bw.Flush() // blocks must be on disk before we re-read them
		dictOff = w.off
		w.writeTermDict() // re-reads own [I] blocks → ordinal-ordered strings, bounded memory
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
```

`writeTermDict` (port spike `main.go:657-706`, change `w.cod` → `w.dictCodec`, `w.blockTarget` →
`w.dictChunk`; it already emits `uvarint(firstOrd) uvarint(rawLen) uvarint(compLen) codec(strings)`).

`openSegment` (port spike `main.go:709-738`, but read a **25-byte** footer):

```go
type segment struct {
	f                    *os.File
	dataCodec, dictCodec *codec
	idx                  []blockEntry
	biOff, dictOff       int64
	path                 string
	dictChunks           []dictChunk // built lazily for resolve (P3 index mode)
	dictBuilt            bool
}

func openSegment(path string) *segment {
	f, _ := os.Open(path)
	fi, _ := f.Stat()
	sz := fi.Size()
	foot := make([]byte, 25)
	f.ReadAt(foot, sz-25)
	biOff := int64(binary.BigEndian.Uint64(foot[0:8]))
	dictOff := int64(binary.BigEndian.Uint64(foot[8:16]))
	s := &segment{f: f, dataCodec: newCodec(foot[16]), dictCodec: newCodec(foot[17]),
		biOff: biOff, dictOff: dictOff, path: path}
	// parse block index [biOff, sz-25) exactly as spike main.go:720-737
	// ... (port verbatim; firstKey/off pairs) ...
	return s
}
func (s *segment) close() { s.f.Close() }
```

`ensureDictIndex` + `resolveOrds` (port spike `main.go:953-1009` index-mode `ensureDictIndex` +
`resolveOrdsIndex`, renamed `resolveOrds`; uses `s.dictChunk` headers and `s.dictCodec`). This is the
no-cache resolve; the Store-level chunk LRU is a later task (design T3) that wraps it.

- [ ] **Step 5: Run, verify pass** — `cd core && go test ./invertedstore/ -run 'TestSegment|TestKey|TestPostings|TestInverted|TestForward|TestCodec' -v` → all PASS.

- [ ] **Step 6: Add a golden footer test** — append to `segment_test.go`: write a segment, read its last
25 bytes, assert `foot[18:25] == "SRSEG\x00\x00"` and that `foot[16]/foot[17]` are the data/dict codec
ids. Run → PASS.

- [ ] **Step 7: Commit**

```bash
git add core/invertedstore/segment.go core/invertedstore/segment_test.go
git commit -m "feat(invertedstore): segment writer/reader + term-dict region (25B footer, 2 codecs)"
```

---

## Self-review (writing-plans)

- **Spec coverage (design T1 = §5 format):** key/value encoding (P1), codecs (P2/§7), segment blocks +
  inline/external + term-dict region + 25B footer + scanPrefix + ord→string resolve (P3). ✓ The format
  contract is fully covered. Forward-tombstone non-aliasing (the blocker) is locked by a golden test (P1
  Step 5). tableId(4 BE) + int64 docid are exercised (P1 uses docid `1<<40`).
- **Type consistency:** `newCodec(id byte)`, `segment.dataCodec/dictCodec`, `decodeForward → (ords, deleted)`,
  `resolveOrds(map[uint32]struct{}) map[uint32]string` are used consistently across P1–P3 and match the
  later tasks' references in the task breakdown.
- **No placeholders:** every step has runnable test code, exact `go test` commands with expected
  PASS/FAIL, and either complete new code or a precise "port spike `main.go:X-Y`, change A→B" with the
  changed code shown. The spike functions cited are real, in-repo, and unchanged-in-logic ports.

## Next tasks (detailed just-in-time)

P4+ (design T2–T11) are detailed once P1–P3 land and their real interfaces exist — writing complete code
for the head/spill/merge/concurrency now would speculate on the segment API this task produces. The task
breakdown ([invertedstore-tasks.md](invertedstore-tasks.md)) holds their deliverables + acceptance;
each becomes a P-section (TDD steps) just before it is executed. Order: P4 head+spill+MANIFEST (T2) → P5
forward+chunk-LRU (T3) → P6 search/GetDocs (T4) → P7 Update/Batch (T5) → P8 merger+covering-merge (T6) →
P9 concurrency (T8) → P10 interface+wiring+migration (T9) → P11 recovery (T10) → P12 diff/regression (T11).

## Execution handoff

Plan saved to `docs/design/invertedstore-plan.md`. P1–P3 (the format contract) are execution-ready.
Two options:

1. **Subagent-Driven (recommended)** — `superpowers:subagent-driven-development`: a fresh subagent per
   P-task + two-stage review between tasks, in a worktree off `main`.
2. **Inline** — `superpowers:executing-plans`: execute P1→P3 here with checkpoints.


