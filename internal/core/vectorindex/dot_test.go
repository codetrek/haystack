//go:build arm64

package vectorindex

import (
	"math"
	"math/rand"
	"testing"
)

// scalarDotRef is an independent, obviously-correct reference used to validate
// the platform dot() (NEON on arm64, vek/AVX2 elsewhere), including the tail
// handling for lengths that aren't a multiple of 4.
func scalarDotRef(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

func TestDotMatchesScalar(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	// dims chosen to exercise the SIMD body and every tail remainder (n%4).
	// (empty is not tested: it never occurs on the distance path, and amd64's
	// vek path panics on it — matching pre-NEON behavior.)
	for _, dim := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 15, 16, 17, 31, 33, 127, 128, 129, 768, 769, 1536} {
		a := make([]float32, dim)
		b := make([]float32, dim)
		for i := range a {
			a[i] = r.Float32()*2 - 1
			b[i] = r.Float32()*2 - 1
		}
		want := scalarDotRef(a, b)
		got := dot(a, b)
		tol := 1e-4 * (1 + float32(math.Abs(float64(want))))
		if float32(math.Abs(float64(want-got))) > tol {
			t.Errorf("dim=%d: dot=%v want(scalar)=%v |d|=%v", dim, got, want, math.Abs(float64(want-got)))
		}
	}
}
