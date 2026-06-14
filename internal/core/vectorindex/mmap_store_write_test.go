package vectorindex

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func openTestMmapStore(t *testing.T) *MmapStore {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
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
	// Only cosine persists a norm (to restore the original scale); the raw
	// metrics store the vector verbatim and skip the norm computation.
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: Cosine, Dim: 4, M: 4})
	requireNoError(t, err)
	defer s.Close()

	vec := []float32{3.0, 4.0, 0.0, 0.0} // norm = 5.0
	requireNoError(t, s.PutNode(0, 0, vec))

	norm, err := s.GetNorm(0)
	requireNoError(t, err)
	assert.InDelta(t, float32(5.0), norm, 0.01)
}

func TestMmapStorePutNodeRawMetricNoNorm(t *testing.T) {
	// A raw metric (dot/euclidean) stores the original vector and reports norm 0,
	// since the norm is never needed to restore or compare it.
	s := openTestMmapStore(t) // DotProduct
	defer s.Close()

	requireNoError(t, s.PutNode(0, 0, []float32{3.0, 4.0, 0.0, 0.0}))

	norm, err := s.GetNorm(0)
	requireNoError(t, err)
	assert.Equal(t, float32(0), norm)
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
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4}

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

	requireNoError(t, s.txnBegin())
	for i := uint64(0); i < 10; i++ {
		vec := []float32{float32(i), 0, 0, 0}
		requireNoError(t, s.PutNode(i, 0, vec))
		requireNoError(t, s.SetNeighbors(i, 0, []uint64{(i + 1) % 10}))
	}
	requireNoError(t, s.txnCommit())

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
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4}

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

func TestTxnInsertSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4}

	s, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)

	const n = 100
	requireNoError(t, s.txnBegin())
	for i := 0; i < n; i++ {
		v := []float32{float32(i), float32(i + 1), float32(i + 2), float32(i + 3)}
		requireNoError(t, s.SetNodeMapping(fmt.Sprintf("doc-%d", i), uint64(i)))
		requireNoError(t, s.PutNode(uint64(i), 0, v))
	}
	requireNoError(t, s.txnCommit())
	requireNoError(t, s.Close())

	s2, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	defer s2.Close()

	for i := 0; i < n; i++ {
		v := []float32{float32(i), float32(i + 1), float32(i + 2), float32(i + 3)}
		got, err := s2.GetVector(uint64(i))
		requireNoError(t, err)
		assert.Equal(t, v, got, "vector %d mismatch after reopen", i)
	}
	nodeId, ok, err := s2.GetNodeId("doc-50")
	requireNoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, uint64(50), nodeId)
}
