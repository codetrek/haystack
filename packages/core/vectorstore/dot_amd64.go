//go:build amd64

package vectorstore

// dotAVX2FMA returns the dot product of a and b (len(a) must equal len(b)).
// Implemented in dot_amd64.s with a 4-accumulator AVX2+FMA kernel. Requires the
// CPU to support AVX2+FMA (true on the build target; guarded by hasAVX2FMA before
// it is wired into the distance hot path).
//
//go:noescape
func dotAVX2FMA(a, b []float32) float32
