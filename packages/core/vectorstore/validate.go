package vectorstore

import (
	"fmt"
	"math"
)

// validateVector rejects inputs that would corrupt the segment or panic the SIMD
// kernel, BEFORE any state mutation. dim == 0 means "dimension not yet learned"
// (first Put), so any non-empty length is accepted and becomes the fixed dim.
// For cosine it additionally rejects vectors whose norm is non-finite or too
// small to normalize without 1/norm overflowing to +Inf. A zero vector (norm 0)
// is allowed: prepare stores it as-is with norm 0.
func validateVector(v []float32, dim int, m Metric) error {
	if len(v) == 0 {
		return fmt.Errorf("vectorstore: empty vector")
	}
	if dim != 0 && len(v) != dim {
		return fmt.Errorf("vectorstore: vector dimension mismatch: got %d, want %d", len(v), dim)
	}
	if m == Cosine {
		n := m.norm(v)
		if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
			return fmt.Errorf("vectorstore: cosine vector has non-finite norm")
		}
		// Reject norms so small that 1/norm overflows to +Inf in float32.
		if n != 0 && math.IsInf(float64(1/n), 0) {
			return fmt.Errorf("vectorstore: cosine vector norm %g too small to normalize", n)
		}
	}
	return nil
}
