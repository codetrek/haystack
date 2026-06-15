package vectorindex

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRandomLevel(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store)

	// Call many times, verify always >= 0
	for i := 0; i < 1000; i++ {
		level := idx.randomLevel()
		assert.GreaterOrEqual(t, level, 0, "randomLevel must be >= 0")
	}
}

func TestNodeDistance_NonExistentNode(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store)

	// nodeDistance on non-existent node should return error
	_, err := idx.nodeDist(999, []float32{1, 2, 3})
	assert.Error(t, err)
}

func TestNodeDistance_ValidNode(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store)

	err := idx.Insert("doc1", []float32{1, 0, 0})
	assert.NoError(t, err)

	dist, err := idx.nodeDist(1, []float32{0, 1, 0})
	assert.NoError(t, err)
	assert.InDelta(t, 1.0, dist, 0.01) // orthogonal vectors -> cosine distance ~1.0
}
