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
