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
