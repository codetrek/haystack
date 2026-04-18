package vectorindex

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMmapStoreGrowOnInsert(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	requireNoError(t, err)
	defer s.Close()

	// Default initial capacity is 1024. Insert at id=1024 should trigger grow.
	vec := []float32{1.0, 2.0, 3.0, 4.0}
	requireNoError(t, s.PutNode(1024, 0, vec))

	got, err := s.GetVector(1024)
	requireNoError(t, err)
	assert.Equal(t, vec, got)

	// Verify capacity doubled.
	assert.True(t, s.vecCapacity >= 2048)
}

func TestMmapStoreGrowMultiple(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	requireNoError(t, err)
	defer s.Close()

	// Insert at id=5000 should require multiple doublings.
	vec := []float32{1.0, 2.0, 3.0, 4.0}
	requireNoError(t, s.PutNode(5000, 0, vec))

	got, err := s.GetVector(5000)
	requireNoError(t, err)
	assert.Equal(t, vec, got)
	assert.True(t, s.vecCapacity >= 5001)
}

func TestMmapStoreGrowPreservesOldData(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	requireNoError(t, err)
	defer s.Close()

	// Write data within initial capacity.
	for i := uint64(0); i < 10; i++ {
		vec := []float32{float32(i), float32(i + 1), float32(i + 2), float32(i + 3)}
		requireNoError(t, s.PutNode(i, 0, vec))
	}

	// Trigger grow.
	vec := []float32{99.0, 98.0, 97.0, 96.0}
	requireNoError(t, s.PutNode(1500, 0, vec))

	// Old data should still be readable.
	for i := uint64(0); i < 10; i++ {
		got, err := s.GetVector(i)
		requireNoError(t, err)
		expected := []float32{float32(i), float32(i + 1), float32(i + 2), float32(i + 3)}
		assert.Equal(t, expected, got)
	}
}

func TestMmapStoreGrowConcurrent(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	requireNoError(t, err)
	defer s.Close()

	// Pre-populate some data.
	for i := uint64(0); i < 100; i++ {
		vec := []float32{float32(i), 0, 0, 0}
		requireNoError(t, s.PutNode(i, 0, vec))
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 20)

	// 10 goroutines reading concurrently.
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := uint64(0); i < 100; i++ {
				_, err := s.GetVector(i)
				if err != nil {
					errCh <- err
					return
				}
			}
		}()
	}

	// 1 goroutine writing past capacity.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := uint64(1024); i < 1024+100; i++ {
			vec := []float32{float32(i), 0, 0, 0}
			if err := s.PutNode(i, 0, vec); err != nil {
				errCh <- err
				return
			}
		}
	}()

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent error: %v", err)
	}
}
