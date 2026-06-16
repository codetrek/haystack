package vectorindex

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/viterin/vek/vek32"
)

// --- Change 1 & 2: prepare/restore/storesNormalized/distanceN (raw cosine) ---

// TestCosineRawPrepareReturnsRawAndNorm proves prepare no longer scales cosine
// vectors to unit length: it returns the RAW vector unchanged plus |v|.
func TestCosineRawPrepareReturnsRawAndNorm(t *testing.T) {
	raw := []float32{3, 0, 4, 0} // |raw| = 5
	stored, norm := Cosine.prepare(raw)
	require.InDelta(t, 5.0, float64(norm), 1e-5, "norm must be |v|")
	// stored is the RAW vector, NOT a unit vector.
	approxEqualRaw(t, stored, raw, 0)

	// Raw metrics still return the input unchanged with norm 0.
	for _, m := range []Metric{DotProduct, Euclidean} {
		s, n := m.prepare(raw)
		approxEqualRaw(t, s, raw, 0)
		require.Zero(t, n, "%s norm must be 0", m)
	}
}

// TestCosineRawRestoreIsIdentity proves restore returns the stored (raw) vector
// verbatim for ALL metrics now (storage is always raw).
func TestCosineRawRestoreIsIdentity(t *testing.T) {
	raw := []float32{3, 0, 4, 0}
	for _, m := range []Metric{Cosine, DotProduct, Euclidean} {
		stored, norm := m.prepare(raw)
		approxEqualRaw(t, m.restore(stored, norm), raw, 0)
	}
}

// TestCosineRawStoresNormalizedAllFalse proves cosine no longer normalizes
// storage.
func TestCosineRawStoresNormalizedAllFalse(t *testing.T) {
	require.False(t, Cosine.storesNormalized())
	require.False(t, DotProduct.storesNormalized())
	require.False(t, Euclidean.storesNormalized())
}

// TestCosineRawDistanceN proves distanceN divides by the two precomputed norms
// for cosine, and matches CosineDistance(rawA, rawB) from distance.go.
func TestCosineRawDistanceN(t *testing.T) {
	cases := [][2][]float32{
		{{1, 0, 0, 0}, {1, 0, 0, 0}},
		{{1, 0, 0, 0}, {0, 1, 0, 0}},
		{{3, 0, 4, 0}, {6, 0, 8, 0}}, // same direction, different scale
		{{1, 2, 3, 4}, {4, 3, 2, 1}},
		{{0.5, -0.5, 0.5, -0.5}, {-0.5, 0.5, 0.5, -0.5}},
	}
	for _, c := range cases {
		a, b := c[0], c[1]
		na, nb := vek32.Norm(a), vek32.Norm(b)
		got := Cosine.distanceN(a, b, na, nb)
		want := CosineDistance(a, b)
		require.InDelta(t, float64(want), float64(got), 1e-6)

		// Numerical equivalence to the OLD "1 - dot on unit vectors" form.
		ua := vek32.MulNumber(a, 1.0/na)
		ub := vek32.MulNumber(b, 1.0/nb)
		oldForm := 1 - vek32.Dot(ua, ub)
		require.InDelta(t, float64(oldForm), float64(got), 1e-5)
	}
}

// TestCosineRawDistanceNZeroNorm proves denom==0 yields distance 1.
func TestCosineRawDistanceNZeroNorm(t *testing.T) {
	a := []float32{1, 0, 0, 0}
	b := []float32{0, 0, 0, 0}
	require.InDelta(t, 1.0, float64(Cosine.distanceN(a, b, 1, 0)), 1e-6)
	require.InDelta(t, 1.0, float64(Cosine.distanceN(b, a, 0, 1)), 1e-6)
}

// TestRawDistanceNDotAndEuclidean proves DotProduct and Euclidean are unchanged
// (norms ignored).
func TestRawDistanceNDotAndEuclidean(t *testing.T) {
	d := DotProduct.distanceN([]float32{1, 2}, []float32{3, 4}, 0, 0)
	require.InDelta(t, float64(1-11), float64(d), 1e-5)
	e := Euclidean.distanceN([]float32{0, 0}, []float32{3, 4}, 0, 0)
	require.InDelta(t, 5.0, float64(e), 1e-5)
}

func approxEqualRaw(t *testing.T, got, want []float32, eps float32) {
	t.Helper()
	require.Equal(t, len(want), len(got), "length mismatch")
	for i := range got {
		require.LessOrEqual(t, math.Abs(float64(got[i]-want[i])), float64(eps),
			"index %d: got %v want %v", i, got[i], want[i])
	}
}

// --- Change 3: GetVectorRefWithNorm on both stores ---

// TestGetVectorRefWithNormMem proves MemNodeStore returns the raw vector ref and
// its norm together; matches GetVectorRef + GetNorm.
func TestGetVectorRefWithNormMem(t *testing.T) {
	s := NewMemNodeStore(Cosine)
	raw := []float32{3, 0, 4, 0} // |raw| = 5
	require.NoError(t, s.PutNode(7, 0, raw, 100))

	vec, norm, err := s.GetVectorRefWithNorm(7)
	require.NoError(t, err)
	approxEqualRaw(t, vec, raw, 0) // raw stored verbatim
	require.InDelta(t, 5.0, float64(norm), 1e-5)

	// Consistent with the separate accessors.
	refVec, err := s.GetVectorRef(7)
	require.NoError(t, err)
	approxEqualRaw(t, vec, refVec, 0)
	refNorm, err := s.GetNorm(7)
	require.NoError(t, err)
	require.Equal(t, refNorm, norm)

	_, _, err = s.GetVectorRefWithNorm(999)
	require.Error(t, err)
}

// TestGetVectorRefWithNormMmap proves MmapStore returns the raw vector ref and
// its norm under a single lock, matching the separate accessors.
func TestGetVectorRefWithNormMmap(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: Cosine, Dim: 4, M: 8})
	require.NoError(t, err)
	defer s.Close()

	raw := []float32{3, 0, 4, 0} // |raw| = 5
	id, err := s.NextNodeId()
	require.NoError(t, err)
	require.NoError(t, s.txnBegin())
	require.NoError(t, s.PutNode(id, 0, raw, 100))
	require.NoError(t, s.txnCommit())

	vec, norm, err := s.GetVectorRefWithNorm(id)
	require.NoError(t, err)
	approxEqualRaw(t, vec, raw, 1e-6) // raw round-trip on disk
	require.InDelta(t, 5.0, float64(norm), 1e-5)

	refVec, err := s.GetVectorRef(id)
	require.NoError(t, err)
	approxEqualRaw(t, vec, refVec, 0)
	refNorm, err := s.GetNorm(id)
	require.NoError(t, err)
	require.Equal(t, refNorm, norm)

	_, _, err = s.GetVectorRefWithNorm(99999)
	require.Error(t, err)
}

// --- Change 6: cosine MmapStore raw round-trip via GetVector ---

// TestCosineMmapRawRoundTrip proves a cosine Put then GetVector returns the RAW
// vector verbatim (no lossy unit*norm).
func TestCosineMmapRawRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: Cosine, Dim: 4, M: 8})
	require.NoError(t, err)
	defer s.Close()

	raw := []float32{3, 0, 4, 0}
	id, err := s.NextNodeId()
	require.NoError(t, err)
	require.NoError(t, s.txnBegin())
	require.NoError(t, s.PutNode(id, 0, raw, 100))
	require.NoError(t, s.txnCommit())

	got, err := s.GetVector(id)
	require.NoError(t, err)
	approxEqualRaw(t, got, raw, 1e-6) // exact raw, not restored unit*norm
}

// --- Change 6: validateVector still rejects poison cosine vectors ---

// TestCosineValidateRejectsPoison proves the cosine norm guards stay protective.
func TestCosineValidateRejectsPoison(t *testing.T) {
	idx := NewHNSWIndex(NewMemNodeStore(Cosine))

	inf := float32(math.Inf(1))
	require.Error(t, idx.validateVector([]float32{inf, 0}), "non-finite component")
	nan := float32(math.NaN())
	require.Error(t, idx.validateVector([]float32{nan, 0}), "NaN component")

	// A true L2 norm that overflows float32 (|v| ~ 4.2e38 > 3.4e38 max) is
	// rejected: the float64 recompute yields a magnitude that casts to +Inf.
	big := float32(3e38)
	require.Error(t, idx.validateVector([]float32{big, big}), "norm overflow")

	// A tiny but finite, normalizable vector is accepted (denom != 0).
	require.NoError(t, idx.validateVector([]float32{1e-20, 0}))
}

// --- Change 5: on-disk format version bump 2 → 3 ---

// TestFreshStoreWritesVersion3 proves a new index writes meta Version 3.
func TestFreshStoreWritesVersion3(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: Cosine, Dim: 4, M: 8})
	require.NoError(t, err)
	require.NoError(t, s.Close())

	h, err := readMetaHeader(dir)
	require.NoError(t, err)
	require.Equal(t, uint32(3), h.Version, "fresh store must be Version 3")
}

// TestOpenRejectsVersion2 proves an old (raw-incompatible) Version 2 index is
// rejected with a clear error rather than silently misread.
func TestOpenRejectsVersion2(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: Cosine, Dim: 4, M: 8})
	require.NoError(t, err)
	// Stamp the on-disk version back to 2, then simulate a crash so Close does
	// not rewrite meta.
	s.meta.Version = 2
	require.NoError(t, writeMetaHeader(s.dir, &s.meta))
	s.simulateCrashNoClose()

	_, err = OpenMmapStore(dir, MmapStoreOptions{Metric: Cosine, Dim: 4, M: 8})
	require.Error(t, err, "Version 2 index must be rejected")
	require.Contains(t, err.Error(), "version")
}
