package vectorstore

import "testing"

func approxEqual(a, b, eps float32) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= eps
}

func TestMetric_PrepareRestore_Cosine(t *testing.T) {
	m := Cosine
	v := []float32{3, 4} // norm 5
	stored, norm := m.prepare(v)
	if !approxEqual(norm, 5, 1e-5) {
		t.Fatalf("norm = %v, want 5", norm)
	}
	if !approxEqual(stored[0], 0.6, 1e-6) || !approxEqual(stored[1], 0.8, 1e-6) {
		t.Fatalf("stored = %v, want unit [0.6 0.8]", stored)
	}
	got := m.restore(stored, norm)
	if !approxEqual(got[0], 3, 1e-4) || !approxEqual(got[1], 4, 1e-4) {
		t.Fatalf("restore = %v, want [3 4]", got)
	}
	if v[0] != 3 || v[1] != 4 {
		t.Fatalf("prepare mutated input: %v", v)
	}
}

func TestMetric_PrepareRestore_Raw(t *testing.T) {
	for _, m := range []Metric{DotProduct, Euclidean} {
		stored, norm := m.prepare([]float32{1, 2})
		if norm != 0 {
			t.Fatalf("%v: norm = %v, want 0", m, norm)
		}
		got := m.restore(stored, norm)
		if got[0] != 1 || got[1] != 2 {
			t.Fatalf("%v: restore = %v, want [1 2]", m, got)
		}
	}
}

func TestMetric_ZeroVector(t *testing.T) {
	stored, norm := Cosine.prepare([]float32{0, 0})
	if norm != 0 || stored[0] != 0 || stored[1] != 0 {
		t.Fatalf("zero vector: stored=%v norm=%v, want zeros/0", stored, norm)
	}
}

func TestMetric_Distance_Cosine(t *testing.T) {
	a, _ := Cosine.prepare([]float32{1, 0})
	b, _ := Cosine.prepare([]float32{0, 1})
	if d := Cosine.distance(a, b); !approxEqual(d, 1, 1e-6) {
		t.Fatalf("orthogonal cosine distance = %v, want 1", d)
	}
	if d := Cosine.distance(a, a); !approxEqual(d, 0, 1e-6) {
		t.Fatalf("identical cosine distance = %v, want 0", d)
	}
}

func TestMetric_Distance_Euclidean(t *testing.T) {
	a, _ := Euclidean.prepare([]float32{0, 0})
	b, _ := Euclidean.prepare([]float32{3, 4})
	if d := Euclidean.distance(a, b); !approxEqual(d, 5, 1e-5) {
		t.Fatalf("euclidean distance([0 0],[3 4]) = %v, want 5", d)
	}
}

func TestMetric_Distance_DotProduct(t *testing.T) {
	a, _ := DotProduct.prepare([]float32{1, 2})
	b, _ := DotProduct.prepare([]float32{3, 4}) // dot = 11 → 1 - 11 = -10
	if d := DotProduct.distance(a, b); !approxEqual(d, -10, 1e-5) {
		t.Fatalf("dot distance = %v, want -10", d)
	}
}

func TestMetric_String(t *testing.T) {
	cases := map[Metric]string{Cosine: "cosine", DotProduct: "dot", Euclidean: "euclidean", Metric(9): "unknown"}
	for m, want := range cases {
		if got := m.String(); got != want {
			t.Fatalf("%d.String() = %q, want %q", m, got, want)
		}
	}
}
