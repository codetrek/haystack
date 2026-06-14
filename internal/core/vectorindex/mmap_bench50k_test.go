//go:build benchmark

package vectorindex

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestBenchmark50K_MmapStore(t *testing.T) {
	siftDir := "testdata/sift/sift"
	basePath := filepath.Join(siftDir, "sift_base.fvecs")

	if _, err := os.Stat(basePath); os.IsNotExist(err) {
		t.Skip("SIFT dataset not found — download from ftp://ftp.irisa.fr/local/texmex/corpus/sift.tar.gz")
	}

	const (
		nBase  = 50000
		dim    = 128
		mParam = 16
	)

	t.Logf("Loading %d SIFT base vectors...", nBase)
	base, err := loadFvecs(basePath, nBase)
	if err != nil {
		t.Fatalf("load base: %v", err)
	}
	if len(base) < nBase {
		t.Fatalf("expected %d base vectors, got %d", nBase, len(base))
	}
	base = base[:nBase]
	t.Logf("Loaded %d vectors, dim=%d", len(base), len(base[0]))

	batchSizes := []int{10, 20, 40, 60, 80, 100, 150, 200}

	for _, bs := range batchSizes {
		bs := bs
		t.Run(fmt.Sprintf("batch%d", bs), func(t *testing.T) {
			dir := t.TempDir()
			store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: mParam, Metric: Euclidean})
			if err != nil {
				t.Fatalf("OpenMmapStore: %v", err)
			}
			defer store.Close()

			idx := NewHNSWIndex(store)

			t.Logf("Inserting %d vectors: batch_size=%d, sync per batch...", nBase, bs)
			start := time.Now()
			nBatches := nBase / bs
			remainder := nBase % bs
			for i := 0; i < nBatches; i++ {
				store.BeginBatch()
				for j := 0; j < bs; j++ {
					vecIdx := i*bs + j
					if err := idx.Insert(fmt.Sprintf("%d", vecIdx), base[vecIdx]); err != nil {
						t.Fatalf("insert vector %d: %v", vecIdx, err)
					}
				}
				if err := store.CommitBatch(true); err != nil {
					t.Fatalf("CommitBatch at batch %d: %v", i, err)
				}
				if (i+1)%200 == 0 {
					t.Logf("  completed %d/%d batches (%d vectors, elapsed %v)",
						i+1, nBatches, (i+1)*bs, time.Since(start).Round(time.Millisecond))
				}
			}
			if remainder > 0 {
				store.BeginBatch()
				for j := 0; j < remainder; j++ {
					vecIdx := nBatches*bs + j
					if err := idx.Insert(fmt.Sprintf("%d", vecIdx), base[vecIdx]); err != nil {
						t.Fatalf("insert vector %d: %v", vecIdx, err)
					}
				}
				if err := store.CommitBatch(true); err != nil {
					t.Fatalf("CommitBatch remainder: %v", err)
				}
			}
			insertElapsed := time.Since(start)

			t.Logf("Insert complete: %v (%.4fms/op)",
				insertElapsed.Round(time.Millisecond),
				float64(insertElapsed.Milliseconds())/float64(nBase))

			var memStats runtime.MemStats
			runtime.ReadMemStats(&memStats)
			t.Logf("Memory: HeapInuse=%dMB  Sys=%dMB",
				memStats.HeapInuse/(1024*1024), memStats.Sys/(1024*1024))

			var diskBytes int64
			filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() {
					diskBytes += info.Size()
				}
				return nil
			})
			t.Logf("Disk usage: %.2fMB", float64(diskBytes)/(1024*1024))

			t.Log("========== BENCHMARK RESULTS ==========")
			t.Logf("Dataset:        SIFT 50K (dim=%d)", dim)
			t.Logf("Store:          MmapStore (M=%d)", mParam)
			t.Logf("Mode:           batch%d (sync per batch)", bs)
			t.Logf("Insert time:    %v (%.4fms/op)",
				insertElapsed.Round(time.Millisecond),
				float64(insertElapsed.Milliseconds())/float64(nBase))
			t.Logf("Memory:         HeapInuse=%dMB  Sys=%dMB",
				memStats.HeapInuse/(1024*1024), memStats.Sys/(1024*1024))
			t.Logf("Disk:           %.2fMB", float64(diskBytes)/(1024*1024))
			t.Log("========================================")
		})
	}

	t.Run("bulk", func(t *testing.T) {
		dir := t.TempDir()
		store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: mParam})
		if err != nil {
			t.Fatalf("OpenMmapStore: %v", err)
		}
		defer store.Close()

		idx := NewHNSWIndex(store)

		t.Log("Inserting 50K vectors: deferred sync (single bulk)...")
		if err := store.SetSyncMode(SyncDeferred); err != nil {
			t.Fatalf("SetSyncMode(Deferred): %v", err)
		}
		start := time.Now()
		for i, v := range base {
			if err := idx.Insert(fmt.Sprintf("%d", i), v); err != nil {
				t.Fatalf("insert vector %d: %v", i, err)
			}
			if (i+1)%10000 == 0 {
				t.Logf("  inserted %d/%d vectors (elapsed %v)",
					i+1, len(base), time.Since(start).Round(time.Millisecond))
			}
		}
		insertElapsed := time.Since(start)
		if err := store.Sync(); err != nil {
			t.Fatalf("store.Sync: %v", err)
		}
		if err := store.SetSyncMode(SyncImmediate); err != nil {
			t.Fatalf("SetSyncMode(Immediate): %v", err)
		}

		t.Logf("Insert complete: %v (%.4fms/op)",
			insertElapsed.Round(time.Millisecond),
			float64(insertElapsed.Milliseconds())/float64(nBase))

		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)
		t.Logf("Memory: HeapInuse=%dMB  Sys=%dMB",
			memStats.HeapInuse/(1024*1024), memStats.Sys/(1024*1024))

		var diskBytes int64
		filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				diskBytes += info.Size()
			}
			return nil
		})
		t.Logf("Disk usage: %.2fMB", float64(diskBytes)/(1024*1024))

		t.Log("========== BENCHMARK RESULTS ==========")
		t.Logf("Dataset:        SIFT 50K (dim=%d)", dim)
		t.Logf("Store:          MmapStore (M=%d)", mParam)
		t.Logf("Mode:           bulk (deferred sync)")
		t.Logf("Insert time:    %v (%.4fms/op)",
			insertElapsed.Round(time.Millisecond),
			float64(insertElapsed.Milliseconds())/float64(nBase))
		t.Logf("Memory:         HeapInuse=%dMB  Sys=%dMB",
			memStats.HeapInuse/(1024*1024), memStats.Sys/(1024*1024))
		t.Logf("Disk:           %.2fMB", float64(diskBytes)/(1024*1024))
		t.Log("========================================")
	})
}
