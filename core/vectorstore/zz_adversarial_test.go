package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestAdv_DropDuringBuild_LeaksGraphFile forces the Drop-vs-build ordering where
// DropVectorIndex completes (file absent → no fsync, map entry removed) BEFORE the
// in-flight build writes graph-<name>.dat. The build then re-checks under s.mu,
// finds the index gone, returns WITHOUT installing — but leaves the file on disk.
func TestAdv_DropDuringBuild_LeaksGraphFile(t *testing.T) {
	s := openTestStore(t, Cosine)
	dim := 16
	rng := rand.New(rand.NewSource(99))
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	for i := 0; i < 40; i++ {
		requireNoError(t, s.Put("d-"+itoa(i), randVec(), nil))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	segDir := filepath.Join(s.dir, segDirName(segID(1), 0))
	auxPath := filepath.Join(segDir, "graph-aux.dat")

	dropDone := make(chan struct{})
	var once sync.Once
	orig := fsCreate
	t.Cleanup(func() { fsCreate = orig })
	fsCreate = func(name string) (osFile, error) {
		if strings.HasSuffix(name, "graph-aux.dat") {
			// The aux build is about to write its graph file. Run Drop to completion
			// FIRST (in another goroutine), then proceed with the write — modeling the
			// "Drop wins, then build writes" race window.
			once.Do(func() {
				go func() {
					_ = s.DropVectorIndex("aux")
					close(dropDone)
				}()
			})
			<-dropDone
		}
		return orig(name)
	}

	requireNoError(t, s.CreateVectorIndex("aux", VectorIndexConfig{Type: "hnsw", Metric: Cosine, M: 8}))
	requireNoError(t, s.WaitForIndex())
	<-dropDone

	// aux is gone from the map (Drop removed it) and has no manifest entry, but the
	// build wrote graph-aux.dat AFTER Drop's fsRemove found nothing → orphan leak.
	if _, err := os.Stat(auxPath); err == nil {
		t.Fatalf("ORPHAN LEAK: graph-aux.dat exists on disk with no map/manifest entry: %s", auxPath)
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected stat error: %v", err)
	}
	if _, ok := s.indexes["aux"]; ok {
		t.Fatal("aux should be dropped from the map")
	}
}
