package vectorindex

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func BenchmarkMmapStore50KInsert(b *testing.B) {
	const (
		numVectors = 50000
		dim        = 128
		m          = 16
	)

	// Pre-generate random vectors.
	rng := rand.New(rand.NewSource(42))
	vectors := make([][]float32, numVectors)
	for i := range vectors {
		vec := make([]float32, dim)
		for j := range vec {
			vec[j] = rng.Float32()
		}
		vectors[i] = vec
	}

	for n := 0; n < b.N; n++ {
		dir := b.TempDir()
		s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: dim, M: m})
		if err != nil {
			b.Fatal(err)
		}

		start := time.Now()

		s.BeginBatch()
		for i := 0; i < numVectors; i++ {
			id, err := s.NextNodeId()
			if err != nil {
				b.Fatal(err)
			}
			docId := fmt.Sprintf("doc-%d", id)
			if err := s.SetNodeMapping(docId, id); err != nil {
				b.Fatal(err)
			}
			if err := s.PutNode(id, 0, vectors[i]); err != nil {
				b.Fatal(err)
			}

			// Simulate HNSW L0 connections: random M*2 neighbors from already-inserted nodes.
			if i > 0 {
				nbCount := m * 2
				if i < nbCount {
					nbCount = i
				}
				nbs := make([]uint64, nbCount)
				for j := range nbs {
					nbs[j] = uint64(rng.Intn(i))
				}
				if err := s.SetNeighbors(id, 0, nbs); err != nil {
					b.Fatal(err)
				}
			}
		}
		if err := s.CommitBatch(true); err != nil {
			b.Fatal(err)
		}

		elapsed := time.Since(start)
		b.ReportMetric(elapsed.Seconds(), "total_sec")
		b.ReportMetric(float64(numVectors)/elapsed.Seconds(), "inserts/sec")

		// Spot-check correctness: sample 100 random nodes.
		for i := 0; i < 100; i++ {
			idx := uint64(rng.Intn(numVectors))
			got, err := s.GetVector(idx)
			if err != nil {
				b.Fatal(err)
			}
			assert.Equal(b, vectors[idx], got)

			docId := fmt.Sprintf("doc-%d", idx)
			nodeId, ok, err := s.GetNodeId(docId)
			if err != nil {
				b.Fatal(err)
			}
			assert.True(b, ok)
			assert.Equal(b, idx, nodeId)
		}

		if err := s.Close(); err != nil {
			b.Fatal(err)
		}

		// Assert < 90s (the task target).
		if elapsed > 90*time.Second {
			b.Fatalf("50K insert took %v, exceeds 90s target", elapsed)
		}
	}
}
