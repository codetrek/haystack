//go:build !arm64

package vectorindex

import "github.com/viterin/vek/vek32"

// distanceN compares two stored (raw) vectors. na/nb are their precomputed L2
// norms, used only by cosine. amd64 calls vek32.Dot directly rather than through
// the local dot() wrapper: vek is an external package, so -coverpkg=./... does
// not instrument it, and there is no extra per-distance coverage counter on the
// hot path (which under the gate's atomic coverage / -race would cost seconds
// across millions of calls).
func (m Metric) distanceN(a, b []float32, na, nb float32) float32 {
	if m == Euclidean {
		return vek32.Distance(a, b)
	}
	if m == Cosine {
		denom := na * nb
		if denom == 0 {
			return 1
		}
		return 1 - vek32.Dot(a, b)/denom
	}
	return 1 - vek32.Dot(a, b)
}
