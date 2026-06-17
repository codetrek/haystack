//go:build arm64

package vectorstore

// dotNEONPartial is implemented in dot_arm64.s. n must be a multiple of 4.
//
//go:noescape
func dotNEONPartial(a, b *float32, n int, out *float32)

// dot computes the float32 dot product using a NEON kernel on arm64, where
// viterin/vek has no SIMD path (it falls back to a scalar loop). The vectorized
// loop handles len rounded down to a multiple of 4; the remainder is scalar.
func dot(a, b []float32) float32 {
	if len(a) != len(b) {
		panic("vectorindex: dot length mismatch")
	}
	n := len(a)
	nn := n &^ 3 // round down to a multiple of 4
	var sum float32
	if nn > 0 {
		var partials [4]float32
		dotNEONPartial(&a[0], &b[0], nn, &partials[0])
		sum = partials[0] + partials[1] + partials[2] + partials[3]
	}
	for i := nn; i < n; i++ {
		sum += a[i] * b[i]
	}
	return sum
}
