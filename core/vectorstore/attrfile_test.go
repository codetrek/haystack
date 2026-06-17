package vectorstore

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func sealedWithPayloads(t *testing.T, dir string, payloads []Payload, dim int) *sealedSegment {
	t.Helper()
	head := newSegment(Cosine)
	for i, pl := range payloads {
		v := make([]float32, dim)
		v[0] = float32(i + 1)
		stored, norm := Cosine.prepare(v)
		head.append(int64(i+1), stored, norm, pl)
	}
	requireNoError(t, writeSealedSegment(dir, head, nil))
	ss, err := openSealedSegment(dir, Cosine)
	requireNoError(t, err)
	t.Cleanup(ss.close)
	return ss
}

func TestAttrFile_WriteOpenRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "seg-1-0")
	payloads := []Payload{
		{"color": StringValue("red"), "n": Int64Value(1)},
		{"color": StringValue("blue"), "n": Int64Value(2)},
		{"color": StringValue("red"), "n": Int64Value(3)},
	}
	ss := sealedWithPayloads(t, dir, payloads, 4)
	decls := map[string]AttrKind{"color": Keyword, "n": Numeric}
	ai := buildSegAttr(decls, ss.count(), func(slot int) Payload { p, _ := ss.payloadDecoded(slot); return p })
	requireNoError(t, writeAttrFile(dir, ai, ss.count()))

	got, err := openAttrFile(dir, ss, decls)
	requireNoError(t, err)
	bm, _ := got.evalSeg(Eq("color", StringValue("red")), ss.count(), func(slot int) Payload { p, _ := ss.payloadDecoded(slot); return p })
	c := bm.collect()
	sort.Ints(c)
	if !intsEqual(c, []int{0, 2}) {
		t.Fatalf("reopened attr eq = %v, want [0 2]", c)
	}
}

func TestAttrFile_MissingOrCorrupt_RebuildsFromPayload(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "seg-2-0")
	payloads := []Payload{
		{"color": StringValue("red")},
		{"color": StringValue("blue")},
	}
	ss := sealedWithPayloads(t, dir, payloads, 4)
	decls := map[string]AttrKind{"color": Keyword}
	// No attr.dat on disk: open must rebuild from payload.dat (derived floor).
	if _, err := os.Stat(filepath.Join(dir, "attr.dat")); !os.IsNotExist(err) {
		t.Fatal("precondition: attr.dat should not exist yet")
	}
	got, err := openAttrFile(dir, ss, decls)
	requireNoError(t, err)
	bm, _ := got.evalSeg(Eq("color", StringValue("blue")), ss.count(), func(slot int) Payload { p, _ := ss.payloadDecoded(slot); return p })
	if !intsEqual(bm.collect(), []int{1}) {
		t.Fatalf("rebuilt attr eq = %v, want [1]", bm.collect())
	}
}

// TestAttrFile_NumericRangeRoundTrip exercises the Numeric-property write/parse
// path (ordered values + posting bitmaps) and Range over the reopened index.
func TestAttrFile_NumericRangeRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "seg-3-0")
	payloads := []Payload{
		{"n": Int64Value(1)},
		{"n": Int64Value(2)},
		{"n": Int64Value(3)},
		{"n": Int64Value(4)},
	}
	ss := sealedWithPayloads(t, dir, payloads, 4)
	decls := map[string]AttrKind{"n": Numeric}
	ai := buildSegAttr(decls, ss.count(), func(slot int) Payload { p, _ := ss.payloadDecoded(slot); return p })
	requireNoError(t, writeAttrFile(dir, ai, ss.count()))

	got, err := openAttrFile(dir, ss, decls)
	requireNoError(t, err)
	bm, _ := got.evalSeg(Range("n", Int64Value(2), Int64Value(3)), ss.count(), func(slot int) Payload { p, _ := ss.payloadDecoded(slot); return p })
	c := bm.collect()
	sort.Ints(c)
	if !intsEqual(c, []int{1, 2}) {
		t.Fatalf("reopened numeric range = %v, want [1 2]", c)
	}
}

// TestAttrFile_AllValueKindsRoundTrip exercises every scalar Value kind through a
// Keyword posting's value serialization (appendValue/readValue/valueLess).
func TestAttrFile_AllValueKindsRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "seg-4-0")
	payloads := []Payload{
		{"s": StringValue("a"), "i": Int64Value(7), "f": Float64Value(1.5), "b": BoolValue(true)},
		{"s": StringValue("b"), "i": Int64Value(8), "f": Float64Value(2.5), "b": BoolValue(false)},
	}
	ss := sealedWithPayloads(t, dir, payloads, 4)
	decls := map[string]AttrKind{"s": Keyword, "i": Keyword, "f": Keyword, "b": Keyword}
	ai := buildSegAttr(decls, ss.count(), func(slot int) Payload { p, _ := ss.payloadDecoded(slot); return p })
	requireNoError(t, writeAttrFile(dir, ai, ss.count()))

	got, err := openAttrFile(dir, ss, decls)
	requireNoError(t, err)
	payloadAt := func(slot int) Payload { p, _ := ss.payloadDecoded(slot); return p }
	check := func(pred Predicate, want []int) {
		t.Helper()
		bm, _ := got.evalSeg(pred, ss.count(), payloadAt)
		c := bm.collect()
		sort.Ints(c)
		if !intsEqual(c, want) {
			t.Fatalf("evalSeg(%v) = %v, want %v", pred, c, want)
		}
	}
	check(Eq("s", StringValue("a")), []int{0})
	check(Eq("i", Int64Value(8)), []int{1})
	check(Eq("f", Float64Value(1.5)), []int{0})
	check(Eq("b", BoolValue(true)), []int{0})
	check(Eq("b", BoolValue(false)), []int{1})
}

// TestValueCodec_AllKindsAndCorruptKind exercises valueLess (kind-aware total
// order) and readValue round-trip for every scalar kind plus the corrupt-kind
// reject, lifting both functions over the go-cov function floor directly.
func TestValueCodec_AllKindsAndCorruptKind(t *testing.T) {
	// valueLess total order across kinds, in-kind ties, and the bool arm.
	if !valueLess(StringValue("a"), Int64Value(0)) { // Kind 1 < Kind 2
		t.Fatal("cross-kind order: String < Int64")
	}
	if !valueLess(StringValue("a"), StringValue("b")) {
		t.Fatal("String in-kind order")
	}
	if !valueLess(Int64Value(1), Int64Value(2)) {
		t.Fatal("Int64 in-kind order")
	}
	if !valueLess(Float64Value(1), Float64Value(2)) {
		t.Fatal("Float64 in-kind order")
	}
	if !valueLess(BoolValue(false), BoolValue(true)) || valueLess(BoolValue(true), BoolValue(false)) {
		t.Fatal("Bool in-kind order: false < true")
	}
	if valueLess(BoolValue(true), BoolValue(true)) {
		t.Fatal("equal bools are not less")
	}
	if valueLess(Value{Kind: 99}, Value{Kind: 99}) {
		t.Fatal("unknown-kind ties fall through to false")
	}

	// readValue round-trips every kind and rejects an unknown kind byte.
	for _, v := range []Value{StringValue("x"), Int64Value(-3), Float64Value(2.25), BoolValue(true)} {
		enc := appendValue(nil, v)
		got, off, ok := readValue(enc, 0)
		if !ok || off != len(enc) || got != v {
			t.Fatalf("readValue(%v) = %v off=%d ok=%v", v, got, off, ok)
		}
	}
	if _, _, ok := readValue([]byte{0x7F}, 0); ok {
		t.Fatal("readValue must reject an unknown kind byte")
	}
	if _, _, ok := readValue(nil, 0); ok {
		t.Fatal("readValue must reject an empty buffer")
	}
}

// TestParseAttrFile_RejectsCorrupt covers parseAttrFile's reject branches (too
// short / bad magic / count mismatch / truncated body) so openAttrFile falls back
// to the rebuild-from-payload floor on a damaged file.
func TestParseAttrFile_RejectsCorrupt(t *testing.T) {
	decls := map[string]AttrKind{"color": Keyword}
	good := func() []byte {
		var ai segAttrIndex
		ai.decls = decls
		ai.keyword = map[string]map[Value]*bitmap{"color": {StringValue("red"): {words: []uint64{0b101}}}}
		ai.numeric = map[string]*numericIndex{}
		dir := filepath.Join(t.TempDir(), "seg-g-0")
		requireNoError(t, os.MkdirAll(dir, 0755))
		requireNoError(t, writeAttrFile(dir, &ai, 3))
		b, err := os.ReadFile(filepath.Join(dir, "attr.dat"))
		requireNoError(t, err)
		return b
	}

	// too short
	if _, ok := parseAttrFile([]byte{1, 2, 3}, 3, decls); ok {
		t.Fatal("too-short attr.dat must be rejected")
	}
	// bad magic
	bad := good()
	bad[0] = 'X'
	if _, ok := parseAttrFile(bad, 3, decls); ok {
		t.Fatal("bad-magic attr.dat must be rejected")
	}
	// count mismatch (stale)
	if _, ok := parseAttrFile(good(), 99, decls); ok {
		t.Fatal("count-mismatched attr.dat must be rejected")
	}
	// truncated body (drop the trailing word bytes)
	tr := good()
	if _, ok := parseAttrFile(tr[:len(tr)-3], 3, decls); ok {
		t.Fatal("truncated attr.dat body must be rejected")
	}
	// a valid blob parses back to the same posting
	ai, ok := parseAttrFile(good(), 3, decls)
	if !ok {
		t.Fatal("a well-formed attr.dat must parse")
	}
	bm := ai.keyword["color"][StringValue("red")]
	if bm == nil || !intsEqual(bm.collect(), []int{0, 2}) {
		t.Fatalf("parsed posting = %v, want bits [0 2]", bm)
	}
}
