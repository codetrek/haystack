package vectorindex

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
	// Cosine stores RAW vectors plus their precomputed L2 norm; cosine distance
	// is 1 - dot(a, b)/(|a||b|), dividing by the two stored norms. (Storage is no
	// longer normalized — see vectorstore §3.4: this gives up the #69 "store unit
	// ⇒ distance = 1-dot" micro-optimization in exchange for a lossless raw
	// round-trip; the norms are threaded through the hot path, so no per-distance
	// norm recompute or lock.)
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

// storesNormalized reports whether vectors are normalized before being stored.
// Always false now: cosine stores the raw vector + its norm, not a unit vector.
func (m Metric) storesNormalized() bool { return false }

// norm returns the L2 norm to persist for a vector. Only cosine needs it: it
// stores raw vectors and uses the norm to divide in the cosine distance. The raw
// metrics have no use for a norm and skip the computation, reporting 0.
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
// persist alongside it. Storage is now raw for ALL metrics: the input slice is
// returned unchanged. For cosine the returned norm is |v| (so the cosine
// distance can divide by it without recomputing); for the raw metrics it is 0.
// A zero vector is stored as-is (norm 0).
func (m Metric) prepare(v []float32) (stored []float32, norm float32) {
	norm = m.norm(v) // 0 for non-cosine, |v| for cosine
	return v, norm
}

// restore maps a stored vector back to its original raw form. Storage is raw for
// all metrics now, so this is the identity (the norm parameter is unused, kept
// for interface symmetry with prepare).
func (m Metric) restore(stored []float32, norm float32) []float32 {
	return stored
}

// distanceN is defined per-architecture (metric_distance_{arm64,amd64}.go): on
// amd64 it calls vek32.Dot directly (so -coverpkg=./... adds no extra per-call
// coverage counter on the hot path); on arm64 it routes through the NEON dot().
// na/nb are the precomputed L2 norms of a/b (used only by cosine).
