package vectorindex

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func openTestMmapStore(t *testing.T) *MmapStore {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	requireNoError(t, err)
	return s
}

func TestMmapStorePutNodeAndGetVector(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	vec := []float32{1.0, 2.0, 3.0, 4.0}
	requireNoError(t, s.SetNodeMapping("doc-0", 0))
	requireNoError(t, s.PutNode(0, 0, vec))

	got, err := s.GetVector(0)
	requireNoError(t, err)
	assert.Equal(t, vec, got)
}

func TestMmapStorePutNodeAndGetNorm(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	vec := []float32{3.0, 4.0, 0.0, 0.0} // norm = 5.0
	requireNoError(t, s.PutNode(0, 0, vec))

	norm, err := s.GetNorm(0)
	requireNoError(t, err)
	assert.InDelta(t, float32(5.0), norm, 0.01)
}

func TestMmapStorePutNodeWithLevel(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	vec := []float32{1.0, 2.0, 3.0, 4.0}
	requireNoError(t, s.PutNode(0, 2, vec))

	level, err := s.GetNodeLevel(0)
	requireNoError(t, err)
	assert.Equal(t, 2, level)
}

func TestMmapStoreSetNeighborsL0(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	vec := []float32{1.0, 2.0, 3.0, 4.0}
	requireNoError(t, s.PutNode(0, 0, vec))
	requireNoError(t, s.PutNode(1, 0, vec))
	requireNoError(t, s.PutNode(2, 0, vec))

	nbs := []uint64{1, 2}
	requireNoError(t, s.SetNeighbors(0, 0, nbs))

	got, err := s.GetNeighbors(0, 0)
	requireNoError(t, err)
	assert.Equal(t, nbs, got)
}

func TestMmapStoreSetNeighborsUpper(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	vec := []float32{1.0, 2.0, 3.0, 4.0}
	requireNoError(t, s.PutNode(0, 3, vec)) // level=3 → allocates upper slot

	nbs := []uint64{1, 2}
	requireNoError(t, s.SetNeighbors(0, 2, nbs))

	got, err := s.GetNeighbors(0, 2)
	requireNoError(t, err)
	assert.Equal(t, nbs, got)
}

func TestMmapStoreSetNorm(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	vec := []float32{1.0, 0.0, 0.0, 0.0}
	requireNoError(t, s.PutNode(0, 0, vec))

	requireNoError(t, s.SetNorm(0, 99.5))

	norm, err := s.GetNorm(0)
	requireNoError(t, err)
	assert.InDelta(t, float32(99.5), norm, 0.01)
}

func TestMmapStoreSetEntryPoint(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	requireNoError(t, s.SetEntryPoint(42, 3))

	id, level, err := s.GetEntryPoint()
	requireNoError(t, err)
	assert.Equal(t, uint64(42), id)
	assert.Equal(t, 3, level)
}

func TestMmapStoreNodeMapping(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	requireNoError(t, s.SetNodeMapping("doc-1", 42))

	id, ok, err := s.GetNodeId("doc-1")
	requireNoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, uint64(42), id)
}

func TestMmapStoreNodeMappingPersistence(t *testing.T) {
	dir := t.TempDir()
	opts := MmapStoreOptions{Dim: 4, M: 4}

	s, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	requireNoError(t, s.SetNodeMapping("doc-1", 42))
	requireNoError(t, s.Close())

	// Reopen.
	s2, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	defer s2.Close()

	id, ok, err := s2.GetNodeId("doc-1")
	requireNoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, uint64(42), id)
}

func TestMmapStoreDeleteNodeMapping(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	requireNoError(t, s.SetNodeMapping("doc-1", 42))
	requireNoError(t, s.DeleteNodeMapping("doc-1"))

	_, ok, err := s.GetNodeId("doc-1")
	requireNoError(t, err)
	assert.False(t, ok)
}

func TestMmapStoreBatchWriteAndRead(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	s.BeginBatch()
	assert.Equal(t, 1, s.BatchDepth())

	for i := uint64(0); i < 10; i++ {
		vec := []float32{float32(i), 0, 0, 0}
		requireNoError(t, s.PutNode(i, 0, vec))
		requireNoError(t, s.SetNeighbors(i, 0, []uint64{(i + 1) % 10}))
	}

	requireNoError(t, s.CommitBatch(true))
	assert.Equal(t, 0, s.BatchDepth())

	// Verify all data is readable.
	for i := uint64(0); i < 10; i++ {
		got, err := s.GetVector(i)
		requireNoError(t, err)
		assert.Equal(t, []float32{float32(i), 0, 0, 0}, got)

		nbs, err := s.GetNeighbors(i, 0)
		requireNoError(t, err)
		assert.Equal(t, []uint64{(i + 1) % 10}, nbs)
	}
}

func TestMmapStoreBatchNesting(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	s.BeginBatch()
	assert.Equal(t, 1, s.BatchDepth())

	s.BeginBatch()
	assert.Equal(t, 2, s.BatchDepth())

	requireNoError(t, s.CommitBatch(false))
	assert.Equal(t, 1, s.BatchDepth())
	assert.True(t, s.batchMode) // still in batch

	requireNoError(t, s.CommitBatch(true))
	assert.Equal(t, 0, s.BatchDepth())
	assert.False(t, s.batchMode)
}

func TestMmapStoreDiscardBatch(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	s.BeginBatch()
	s.BeginBatch()
	assert.Equal(t, 2, s.BatchDepth())

	s.DiscardBatch()
	assert.Equal(t, 0, s.BatchDepth())
	assert.False(t, s.batchMode)
}

func TestMmapStoreNextNodeId(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	id0, err := s.NextNodeId()
	requireNoError(t, err)
	assert.Equal(t, uint64(0), id0)

	id1, err := s.NextNodeId()
	requireNoError(t, err)
	assert.Equal(t, uint64(1), id1)

	id2, err := s.NextNodeId()
	requireNoError(t, err)
	assert.Equal(t, uint64(2), id2)
}

func TestMmapStoreNextNodeIdPersistence(t *testing.T) {
	dir := t.TempDir()
	opts := MmapStoreOptions{Dim: 4, M: 4}

	s, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)

	for i := 0; i < 5; i++ {
		_, err := s.NextNodeId()
		requireNoError(t, err)
	}
	requireNoError(t, s.Close())

	// Reopen — NextNodeId should continue from 5.
	s2, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	defer s2.Close()

	id, err := s2.NextNodeId()
	requireNoError(t, err)
	assert.Equal(t, uint64(5), id)
}
