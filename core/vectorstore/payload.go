package vectorstore

import (
	"encoding/binary"
	"errors"
	"math"
	"sort"
)

// ValueKind tags a scalar payload value. Only scalars are supported in v1
// (architecture §6 model 方案1: structured + declared-filterable scalars;
// nested/array values are 留口 — rejected at the API edge, see store.go Put).
type ValueKind uint8

const (
	KindString  ValueKind = 1
	KindInt64   ValueKind = 2
	KindFloat64 ValueKind = 3
	KindBool    ValueKind = 4
)

// Value is a tagged scalar. Exactly one of Str/Int/Flt/Bool is meaningful,
// selected by Kind. Numbers are split into Int64 (for exact equality + integer
// range) and Float64 (for fractional range) so the Numeric attr index can keep a
// total order without float/int aliasing.
type Value struct {
	Kind ValueKind
	Str  string
	Int  int64
	Flt  float64
	Bool bool
}

func StringValue(s string) Value   { return Value{Kind: KindString, Str: s} }
func Int64Value(i int64) Value     { return Value{Kind: KindInt64, Int: i} }
func Float64Value(f float64) Value { return Value{Kind: KindFloat64, Flt: f} }
func BoolValue(b bool) Value       { return Value{Kind: KindBool, Bool: b} }

// Payload is a structured record annotation: a map of property name → scalar
// Value. Declared properties (CreateAttrIndex, later phase 5 task) are indexed
// for filtering; non-declared properties are stored and returned by Get but not
// indexed (architecture §6 "非声明字段仍存供返回、不索引").
type Payload map[string]Value

// payloadFmtVersion is the on-disk/in-WAL serialization version of a Payload
// blob. The Phase-1 opaque-[]byte payloads were UNVERSIONED raw bytes; bumping a
// version byte at the front lets decodePayload reject any pre-Phase-5 blob (the
// format predates production data — same clean-break precedent as manifest v1,
// manifest.go). Encoding: ver(1) | count(uvarint) | [ keyLen(uvarint)|key |
// kind(1)| value ]* with key order sorted for a canonical, byte-stable blob.
const payloadFmtVersion = byte(1)

var errBadPayload = errors.New("vectorstore: malformed payload blob")

func encodePayload(p Payload) ([]byte, error) {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buf := []byte{payloadFmtVersion}
	buf = appendUvarint(buf, uint64(len(keys)))
	for _, k := range keys {
		buf = appendUvarint(buf, uint64(len(k)))
		buf = append(buf, k...)
		v := p[k]
		buf = append(buf, byte(v.Kind))
		switch v.Kind {
		case KindString:
			buf = appendUvarint(buf, uint64(len(v.Str)))
			buf = append(buf, v.Str...)
		case KindInt64:
			var tmp [8]byte
			binary.LittleEndian.PutUint64(tmp[:], uint64(v.Int))
			buf = append(buf, tmp[:]...)
		case KindFloat64:
			var tmp [8]byte
			binary.LittleEndian.PutUint64(tmp[:], math.Float64bits(v.Flt))
			buf = append(buf, tmp[:]...)
		case KindBool:
			if v.Bool {
				buf = append(buf, 1)
			} else {
				buf = append(buf, 0)
			}
		default:
			return nil, errBadPayload
		}
	}
	return buf, nil
}

func decodePayload(b []byte) (Payload, error) {
	if len(b) == 0 {
		return nil, nil // an empty blob is an empty payload
	}
	if b[0] != payloadFmtVersion {
		return nil, errBadPayload
	}
	off := 1
	n, m := binary.Uvarint(b[off:])
	if m <= 0 {
		return nil, errBadPayload
	}
	off += m
	p := make(Payload, n)
	for i := uint64(0); i < n; i++ {
		kl, m := binary.Uvarint(b[off:])
		if m <= 0 || off+m+int(kl) > len(b) {
			return nil, errBadPayload
		}
		off += m
		key := string(b[off : off+int(kl)])
		off += int(kl)
		if off >= len(b) {
			return nil, errBadPayload
		}
		kind := ValueKind(b[off])
		off++
		switch kind {
		case KindString:
			sl, m := binary.Uvarint(b[off:])
			if m <= 0 || off+m+int(sl) > len(b) {
				return nil, errBadPayload
			}
			off += m
			p[key] = StringValue(string(b[off : off+int(sl)]))
			off += int(sl)
		case KindInt64:
			if off+8 > len(b) {
				return nil, errBadPayload
			}
			p[key] = Int64Value(int64(binary.LittleEndian.Uint64(b[off:])))
			off += 8
		case KindFloat64:
			if off+8 > len(b) {
				return nil, errBadPayload
			}
			p[key] = Float64Value(math.Float64frombits(binary.LittleEndian.Uint64(b[off:])))
			off += 8
		case KindBool:
			if off+1 > len(b) {
				return nil, errBadPayload
			}
			p[key] = BoolValue(b[off] != 0)
			off++
		default:
			return nil, errBadPayload
		}
	}
	if off != len(b) {
		return nil, errBadPayload // trailing garbage / truncation
	}
	return p, nil
}

func appendUvarint(b []byte, v uint64) []byte {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	return append(b, tmp[:n]...)
}
