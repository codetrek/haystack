//go:build benchmark

package vectorindex

import (
	"fmt"
	"testing"
)

// BenchmarkMmapHighestLiveNodeExcluding characterizes the O(TotalSlots) nodes.dat
// sweep that reseats the entry point on delete (VEC-007). It runs only on a cold
// path (the deleted node is the entry point AND has no live neighbors), but the
// sweep scans the never-decremented high-water mark, so a delete-the-current-EP
// churn workload could pay O(N) per delete. Measured across sizes so the cost is
// on record (review follow-up: BenchmarkHNSWDelete uses MemNodeStore in insertion
// order and never exercises this mmap sweep).
func BenchmarkMmapHighestLiveNodeExcluding(b *testing.B) {
	for _, n := range []int{1000, 10000, 50000} {
		b.Run(fmt.Sprintf("N=%d", n), func(b *testing.B) {
			dir := b.TempDir()
			ms, err := OpenMmapStore(dir, MmapStoreOptions{Metric: Euclidean, Dim: 16, M: 16})
			if err != nil {
				b.Fatal(err)
			}
			defer ms.Close()
			if err := ms.txnBegin(); err != nil { // PutNode auto-grows past the initial capacity
				b.Fatal(err)
			}
			for i := 0; i < n; i++ {
				v := make([]float32, 16)
				v[i%16] = float32(i + 1)
				if err := ms.PutNode(uint64(i), 0, v, int64(i)); err != nil {
					b.Fatal(err)
				}
			}
			if err := ms.txnCommit(); err != nil {
				b.Fatal(err)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _, ok, err := ms.HighestLiveNodeExcluding(uint64(i % n))
				if err != nil || !ok {
					b.Fatalf("HighestLiveNodeExcluding: ok=%v err=%v", ok, err)
				}
			}
		})
	}
}
