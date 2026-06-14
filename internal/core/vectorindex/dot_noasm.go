//go:build !arm64

package vectorindex

import "github.com/viterin/vek/vek32"

// dot computes the float32 dot product. On non-arm64 (amd64) vek provides an
// AVX2 kernel, so we use it directly; only arm64 needs the hand-written NEON
// path (see dot_arm64.s). vek panics on an empty slice, so guard it (the arm64
// path returns 0 for empty too).
func dot(a, b []float32) float32 {
	if len(a) == 0 {
		return 0
	}
	return vek32.Dot(a, b)
}
