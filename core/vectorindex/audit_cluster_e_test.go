package vectorindex

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cluster E (审计 #1/#9/#11): 崩溃恢复耐久。

func eStoreOpts() MmapStoreOptions {
	return MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4, CheckpointInterval: 1000}
}

// #1 (critical): checkpoint 截断 WAL 后重开，LSN 不能重置为 0；否则之后写入的记录
// 会在下一次崩溃恢复时被 Replay(afterLSN=checkpointLSN) 静默丢弃。
func TestWALLSNSurvivesCheckpointReopenCrash(t *testing.T) {
	dir := t.TempDir()

	// Phase 1: 写 3 条 + 显式 checkpoint + 干净 Close（截断 WAL、推进 WalCheckpointLSN）。
	s1, err := OpenMmapStore(dir, eStoreOpts())
	require.NoError(t, err)
	require.NoError(t, s1.txnBegin())
	for i := 0; i < 3; i++ {
		require.NoError(t, s1.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 0}, int64(100+i)))
	}
	require.NoError(t, s1.txnCommit())
	require.NoError(t, s1.Checkpoint())
	require.NoError(t, s1.Close())

	// Phase 2: 重开（WAL 已被截断）→ 再写 3 条 → 模拟崩溃（不 checkpoint）。
	s2, err := OpenMmapStore(dir, eStoreOpts())
	require.NoError(t, err)
	require.NoError(t, s2.txnBegin())
	for i := 3; i < 6; i++ {
		require.NoError(t, s2.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 0}, int64(100+i)))
	}
	require.NoError(t, s2.txnCommit())
	simulateCrash(s2)

	// Phase 3: 重开 → replay 必须恢复 Phase-2 的写入（LSN bug 会把它们丢弃）。
	s3, err := OpenMmapStore(dir, eStoreOpts())
	require.NoError(t, err)
	defer s3.Close()
	for i := 0; i < 6; i++ {
		docId, ok, err := s3.GetDocId(uint64(i))
		require.NoError(t, err)
		require.True(t, ok, "node %d (docId %d) must survive checkpoint→reopen→write→crash→replay", i, 100+i)
		assert.Equal(t, int64(100+i), docId)
	}
}

// #1 白盒：重开后 WAL 的 LSN 必须 >= 上次 checkpoint 的 LSN（不能归 0）。
func TestWALLSNSeededFromCheckpointOnReopen(t *testing.T) {
	dir := t.TempDir()
	s1, err := OpenMmapStore(dir, eStoreOpts())
	require.NoError(t, err)
	require.NoError(t, s1.txnBegin())
	for i := 0; i < 3; i++ {
		require.NoError(t, s1.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 0}, int64(i)))
	}
	require.NoError(t, s1.txnCommit())
	require.NoError(t, s1.Checkpoint())
	cpLSN := s1.meta.WalCheckpointLSN
	require.Greater(t, cpLSN, uint64(0))
	require.NoError(t, s1.Close())

	s2, err := OpenMmapStore(dir, eStoreOpts())
	require.NoError(t, err)
	defer s2.Close()
	assert.GreaterOrEqual(t, s2.wal.LSN(), cpLSN,
		"WAL LSN must be seeded from the checkpoint, not reset to 0")
}

// #11: faulted store 上调 Checkpoint() 必须返回 error，不得持久化未提交状态、不得截断 WAL。
func TestCheckpointOnFaultedStoreErrors(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, eStoreOpts())
	require.NoError(t, err)
	defer s.Close()

	_ = s.fault(fmt.Errorf("injected fault"))
	require.Error(t, s.Checkpoint(), "Checkpoint() on a faulted store must return an error")
}

// #9: fsyncDir 在正常目录上成功、对不存在的目录报错。
func TestFsyncDir(t *testing.T) {
	require.NoError(t, fsyncDir(t.TempDir()))
	require.Error(t, fsyncDir(filepath.Join(t.TempDir(), "does-not-exist")))
}
