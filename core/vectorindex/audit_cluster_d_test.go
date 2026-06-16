package vectorindex

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cluster D (审计 #2/#3): mmap grow 的并发与失败安全。
//
// #2 (GetVector 与并发 grow 的 use-after-munmap) 由 TestMmapStoreGrowConcurrent 在
// -race 下覆盖：修复前 GetVector 在释放锁后才 copy，竞态会读到被 munmap 的页。

// #3: remap 中途失败 (Truncate/mmap) 不得留下指向已解映射内存的悬挂切片——旧映射必须
// 保持有效，读路径不崩溃。修复前 remapFile 先 munmap 再 Truncate，失败即悬挂。
func TestRemapTruncateFailureKeepsOldMappingReadable(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	require.NoError(t, err)
	defer s.Close()

	require.NoError(t, s.txnBegin())
	require.NoError(t, s.PutNode(0, 0, []float32{1, 2, 3, 4}, 100))
	require.NoError(t, s.txnCommit())

	// 让向量文件的 Truncate 失败，然后插入越过容量以强制 grow。
	s.vecFile = &faultFile{osFile: s.vecFile, failTruncate: true}
	require.Error(t, s.PutNode(s.vecCapacity, 0, []float32{5, 6, 7, 8}, 101),
		"grow must fail when truncate fails")

	// 关键：旧映射必须仍然有效——读 node 0 不得崩溃 (use-after-munmap)，且返回原向量。
	v, err := s.GetVectorRef(0)
	require.NoError(t, err)
	assert.Equal(t, []float32{1, 2, 3, 4}, v)
}
