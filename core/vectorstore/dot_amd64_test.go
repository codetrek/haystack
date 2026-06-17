//go:build amd64

package vectorstore

import (
	"math"
	"math/rand"
	"testing"

	"github.com/viterin/vek/vek32"
)

func naiveDot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// TestDotAVX2FMA_MatchesNaive guards the hand-written kernel against operand-order
// and tail-handling bugs across dims that exercise the 32-block, 8-block, and
// scalar-tail paths (and sub-8 lengths). Tolerance is relative: SIMD partial sums
// differ from sequential summation only in the last float32 bits.
func TestDotAVX2FMA_MatchesNaive(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for _, n := range []int{1, 2, 7, 8, 9, 15, 16, 31, 32, 33, 64, 96, 127, 128, 129, 256, 257} {
		a := make([]float32, n)
		b := make([]float32, n)
		for i := range a {
			a[i] = rng.Float32()*2 - 1
			b[i] = rng.Float32()*2 - 1
		}
		got := dotAVX2FMA(a, b)
		want := naiveDot(a, b)
		if d := math.Abs(float64(got - want)); d > 1e-4*(math.Abs(float64(want))+1) {
			t.Fatalf("n=%d: dotAVX2FMA=%v naive=%v (|diff|=%g)", n, got, want, d)
		}
	}
}

func BenchmarkDot128_Vek(b *testing.B) {
	a, c := randPair(128)
	b.ResetTimer()
	var s float32
	for i := 0; i < b.N; i++ {
		s += vek32.Dot(a, c)
	}
	_ = s
}

func BenchmarkDot128_AVX2FMA(b *testing.B) {
	a, c := randPair(128)
	b.ResetTimer()
	var s float32
	for i := 0; i < b.N; i++ {
		s += dotAVX2FMA(a, c)
	}
	_ = s
}

func randPair(n int) ([]float32, []float32) {
	rng := rand.New(rand.NewSource(1))
	a := make([]float32, n)
	c := make([]float32, n)
	for i := range a {
		a[i] = rng.Float32()
		c[i] = rng.Float32()
	}
	return a, c
}
