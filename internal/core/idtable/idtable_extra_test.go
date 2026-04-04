package idtable

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/codetrek/haystack/internal/core/storage"
	"github.com/stretchr/testify/assert"
)

// TestInitAlreadyInitialized covers the "already initialized" branch in Init.
func TestInitAlreadyInitialized(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-idtable-extra-*")
	assert.NoError(t, err)
	defer os.RemoveAll(tempDir)

	database, err := storage.Open(filepath.Join(tempDir, "data"), 0)
	assert.NoError(t, err)
	defer database.Close()

	err = Init(database)
	assert.NoError(t, err)

	// Second Init should fail with "already initialized".
	err = Init(database)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already initialized")

	Close()
}

// TestCloseWhenNotInitialized ensures Close on a nil state does not panic.
func TestCloseWhenNotInitialized(t *testing.T) {
	// Save and clear global state.
	oldDB := db
	oldClosing := closing
	oldDone := done
	db = nil
	closing = nil
	done = nil

	Close() // should be a no-op

	// Restore state.
	db = oldDB
	closing = oldClosing
	done = oldDone
}

// TestGetIdDBReadPath covers the path where the key exists in the database
// but not in the LRU cache (cache miss, DB hit).
func TestGetIdDBReadPath(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-idtable-dbread-*")
	assert.NoError(t, err)
	defer os.RemoveAll(tempDir)

	database, err := storage.Open(filepath.Join(tempDir, "data"), 0)
	assert.NoError(t, err)
	defer database.Close()

	err = Init(database)
	assert.NoError(t, err)

	// Create a key.
	key := []byte("db-read-test-key")
	id1, err := GetId(key)
	assert.NoError(t, err)

	// Force a batch commit so the data is visible via db.Get.
	mu.Lock()
	tryCommit()
	mu.Unlock()

	// Evict it from cache so the next GetId must read from DB.
	lru.Delete(string(key))

	// Get it again — should come from DB, not cache.
	id2, err := GetId(key)
	assert.NoError(t, err)
	assert.Equal(t, id1, id2)

	Close()
}

// TestInitPeriodicCommit covers the background goroutine's timer-triggered tryCommit path.
func TestInitPeriodicCommit(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-idtable-periodic-*")
	assert.NoError(t, err)
	defer os.RemoveAll(tempDir)

	database, err := storage.Open(filepath.Join(tempDir, "data"), 0)
	assert.NoError(t, err)
	defer database.Close()

	// Use a short commit interval so we don't wait 5+ seconds.
	saved := CommitInterval
	CommitInterval = 50 * time.Millisecond
	defer func() { CommitInterval = saved }()

	err = Init(database)
	assert.NoError(t, err)

	// Create some keys so there's data in the batch.
	for i := 0; i < 5; i++ {
		key := []byte("periodic-key-" + strconv.Itoa(i))
		_, err := GetId(key)
		assert.NoError(t, err)
	}

	// Wait long enough for the background goroutine's timer to fire.
	time.Sleep(200 * time.Millisecond)

	// Verify the keys are still accessible (the commit didn't break anything).
	for i := 0; i < 5; i++ {
		key := []byte("periodic-key-" + strconv.Itoa(i))
		_, err := GetId(key)
		assert.NoError(t, err)
	}

	Close()
}

// TestGetIdMultipleCallsSameKey verifies idempotent ID assignment with cache.
func TestGetIdMultipleCallsSameKey(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-idtable-same-*")
	assert.NoError(t, err)
	defer os.RemoveAll(tempDir)

	database, err := storage.Open(filepath.Join(tempDir, "data"), 0)
	assert.NoError(t, err)
	defer database.Close()

	err = Init(database)
	assert.NoError(t, err)

	key := []byte("same-key")

	// Call GetId multiple times — should always return same ID (cache hit).
	id1, err := GetId(key)
	assert.NoError(t, err)

	id2, err := GetId(key)
	assert.NoError(t, err)
	assert.Equal(t, id1, id2)

	// Force commit, evict from cache, then call again (DB read path).
	mu.Lock()
	tryCommit()
	mu.Unlock()

	lru.Delete(string(key))
	id3, err := GetId(key)
	assert.NoError(t, err)
	assert.Equal(t, id1, id3)

	Close()
}

// TestGetIdWithExistingNextId covers Init when the DB already has a nextId value.
func TestGetIdWithExistingNextId(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-idtable-existid-*")
	assert.NoError(t, err)
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "data")
	database, err := storage.Open(dbPath, 0)
	assert.NoError(t, err)

	// Pre-seed a nextId into the DB.
	err = database.Put(EncodeIncrIdKey(), []byte("100"))
	assert.NoError(t, err)

	err = Init(database)
	assert.NoError(t, err)
	assert.Equal(t, int64(100), nextId)

	// The next ID assigned should be 100.
	key := []byte("key-after-seed")
	id, err := GetId(key)
	assert.NoError(t, err)
	assert.NotEmpty(t, id)
	// nextId should have incremented to 101.
	assert.Equal(t, int64(101), nextId)

	Close()
	database.Close()
}

// TestCloseCommitsPendingBatch verifies that Close flushes the pending batch.
func TestCloseCommitsPendingBatch(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-idtable-closecommit-*")
	assert.NoError(t, err)
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "data")
	database, err := storage.Open(dbPath, 0)
	assert.NoError(t, err)

	err = Init(database)
	assert.NoError(t, err)

	// Create keys that add to the batch.
	key := []byte("batch-test-key")
	id1, err := GetId(key)
	assert.NoError(t, err)

	// Close should flush the batch to the database.
	Close()
	database.Close()

	// Reopen and verify the key survived.
	database2, err := storage.Open(dbPath, 0)
	assert.NoError(t, err)
	defer database2.Close()

	err = Init(database2)
	assert.NoError(t, err)

	id2, err := GetId(key)
	assert.NoError(t, err)
	assert.Equal(t, id1, id2, "ID should survive close and reopen")

	Close()
}
