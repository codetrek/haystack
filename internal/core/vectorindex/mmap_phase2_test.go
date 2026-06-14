package vectorindex

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Write-path: PutNode → verify mmap contents directly
// ---------------------------------------------------------------------------

func TestMmapStorePutNodeWritesMmapContents(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	vec := []float32{1.0, 2.0, 3.0, 4.0}
	requireNoError(t, s.PutNode(0, 0, vec))

	// Read raw vector bytes from mmap.
	for i, v := range vec {
		off := pageSize + 0*s.vecSlotSize + i*4
		got := math.Float32frombits(binary.LittleEndian.Uint32(s.vectors[off:]))
		assert.Equal(t, v, got, "vector[%d]", i)
	}

	// Read raw node bytes: level=0, flags=0, norm=sqrt(1+4+9+16)=sqrt(30).
	nodeOff := pageSize + 0*nodeSlotSize
	assert.Equal(t, uint8(0), s.nodes[nodeOff])   // level
	assert.Equal(t, uint8(0), s.nodes[nodeOff+1]) // flags (not deleted)
	norm := math.Float32frombits(binary.LittleEndian.Uint32(s.nodes[nodeOff+4:]))
	assert.InDelta(t, float32(math.Sqrt(30)), norm, 0.01)
}

func TestMmapStorePutNodeWithUpperLevel(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	vec := []float32{1.0, 2.0, 3.0, 4.0}
	requireNoError(t, s.PutNode(0, 3, vec))

	// Node should have an upper slot allocated.
	nodeOff := pageSize + 0*nodeSlotSize
	upperSlot := binary.LittleEndian.Uint32(s.nodes[nodeOff+8:])
	assert.NotEqual(t, uint32(0), upperSlot, "upper slot should be allocated for level > 0")
}

// ---------------------------------------------------------------------------
// SetNeighbors upper layer: multiple layers, verifying mmap content
// ---------------------------------------------------------------------------

func TestMmapStoreSetNeighborsUpperMultipleLayers(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	vec := []float32{1.0, 2.0, 3.0, 4.0}
	// Create node at level=3 so layers 1,2,3 are available.
	requireNoError(t, s.PutNode(0, 3, vec))
	requireNoError(t, s.PutNode(1, 0, vec))
	requireNoError(t, s.PutNode(2, 0, vec))

	// Set neighbors on layers 1, 2, 3.
	requireNoError(t, s.SetNeighbors(0, 1, []uint64{1}))
	requireNoError(t, s.SetNeighbors(0, 2, []uint64{1, 2}))
	requireNoError(t, s.SetNeighbors(0, 3, []uint64{2}))

	got1, err := s.GetNeighbors(0, 1)
	requireNoError(t, err)
	assert.Equal(t, []uint64{1}, got1)

	got2, err := s.GetNeighbors(0, 2)
	requireNoError(t, err)
	assert.Equal(t, []uint64{1, 2}, got2)

	got3, err := s.GetNeighbors(0, 3)
	requireNoError(t, err)
	assert.Equal(t, []uint64{2}, got3)
}

// ---------------------------------------------------------------------------
// Grow: upper graph grow via high-level insert
// ---------------------------------------------------------------------------

func TestMmapStoreGrowUpperGraph(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	requireNoError(t, err)
	defer s.Close()

	initialUpperCap := s.upperCapacity
	vec := []float32{1.0, 0, 0, 0}

	// Insert many nodes with level > 0 to exhaust the initial upper capacity.
	// Each level>0 node allocates one upper slot. Upper starts at cap/4 = 256 (for default 1024).
	// We need ~256+ upper slots to trigger a grow.
	for i := uint64(0); i < initialUpperCap+10; i++ {
		requireNoError(t, s.PutNode(i, 1, vec))
	}

	assert.Greater(t, s.upperCapacity, initialUpperCap, "upper capacity should have grown")
}

// ---------------------------------------------------------------------------
// Close → Reopen: verify full data persistence
// ---------------------------------------------------------------------------

func TestMmapStoreCloseReopenPersistence(t *testing.T) {
	dir := t.TempDir()
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4}

	s, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)

	// Write data.
	vec := []float32{1.0, 2.0, 3.0, 4.0}
	requireNoError(t, s.SetNodeMapping("doc-a", 0))
	requireNoError(t, s.PutNode(0, 2, vec))
	requireNoError(t, s.SetNeighbors(0, 0, []uint64{1}))
	requireNoError(t, s.SetNeighbors(0, 1, []uint64{2}))
	requireNoError(t, s.SetEntryPoint(0, 2))

	requireNoError(t, s.PutNode(1, 0, vec))
	requireNoError(t, s.SetNodeMapping("doc-b", 1))

	requireNoError(t, s.Close())

	// Reopen.
	s2, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	defer s2.Close()

	// Verify vectors.
	got, err := s2.GetVector(0)
	requireNoError(t, err)
	assert.Equal(t, vec, got)

	// Verify node level.
	level, err := s2.GetNodeLevel(0)
	requireNoError(t, err)
	assert.Equal(t, 2, level)

	// Verify entry point.
	epId, epLevel, err := s2.GetEntryPoint()
	requireNoError(t, err)
	assert.Equal(t, uint64(0), epId)
	assert.Equal(t, 2, epLevel)

	// Verify node mapping persistence.
	id, ok, err := s2.GetNodeId("doc-a")
	requireNoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, uint64(0), id)

	id2, ok2, err := s2.GetNodeId("doc-b")
	requireNoError(t, err)
	assert.True(t, ok2)
	assert.Equal(t, uint64(1), id2)

	// Verify L0 neighbors persist via WAL replay.
	nbs, err := s2.GetNeighbors(0, 0)
	requireNoError(t, err)
	assert.Equal(t, []uint64{1}, nbs)
}

// ---------------------------------------------------------------------------
// loadIdmap: verify idmap.dat is correctly loaded
// ---------------------------------------------------------------------------

func TestMmapStoreLoadIdmap(t *testing.T) {
	dir := t.TempDir()
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4}

	// Write multiple mappings, close, reopen — all should be present.
	s, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)

	for i := uint64(0); i < 20; i++ {
		requireNoError(t, s.SetNodeMapping("doc-"+string(rune('A'+i)), i))
	}
	requireNoError(t, s.Close())

	s2, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	defer s2.Close()

	for i := uint64(0); i < 20; i++ {
		id, ok, err := s2.GetNodeId("doc-" + string(rune('A'+i)))
		requireNoError(t, err)
		assert.True(t, ok, "doc-%c should exist", rune('A'+i))
		assert.Equal(t, i, id)
	}
}

func TestMmapStoreLoadIdmapCorrupt(t *testing.T) {
	dir := t.TempDir()
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4}

	s, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	requireNoError(t, s.SetNodeMapping("doc-good", 0))
	requireNoError(t, s.SetNodeMapping("doc-bad", 1))
	requireNoError(t, s.Close())

	// Corrupt the last byte of idmap.dat to invalidate CRC of the 2nd entry.
	path := filepath.Join(dir, "idmap.dat")
	data, err := os.ReadFile(path)
	requireNoError(t, err)
	data[len(data)-1] ^= 0xFF
	requireNoError(t, os.WriteFile(path, data, 0644))

	s2, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	defer s2.Close()

	// First mapping should survive; second is corrupt.
	_, ok, err := s2.GetNodeId("doc-good")
	requireNoError(t, err)
	assert.True(t, ok)

	_, ok2, err := s2.GetNodeId("doc-bad")
	requireNoError(t, err)
	assert.False(t, ok2, "corrupt entry should be skipped")
}

// ---------------------------------------------------------------------------
// syncAll: verify it doesn't panic
// ---------------------------------------------------------------------------

func TestMmapStoreSyncAll(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	// Write some data so the regions are non-trivial.
	vec := []float32{1.0, 2.0, 3.0, 4.0}
	requireNoError(t, s.PutNode(0, 0, vec))

	err := s.syncAll()
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// DeleteNode: write + verify tombstone + close/reopen
// ---------------------------------------------------------------------------

func TestMmapStoreDeleteNodeAndReopen(t *testing.T) {
	dir := t.TempDir()
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4}

	s, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)

	vec := []float32{1.0, 2.0, 3.0, 4.0}
	requireNoError(t, s.PutNode(0, 0, vec))
	requireNoError(t, s.PutNode(1, 0, vec))
	requireNoError(t, s.DeleteNode(0))
	requireNoError(t, s.Close())

	// Reopen: WAL replays insert + delete → node 0 should be deleted.
	s2, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	defer s2.Close()

	_, err = s2.GetNodeLevel(0)
	assert.Error(t, err) // "node 0 is deleted"

	// Node 1 should still be valid.
	level, err := s2.GetNodeLevel(1)
	requireNoError(t, err)
	assert.Equal(t, 0, level)
}

// ---------------------------------------------------------------------------
// rebuildNodeCount: verified indirectly via WAL replay
// ---------------------------------------------------------------------------

func TestMmapStoreRebuildNodeCount(t *testing.T) {
	dir := t.TempDir()
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4}

	s, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)

	vec := []float32{1.0, 2.0, 3.0, 4.0}
	for i := uint64(0); i < 5; i++ {
		requireNoError(t, s.PutNode(i, 0, vec))
	}
	requireNoError(t, s.DeleteNode(2))
	requireNoError(t, s.Close())

	// Reopen triggers WAL replay → rebuildNodeCount.
	s2, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	defer s2.Close()

	// 5 inserted - 1 deleted = 4 active nodes.
	assert.Equal(t, uint64(4), s2.meta.NodeCount)
}

// ---------------------------------------------------------------------------
// CommitBatch exercises WAL flush + syncAll paths
// ---------------------------------------------------------------------------

func TestMmapStoreCommitBatchSyncs(t *testing.T) {
	dir := t.TempDir()
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4}

	s, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	defer s.Close()

	s.BeginBatch()
	vec := []float32{1.0, 2.0, 3.0, 4.0}
	for i := uint64(0); i < 5; i++ {
		requireNoError(t, s.PutNode(i, 0, vec))
	}
	requireNoError(t, s.CommitBatch(true))

	// After commit, data should be readable.
	for i := uint64(0); i < 5; i++ {
		got, err := s.GetVector(i)
		requireNoError(t, err)
		assert.Equal(t, vec, got)
	}
}
