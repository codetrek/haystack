//go:build !arm64

package vectorstore

import (
	"github.com/viterin/vek/vek32"
	"golang.org/x/sys/cpu"
)

// dot is the dot-product kernel, chosen once at init: the hand-written
// 8-accumulator AVX2+FMA kernel (dot_amd64.s) when the CPU has AVX2+FMA, else
// vek32.Dot. Selecting via a package var keeps the per-distance hot path a single
// indirect call with no per-call CPU branch.
var dot = vek32.Dot

func init() {
	if cpu.X86.HasAVX2 && cpu.X86.HasFMA {
		dot = dotAVX2FMA
	}
}

// distance compares two vectors already in stored form. amd64 calls the kernel
// directly rather than through a wrapper that -coverpkg would instrument: vek and
// the .s kernel are not coverage-counted, so there is no extra per-distance
// coverage counter on the hot path (which under the gate's atomic coverage /
// -race would cost seconds across millions of calls).
func (m Metric) distance(a, b []float32) float32 {
	if m == Euclidean {
		return vek32.Distance(a, b)
	}
	return 1 - dot(a, b)
}
