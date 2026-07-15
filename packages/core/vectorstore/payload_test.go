package vectorstore

import (
	"reflect"
	"testing"
)

func TestPayload_RoundTrip_AllScalarKinds(t *testing.T) {
	p := Payload{
		"title":  StringValue("hello"),
		"count":  Int64Value(-42),
		"score":  Float64Value(3.5),
		"active": BoolValue(true),
		"big":    Int64Value(1 << 40),
	}
	b, err := encodePayload(p)
	requireNoError(t, err)
	got, err := decodePayload(b)
	requireNoError(t, err)
	if !reflect.DeepEqual(got, p) {
		t.Fatalf("round-trip mismatch:\n got=%#v\nwant=%#v", got, p)
	}
}

func TestPayload_RoundTrip_EmptyAndNil(t *testing.T) {
	for _, p := range []Payload{nil, {}} {
		b, err := encodePayload(p)
		requireNoError(t, err)
		got, err := decodePayload(b)
		requireNoError(t, err)
		if len(got) != 0 {
			t.Fatalf("empty payload round-trip = %#v, want empty", got)
		}
	}
}

func TestDecodePayload_RejectsUnknownVersion(t *testing.T) {
	if _, err := decodePayload([]byte{0xFF, 0x00}); err == nil {
		t.Fatal("decodePayload should reject an unknown format version byte")
	}
}

func TestDecodePayload_RejectsTruncated(t *testing.T) {
	good, _ := encodePayload(Payload{"k": StringValue("v")})
	if _, err := decodePayload(good[:len(good)-1]); err == nil {
		t.Fatal("decodePayload should reject a truncated blob")
	}
}

// TestDecodePayload_RejectsMalformedShapes exercises every malformed-blob branch
// of decodePayload (truncation at each field type, a bad count varint, a key that
// overruns the buffer, an unknown value kind, and trailing garbage) so the
// version-gated codec rejects corruption rather than returning a bogus Payload.
func TestDecodePayload_RejectsMalformedShapes(t *testing.T) {
	v := payloadFmtVersion
	cases := map[string][]byte{
		"bad count varint":       {v, 0x80},            // continuation bit set, no terminator
		"count past buffer":      {v, 0x01},            // claims 1 entry, no bytes follow
		"key len past buffer":    {v, 0x01, 0x05, 'a'}, // keyLen=5 but only 1 byte
		"missing kind byte":      {v, 0x01, 0x01, 'k'}, // key "k", then EOF before kind
		"unknown kind":           {v, 0x01, 0x01, 'k', 0xEE},
		"string len past buffer": {v, 0x01, 0x01, 'k', byte(KindString), 0x09, 'h'},
		"int64 truncated":        {v, 0x01, 0x01, 'k', byte(KindInt64), 0x00, 0x00},
		"float64 truncated":      {v, 0x01, 0x01, 'k', byte(KindFloat64), 0x00},
		"bool truncated":         {v, 0x01, 0x01, 'k', byte(KindBool)},
	}
	for name, blob := range cases {
		if _, err := decodePayload(blob); err == nil {
			t.Fatalf("%s: decodePayload accepted a malformed blob %v", name, blob)
		}
	}
	// Trailing garbage after a well-formed bool entry must be rejected.
	good, _ := encodePayload(Payload{"k": BoolValue(true)})
	if _, err := decodePayload(append(good, 0x00)); err == nil {
		t.Fatal("decodePayload should reject trailing garbage")
	}
}

// TestEncodePayload_AllKindsRoundTripIndividually round-trips each scalar kind on
// its own so encode/decode of every kind branch is exercised.
func TestEncodePayload_AllKindsRoundTripIndividually(t *testing.T) {
	for _, v := range []Value{
		StringValue(""), StringValue("abc"),
		Int64Value(0), Int64Value(-1), Int64Value(1 << 62),
		Float64Value(0), Float64Value(-2.25),
		BoolValue(false), BoolValue(true),
	} {
		b, err := encodePayload(Payload{"k": v})
		requireNoError(t, err)
		got, err := decodePayload(b)
		requireNoError(t, err)
		if !got["k"].equalForTest(v) {
			t.Fatalf("round-trip of %#v = %#v", v, got["k"])
		}
	}
}

// equalForTest is a test-only scalar comparator (the production equal() lands in
// a later phase-5 task with the predicate evaluator).
func (v Value) equalForTest(o Value) bool {
	if v.Kind != o.Kind {
		return false
	}
	switch v.Kind {
	case KindString:
		return v.Str == o.Str
	case KindInt64:
		return v.Int == o.Int
	case KindFloat64:
		return v.Flt == o.Flt
	default:
		return v.Bool == o.Bool
	}
}

func TestValue_Kinds(t *testing.T) {
	if StringValue("x").Kind != KindString || StringValue("x").Str != "x" {
		t.Fatal("StringValue")
	}
	if Int64Value(7).Kind != KindInt64 || Int64Value(7).Int != 7 {
		t.Fatal("Int64Value")
	}
	if Float64Value(1.5).Kind != KindFloat64 || Float64Value(1.5).Flt != 1.5 {
		t.Fatal("Float64Value")
	}
	if BoolValue(true).Kind != KindBool || !BoolValue(true).Bool {
		t.Fatal("BoolValue")
	}
}
