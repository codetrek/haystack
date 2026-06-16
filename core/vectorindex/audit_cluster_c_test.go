package vectorindex

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Cluster C (审计 #5/#8): 墓碑/僵尸节点不得从读访问器泄漏出去，否则会成为搜索导航
// 起点/入口点，导致召回崩塌。GetDocId 已有守卫；其余访问器也必须有。

// #5: 删除后，读访问器必须报错（而非返回过期数据），HNSW 的 skip-on-error 自愈逻辑才生效。
func TestReadAccessorsRejectDeletedNode(t *testing.T) {
	store := openTestMmapStore(t) // Dim 4
	defer store.Close()

	require.NoError(t, store.PutNode(0, 0, []float32{1, 2, 3, 4}, 100))
	_, err := store.GetVectorRef(0)
	require.NoError(t, err, "live node must be readable")

	require.NoError(t, store.DeleteNode(0))
	_, err = store.GetVectorRef(0)
	require.Error(t, err, "GetVectorRef on a deleted node must error")
	_, err = store.GetNodeLevel(0)
	require.Error(t, err, "GetNodeLevel on a deleted node must error")
	_, err = store.GetNorm(0)
	require.Error(t, err, "GetNorm on a deleted node must error")
}

// #8: aborted-txn 泄漏的僵尸槽 (id >= TotalSlots，但 occupied 字节已落盘) 必须被所有
// 读访问器拒绝，而不只是 GetDocId——否则僵尸会污染导航、甚至被选为持久化入口点。
func TestReadAccessorsRejectZombieSlot(t *testing.T) {
	dir := t.TempDir()
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4, CheckpointInterval: 1_000_000}
	s, err := OpenMmapStore(dir, opts)
	require.NoError(t, err)

	// 提交 node 0 作为入口点。
	require.NoError(t, s.txnBegin())
	require.NoError(t, s.PutNode(0, 0, []float32{1, 0, 0, 0}, 100))
	require.NoError(t, s.SetEntryPoint(0, 0))
	require.NoError(t, s.txnCommit())

	// 未提交事务：写僵尸槽 1 + committed→zombie 边，刷盘后崩溃（无 COMMIT）。
	require.NoError(t, s.txnBegin())
	zid, err := s.NextNodeId()
	require.NoError(t, err)
	require.Equal(t, uint64(1), zid)
	require.NoError(t, s.PutNode(zid, 0, []float32{0, 1, 0, 0}, 999))
	require.NoError(t, s.SetNeighbors(0, 0, []uint64{zid}))
	require.NoError(t, s.syncAll())
	simulateCrash(s)

	// 重开：replay 丢弃未提交 txn → TotalSlots == 1。
	s2, err := OpenMmapStore(dir, opts)
	require.NoError(t, err)
	defer s2.Close()
	require.Equal(t, uint64(1), s2.meta.TotalSlots)

	// 所有读访问器都必须把僵尸槽当 error，而不只是 GetDocId。
	_, err = s2.GetVectorRef(zid)
	require.Error(t, err, "GetVectorRef must reject the zombie slot")
	_, err = s2.GetNodeLevel(zid)
	require.Error(t, err, "GetNodeLevel must reject the zombie slot")
	_, err = s2.GetNorm(zid)
	require.Error(t, err, "GetNorm must reject the zombie slot")

	// committed node 仍完全可读。
	_, err = s2.GetVectorRef(0)
	require.NoError(t, err)
}
