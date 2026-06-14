package vectorindex

import (
	"math/rand"
	"testing"

	"github.com/viterin/vek/vek32"
)

// Experiment benchmarks: the platform dot() (NEON on arm64) vs vek's Dot (scalar
// on arm64, AVX2 on amd64), to quantify the arm64 win. Throwaway.

func benchDotImpl(b *testing.B, dim int, fn func(x, y []float32) float32) {
	r := rand.New(rand.NewSource(1))
	x := make([]float32, dim)
	y := make([]float32, dim)
	for i := range x {
		x[i] = r.Float32()*2 - 1
		y[i] = r.Float32()*2 - 1
	}
	b.ResetTimer()
	var s float32
	for i := 0; i < b.N; i++ {
		s += fn(x, y)
	}
	_ = s
}

func BenchmarkDot_impl_128(b *testing.B) { benchDotImpl(b, 128, dot) }       // NEON on arm64
func BenchmarkDot_vek_128(b *testing.B)  { benchDotImpl(b, 128, vek32.Dot) } // scalar on arm64
func BenchmarkDot_impl_768(b *testing.B) { benchDotImpl(b, 768, dot) }
func BenchmarkDot_vek_768(b *testing.B)  { benchDotImpl(b, 768, vek32.Dot) }
