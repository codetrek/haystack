package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildSegmentGraph_ProducesSearchableGraphFile(t *testing.T) {
	rng := rand.New(rand.NewSource(21))
	dim := 16
	n := 200
	rows := make([]struct {
		doc int64
		v   []float32
		pl  Payload
	}, 0, n)
	vecs := make(map[int64][]float32, n)
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		rows = append(rows, struct {
			doc int64
			v   []float32
			pl  Payload
		}{int64(i), v, nil})
		vecs[int64(i)] = v
	}
	head := buildHeadSeg(Cosine, rows)
	segDir := filepath.Join(t.TempDir(), "seg-3-0")
	requireNoError(t, writeSealedSegment(segDir, head, nil))
	ss, err := openSealedSegment(segDir, Cosine)
	requireNoError(t, err)
	defer ss.close()

	cfg := graphConfig{M: 16, EfConstruction: 100, EfSearch: 64, Seed: 42}
	gs, err := buildSegmentGraph(segDir, ss, cfg)
	requireNoError(t, err)

	if _, err := os.Stat(filepath.Join(segDir, "graph.dat")); err != nil {
		t.Fatalf("graph.dat not written: %v", err)
	}
	idx := newHNSWIndex(gs, withGraphEfSearch(64))
	q := make([]float32, dim)
	for d := range q {
		q[d] = rng.Float32()
	}
	got, err := idx.search(q, 10)
	requireNoError(t, err)
	want := bruteForceKNN(Cosine, q, vecs, 10)
	if r := recallAtK(got, want); r < 0.8 {
		t.Fatalf("built-graph recall@10 = %.2f, want >= 0.8", r)
	}
}
