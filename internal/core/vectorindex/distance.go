package vectorindex

import (
	"github.com/viterin/vek/vek32"
)

// DistanceFunc computes the distance between two vectors.
// Lower values indicate more similar vectors.
type DistanceFunc func(a, b []float32) float32

// CosineDistance returns 1 - cosine_similarity(a, b).
// Result is in [0, 2]; 0 means identical direction.
// Uses SIMD-accelerated dot product and norm via the vek library.
func CosineDistance(a, b []float32) float32 {
	normA := vek32.Norm(a)
	normB := vek32.Norm(b)
	denom := normA * normB
	if denom == 0 {
		return 1
	}
	dot := vek32.Dot(a, b)
	return 1 - dot/denom
}

// CosineDistanceWithNorms computes cosine distance using precomputed norms.
// This avoids recomputing norms when they are already known.
func CosineDistanceWithNorms(a, b []float32, normA, normB float32) float32 {
	denom := normA * normB
	if denom == 0 {
		return 1
	}
	dot := vek32.Dot(a, b)
	return 1 - dot/denom
}

// EuclideanDistance returns the L2 distance between a and b.
func EuclideanDistance(a, b []float32) float32 {
	return vek32.Distance(a, b)
}

// DotProductDistance returns 1 - dot(a, b).
// Intended for normalized vectors where dot product is in [-1, 1].
func DotProductDistance(a, b []float32) float32 {
	return 1 - vek32.Dot(a, b)
}
