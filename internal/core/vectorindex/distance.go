package vectorindex

import "math"

// DistanceFunc computes the distance between two vectors.
// Lower values indicate more similar vectors.
type DistanceFunc func(a, b []float32) float32

// CosineDistance returns 1 - cosine_similarity(a, b).
// Result is in [0, 2]; 0 means identical direction.
func CosineDistance(a, b []float32) float32 {
	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	denom := float32(math.Sqrt(float64(normA)) * math.Sqrt(float64(normB)))
	if denom == 0 {
		return 1
	}
	return 1 - dot/denom
}

// EuclideanDistance returns the L2 distance between a and b.
func EuclideanDistance(a, b []float32) float32 {
	var sum float32
	for i := range a {
		d := a[i] - b[i]
		sum += d * d
	}
	return float32(math.Sqrt(float64(sum)))
}

// DotProductDistance returns 1 - dot(a, b).
// Intended for normalized vectors where dot product is in [-1, 1].
func DotProductDistance(a, b []float32) float32 {
	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}
	return 1 - dot
}
