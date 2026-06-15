package vectorindex

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cluster A (审计 #4/#12/#14/#15): 输入/维度校验。
// 错维度/空向量必须返回 error，而不是越槽覆写、SIMD 越界读或 panic 卡死 inTxn。

func TestInsertWrongDimReturnsError(t *testing.T) {
	store := openTestMmapStore(t) // Dim 4, DotProduct (raw stored)
	defer store.Close()
	idx := NewHNSWIndex(store)

	require.NoError(t, idx.Insert(1, []float32{1, 2, 3, 4}))

	// 过长向量必须被拒，且不破坏相邻槽。
	require.Error(t, idx.Insert(2, []float32{1, 2, 3, 4, 5}), "over-long vector must be rejected")

	v, err := store.GetVectorRef(0)
	require.NoError(t, err)
	assert.Equal(t, []float32{1, 2, 3, 4}, v, "node 0 vector must be intact")

	// store 仍可用。
	require.NoError(t, idx.Insert(3, []float32{5, 6, 7, 8}))
}

func TestSearchEmptyOrWrongDimQueryReturnsError(t *testing.T) {
	store := openTestMmapStore(t)
	defer store.Close()
	idx := NewHNSWIndex(store)
	require.NoError(t, idx.Insert(1, []float32{1, 2, 3, 4}))

	_, err := idx.Search(nil, 5)
	require.Error(t, err, "nil query must error, not panic")

	_, err = idx.Search([]float32{}, 5)
	require.Error(t, err, "empty query must error, not panic")

	_, err = idx.Search([]float32{1, 2, 3}, 5)
	require.Error(t, err, "wrong-dim query must error")
}

func TestCommitWrongDimReturnsErrorNoFault(t *testing.T) {
	store := openTestMmapStore(t)
	defer store.Close()
	idx := NewHNSWIndex(store)

	b := idx.NewBatch()
	b.Put(1, []float32{1, 2, 3, 4})
	b.Put(2, []float32{1, 2, 3}) // wrong dim
	require.Error(t, b.Commit(), "batch with a wrong-dim op must error")

	// 被拒批次不得应用任何内容。
	res, err := idx.Search([]float32{1, 2, 3, 4}, 5)
	require.NoError(t, err)
	assert.Empty(t, res, "rejected batch must not have inserted anything")

	// store 未 fault：全新合法批次能 commit。
	b2 := idx.NewBatch()
	b2.Put(3, []float32{5, 6, 7, 8})
	require.NoError(t, b2.Commit())
}

func TestPutNodeWrongDimDefense(t *testing.T) {
	store := openTestMmapStore(t)
	defer store.Close()

	require.NoError(t, store.PutNode(0, 0, []float32{1, 2, 3, 4}, 100))
	require.Error(t, store.PutNode(1, 0, []float32{1, 2, 3, 4, 5, 6}, 101),
		"PutNode must reject a vector whose length != dim")

	v, err := store.GetVectorRef(0)
	require.NoError(t, err)
	assert.Equal(t, []float32{1, 2, 3, 4}, v, "adjacent slot must be intact")
}

func TestMemStoreDimMismatchRejected(t *testing.T) {
	idx := NewHNSWIndex(NewMemNodeStore(DotProduct))
	require.NoError(t, idx.Insert(1, []float32{1, 2, 3, 4}))
	require.Error(t, idx.Insert(2, []float32{1, 2, 3}),
		"mem store should reject a vector of different dim than the first")
}
