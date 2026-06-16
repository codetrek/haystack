//go:build !arm64

package vectorstore

import "github.com/viterin/vek/vek32"

// distance compares two vectors already in stored form. amd64 calls vek32.Dot
// directly rather than through the local dot() wrapper: vek is an external
// package, so -coverpkg=./... does not instrument it, and there is no extra
// per-distance coverage counter on the hot path (which under the gate's atomic
// coverage / -race would cost seconds across millions of calls).
func (m Metric) distance(a, b []float32) float32 {
	if m == Euclidean {
		return vek32.Distance(a, b)
	}
	return 1 - vek32.Dot(a, b)
}
