//go:build arm64

package vectorstore

import "github.com/viterin/vek/vek32"

// distance compares two vectors already in stored form. arm64 routes the dot
// product through the hand-written NEON kernel (dot()); vek ships AVX2 for
// amd64 only and falls back to scalar on arm64 (see dot_arm64.s).
func (m Metric) distance(a, b []float32) float32 {
	if m == Euclidean {
		return vek32.Distance(a, b)
	}
	return 1 - dot(a, b)
}
