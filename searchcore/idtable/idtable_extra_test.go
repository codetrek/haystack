package idtable

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/codetrek/haystack/searchcore/kv/pebblekv"
	"github.com/stretchr/testify/assert"
)

// TestCloseWhenNotInitialized ensures Close on a zero-value Allocator does not panic.
func TestCloseWhenNotInitialized(t *testing.T) {
	a := &Allocator{}
	a.Close() // should be a no-op
}

// TestGetIdDBReadPath covers the path where the key exists in the database
// but not in the LRU cache (cache miss, DB hit).
func TestGetIdDBReadPath(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-idtable-dbread-*")
	assert.NoError(t, err)
	defer os.RemoveAll(tempDir)

	store, err := pebblekv.Open(filepath.Join(tempDir, "data"), 0)
	assert.NoError(t, err)
	defer store.Close()

	alloc, err := New(store, Options{CommitInterval: 50 * time.Millisecond})
	assert.NoError(t, err)

	// Create a key.
	key := []byte("db-read-test-key")
	id1, err := alloc.GetId(key)
	assert.NoError(t, err)

	// Force a batch commit so the data is visible via store.Get.
	alloc.mu.Lock()
	alloc.tryCommit()
	alloc.mu.Unlock()

	// Evict it from cache so the next GetId must read from DB.
	alloc.lru.Delete(string(key))

	// Get it again — should come from DB, not cache.
	id2, err := alloc.GetId(key)
	assert.NoError(t, err)
	assert.Equal(t, id1, id2)

	alloc.Close()
}

// TestPeriodicCommit covers the background goroutine's timer-triggered tryCommit path.
func TestPeriodicCommit(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-idtable-periodic-*")
	assert.NoError(t, err)
	defer os.RemoveAll(tempDir)

	store, err := pebblekv.Open(filepath.Join(tempDir, "data"), 0)
	assert.NoError(t, err)
	defer store.Close()

	// Use a short commit interval so we don't wait 5+ seconds.
	alloc, err := New(store, Options{CommitInterval: 50 * time.Millisecond})
	assert.NoError(t, err)

	// Create some keys so there's data in the batch.
	for i := 0; i < 5; i++ {
		key := []byte("periodic-key-" + strconv.Itoa(i))
		_, err := alloc.GetId(key)
		assert.NoError(t, err)
	}

	// Wait long enough for the background goroutine's timer to fire.
	time.Sleep(200 * time.Millisecond)

	// Verify the keys are still accessible (the commit didn't break anything).
	for i := 0; i < 5; i++ {
		key := []byte("periodic-key-" + strconv.Itoa(i))
		_, err := alloc.GetId(key)
		assert.NoError(t, err)
	}

	alloc.Close()
}

// TestGetIdMultipleCallsSameKey verifies idempotent ID assignment with cache.
func TestGetIdMultipleCallsSameKey(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-idtable-same-*")
	assert.NoError(t, err)
	defer os.RemoveAll(tempDir)

	store, err := pebblekv.Open(filepath.Join(tempDir, "data"), 0)
	assert.NoError(t, err)
	defer store.Close()

	alloc, err := New(store, Options{CommitInterval: 50 * time.Millisecond})
	assert.NoError(t, err)

	key := []byte("same-key")

	// Call GetId multiple times — should always return same ID (cache hit).
	id1, err := alloc.GetId(key)
	assert.NoError(t, err)

	id2, err := alloc.GetId(key)
	assert.NoError(t, err)
	assert.Equal(t, id1, id2)

	// Force commit, evict from cache, then call again (DB read path).
	alloc.mu.Lock()
	alloc.tryCommit()
	alloc.mu.Unlock()

	alloc.lru.Delete(string(key))
	id3, err := alloc.GetId(key)
	assert.NoError(t, err)
	assert.Equal(t, id1, id3)

	alloc.Close()
}

// TestGetIdWithExistingNextId covers New when the DB already has a nextId value.
func TestGetIdWithExistingNextId(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-idtable-existid-*")
	assert.NoError(t, err)
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "data")
	store, err := pebblekv.Open(dbPath, 0)
	assert.NoError(t, err)

	// Pre-seed a nextId into the DB.
	err = store.Put([]byte{DefaultKeyTypeNextId}, []byte("100"))
	assert.NoError(t, err)

	alloc, err := New(store, Options{CommitInterval: 50 * time.Millisecond})
	assert.NoError(t, err)
	assert.Equal(t, int64(100), alloc.nextId)

	// The next ID assigned should be 100.
	key := []byte("key-after-seed")
	id, err := alloc.GetId(key)
	assert.NoError(t, err)
	assert.NotEmpty(t, id)
	// nextId should have incremented to 101.
	assert.Equal(t, int64(101), alloc.nextId)

	alloc.Close()
	store.Close()
}

// TestCloseCommitsPendingBatch verifies that Close flushes the pending batch.
func TestCloseCommitsPendingBatch(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-idtable-closecommit-*")
	assert.NoError(t, err)
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "data")
	store, err := pebblekv.Open(dbPath, 0)
	assert.NoError(t, err)

	alloc, err := New(store, Options{CommitInterval: 50 * time.Millisecond})
	assert.NoError(t, err)

	// Create keys that add to the batch.
	key := []byte("batch-test-key")
	id1, err := alloc.GetId(key)
	assert.NoError(t, err)

	// Close should flush the batch to the database.
	alloc.Close()
	store.Close()

	// Reopen and verify the key survived.
	store2, err := pebblekv.Open(dbPath, 0)
	assert.NoError(t, err)
	defer store2.Close()

	alloc2, err := New(store2, Options{CommitInterval: 50 * time.Millisecond})
	assert.NoError(t, err)
	defer alloc2.Close()

	id2, err := alloc2.GetId(key)
	assert.NoError(t, err)
	assert.Equal(t, id1, id2, "ID should survive close and reopen")
}
