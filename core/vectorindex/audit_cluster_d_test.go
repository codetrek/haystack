package vectorindex

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Cluster D (审计 #2/#3): mmap grow 的并发与失败安全。
//
// #2 (GetVector 与并发 grow 的 use-after-munmap) 由 TestMmapStoreGrowConcurrent 在
// -race 下覆盖：修复前 GetVector 在释放锁后才 copy，竞态会读到被 munmap 的页。

// #3: a grow failure (Truncate/mmap) must not leave a dangling mapping that
// SIGSEGVs on the next read. The slice is nil'd and capacity zeroed before the
// fallible steps, so a read after a failed grow returns an error, not a crash.
// (Windows can't resize a mapped file, so map-new-then-unmap-old isn't portable;
// the cross-platform guarantee is "no use-after-munmap", not old-data readability.)
func TestRemapGrowFailureNoCrash(t *testing.T) {
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

	// 关键：读 node 0 不得崩溃 (use-after-munmap)——返回 error 即可。
	_, err = s.GetVectorRef(0)
	require.Error(t, err, "read after a failed grow must return an error, not SIGSEGV")
}
