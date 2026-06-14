//go:build arm64 && dotbench

package vectorindex

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

// dotBenchDims spans the small/medium/large embedding sizes we care about.
// 64 stresses fixed overhead; 768/1536 are common transformer embedding dims.
var dotBenchDims = []int{64, 128, 384, 768, 1536}

func makeDotInputs(dim int) ([]float32, []float32) {
	r := rand.New(rand.NewSource(int64(dim) + 1))
	a := make([]float32, dim)
	b := make([]float32, dim)
	for i := range a {
		a[i] = r.Float32()*2 - 1
		b[i] = r.Float32()*2 - 1
	}
	return a, b
}

// TestDotVariants validates the benchmark variants against the scalar reference
// (scalarDotRef, in dot_test.go) across dims that exercise every tail remainder.
// A fast-but-wrong kernel is worthless, so this must pass before the timings
// mean anything.
func TestDotVariants(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	for _, dim := range []int{1, 2, 3, 4, 5, 7, 8, 15, 16, 17, 19, 31, 33, 63, 64, 65, 127, 128, 129, 384, 768, 769, 1536} {
		a := make([]float32, dim)
		b := make([]float32, dim)
		for i := range a {
			a[i] = r.Float32()*2 - 1
			b[i] = r.Float32()*2 - 1
		}
		want := scalarDotRef(a, b)
		got := map[string]float32{
			"4AccGoTail": dot4AccGoTail(a, b),
			"4AccPure":   dot4AccPure(a, b),
		}
		for name, g := range got {
			tol := 1e-4 * (1 + float32(math.Abs(float64(want))))
			if float32(math.Abs(float64(want-g))) > tol {
				t.Errorf("%s dim=%d: got=%v want(scalar)=%v |d|=%v",
					name, dim, g, want, math.Abs(float64(want-g)))
			}
		}
	}
}

var dotSink float32

// BenchmarkDot measures the L1-hot compute ceiling for the shipped 1-accumulator
// hybrid vs the 4-accumulator kernel (Go tail) vs the fully-pure-asm variant.
// Inputs are reused across iterations, so this is the cache-resident ceiling —
// the end-to-end HNSW search benchmark shows whether it translates under the
// cache-cold streaming the real index does.
func BenchmarkDot(b *testing.B) {
	variants := []struct {
		name string
		fn   func(a, b []float32) float32
	}{
		{"1Acc_GoTail_prod", dot},
		{"4Acc_GoTail", dot4AccGoTail},
		{"4Acc_Pure", dot4AccPure},
	}
	for _, dim := range dotBenchDims {
		x, y := makeDotInputs(dim)
		for _, v := range variants {
			b.Run(fmt.Sprintf("dim=%d/%s", dim, v.name), func(b *testing.B) {
				var s float32
				for i := 0; i < b.N; i++ {
					s = v.fn(x, y)
				}
				dotSink = s
			})
		}
	}
}
