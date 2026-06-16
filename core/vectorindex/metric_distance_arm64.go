//go:build arm64

package vectorindex

import "github.com/viterin/vek/vek32"

// distanceN compares two stored (raw) vectors. na/nb are their precomputed L2
// norms, used only by cosine. arm64 routes the dot product through the
// hand-written NEON kernel (dot()); vek ships AVX2 for amd64 only and falls back
// to scalar on arm64 (see dot_arm64.s).
func (m Metric) distanceN(a, b []float32, na, nb float32) float32 {
	if m == Euclidean {
		return vek32.Distance(a, b)
	}
	if m == Cosine {
		denom := na * nb
		if denom == 0 {
			return 1
		}
		return 1 - dot(a, b)/denom
	}
	return 1 - dot(a, b)
}
