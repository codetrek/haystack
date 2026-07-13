package vectorstore

import (
	"sort"
	"testing"
)

func TestSegAttr_BuildAndEvalSeg_KeywordAndNumeric(t *testing.T) {
	// 6 slots; declare color(Keyword) + n(Numeric).
	payloads := []Payload{
		{"color": StringValue("red"), "n": Int64Value(1)},   // 0
		{"color": StringValue("blue"), "n": Int64Value(2)},  // 1
		{"color": StringValue("red"), "n": Int64Value(3)},   // 2
		{"color": StringValue("green"), "n": Int64Value(4)}, // 3
		{"color": StringValue("red"), "n": Int64Value(5)},   // 4
		{"color": StringValue("blue"), "n": Int64Value(6)},  // 5
	}
	decls := map[string]AttrKind{"color": Keyword, "n": Numeric}
	ai := buildSegAttr(decls, len(payloads), func(slot int) Payload { return payloads[slot] })

	check := func(pred Predicate, want []int) {
		t.Helper()
		bm, ok := ai.evalSeg(pred, len(payloads), func(slot int) Payload { return payloads[slot] })
		if !ok {
			t.Fatalf("evalSeg unexpectedly returned ok=false for %v", pred)
		}
		got := bm.collect()
		sort.Ints(got)
		if !intsEqual(got, want) {
			t.Fatalf("evalSeg(%v) = %v, want %v", pred, got, want)
		}
	}
	check(Eq("color", StringValue("red")), []int{0, 2, 4})
	check(In("color", StringValue("blue"), StringValue("green")), []int{1, 3, 5})
	check(Range("n", Int64Value(3), Int64Value(5)), []int{2, 3, 4})
	check(And(Eq("color", StringValue("red")), Range("n", Int64Value(3), Int64Value(10))), []int{2, 4})
}

func TestSegAttr_NonDeclaredField_ResidualScan(t *testing.T) {
	payloads := []Payload{
		{"color": StringValue("red"), "extra": StringValue("x")},
		{"color": StringValue("red"), "extra": StringValue("y")},
	}
	decls := map[string]AttrKind{"color": Keyword} // "extra" NOT declared
	ai := buildSegAttr(decls, len(payloads), func(slot int) Payload { return payloads[slot] })
	// Filter on a non-declared field falls back to a residual payload scan and is
	// still correct (architecture §6: non-declared fields stored but not indexed).
	bm, ok := ai.evalSeg(Eq("extra", StringValue("y")), len(payloads), func(slot int) Payload { return payloads[slot] })
	if !ok {
		t.Fatal("residual eval on non-declared field must still succeed")
	}
	if !intsEqual(bm.collect(), []int{1}) {
		t.Fatalf("residual eval = %v, want [1]", bm.collect())
	}
}

// TestSegAttr_EvalEq_Branches covers evalEq's three paths: a declared-Keyword miss
// (value absent from postings), a declared-Numeric Eq hit (binary-searched), and a
// declared-Numeric Eq miss (value not present).
func TestSegAttr_EvalEq_Branches(t *testing.T) {
	payloads := []Payload{
		{"color": StringValue("red"), "n": Int64Value(10)},
		{"color": StringValue("blue"), "n": Int64Value(20)},
	}
	decls := map[string]AttrKind{"color": Keyword, "n": Numeric}
	ai := buildSegAttr(decls, len(payloads), func(slot int) Payload { return payloads[slot] })
	at := func(slot int) Payload { return payloads[slot] }

	// Keyword miss: a value never stored → empty bitmap.
	if bm, _ := ai.evalSeg(Eq("color", StringValue("green")), 2, at); bm.count() != 0 {
		t.Fatalf("Keyword Eq miss = %v, want empty", bm.collect())
	}
	// Numeric Eq hit via the ordered structure.
	if bm, _ := ai.evalSeg(Eq("n", Int64Value(20)), 2, at); !intsEqual(bm.collect(), []int{1}) {
		t.Fatalf("Numeric Eq hit = %v, want [1]", bm.collect())
	}
	// Numeric Eq miss: a value not present in the sorted set.
	if bm, _ := ai.evalSeg(Eq("n", Int64Value(99)), 2, at); bm.count() != 0 {
		t.Fatalf("Numeric Eq miss = %v, want empty", bm.collect())
	}
	// Numeric Eq with a non-numeric query value → empty (numeric() returns !ok).
	if bm, _ := ai.evalSeg(Eq("n", StringValue("nope")), 2, at); bm.count() != 0 {
		t.Fatalf("Numeric Eq with non-numeric query = %v, want empty", bm.collect())
	}
}

// TestSegAttr_EvalRange_ResidualAndEmpty covers evalRange's residual fallback (a
// Range on a non-declared field) and the empty-result span.
func TestSegAttr_EvalRange_ResidualAndEmpty(t *testing.T) {
	payloads := []Payload{
		{"price": Int64Value(5)},
		{"price": Int64Value(50)},
	}
	decls := map[string]AttrKind{} // price NOT declared → residual scan
	ai := buildSegAttr(decls, len(payloads), func(slot int) Payload { return payloads[slot] })
	at := func(slot int) Payload { return payloads[slot] }

	if bm, _ := ai.evalSeg(Range("price", Int64Value(1), Int64Value(10)), 2, at); !intsEqual(bm.collect(), []int{0}) {
		t.Fatalf("residual Range = %v, want [0]", bm.collect())
	}
	// Declared Numeric Range that spans no stored values → empty.
	decls2 := map[string]AttrKind{"price": Numeric}
	ai2 := buildSegAttr(decls2, len(payloads), func(slot int) Payload { return payloads[slot] })
	if bm, _ := ai2.evalSeg(Range("price", Int64Value(100), Int64Value(200)), 2, at); bm.count() != 0 {
		t.Fatalf("empty Range span = %v, want empty", bm.collect())
	}
	// Declared Numeric Range with a non-numeric bound → empty (defensive).
	if bm, _ := ai2.evalSeg(Range("price", StringValue("x"), Int64Value(10)), 2, at); bm.count() != 0 {
		t.Fatalf("non-numeric bound Range = %v, want empty", bm.collect())
	}
}

// TestSegAttr_EmptyAnd_AllBits covers the And-with-no-children → allBits path
// (a degenerate but well-defined "match everything" predicate).
func TestSegAttr_EmptyAnd_AllBits(t *testing.T) {
	payloads := []Payload{{"a": StringValue("x")}, {"a": StringValue("y")}, {"a": StringValue("z")}}
	ai := buildSegAttr(map[string]AttrKind{"a": Keyword}, 3, func(slot int) Payload { return payloads[slot] })
	bm, ok := ai.evalSeg(And(), 3, func(slot int) Payload { return payloads[slot] })
	if !ok {
		t.Fatal("empty And must succeed")
	}
	if !intsEqual(bm.collect(), []int{0, 1, 2}) {
		t.Fatalf("empty And = %v, want all slots [0 1 2]", bm.collect())
	}
}

// TestSegAttr_BuildSkipsNonNumericUnderNumericDecl covers buildSegAttr's branch
// where a value declared Numeric is actually non-numeric in some payload (skipped,
// not indexed) — a robustness path that must not panic or mis-index.
func TestSegAttr_BuildSkipsNonNumericUnderNumericDecl(t *testing.T) {
	payloads := []Payload{
		{"n": Int64Value(1)},
		{"n": StringValue("oops")}, // declared Numeric but stored as String
		{"n": Float64Value(3)},
	}
	decls := map[string]AttrKind{"n": Numeric}
	ai := buildSegAttr(decls, 3, func(slot int) Payload { return payloads[slot] })
	at := func(slot int) Payload { return payloads[slot] }
	// The String slot is skipped from the numeric index; Range still finds 1 and 3.
	bm, _ := ai.evalSeg(Range("n", Int64Value(0), Int64Value(10)), 3, at)
	if !intsEqual(bm.collect(), []int{0, 2}) {
		t.Fatalf("Range over mixed-kind = %v, want [0 2]", bm.collect())
	}
}

func TestHeadAttr_MaintainedOnAppend(t *testing.T) {
	ha := newHeadAttr(map[string]AttrKind{"color": Keyword})
	ha.index(0, Payload{"color": StringValue("red")})
	ha.index(1, Payload{"color": StringValue("blue")})
	ha.index(2, Payload{"color": StringValue("red")})
	bm := ha.eq("color", StringValue("red"))
	if !intsEqual(bm.collect(), []int{0, 2}) {
		t.Fatalf("headAttr eq = %v, want [0 2]", bm.collect())
	}
}

// TestHeadAttr_NumericAndMisses covers the Numeric index branch of headAttr.index,
// the eq miss for a never-stored Keyword value, and the eq path for an undeclared
// field (returns empty — the head's brute path is the correctness floor).
func TestHeadAttr_NumericAndMisses(t *testing.T) {
	ha := newHeadAttr(map[string]AttrKind{"color": Keyword, "n": Numeric})
	ha.index(0, Payload{"color": StringValue("red"), "n": Int64Value(10)})
	ha.index(1, Payload{"n": Float64Value(10)}) // numeric, same value, no color
	ha.index(2, Payload{"unrelated": StringValue("x")})

	// Keyword eq miss: a value never indexed → empty.
	if bm := ha.eq("color", StringValue("green")); bm.count() != 0 {
		t.Fatalf("headAttr eq miss = %v, want empty", bm.collect())
	}
	// eq on an undeclared field → empty (no panic; head falls back to brute).
	if bm := ha.eq("unrelated", StringValue("x")); bm.count() != 0 {
		t.Fatalf("headAttr eq on undeclared field = %v, want empty", bm.collect())
	}
	// The Numeric index recorded both slot 0 and slot 1 under value 10.
	m := ha.numeric["n"]
	if m == nil || m[10] == nil || !intsEqual(m[10].collect(), []int{0, 1}) {
		t.Fatalf("headAttr numeric posting for 10 = %v, want [0 1]", m[10].collect())
	}
}

// TestSegment_Append_MaintainsHeadAttr proves segment.append feeds new slots into
// an attached headAttr (the "maintained on Put" wiring), while a segment with no
// headAttr (attr == nil) appends without indexing and does not panic.
func TestSegment_Append_MaintainsHeadAttr(t *testing.T) {
	seg := newSegment(Cosine)
	seg.attr = newHeadAttr(map[string]AttrKind{"color": Keyword})
	stored, norm := Cosine.prepare([]float32{1, 0, 0})
	seg.append(1, stored, norm, Payload{"color": StringValue("red")})
	seg.append(2, stored, norm, Payload{"color": StringValue("blue")})
	seg.append(3, stored, norm, Payload{"color": StringValue("red")})
	if bm := seg.attr.eq("color", StringValue("red")); !intsEqual(bm.collect(), []int{0, 2}) {
		t.Fatalf("head attr after appends = %v, want [0 2]", bm.collect())
	}

	// A segment with no attr index must append cleanly (the nil-attr branch).
	plain := newSegment(Cosine)
	plain.append(1, stored, norm, Payload{"color": StringValue("red")})
	if len(plain.payloads) != 1 || plain.attr != nil {
		t.Fatal("plain segment append must not allocate a head attr")
	}
}

// TestRange_BoundaryInclusive proves the Numeric ordered structure answers a
// closed [lo,hi] Range inclusively on both ends: values {1,2,3} at slots {0,1,2},
// Range(1,2) selects {0,1} (n=1 and n=2), excluding n=3. This pins the
// evalRange binary-search span (first value >= lo, while value <= hi).
func TestRange_BoundaryInclusive(t *testing.T) {
	pls := []Payload{{"n": Int64Value(1)}, {"n": Int64Value(2)}, {"n": Int64Value(3)}}
	ai := buildSegAttr(map[string]AttrKind{"n": Numeric}, 3, func(s int) Payload { return pls[s] })
	bm, _ := ai.evalSeg(Range("n", Int64Value(1), Int64Value(2)), 3, func(s int) Payload { return pls[s] })
	if !intsEqual(bm.collect(), []int{0, 1}) {
		t.Fatalf("inclusive range = %v, want [0 1]", bm.collect())
	}
}
