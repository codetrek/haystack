package vectorstore

import "testing"

func TestPredicate_EvalPayload_TruthTable(t *testing.T) {
	p := Payload{"color": StringValue("red"), "n": Int64Value(5)}
	cases := []struct {
		name string
		pred Predicate
		want bool
	}{
		{"eq-hit", Eq("color", StringValue("red")), true},
		{"eq-miss", Eq("color", StringValue("blue")), false},
		{"in-hit", In("color", StringValue("a"), StringValue("red")), true},
		{"in-miss", In("color", StringValue("a"), StringValue("b")), false},
		{"range-hit", Range("n", Int64Value(1), Int64Value(10)), true},
		{"range-miss", Range("n", Int64Value(6), Int64Value(10)), false},
		{"and-hit", And(Eq("color", StringValue("red")), Range("n", Int64Value(1), Int64Value(10))), true},
		{"and-miss", And(Eq("color", StringValue("red")), Eq("n", Int64Value(99))), false},
		{"missing-field", Eq("nope", StringValue("x")), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.pred.evalPayload(p); got != c.want {
				t.Fatalf("evalPayload = %v, want %v", got, c.want)
			}
		})
	}
}

func TestPredicate_Unsupported_ReturnsError(t *testing.T) {
	// A nil predicate means "no filter" — explicitly allowed, no error.
	if err := validatePredicate(nil, map[string]AttrKind{}); err != nil {
		t.Fatalf("nil filter must be allowed: %v", err)
	}
	// Range on a Keyword-declared field is a kind mismatch.
	decls := map[string]AttrKind{"color": Keyword}
	if err := validatePredicate(Range("color", Int64Value(1), Int64Value(2)), decls); err == nil {
		t.Fatal("Range on a Keyword field must error")
	}
}

// TestPredicate_Validate_AcceptsClosedSet exercises every accepting branch of
// validatePredicate (Eq/In/Range on a Numeric-or-undeclared field/nested And) so
// the closed-set acceptance paths are covered, plus the And-propagates-error path.
func TestPredicate_Validate_AcceptsClosedSet(t *testing.T) {
	decls := map[string]AttrKind{"color": Keyword, "n": Numeric}
	good := []Predicate{
		Eq("color", StringValue("red")),
		In("color", StringValue("a"), StringValue("b")),
		Range("n", Int64Value(1), Int64Value(2)),          // Numeric field — ok
		Range("undeclared", Int64Value(1), Int64Value(2)), // not declared — ok (residual)
		And(Eq("color", StringValue("red")), Range("n", Int64Value(1), Int64Value(2))),
	}
	for _, p := range good {
		if err := validatePredicate(p, decls); err != nil {
			t.Fatalf("validatePredicate(%v) = %v, want nil", p, err)
		}
	}
	// An And containing an invalid child must surface the child's error.
	bad := And(Eq("color", StringValue("red")), Range("color", Int64Value(1), Int64Value(2)))
	if err := validatePredicate(bad, decls); err == nil {
		t.Fatal("And wrapping a Range-on-Keyword must propagate the error")
	}
}

// TestPredicate_KindAndProps covers the interface bookkeeping methods (kind/props)
// that the adaptive Search dispatch (later task) relies on.
func TestPredicate_KindAndProps(t *testing.T) {
	cases := []struct {
		pred  Predicate
		kind  predKind
		props []string
	}{
		{Eq("color", StringValue("red")), predEq, []string{"color"}},
		{In("color", StringValue("a")), predIn, []string{"color"}},
		{Range("n", Int64Value(1), Int64Value(2)), predRange, []string{"n"}},
		{And(Eq("a", StringValue("x")), Eq("b", StringValue("y"))), predAnd, []string{"a", "b"}},
	}
	for _, c := range cases {
		if c.pred.kind() != c.kind {
			t.Fatalf("kind(%v) = %v, want %v", c.pred, c.pred.kind(), c.kind)
		}
		gotProps := c.pred.props()
		if len(gotProps) != len(c.props) {
			t.Fatalf("props(%v) = %v, want %v", c.pred, gotProps, c.props)
		}
		for i := range c.props {
			if gotProps[i] != c.props[i] {
				t.Fatalf("props(%v) = %v, want %v", c.pred, gotProps, c.props)
			}
		}
	}
}

// TestPredicate_RangePayload_NonNumericValue covers the rangePred branch where the
// payload value exists but is non-numeric (a String under a Range query → false).
func TestPredicate_RangePayload_NonNumericValue(t *testing.T) {
	p := Payload{"s": StringValue("hi")}
	if Range("s", Int64Value(0), Int64Value(10)).evalPayload(p) {
		t.Fatal("Range over a non-numeric value must be false")
	}
	if Range("missing", Int64Value(0), Int64Value(10)).evalPayload(p) {
		t.Fatal("Range over a missing field must be false")
	}
}

// TestValue_NumericAndEqual covers every Kind branch of Value.numeric/Value.equal.
func TestValue_NumericAndEqual(t *testing.T) {
	if x, ok := Int64Value(5).numeric(); !ok || x != 5 {
		t.Fatalf("Int64 numeric = %v,%v", x, ok)
	}
	if x, ok := Float64Value(1.5).numeric(); !ok || x != 1.5 {
		t.Fatalf("Float64 numeric = %v,%v", x, ok)
	}
	if _, ok := StringValue("x").numeric(); ok {
		t.Fatal("String must not be numeric")
	}
	if _, ok := BoolValue(true).numeric(); ok {
		t.Fatal("Bool must not be numeric")
	}
	// equal: same-kind hits across every kind, plus kind-mismatch and miss.
	if !StringValue("a").equal(StringValue("a")) || StringValue("a").equal(StringValue("b")) {
		t.Fatal("String equal")
	}
	if !Int64Value(7).equal(Int64Value(7)) || Int64Value(7).equal(Int64Value(8)) {
		t.Fatal("Int64 equal")
	}
	if !Float64Value(2.5).equal(Float64Value(2.5)) || Float64Value(2.5).equal(Float64Value(2.6)) {
		t.Fatal("Float64 equal")
	}
	if !BoolValue(true).equal(BoolValue(true)) || BoolValue(true).equal(BoolValue(false)) {
		t.Fatal("Bool equal")
	}
	if Int64Value(1).equal(Float64Value(1)) {
		t.Fatal("cross-kind values must not be equal")
	}
}
