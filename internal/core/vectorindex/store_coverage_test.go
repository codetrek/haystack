package vectorindex

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Tests to improve Pebble store coverage by exercising batch and non-batch paths.

func TestPebbleDeleteNode_WithBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewPebbleNodeStore(db, 1)

	// Insert a node with neighbors on multiple layers
	err := s.PutNode(1, 2, []float32{1, 2, 3})
	assert.NoError(t, err)
	err = s.SetNeighbors(1, 0, []uint64{2, 3})
	assert.NoError(t, err)
	err = s.SetNeighbors(1, 1, []uint64{4})
	assert.NoError(t, err)
	err = s.SetNodeMapping("doc1", 1)
	assert.NoError(t, err)
	err = s.SetNorm(1, 3.74)
	assert.NoError(t, err)

	// Delete in batch mode
	s.BeginBatch()
	err = s.DeleteNode(1)
	assert.NoError(t, err)
	err = s.CommitBatch(true)
	assert.NoError(t, err)

	// Verify deleted
	_, err = s.GetVector(1)
	assert.Error(t, err)
}

func TestPebbleDeleteNode_WithoutBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewPebbleNodeStore(db, 1)

	// Insert
	err := s.PutNode(1, 1, []float32{1, 2, 3})
	assert.NoError(t, err)
	err = s.SetNeighbors(1, 0, []uint64{2})
	assert.NoError(t, err)
	err = s.SetNodeMapping("doc1", 1)
	assert.NoError(t, err)
	err = s.SetNorm(1, 3.74)
	assert.NoError(t, err)

	// Delete without batch
	err = s.DeleteNode(1)
	assert.NoError(t, err)

	_, err = s.GetVector(1)
	assert.Error(t, err)
}

func TestPebbleDeleteNode_NonExistent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewPebbleNodeStore(db, 1)

	// Delete non-existent — should be no-op
	err := s.DeleteNode(999)
	assert.NoError(t, err)
}

func TestPebblePutNode_WithBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewPebbleNodeStore(db, 1)

	s.BeginBatch()
	err := s.PutNode(1, 0, []float32{1, 2, 3})
	assert.NoError(t, err)
	err = s.CommitBatch(true)
	assert.NoError(t, err)

	vec, err := s.GetVector(1)
	assert.NoError(t, err)
	assert.Equal(t, []float32{1, 2, 3}, vec)
}

func TestPebblePutNode_WithoutBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewPebbleNodeStore(db, 1)

	err := s.PutNode(1, 0, []float32{4, 5, 6})
	assert.NoError(t, err)

	vec, err := s.GetVector(1)
	assert.NoError(t, err)
	assert.Equal(t, []float32{4, 5, 6}, vec)
}

func TestPebbleSetNodeMapping_WithBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewPebbleNodeStore(db, 1)

	s.BeginBatch()
	err := s.SetNodeMapping("doc1", 1)
	assert.NoError(t, err)
	err = s.CommitBatch(true)
	assert.NoError(t, err)

	id, found, err := s.GetNodeId("doc1")
	assert.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, uint64(1), id)
}

func TestPebbleDeleteNodeMapping_WithBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewPebbleNodeStore(db, 1)

	err := s.SetNodeMapping("doc1", 1)
	assert.NoError(t, err)

	s.BeginBatch()
	err = s.DeleteNodeMapping("doc1")
	assert.NoError(t, err)
	err = s.CommitBatch(true)
	assert.NoError(t, err)

	_, found, err := s.GetNodeId("doc1")
	assert.NoError(t, err)
	assert.False(t, found)
}

func TestPebbleDeleteNodeMapping_NonExistent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewPebbleNodeStore(db, 1)

	err := s.DeleteNodeMapping("nonexistent")
	assert.NoError(t, err)
}

func TestPebbleSetNeighbors_WithBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewPebbleNodeStore(db, 1)

	s.BeginBatch()
	err := s.SetNeighbors(1, 0, []uint64{2, 3, 4})
	assert.NoError(t, err)
	err = s.CommitBatch(true)
	assert.NoError(t, err)

	nbs, err := s.GetNeighbors(1, 0)
	assert.NoError(t, err)
	assert.Equal(t, []uint64{2, 3, 4}, nbs)
}

func TestPebbleSetNorm_WithBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	s := NewPebbleNodeStore(db, 1)

	s.BeginBatch()
	err := s.SetNorm(1, 5.0)
	assert.NoError(t, err)
	err = s.CommitBatch(true)
	assert.NoError(t, err)

	norm, err := s.GetNorm(1)
	assert.NoError(t, err)
	assert.InDelta(t, 5.0, norm, 0.001)
}
