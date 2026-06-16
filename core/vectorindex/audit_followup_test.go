package vectorindex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Fix 1: cosine vectors whose norm is non-zero but sub-tiny overflow float32
// when normalized (1/norm == +Inf), poisoning the stored vector. validateVector
// must reject them. A normal vector must still insert cleanly.
func TestCosineTinyNormRejected(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenMmapStore(dir, MmapStoreOptions{Metric: Cosine, Dim: 4, M: 4})
	require.NoError(t, err)
	defer store.Close()
	idx := NewHNSWIndex(store)

	// 1e-40 is below float32 normal range; 1/1e-40 overflows to +Inf.
	err = idx.Insert(1, []float32{1e-40, 0, 0, 0})
	require.Error(t, err, "tiny-norm cosine vector must be rejected, not poison-stored")

	// A normal vector still inserts cleanly.
	require.NoError(t, idx.Insert(2, []float32{1, 2, 3, 4}))
}

// Fix 2: meta.bin is the new-vs-existing sentinel. If a data file fails to
// initialize, OpenMmapStore must return an error AND leave no durable meta.bin,
// so a reopen does not take the existing-index branch and fail in mmapAll.
func TestInitAllFilesNoMetaSentinelOnDataFileFailure(t *testing.T) {
	dir := t.TempDir()
	wrapNextCreate(t, "vectors.dat", faultFile{failWrite: true})

	_, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	require.Error(t, err, "init must fail when vectors.dat write fails")

	_, statErr := os.Stat(filepath.Join(dir, "meta.bin"))
	require.True(t, os.IsNotExist(statErr),
		"meta.bin must not exist as a durable sentinel after a failed data-file init")
}

// Fix 3: a mixed-dimension batch on a fresh MemNodeStore (Dim()==0) must be
// rejected with a clean error, not panic mid-apply in the SIMD kernel. The
// rejection must come from Batch.Commit's pre-validation (before any apply), so
// we pin on its specific message rather than the PutNode apply-time fallback.
func TestMixedDimBatchRejected(t *testing.T) {
	store := NewMemNodeStore(DotProduct)
	idx := NewHNSWIndex(store, WithM(4))
	b := idx.NewBatch()
	b.Put(1, []float32{1, 2, 3, 4})
	b.Put(2, []float32{5, 6, 7})
	require.ErrorContains(t, b.Commit(), "batch has mixed vector dimensions",
		"mixed-dimension batch must be rejected by Commit's pre-validation")
}

// Fix 5: randomLevel must be clamped to defaultMaxLayers — the store pre-allocates
// exactly that many upper layers, so level==defaultMaxLayers is the highest valid
// level and anything above would fault the store. The clamp must not over-restrict
// to defaultMaxLayers-1 (off-by-one guard). With M=2 the unclamped draw exceeds
// the cap, so the clamped max must equal defaultMaxLayers.
func TestRandomLevelClamped(t *testing.T) {
	h := NewHNSWIndex(NewMemNodeStore(DotProduct), WithM(2))
	max := 0
	for i := 0; i < 100000; i++ {
		if l := h.randomLevel(); l > max {
			max = l
		}
	}
	require.LessOrEqual(t, max, defaultMaxLayers,
		"randomLevel must never exceed the pre-allocated layer budget")
	require.Equal(t, defaultMaxLayers, max,
		"level==defaultMaxLayers is valid and reachable; clamp must not over-restrict")
}

// Fix 6: Search with k<=0 must return an error, not panic on a negative slice
// bound (results[:k]).
func TestSearchNonPositiveK(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenMmapStore(dir, MmapStoreOptions{Metric: Cosine, Dim: 4, M: 4})
	require.NoError(t, err)
	defer store.Close()
	idx := NewHNSWIndex(store)
	require.NoError(t, idx.Insert(1, []float32{1, 2, 3, 4}))

	query := []float32{1, 0, 0, 0}
	_, err = idx.Search(query, -1)
	require.Error(t, err, "Search with k<0 must return an error, not panic")
	_, err = idx.Search(query, 0)
	require.Error(t, err, "Search with k==0 must return an error")
}
