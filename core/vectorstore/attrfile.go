package vectorstore

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"sort"
)

// attr.dat is the 5th sealed-segment file: the per-segment derived attr index
// (segAttrIndex) serialized as dense per-value bitmaps. It is DERIVED and
// rebuildable from payload.dat — so it is rewritten with the segment on
// merge/compact and, on a missing/corrupt/stale file, openAttrFile silently
// REBUILDS it from the segment's payloads (the derived floor, architecture §6/§E).
//
// Layout: attrHeader (segPageSize-padded) | nProps(4) | per prop:
//   kind(1) | propLen(2) | prop | nEntries(4) | [ value | bitmap ]*
// where value is a tagged scalar (kind(1) | body) and bitmap is nWords(4) | words.
// Properties are written in sorted order and Keyword/Numeric values in a stable
// (sorted) order, so the file is byte-deterministic for a given index.
//
// DELIBERATE DEVIATION (§Architecture 5): the postings are plain DENSE bitmaps,
// NOT roaring serialization. Per-value cardinality is bounded by the segment's
// ≤ maxSegSize rows, so a flat []uint64 is both adequate and faster on the
// per-candidate graph∩S traversal gate; roaring is deferred (no new dependency).

func writeAttrFile(dir string, ai *segAttrIndex, n int) error {
	path := filepath.Join(dir, "attr.dat")
	f, err := fsCreate(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hdr := make([]byte, segPageSize)
	copy(hdr[0:4], magicAttr[:])
	binary.LittleEndian.PutUint64(hdr[8:16], uint64(n))
	if _, err := f.Write(hdr); err != nil {
		return err
	}
	body := []byte{}
	// Stable property order for a deterministic file.
	props := make([]string, 0, len(ai.decls))
	for p := range ai.decls {
		props = append(props, p)
	}
	sort.Strings(props)
	body = appendU32(body, uint32(len(props)))
	for _, prop := range props {
		kind := ai.decls[prop]
		body = append(body, byte(kind))
		body = appendU16(body, uint16(len(prop)))
		body = append(body, prop...)
		if kind == Keyword {
			m := ai.keyword[prop]
			body = appendU32(body, uint32(len(m)))
			for _, v := range sortedValues(m) {
				body = appendValue(body, v)
				body = appendBitmap(body, m[v])
			}
		} else {
			ni := ai.numeric[prop]
			body = appendU32(body, uint32(len(ni.values)))
			for i, x := range ni.values {
				body = appendValue(body, Float64Value(x))
				body = appendBitmap(body, ni.posts[i])
			}
		}
	}
	if _, err := f.Write(body); err != nil {
		return err
	}
	return f.Sync()
}

// openAttrFile loads attr.dat into a segAttrIndex. On a missing/corrupt/count-
// mismatched file it REBUILDS from the segment's payloads (derived floor, §6).
func openAttrFile(dir string, ss *sealedSegment, decls map[string]AttrKind) (*segAttrIndex, error) {
	rebuild := func() *segAttrIndex {
		return buildSegAttr(decls, ss.count(), func(slot int) Payload {
			p, _ := ss.payloadDecoded(slot)
			return p
		})
	}
	b, err := os.ReadFile(filepath.Join(dir, "attr.dat"))
	if err != nil {
		return rebuild(), nil // missing → rebuild
	}
	ai, ok := parseAttrFile(b, ss.count(), decls)
	if !ok {
		return rebuild(), nil // corrupt / stale → rebuild
	}
	return ai, nil
}

func parseAttrFile(b []byte, n int, decls map[string]AttrKind) (*segAttrIndex, bool) {
	if len(b) < segPageSize+4 {
		return nil, false
	}
	if string(b[0:4]) != string(magicAttr[:]) {
		return nil, false
	}
	if binary.LittleEndian.Uint64(b[8:16]) != uint64(n) {
		return nil, false
	}
	off := segPageSize
	ai := &segAttrIndex{decls: decls, keyword: map[string]map[Value]*bitmap{}, numeric: map[string]*numericIndex{}}
	nProps := int(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	for i := 0; i < nProps; i++ {
		if off >= len(b) {
			return nil, false
		}
		kind := AttrKind(b[off])
		off++
		if off+2 > len(b) {
			return nil, false
		}
		pl := int(binary.LittleEndian.Uint16(b[off:]))
		off += 2
		if off+pl > len(b) {
			return nil, false
		}
		prop := string(b[off : off+pl])
		off += pl
		if off+4 > len(b) {
			return nil, false
		}
		nEntries := int(binary.LittleEndian.Uint32(b[off:]))
		off += 4
		if kind == Keyword {
			m := map[Value]*bitmap{}
			for e := 0; e < nEntries; e++ {
				var v Value
				var bm *bitmap
				var ok bool
				if v, off, ok = readValue(b, off); !ok {
					return nil, false
				}
				if bm, off, ok = readBitmap(b, off); !ok {
					return nil, false
				}
				m[v] = bm
			}
			ai.keyword[prop] = m
		} else {
			ni := &numericIndex{}
			for e := 0; e < nEntries; e++ {
				var v Value
				var bm *bitmap
				var ok bool
				if v, off, ok = readValue(b, off); !ok {
					return nil, false
				}
				if bm, off, ok = readBitmap(b, off); !ok {
					return nil, false
				}
				ni.values = append(ni.values, v.Flt)
				ni.posts = append(ni.posts, bm)
			}
			ai.numeric[prop] = ni
		}
	}
	return ai, true
}

// sortedValues returns the keys of a value→bitmap map in a stable, kind-aware
// order so attr.dat is byte-deterministic for a given index.
func sortedValues(m map[Value]*bitmap) []Value {
	out := make([]Value, 0, len(m))
	for v := range m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return valueLess(out[i], out[j]) })
	return out
}

// valueLess is a total order over scalar Values: first by Kind, then by the
// in-kind natural order. Only used for a deterministic on-disk value order.
func valueLess(a, b Value) bool {
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	switch a.Kind {
	case KindString:
		return a.Str < b.Str
	case KindInt64:
		return a.Int < b.Int
	case KindFloat64:
		return a.Flt < b.Flt
	case KindBool:
		return !a.Bool && b.Bool
	}
	return false
}

func appendU16(b []byte, v uint16) []byte {
	var tmp [2]byte
	binary.LittleEndian.PutUint16(tmp[:], v)
	return append(b, tmp[:]...)
}

// appendValue serializes a tagged scalar: kind(1) | body. String bodies are
// length-prefixed (varint); numeric/bool bodies are fixed width.
func appendValue(b []byte, v Value) []byte {
	b = append(b, byte(v.Kind))
	switch v.Kind {
	case KindString:
		b = appendUvarint(b, uint64(len(v.Str)))
		b = append(b, v.Str...)
	case KindInt64:
		var tmp [8]byte
		binary.LittleEndian.PutUint64(tmp[:], uint64(v.Int))
		b = append(b, tmp[:]...)
	case KindFloat64:
		var tmp [8]byte
		binary.LittleEndian.PutUint64(tmp[:], math.Float64bits(v.Flt))
		b = append(b, tmp[:]...)
	case KindBool:
		if v.Bool {
			b = append(b, 1)
		} else {
			b = append(b, 0)
		}
	}
	return b
}

// readValue decodes a tagged scalar written by appendValue, returning the value,
// the new offset, and ok=false on any out-of-bounds read (corrupt file).
func readValue(b []byte, off int) (Value, int, bool) {
	if off >= len(b) {
		return Value{}, off, false
	}
	kind := ValueKind(b[off])
	off++
	switch kind {
	case KindString:
		sl, m := binary.Uvarint(b[off:])
		if m <= 0 || off+m+int(sl) > len(b) {
			return Value{}, off, false
		}
		off += m
		s := string(b[off : off+int(sl)])
		off += int(sl)
		return StringValue(s), off, true
	case KindInt64:
		if off+8 > len(b) {
			return Value{}, off, false
		}
		v := Int64Value(int64(binary.LittleEndian.Uint64(b[off:])))
		off += 8
		return v, off, true
	case KindFloat64:
		if off+8 > len(b) {
			return Value{}, off, false
		}
		v := Float64Value(math.Float64frombits(binary.LittleEndian.Uint64(b[off:])))
		off += 8
		return v, off, true
	case KindBool:
		if off+1 > len(b) {
			return Value{}, off, false
		}
		v := BoolValue(b[off] != 0)
		off++
		return v, off, true
	default:
		return Value{}, off, false
	}
}

// appendBitmap serializes a dense bitmap as nWords(4) | words (little-endian).
func appendBitmap(b []byte, bm *bitmap) []byte {
	b = appendU32(b, uint32(len(bm.words)))
	for _, w := range bm.words {
		var tmp [8]byte
		binary.LittleEndian.PutUint64(tmp[:], w)
		b = append(b, tmp[:]...)
	}
	return b
}

// readBitmap decodes a dense bitmap written by appendBitmap, bounds-checking the
// word count against the remaining buffer (corrupt file → ok=false).
func readBitmap(b []byte, off int) (*bitmap, int, bool) {
	if off+4 > len(b) {
		return nil, off, false
	}
	nWords := int(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	if nWords < 0 || off+nWords*8 > len(b) {
		return nil, off, false
	}
	words := make([]uint64, nWords)
	for i := 0; i < nWords; i++ {
		words[i] = binary.LittleEndian.Uint64(b[off:])
		off += 8
	}
	return &bitmap{words: words}, off, true
}
