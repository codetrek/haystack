package vectorstore

import (
	"math"

	"github.com/viterin/vek/vek32"
)

// Metric is the immutable distance metric of an index. It is chosen when the
// index is created, persisted in the store metadata, and must never change for
// a given index: both the graph structure and the on-disk vector form depend on
// it (see the package docs on why a metric cannot be swapped after build).
type Metric uint8

const (
	// Cosine stores vectors normalized to unit length, so cosine distance
	// reduces to 1 - dot(a, b) with no per-distance norm division.
	Cosine Metric = 0
	// DotProduct stores raw vectors; distance is 1 - dot(a, b).
	DotProduct Metric = 1
	// Euclidean stores raw vectors; distance is the L2 norm of a - b.
	Euclidean Metric = 2
)

func (m Metric) String() string {
	switch m {
	case Cosine:
		return "cosine"
	case DotProduct:
		return "dot"
	case Euclidean:
		return "euclidean"
	default:
		return "unknown"
	}
}

// norm returns the L2 norm to persist for a vector. Only cosine needs it: it
// stores unit vectors and uses the norm to restore the original scale in
// GetVector. The raw metrics store the original vector verbatim, so they have
// no use for a norm and skip the computation, reporting 0.
func (m Metric) norm(v []float32) float32 {
	if m != Cosine {
		return 0
	}
	n := vek32.Norm(v) // SIMD fast path — kept for the overwhelming common case
	// Recompute in float64 ONLY for the inputs the float32 SIMD path mishandles:
	// a non-finite result (large-magnitude overflow → NaN on AVX2 / +Inf on
	// scalar, which also diverges across architectures — audit #10), or a zero
	// result that may be a tiny-vector underflow (audit #13). float64 is
	// deterministic and overflow/underflow-free; the cost is paid only here.
	if !math.IsNaN(float64(n)) && !math.IsInf(float64(n), 0) && n != 0 {
		return n
	}
	var sumSq float64
	for _, x := range v {
		sumSq += float64(x) * float64(x)
	}
	return float32(math.Sqrt(sumSq))
}

// prepare maps a raw vector to the form stored on disk and returns the norm to
// persist alongside it. For cosine it returns a new unit-length slice and the
// original L2 norm (so GetVector can restore the original scale). For the raw
// metrics it returns the input slice unchanged with norm 0. A zero vector is
// stored as-is (norm 0).
func (m Metric) prepare(v []float32) (stored []float32, norm float32) {
	norm = m.norm(v)
	if m != Cosine || norm == 0 {
		return v, norm
	}
	return vek32.MulNumber(v, 1.0/norm), norm // SIMD scale into a fresh slice
}

// restore maps a stored vector back to its original raw form. For cosine it
// multiplies the unit vector by the stored norm; otherwise it is the identity.
func (m Metric) restore(stored []float32, norm float32) []float32 {
	if m != Cosine {
		return stored
	}
	out := make([]float32, len(stored))
	for i, x := range stored {
		out[i] = x * norm
	}
	return out
}

// distance is defined per-architecture (metric_distance_{arm64,amd64}.go): on
// amd64 it calls vek32.Dot directly (so -coverpkg=./... adds no extra per-call
// coverage counter on the hot path); on arm64 it routes through the NEON dot().
