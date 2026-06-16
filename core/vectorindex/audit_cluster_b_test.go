package vectorindex

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cluster B (审计 #6/#10/#13): cosine norm 健壮性。

func openTestCosineStore(t *testing.T) *MmapStore {
	t.Helper()
	s, err := OpenMmapStore(t.TempDir(), MmapStoreOptions{Metric: Cosine, Dim: 4, M: 4})
	require.NoError(t, err)
	return s
}

// #6: NaN/Inf 分量、以及 norm 溢出 float32 的大幅值向量，必须被拒，绝不持久化 NaN/Inf。
func TestCosineRejectsNonFiniteAndOverflow(t *testing.T) {
	store := openTestCosineStore(t)
	defer store.Close()
	idx := NewHNSWIndex(store)

	require.Error(t, idx.Insert(1, []float32{float32(math.NaN()), 1, 0, 0}), "NaN component must be rejected")
	require.Error(t, idx.Insert(2, []float32{float32(math.Inf(1)), 0, 0, 0}), "Inf component must be rejected")

	// 分量有限 (< float32 max) 但 L2 norm = 4e38 > 3.4e38 → 溢出，必须拒。
	huge := float32(2e38)
	require.Error(t, idx.Insert(3, []float32{huge, huge, huge, huge}), "norm overflow must be rejected")

	// NaN query 必须返回 error，而不是污染搜索。
	_, err := idx.Search([]float32{float32(math.NaN()), 0, 0, 0}, 5)
	require.Error(t, err, "NaN query must error, not poison the search")
}

// #13: 极小非零 cosine 向量必须被正确归一化为单位向量(不能因 float32 下溢被当零向量)，
// 且 GetVector 能还原原始向量。
func TestCosineTinyVectorNormalizedNotZeroed(t *testing.T) {
	store := openTestCosineStore(t)
	defer store.Close()
	idx := NewHNSWIndex(store)

	// 1e-23 的平方 (1e-46) 在 float32 下溢为 0(旧 vek32.Norm 会得 0 → 当零向量存)。
	tiny := float32(1e-23)
	require.NoError(t, idx.Insert(1, []float32{tiny, 0, 0, 0}))

	nodeId, ok, err := store.GetNodeId(1)
	require.NoError(t, err)
	require.True(t, ok)

	// 存储形必须是单位向量 {1,0,0,0}，而非被零化。
	ref, err := store.GetVectorRef(nodeId)
	require.NoError(t, err)
	assert.InDelta(t, 1.0, ref[0], 1e-5, "tiny vector must be normalized to unit length, not zeroed")

	// 原始向量必须能还原。
	orig, err := store.GetVector(nodeId)
	require.NoError(t, err)
	assert.InDelta(t, float64(tiny), float64(orig[0]), float64(tiny)*1e-3, "original magnitude must round-trip")
}

// #10: norm 用 float64 计算 → 确定、与架构无关、不溢出。对会让 float32 中间累加下溢的输入，
// 仍得到精确非零 norm。
func TestCosineNormFloat64Deterministic(t *testing.T) {
	// {3,4,0,0} 的 norm = 5，精确。
	assert.InDelta(t, 5.0, float64(Cosine.norm([]float32{3, 4, 0, 0})), 1e-6)
	// 极小向量：float32 累加会下溢为 0，float64 给出精确非零 norm。
	n := Cosine.norm([]float32{1e-23, 0, 0, 0})
	assert.Greater(t, float64(n), 0.0, "tiny vector norm must be non-zero (float64 accumulation)")
}
