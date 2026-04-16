package vectorindex

import (
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/stretchr/testify/assert"
)

// openReadOnlyDB opens a pebble DB in read-only mode.
// Writes will return pebble.ErrReadOnly instead of panicking.
func openReadOnlyDB(t *testing.T) *pebble.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// First create the DB normally
	db, err := pebble.Open(dbPath, &pebble.Options{})
	if err != nil {
		t.Fatalf("failed to create pebble: %v", err)
	}
	// Insert a node so DeleteNode has something to find
	s := NewPebbleNodeStore(db, 1)
	err = s.PutNode(1, 0, []float32{1, 2, 3})
	assert.NoError(t, err)
	err = s.SetNodeMapping("doc1", 1)
	assert.NoError(t, err)
	db.Close()

	// Reopen read-only
	roDB, err := pebble.Open(dbPath, &pebble.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("failed to open pebble read-only: %v", err)
	}
	return roDB
}

func TestPebbleReadOnly_PutNode(t *testing.T) {
	db := openReadOnlyDB(t)
	defer db.Close()
	s := NewPebbleNodeStore(db, 1)

	err := s.PutNode(2, 0, []float32{4, 5, 6})
	assert.Error(t, err, "PutNode should fail on read-only DB")
}

func TestPebbleReadOnly_DeleteNode(t *testing.T) {
	db := openReadOnlyDB(t)
	defer db.Close()
	s := NewPebbleNodeStore(db, 1)

	err := s.DeleteNode(1)
	assert.Error(t, err, "DeleteNode should fail on read-only DB")
}

func TestPebbleReadOnly_SetNodeMapping(t *testing.T) {
	db := openReadOnlyDB(t)
	defer db.Close()
	s := NewPebbleNodeStore(db, 1)

	err := s.SetNodeMapping("doc2", 2)
	assert.Error(t, err, "SetNodeMapping should fail on read-only DB")
}

func TestPebbleReadOnly_DeleteNodeMapping(t *testing.T) {
	db := openReadOnlyDB(t)
	defer db.Close()
	s := NewPebbleNodeStore(db, 1)

	err := s.DeleteNodeMapping("doc1")
	assert.Error(t, err, "DeleteNodeMapping should fail on read-only DB")
}

func TestPebbleReadOnly_SetNeighbors(t *testing.T) {
	db := openReadOnlyDB(t)
	defer db.Close()
	s := NewPebbleNodeStore(db, 1)

	err := s.SetNeighbors(1, 0, []uint64{2, 3})
	assert.Error(t, err, "SetNeighbors should fail on read-only DB")
}

func TestPebbleReadOnly_SetNorm(t *testing.T) {
	db := openReadOnlyDB(t)
	defer db.Close()
	s := NewPebbleNodeStore(db, 1)

	err := s.SetNorm(1, 3.74)
	assert.Error(t, err, "SetNorm should fail on read-only DB")
}

func TestPebbleReadOnly_SetEntryPoint(t *testing.T) {
	db := openReadOnlyDB(t)
	defer db.Close()
	s := NewPebbleNodeStore(db, 1)

	err := s.SetEntryPoint(1, 3)
	assert.Error(t, err, "SetEntryPoint should fail on read-only DB")
}

func TestPebbleReadOnly_NextNodeId(t *testing.T) {
	db := openReadOnlyDB(t)
	defer db.Close()
	s := NewPebbleNodeStore(db, 1)

	_, err := s.NextNodeId()
	assert.Error(t, err, "NextNodeId should fail on read-only DB")
}
