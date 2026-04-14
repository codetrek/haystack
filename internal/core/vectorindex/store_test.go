package vectorindex

import (
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
	"github.com/stretchr/testify/assert"
)

func openTestDB(t *testing.T) *pebble.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := pebble.Open(filepath.Join(dir, "test.db"), &pebble.Options{})
	if err != nil {
		t.Fatalf("failed to open pebble: %v", err)
	}
	return db
}

// requireNoError fails the test immediately if err is non-nil.
func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPutAndGetVector(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	vec := []float32{1.0, 2.5, -3.14, 0.0}
	requireNoError(t, store.PutNode(10, 2, vec))

	got, err := store.GetVector(10)
	requireNoError(t, err)
	assert.Equal(t, vec, got)
}

func TestPutAndGetNodeLevel(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	requireNoError(t, store.PutNode(10, 3, []float32{1.0}))

	level, err := store.GetNodeLevel(10)
	requireNoError(t, err)
	assert.Equal(t, 3, level)
}

func TestDeleteNode(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	vec := []float32{1.0, 2.0, 3.0}
	requireNoError(t, store.PutNode(5, 1, vec))
	requireNoError(t, store.SetNeighbors(5, 0, []uint64{1, 2}))
	requireNoError(t, store.SetNeighbors(5, 1, []uint64{3}))
	requireNoError(t, store.SetNodeMapping("doc-5", 5))

	// Verify data exists
	_, err := store.GetVector(5)
	requireNoError(t, err)

	// Delete
	requireNoError(t, store.DeleteNode(5))

	// Vector should be gone
	_, err = store.GetVector(5)
	assert.Error(t, err)

	// Level should be gone
	_, err = store.GetNodeLevel(5)
	assert.Error(t, err)

	// Neighbors should be gone
	nb, err := store.GetNeighbors(5, 0)
	requireNoError(t, err)
	assert.Nil(t, nb)

	nb, err = store.GetNeighbors(5, 1)
	requireNoError(t, err)
	assert.Nil(t, nb)

	// Doc mapping should be gone
	_, found, err := store.GetNodeId("doc-5")
	requireNoError(t, err)
	assert.False(t, found)
}

func TestNeighborsMultipleLayers(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	requireNoError(t, store.PutNode(1, 2, []float32{0.1}))

	layer0 := []uint64{2, 3, 4, 5}
	layer1 := []uint64{3, 5}
	layer2 := []uint64{5}

	requireNoError(t, store.SetNeighbors(1, 0, layer0))
	requireNoError(t, store.SetNeighbors(1, 1, layer1))
	requireNoError(t, store.SetNeighbors(1, 2, layer2))

	got0, err := store.GetNeighbors(1, 0)
	requireNoError(t, err)
	assert.Equal(t, layer0, got0)

	got1, err := store.GetNeighbors(1, 1)
	requireNoError(t, err)
	assert.Equal(t, layer1, got1)

	got2, err := store.GetNeighbors(1, 2)
	requireNoError(t, err)
	assert.Equal(t, layer2, got2)
}

func TestEntryPoint(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	requireNoError(t, store.SetEntryPoint(42, 5))

	nodeId, maxLayer, err := store.GetEntryPoint()
	requireNoError(t, err)
	assert.Equal(t, uint64(42), nodeId)
	assert.Equal(t, 5, maxLayer)

	// Update entry point
	requireNoError(t, store.SetEntryPoint(100, 3))

	nodeId, maxLayer, err = store.GetEntryPoint()
	requireNoError(t, err)
	assert.Equal(t, uint64(100), nodeId)
	assert.Equal(t, 3, maxLayer)
}

func TestNodeMapping(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	requireNoError(t, store.SetNodeMapping("doc-abc", 7))

	nodeId, found, err := store.GetNodeId("doc-abc")
	requireNoError(t, err)
	assert.True(t, found)
	assert.Equal(t, uint64(7), nodeId)

	// Delete mapping
	requireNoError(t, store.DeleteNodeMapping("doc-abc"))

	_, found, err = store.GetNodeId("doc-abc")
	requireNoError(t, err)
	assert.False(t, found)
}

func TestNextNodeId(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	id1, err := store.NextNodeId()
	requireNoError(t, err)
	assert.Equal(t, uint64(1), id1)

	id2, err := store.NextNodeId()
	requireNoError(t, err)
	assert.Equal(t, uint64(2), id2)

	id3, err := store.NextNodeId()
	requireNoError(t, err)
	assert.Equal(t, uint64(3), id3)
}

func TestRestartPersistence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "persist.db")

	// Phase 1: write data
	db1, err := pebble.Open(dbPath, &pebble.Options{})
	if err != nil {
		t.Fatalf("failed to open pebble: %v", err)
	}

	store1 := NewPebbleNodeStore(db1, 1)

	requireNoError(t, store1.PutNode(1, 2, []float32{1.0, 2.0}))
	requireNoError(t, store1.SetNeighbors(1, 0, []uint64{2, 3}))
	requireNoError(t, store1.SetEntryPoint(1, 2))
	requireNoError(t, store1.SetNodeMapping("doc-1", 1))

	id, err := store1.NextNodeId()
	requireNoError(t, err)
	assert.Equal(t, uint64(1), id)

	requireNoError(t, db1.Close())

	// Phase 2: reopen and verify
	db2, err := pebble.Open(dbPath, &pebble.Options{})
	if err != nil {
		t.Fatalf("failed to reopen pebble: %v", err)
	}
	defer db2.Close()

	store2 := NewPebbleNodeStore(db2, 1)

	vec, err := store2.GetVector(1)
	requireNoError(t, err)
	assert.Equal(t, []float32{1.0, 2.0}, vec)

	level, err := store2.GetNodeLevel(1)
	requireNoError(t, err)
	assert.Equal(t, 2, level)

	nb, err := store2.GetNeighbors(1, 0)
	requireNoError(t, err)
	assert.Equal(t, []uint64{2, 3}, nb)

	epId, epLayer, err := store2.GetEntryPoint()
	requireNoError(t, err)
	assert.Equal(t, uint64(1), epId)
	assert.Equal(t, 2, epLayer)

	nodeId, found, err := store2.GetNodeId("doc-1")
	requireNoError(t, err)
	assert.True(t, found)
	assert.Equal(t, uint64(1), nodeId)

	// NextNodeId should continue from where we left off
	nextId, err := store2.NextNodeId()
	requireNoError(t, err)
	assert.Equal(t, uint64(2), nextId)
}

func TestGetNonExistentNode(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	_, err := store.GetVector(999)
	assert.Error(t, err)

	_, err = store.GetNodeLevel(999)
	assert.Error(t, err)

	nb, err := store.GetNeighbors(999, 0)
	requireNoError(t, err)
	assert.Nil(t, nb)

	_, found, err := store.GetNodeId("nonexistent")
	requireNoError(t, err)
	assert.False(t, found)

	_, _, err = store.GetEntryPoint()
	assert.Error(t, err)
}

func TestDeleteNonExistentNode(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	// Should not error
	assert.NoError(t, store.DeleteNode(999))
	assert.NoError(t, store.DeleteNodeMapping("nonexistent"))
}

func TestTableIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store1 := NewPebbleNodeStore(db, 1)
	store2 := NewPebbleNodeStore(db, 2)

	// Write to table 1
	requireNoError(t, store1.PutNode(1, 0, []float32{1.0}))
	requireNoError(t, store1.SetNodeMapping("doc-a", 1))

	// Write to table 2 with same node ID
	requireNoError(t, store2.PutNode(1, 0, []float32{9.0}))
	requireNoError(t, store2.SetNodeMapping("doc-b", 1))

	// Table 1 data should be independent
	vec1, err := store1.GetVector(1)
	requireNoError(t, err)
	assert.Equal(t, []float32{1.0}, vec1)

	nodeId1, found, err := store1.GetNodeId("doc-a")
	requireNoError(t, err)
	assert.True(t, found)
	assert.Equal(t, uint64(1), nodeId1)

	// Table 2 data should be independent
	vec2, err := store2.GetVector(1)
	requireNoError(t, err)
	assert.Equal(t, []float32{9.0}, vec2)

	nodeId2, found, err := store2.GetNodeId("doc-b")
	requireNoError(t, err)
	assert.True(t, found)
	assert.Equal(t, uint64(1), nodeId2)

	// Cross-table lookups should not find data
	_, found, err = store1.GetNodeId("doc-b")
	requireNoError(t, err)
	assert.False(t, found)
}

func TestEmptyNeighbors(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)

	requireNoError(t, store.SetNeighbors(1, 0, []uint64{}))

	nb, err := store.GetNeighbors(1, 0)
	requireNoError(t, err)
	assert.Equal(t, []uint64{}, nb)
}

func TestCloseDoesNotCloseDB(t *testing.T) {
	db, err := pebble.Open("", &pebble.Options{FS: vfs.NewMem()})
	if err != nil {
		t.Fatalf("failed to open in-memory pebble: %v", err)
	}
	defer db.Close()

	store := NewPebbleNodeStore(db, 1)
	requireNoError(t, store.Close())

	// DB should still be usable after store.Close()
	requireNoError(t, store.PutNode(1, 0, []float32{1.0}))
	_, err = store.GetVector(1)
	requireNoError(t, err)
}
