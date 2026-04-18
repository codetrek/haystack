//go:build benchmark

package vectorindex

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"
)

func TestBenchmark50K_MmapStore(t *testing.T) {
	siftDir := "testdata/sift/sift"
	basePath := filepath.Join(siftDir, "sift_base.fvecs")
	queryPath := filepath.Join(siftDir, "sift_query.fvecs")

	if _, err := os.Stat(basePath); os.IsNotExist(err) {
		t.Skip("SIFT dataset not found — download from ftp://ftp.irisa.fr/local/texmex/corpus/sift.tar.gz")
	}

	const (
		nBase  = 50000
		nQuery = 100
		dim    = 128
		k      = 10
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

	queries, err := loadFvecs(queryPath, nQuery)
	if err != nil {
		t.Fatalf("load queries: %v", err)
	}
	t.Logf("Loaded %d queries", len(queries))

	type benchCase struct {
		name   string
		insert func(t *testing.T, store *MmapStore, idx *HNSWIndex, base [][]float32) time.Duration
	}

	cases := []benchCase{
		{
			name: "batch50",
			insert: func(t *testing.T, store *MmapStore, idx *HNSWIndex, base [][]float32) time.Duration {
				t.Log("Inserting 50K vectors: batch_size=50, 1000 batches, sync per batch...")
				start := time.Now()
				batchSize := 50
				nBatches := len(base) / batchSize
				for i := 0; i < nBatches; i++ {
					store.BeginBatch()
					for j := 0; j < batchSize; j++ {
						vecIdx := i*batchSize + j
						if err := idx.Insert(fmt.Sprintf("%d", vecIdx), base[vecIdx]); err != nil {
							t.Fatalf("insert vector %d: %v", vecIdx, err)
						}
					}
					if err := store.CommitBatch(true); err != nil {
						t.Fatalf("CommitBatch at batch %d: %v", i, err)
					}
					if (i+1)%200 == 0 {
						t.Logf("  completed %d/%d batches (%d vectors, elapsed %v)",
							i+1, nBatches, (i+1)*batchSize, time.Since(start).Round(time.Millisecond))
					}
				}
				return time.Since(start)
			},
		},
		{
			name: "bulk",
			insert: func(t *testing.T, store *MmapStore, idx *HNSWIndex, base [][]float32) time.Duration {
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
				elapsed := time.Since(start)
				if err := store.Sync(); err != nil {
					t.Fatalf("store.Sync: %v", err)
				}
				if err := store.SetSyncMode(SyncImmediate); err != nil {
					t.Fatalf("SetSyncMode(Immediate): %v", err)
				}
				return elapsed
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: mParam})
			if err != nil {
				t.Fatalf("OpenMmapStore: %v", err)
			}
			defer store.Close()

			idx := NewHNSWIndex(store, EuclideanDistance)

			insertElapsed := tc.insert(t, store, idx, base)
			t.Logf("Insert complete: %v (%.2fms/op)",
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

			t.Logf("Computing brute-force ground truth for %d queries over %d vectors...", len(queries), nBase)
			gt := make([][]int, len(queries))
			for qi, q := range queries {
				gt[qi] = bruteForceKNN(q, base, k, EuclideanDistance)
			}

			nodeMapping := buildNodeToBaseIdxMap(store, nBase, "%d")
			latencies := make([]time.Duration, len(queries))
			recalls := make([]float64, len(queries))

			for qi, q := range queries {
				start := time.Now()
				results, err := idx.Search(q, k)
				latencies[qi] = time.Since(start)
				if err != nil {
					t.Fatalf("search query %d: %v", qi, err)
				}
				recalls[qi] = recallAtKMapped(gt[qi], results, k, nodeMapping)
			}

			sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
			p50 := percentile(latencies, 0.50)
			p95 := percentile(latencies, 0.95)
			p99 := percentile(latencies, 0.99)

			var totalRecall float64
			for _, r := range recalls {
				totalRecall += r
			}
			meanRecall := totalRecall / float64(len(recalls))

			minLat := latencies[0]
			maxLat := latencies[len(latencies)-1]
			var totalLat time.Duration
			for _, l := range latencies {
				totalLat += l
			}
			meanLat := totalLat / time.Duration(len(latencies))

			t.Log("========== BENCHMARK RESULTS ==========")
			t.Logf("Dataset:        SIFT 50K (dim=%d)", dim)
			t.Logf("Store:          MmapStore (M=%d)", mParam)
			t.Logf("Mode:           %s", tc.name)
			t.Logf("Insert time:    %v (%.2fms/op)",
				insertElapsed.Round(time.Millisecond),
				float64(insertElapsed.Milliseconds())/float64(nBase))
			t.Logf("Memory:         HeapInuse=%dMB  Sys=%dMB",
				memStats.HeapInuse/(1024*1024), memStats.Sys/(1024*1024))
			t.Logf("Disk:           %.2fMB", float64(diskBytes)/(1024*1024))
			t.Logf("Search latency (n=%d queries):", len(queries))
			t.Logf("  min:  %v", minLat)
			t.Logf("  p50:  %v", p50)
			t.Logf("  p95:  %v", p95)
			t.Logf("  p99:  %v", p99)
			t.Logf("  max:  %v", maxLat)
			t.Logf("  mean: %v", meanLat)
			t.Logf("Recall@%d:      %.4f", k, meanRecall)
			t.Log("========================================")

			if meanRecall < 0.80 {
				t.Errorf("recall@%d = %.4f, want >= 0.80", k, meanRecall)
			}
			p99Ms := float64(p99.Microseconds()) / 1000.0
			if p99Ms > 50.0 {
				t.Errorf("p99 latency %.2fms exceeds threshold 50ms", p99Ms)
			}
		})
	}
}
