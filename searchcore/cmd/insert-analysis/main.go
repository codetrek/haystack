package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync/atomic"

	vi "github.com/codetrek/haystack/searchcore/vectorindex"
)

type countingStore struct {
	vi.NodeStore
	reads        atomic.Int64
	putNodes     atomic.Int64
	setNeighbors atomic.Int64
	setNorms     atomic.Int64
}

func (c *countingStore) GetVectorRef(id uint64) ([]float32, error) {
	c.reads.Add(1)
	return c.NodeStore.GetVectorRef(id)
}
func (c *countingStore) GetVector(id uint64) ([]float32, error) {
	c.reads.Add(1)
	return c.NodeStore.GetVector(id)
}
func (c *countingStore) PutNode(id uint64, level int, vector []float32, docId int64) error {
	c.putNodes.Add(1)
	return c.NodeStore.PutNode(id, level, vector, docId)
}
func (c *countingStore) SetNeighbors(id uint64, layer int, neighbors []uint64) error {
	c.setNeighbors.Add(1)
	return c.NodeStore.SetNeighbors(id, layer, neighbors)
}
func (c *countingStore) SetNorm(id uint64, norm float32) error {
	c.setNorms.Add(1)
	return c.NodeStore.SetNorm(id, norm)
}

func (c *countingStore) ResetAll() (reads, puts, neighbors, norms int64) {
	return c.reads.Swap(0), c.putNodes.Swap(0), c.setNeighbors.Swap(0), c.setNorms.Swap(0)
}

func loadFvecs(path string, limit int) ([][]float32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var vectors [][]float32
	for len(vectors) < limit {
		var dim int32
		if err := binary.Read(f, binary.LittleEndian, &dim); err != nil {
			break
		}
		vec := make([]float32, dim)
		if err := binary.Read(f, binary.LittleEndian, &vec); err != nil {
			return nil, fmt.Errorf("read vector %d: %v", len(vectors), err)
		}
		vectors = append(vectors, vec)
	}
	return vectors, nil
}

func main() {
	n := 50000
	siftPath := "vectorindex/testdata/sift/sift/sift_base.fvecs"

	fmt.Printf("Loading %d SIFT vectors...\n", n)
	vectors, err := loadFvecs(siftPath, n)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load: %v\n", err)
		os.Exit(1)
	}

	inner := vi.NewMemNodeStore(vi.Euclidean)
	cs := &countingStore{NodeStore: inner}
	idx := vi.NewHNSWIndex(cs)

	samplePoints := map[int]bool{
		1000: true, 2000: true, 3000: true, 5000: true,
		10000: true, 15000: true, 20000: true, 30000: true,
		40000: true, 50000: true,
	}

	type sample struct {
		n                                             int
		avgReads, avgPuts, avgNeighbors, avgNorms     float64
		avgTotalWrites                                float64
		cumReads, cumWrites                           int64
	}

	var samples []sample
	var wReads, wPuts, wNeighbors, wNorms int64
	var wCount int
	var cumReads, cumWrites int64

	for i, v := range vectors {
		cs.ResetAll()
		if err := idx.Insert(int64(i), v); err != nil {
			fmt.Fprintf(os.Stderr, "insert %d: %v\n", i, err)
			os.Exit(1)
		}
		reads, puts, neighbors, norms := cs.ResetAll()
		writes := puts + neighbors + norms

		cumReads += reads
		cumWrites += writes
		wReads += reads
		wPuts += puts
		wNeighbors += neighbors
		wNorms += norms
		wCount++

		nodeCount := i + 1
		if samplePoints[nodeCount] {
			c := float64(wCount)
			samples = append(samples, sample{
				n:              nodeCount,
				avgReads:       float64(wReads) / c,
				avgPuts:        float64(wPuts) / c,
				avgNeighbors:   float64(wNeighbors) / c,
				avgNorms:       float64(wNorms) / c,
				avgTotalWrites: float64(wPuts+wNeighbors+wNorms) / c,
				cumReads:       cumReads,
				cumWrites:      cumWrites,
			})
			wReads, wPuts, wNeighbors, wNorms, wCount = 0, 0, 0, 0, 0
		}
	}

	fmt.Printf("\n%-8s %-12s %-10s %-14s %-10s %-14s %-14s %-14s\n",
		"Nodes", "Avg reads", "Avg puts", "Avg neighbors", "Avg norms", "Avg writes", "Cum reads", "Cum writes")
	fmt.Printf("%-8s %-12s %-10s %-14s %-10s %-14s %-14s %-14s\n",
		"-----", "---------", "--------", "-------------", "---------", "----------", "---------", "----------")
	for _, s := range samples {
		fmt.Printf("%-8d %-12.1f %-10.1f %-14.1f %-10.1f %-14.1f %-14d %-14d\n",
			s.n, s.avgReads, s.avgPuts, s.avgNeighbors, s.avgNorms, s.avgTotalWrites, s.cumReads, s.cumWrites)
	}
}
