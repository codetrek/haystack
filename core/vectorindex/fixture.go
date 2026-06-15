package vectorindex

import (
	"encoding/binary"
	"fmt"
	"os"
)

// LoadVectors reads a binary vector file with format:
//
//	[uint32 count][uint32 dim][float32 * count * dim]
//
// and returns vectors as a slice of float32 slices.
func LoadVectors(path string) ([][]float32, error) {
	return loadVectorFile(path)
}

// LoadQueries reads a binary query file (same format as vectors).
func LoadQueries(path string) ([][]float32, error) {
	return loadVectorFile(path)
}

// LoadGroundTruth reads a binary ground truth file with format:
//
//	[uint32 numQueries][uint32 k][uint32 * numQueries * k]
//
// and returns a slice of slices, where each inner slice contains the
// top-k nearest neighbor indices for one query.
func LoadGroundTruth(path string) ([][]uint32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var header [2]uint32
	if err := binary.Read(f, binary.LittleEndian, &header); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	nq, k := int(header[0]), int(header[1])

	flat := make([]uint32, nq*k)
	if err := binary.Read(f, binary.LittleEndian, flat); err != nil {
		return nil, fmt.Errorf("read data: %w", err)
	}

	result := make([][]uint32, nq)
	for i := 0; i < nq; i++ {
		result[i] = flat[i*k : (i+1)*k]
	}
	return result, nil
}

func loadVectorFile(path string) ([][]float32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var header [2]uint32
	if err := binary.Read(f, binary.LittleEndian, &header); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	count, dim := int(header[0]), int(header[1])

	flat := make([]float32, count*dim)
	if err := binary.Read(f, binary.LittleEndian, flat); err != nil {
		return nil, fmt.Errorf("read data: %w", err)
	}

	vectors := make([][]float32, count)
	for i := 0; i < count; i++ {
		vectors[i] = flat[i*dim : (i+1)*dim]
	}
	return vectors, nil
}
