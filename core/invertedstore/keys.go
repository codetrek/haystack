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

// encodeDocs: sort + dedup + delta-varint (gaps are non-negative). int64 (production docid).
//
// It COPIES the input before sorting (copy-before-sort), so a caller may pass a slice it still
// owns/shares without having it reordered out from under it. The merge path (merge.go) builds a
// reconciled posting list and re-encodes it; copy-before-sort guarantees the merge never mutates a
// source-derived slice it might re-read, and is cheap relative to the delta-varint it already does.
func encodeDocs(docs []int64) []byte {
	buf, _ := appendDeltaDocs(nil, nil, docs)
	return buf
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

// encodeScratch holds the reusable byte/int scratch buffers for the merge value-encode path (C.3).
// The merge owns one of these; each encode reuses the same backing arrays. Safe because addEntry
// copies the produced value into blkRaw immediately (the value is never retained across encodes), and
// the encoded output is segment/merge scratch — NOT head storage (so it does not violate the F
// read-only-detached-head invariant).
type encodeScratch struct {
	val  []byte   // the assembled inverted/forward value
	docs []byte   // delta-varint of the adds sub-list (the length-prefixed region)
	srt  []int64  // copy-before-sort scratch for the int64 doc lists
	ord  []uint32 // copy-before-sort scratch for forward ords
}

// appendDeltaDocs sorts a copy of docs (into srt) and appends their dedup'd delta-varint to dst,
// returning (dst, srt) so both backing arrays are reused. It never reorders docs (copy-before-sort).
func appendDeltaDocs(dst []byte, srt, docs []int64) ([]byte, []int64) {
	cp := append(srt[:0], docs...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	var prev int64
	first := true
	for _, d := range cp {
		if !first && d == prev {
			continue
		}
		delta := d
		if !first {
			delta = d - prev
		}
		dst = appendUvarint(dst, uint64(delta))
		prev, first = d, false
	}
	return dst, cp
}

// encodeInvertedValueInto builds an inverted value (uvarint(addsLen) adds-delta dels-delta) into
// e.val[:0], reusing e's buffers — the C.3 reuse of encodeInvertedValue for the merge path.
func (e *encodeScratch) encodeInvertedValueInto(adds, dels []int64) []byte {
	// adds first (into e.docs) so we can length-prefix it; then dels appended straight into e.val.
	ab, srt := appendDeltaDocs(e.docs[:0], e.srt, adds)
	e.docs, e.srt = ab, srt
	out := appendUvarint(e.val[:0], uint64(len(ab)))
	out = append(out, ab...)
	out, srt = appendDeltaDocs(out, e.srt, dels)
	e.srt = srt
	e.val = out
	return out
}

// encodeForwardInto builds a forward value (uvarint(nKw) sorted-ord-delta) into e.val[:0], reusing e's
// buffers — the C.3 reuse of encodeForward for the merge path.
func (e *encodeScratch) encodeForwardInto(ords []uint32) []byte {
	cp := append(e.ord[:0], ords...)
	e.ord = cp
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	out := appendUvarint(e.val[:0], uint64(len(cp)))
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
	e.val = out
	return out
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
