//go:build tools

// Command gen-testdata generates binary fixture files for HNSW benchmark tests.
//
// It produces three files in core/vectorindex/testdata/:
//   - vectors_100k_384d.bin   100,000 random 384-dim float32 vectors
//   - queries_50_384d.bin     50 random 384-dim query vectors
//   - ground_truth_top10.bin  brute-force top-10 nearest neighbors per query (cosine distance)
//
// Usage (run from the core module directory):
//
//	go run ./cmd/gen-testdata/
package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	numVectors = 100_000
	numQueries = 50
	dim        = 384
	k          = 10
	seed       = 42
	outDir     = "vectorindex/testdata"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "gen-testdata: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	rng := rand.New(rand.NewSource(seed))

	// --- generate vectors ---
	fmt.Printf("Generating %d vectors (%d dims)...\n", numVectors, dim)
	vectors := makeRandomVectors(rng, numVectors, dim)
	if err := writeVectorFile(filepath.Join(outDir, "vectors_100k_384d.bin"), vectors, numVectors, dim); err != nil {
		return err
	}
	fmt.Println("  wrote vectors_100k_384d.bin")

	// --- generate queries ---
	fmt.Printf("Generating %d query vectors (%d dims)...\n", numQueries, dim)
	queries := makeRandomVectors(rng, numQueries, dim)
	if err := writeVectorFile(filepath.Join(outDir, "queries_50_384d.bin"), queries, numQueries, dim); err != nil {
		return err
	}
	fmt.Println("  wrote queries_50_384d.bin")

	// --- compute ground truth (brute-force) ---
	fmt.Printf("Computing ground truth (top-%d for %d queries against %d vectors)...\n", k, numQueries, numVectors)
	start := time.Now()
	gt := computeGroundTruth(queries, vectors, k)
	fmt.Printf("  brute force took %v\n", time.Since(start).Round(time.Millisecond))
	if err := writeGroundTruthFile(filepath.Join(outDir, "ground_truth_top10.bin"), gt, numQueries, k); err != nil {
		return err
	}
	fmt.Println("  wrote ground_truth_top10.bin")

	fmt.Println("Done.")
	return nil
}

// makeRandomVectors returns n vectors of the given dimension with values drawn
// from a standard normal distribution.
func makeRandomVectors(rng *rand.Rand, n, d int) []float32 {
	v := make([]float32, n*d)
	for i := range v {
		v[i] = float32(rng.NormFloat64())
	}
	return v
}

// writeVectorFile writes a flat vector file:
//
//	[uint32 count][uint32 dim][float32 * count * dim]
func writeVectorFile(path string, data []float32, count, d int) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()

	header := [2]uint32{uint32(count), uint32(d)}
	if err := binary.Write(f, binary.LittleEndian, header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if err := binary.Write(f, binary.LittleEndian, data); err != nil {
		return fmt.Errorf("write data: %w", err)
	}
	return nil
}

// writeGroundTruthFile writes the ground truth file:
//
//	[uint32 numQueries][uint32 k][uint32 * numQueries * k]
func writeGroundTruthFile(path string, indices []uint32, nq, topK int) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()

	header := [2]uint32{uint32(nq), uint32(topK)}
	if err := binary.Write(f, binary.LittleEndian, header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if err := binary.Write(f, binary.LittleEndian, indices); err != nil {
		return fmt.Errorf("write data: %w", err)
	}
	return nil
}

type idDist struct {
	id   uint32
	dist float32
}

// computeGroundTruth returns a flat slice of numQueries*k indices, where each
// consecutive k entries are the top-k nearest neighbor indices for one query
// using cosine distance.
func computeGroundTruth(queries, vectors []float32, topK int) []uint32 {
	nq := len(queries) / dim
	nv := len(vectors) / dim
	result := make([]uint32, nq*topK)

	for qi := 0; qi < nq; qi++ {
		q := queries[qi*dim : (qi+1)*dim]
		dists := make([]idDist, nv)
		for vi := 0; vi < nv; vi++ {
			v := vectors[vi*dim : (vi+1)*dim]
			dists[vi] = idDist{id: uint32(vi), dist: cosineDistance(q, v)}
		}
		sort.Slice(dists, func(i, j int) bool {
			return dists[i].dist < dists[j].dist
		})
		for i := 0; i < topK; i++ {
			result[qi*topK+i] = dists[i].id
		}
		if (qi+1)%10 == 0 {
			fmt.Printf("  query %d/%d done\n", qi+1, nq)
		}
	}
	return result
}

func cosineDistance(a, b []float32) float32 {
	var dot, normA, normB float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 1
	}
	return float32(1 - dot/denom)
}
