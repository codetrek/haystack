package vectorindex

import (
	"math"
	"testing"
)

func approxEqual(t *testing.T, got, want []float32, eps float32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %d want %d", len(got), len(want))
	}
	for i := range got {
		if d := float32(math.Abs(float64(got[i] - want[i]))); d > eps {
			t.Fatalf("index %d: got %v want %v (|d|=%v > %v)", i, got[i], want[i], d, eps)
		}
	}
}

func TestMetricCosine(t *testing.T) {
	raw := []float32{3, 0, 4, 0} // |raw| = 5
	stored, norm := Cosine.prepare(raw)
	if math.Abs(float64(norm-5)) > 1e-5 {
		t.Fatalf("norm: got %v want 5", norm)
	}
	approxEqual(t, stored, []float32{0.6, 0, 0.8, 0}, 1e-6) // unit vector
	// raw must be untouched (prepare returns a new slice for cosine).
	approxEqual(t, raw, []float32{3, 0, 4, 0}, 0)
	// restore round-trips back to the original.
	approxEqual(t, Cosine.restore(stored, norm), raw, 1e-4)

	// distance on stored (unit) vectors is 1 - dot.
	a, _ := Cosine.prepare([]float32{1, 0, 0, 0})
	b, _ := Cosine.prepare([]float32{1, 0, 0, 0})
	if d := Cosine.distance(a, b); math.Abs(float64(d)) > 1e-6 {
		t.Fatalf("identical-direction distance: got %v want 0", d)
	}
	c, _ := Cosine.prepare([]float32{0, 1, 0, 0})
	if d := Cosine.distance(a, c); math.Abs(float64(d-1)) > 1e-6 {
		t.Fatalf("orthogonal distance: got %v want 1", d)
	}
}

func TestMetricCosineZeroVector(t *testing.T) {
	stored, norm := Cosine.prepare([]float32{0, 0, 0, 0})
	if norm != 0 {
		t.Fatalf("zero-vector norm: got %v want 0", norm)
	}
	approxEqual(t, stored, []float32{0, 0, 0, 0}, 0)
	approxEqual(t, Cosine.restore(stored, norm), []float32{0, 0, 0, 0}, 0)
	// distance to anything is 1 (dot is 0).
	if d := Cosine.distance(stored, []float32{1, 0, 0, 0}); math.Abs(float64(d-1)) > 1e-6 {
		t.Fatalf("zero-vector distance: got %v want 1", d)
	}
}

func TestMetricRawStored(t *testing.T) {
	raw := []float32{3, 0, 4, 0}
	for _, m := range []Metric{DotProduct, Euclidean} {
		stored, norm := m.prepare(raw)
		// Raw metrics store the vector unchanged and skip the norm computation
		// (the norm is never used to restore or compare them).
		approxEqual(t, stored, raw, 0)
		if norm != 0 {
			t.Fatalf("%s norm: got %v want 0", m, norm)
		}
		approxEqual(t, m.restore(stored, norm), raw, 0)
	}

	// DotProduct distance = 1 - dot.
	if d := DotProduct.distance([]float32{1, 2}, []float32{3, 4}); math.Abs(float64(d-(1-11))) > 1e-5 {
		t.Fatalf("dot distance: got %v want %v", d, 1-11)
	}
	// Euclidean distance = L2.
	if d := Euclidean.distance([]float32{0, 0}, []float32{3, 4}); math.Abs(float64(d-5)) > 1e-5 {
		t.Fatalf("euclidean distance: got %v want 5", d)
	}
}

func TestMetricNorm(t *testing.T) {
	v := []float32{3, 0, 4, 0} // |v| = 5
	// Only cosine computes a norm; the raw metrics report 0.
	if n := Cosine.norm(v); math.Abs(float64(n-5)) > 1e-5 {
		t.Fatalf("cosine norm: got %v want 5", n)
	}
	if n := DotProduct.norm(v); n != 0 {
		t.Fatalf("dot norm: got %v want 0", n)
	}
	if n := Euclidean.norm(v); n != 0 {
		t.Fatalf("euclidean norm: got %v want 0", n)
	}
}

func TestMetricStringAndStoresNormalized(t *testing.T) {
	if Cosine.String() != "cosine" || DotProduct.String() != "dot" || Euclidean.String() != "euclidean" {
		t.Fatalf("unexpected String(): %s/%s/%s", Cosine, DotProduct, Euclidean)
	}
	if Metric(99).String() != "unknown" {
		t.Fatalf("unknown metric String(): got %q want %q", Metric(99).String(), "unknown")
	}
	if !Cosine.storesNormalized() || DotProduct.storesNormalized() || Euclidean.storesNormalized() {
		t.Fatal("storesNormalized() should be true only for cosine")
	}
}
