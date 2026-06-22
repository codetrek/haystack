package idtable

import (
	"encoding/binary"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/stretchr/testify/assert"
)

// openTestAlloc opens a fresh bbolt-backed Allocator in a temp file.
func openTestAlloc(t *testing.T, opts Options) *Allocator {
	t.Helper()
	a, err := Open(filepath.Join(t.TempDir(), "idtable.db"), opts)
	assert.NoError(t, err, "Open failed")
	return a
}

// TestNew tests Open initialization and nextId persistence across reopen.
func TestNew(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idtable.db")

	alloc, err := Open(path, Options{CommitInterval: 50 * time.Millisecond})
	assert.NoError(t, err, "Open failed")
	assert.NotNil(t, alloc)

	// Verify nextId is initialized to 1 on a fresh database.
	assert.Equal(t, int64(1), alloc.nextId, "Expected nextId to be 1")

	// Allocate 41 ids so nextId advances to 42, then flush + close.
	for i := 0; i < 41; i++ {
		_, err := alloc.GetId([]byte("seed-" + strconv.Itoa(i)))
		assert.NoError(t, err)
	}
	assert.NoError(t, alloc.Commit())
	alloc.Close()

	// Reopen: nextId must be loaded from the persisted meta.
	alloc2, err := Open(path, Options{CommitInterval: 50 * time.Millisecond})
	assert.NoError(t, err, "Re-Open failed")
	assert.NotNil(t, alloc2)
	assert.Equal(t, int64(42), alloc2.nextId, "Expected nextId to be loaded correctly")
	alloc2.Close()
}

// TestNewWithInvalidData tests Open with a corrupted nextId in the meta bucket.
func TestNewWithInvalidData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idtable.db")

	// Seed a negative nextId directly into the meta bucket.
	seedNextId := func(t *testing.T, raw []byte) {
		t.Helper()
		db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
		assert.NoError(t, err)
		err = db.Update(func(tx *bolt.Tx) error {
			b, err := tx.CreateBucketIfNotExists(bucketMeta)
			if err != nil {
				return err
			}
			return b.Put(metaNextId, raw)
		})
		assert.NoError(t, err)
		db.Close()
	}

	// A negative big-endian nextId is malformed → Open must fail.
	neg := make([]byte, 8)
	var negVal int64 = -5
	binary.BigEndian.PutUint64(neg, uint64(negVal))
	seedNextId(t, neg)
	_, err := Open(path, Options{})
	assert.Error(t, err, "Expected Open to fail with negative nextId")

	// A too-short (non-8-byte) nextId is also malformed (decodeId yields -1).
	short := filepath.Join(t.TempDir(), "idtable.db")
	db, err := bolt.Open(short, 0600, &bolt.Options{Timeout: time.Second})
	assert.NoError(t, err)
	assert.NoError(t, db.Update(func(tx *bolt.Tx) error {
		b, e := tx.CreateBucketIfNotExists(bucketMeta)
		if e != nil {
			return e
		}
		return b.Put(metaNextId, []byte{1, 2, 3}) // 3 bytes
	}))
	db.Close()
	_, err = Open(short, Options{})
	assert.Error(t, err, "Expected Open to fail with a too-short nextId")
}

// TestCrashRelease verifies CrashRelease drops uncommitted (pending) allocations
// and releases the file lock without flushing — mimicking an abrupt termination.
func TestCrashRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idtable.db")
	alloc, err := Open(path, Options{CommitInterval: time.Hour})
	assert.NoError(t, err)

	// Commit one mapping (durable), then allocate another that stays pending.
	committed, err := alloc.GetId([]byte("durable"))
	assert.NoError(t, err)
	assert.NoError(t, alloc.Commit())
	_, err = alloc.GetId([]byte("pending")) // staged, NOT committed
	assert.NoError(t, err)

	alloc.CrashRelease() // drop the lock WITHOUT flushing pending

	// After CrashRelease the allocator is closed.
	_, err = alloc.GetId([]byte("x"))
	assert.Error(t, err, "GetId after CrashRelease must error")
	// Double CrashRelease and a Close afterwards are safe no-ops.
	alloc.CrashRelease()
	alloc.Close()

	// Reopen: the committed mapping survives; the pending one was discarded.
	alloc2, err := Open(path, Options{CommitInterval: time.Hour})
	assert.NoError(t, err)
	defer alloc2.Close()
	_, found, err := alloc2.Lookup([]byte("durable"))
	assert.NoError(t, err)
	assert.True(t, found, "committed mapping must survive CrashRelease")
	got, err := alloc2.GetId([]byte("durable"))
	assert.NoError(t, err)
	assert.Equal(t, committed, got)

	_, found, err = alloc2.Lookup([]byte("pending"))
	assert.NoError(t, err)
	assert.False(t, found, "uncommitted mapping must be discarded by CrashRelease")
}

// TestGetId tests the GetId method.
func TestGetId(t *testing.T) {
	alloc := openTestAlloc(t, Options{CommitInterval: 50 * time.Millisecond})
	defer alloc.Close()

	// Test getting ID for a new key.
	testKey := []byte("test-key-1")
	id1, err := alloc.GetId(testKey)
	assert.NoError(t, err, "GetId failed")

	// Verify the ID is returned as 8 binary-encoded bytes.
	assert.Equal(t, 8, len(id1), "Expected ID length to be 8 bytes")
	assert.Equal(t, uint64(1), binary.BigEndian.Uint64([]byte(id1)), "Expected first ID to be 1")

	// Same key returns the same ID.
	id2, err := alloc.GetId(testKey)
	assert.NoError(t, err, "GetId failed for existing key")
	assert.Equal(t, id1, id2, "Expected same ID for same key")

	// A different key returns a different, incremented ID.
	id3, err := alloc.GetId([]byte("test-key-2"))
	assert.NoError(t, err, "GetId failed for second key")
	assert.NotEqual(t, id1, id3, "Expected different IDs for different keys")
	assert.Equal(t, uint64(2), binary.BigEndian.Uint64([]byte(id3)), "Expected second ID to be 2")
}

// TestLookup verifies the non-allocating lookup path.
func TestLookup(t *testing.T) {
	alloc := openTestAlloc(t, Options{CommitInterval: 50 * time.Millisecond})
	defer alloc.Close()

	// Unknown key: found=false, allocates nothing.
	id, found, err := alloc.Lookup([]byte("never-seen"))
	assert.NoError(t, err)
	assert.False(t, found, "unknown key must not be found")
	assert.Equal(t, int64(0), id)
	assert.Equal(t, int64(1), alloc.nextId, "Lookup must not allocate")

	// Allocate, then Lookup finds the same id (from pending, pre-commit).
	got, err := alloc.GetId([]byte("k"))
	assert.NoError(t, err)
	id, found, err = alloc.Lookup([]byte("k"))
	assert.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, got, EncodeId(id), "Lookup id must match GetId")

	// After commit it is found from the durable store too.
	assert.NoError(t, alloc.Commit())
	id, found, err = alloc.Lookup([]byte("k"))
	assert.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(1), id)
}

// TestGetIdConcurrency tests GetId under concurrent access.
func TestGetIdConcurrency(t *testing.T) {
	alloc := openTestAlloc(t, Options{CommitInterval: 50 * time.Millisecond})
	defer alloc.Close()

	numGoroutines := 10
	numKeysPerGoroutine := 10
	resultChan := make(chan map[string]string, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(goroutineId int) {
			results := make(map[string]string)
			for j := 0; j < numKeysPerGoroutine; j++ {
				key := []byte("key-" + strconv.Itoa(goroutineId) + "-" + strconv.Itoa(j))
				id, err := alloc.GetId(key)
				if err != nil {
					t.Errorf("GetId failed in goroutine %d: %v", goroutineId, err)
					return
				}
				results[string(key)] = id
			}
			resultChan <- results
		}(i)
	}

	allResults := make(map[string]string)
	for i := 0; i < numGoroutines; i++ {
		results := <-resultChan
		for key, id := range results {
			if existingId, exists := allResults[key]; exists {
				assert.Equal(t, existingId, id, "Same key should get same ID: %s", key)
			}
			allResults[key] = id
		}
	}

	idSet := make(map[string]bool)
	for _, id := range allResults {
		idSet[id] = true
	}
	assert.Equal(t, numGoroutines*numKeysPerGoroutine, len(idSet), "Expected all IDs to be unique")
}

// TestGetIdWithClosedStore tests GetId after Close.
func TestGetIdWithClosedStore(t *testing.T) {
	alloc := openTestAlloc(t, Options{CommitInterval: 50 * time.Millisecond})
	alloc.Close()

	_, err := alloc.GetId([]byte("test-key"))
	assert.Error(t, err, "Expected GetId to fail after Close")

	_, _, err = alloc.Lookup([]byte("test-key"))
	assert.Error(t, err, "Expected Lookup to fail after Close")

	err = alloc.Commit()
	assert.Error(t, err, "Expected Commit to fail after Close")
}

// TestParseId tests the parseId function.
func TestParseId(t *testing.T) {
	testCases := []struct {
		input    string
		expected int64
	}{
		{"0", 0},
		{"42", 42},
		{"12345", 12345},
		{"-1", -1},
		{"invalid", -1},
		{"", -1},
		{"12.34", -1},
		{"abc123", -1},
	}

	for _, tc := range testCases {
		result := parseId(tc.input)
		assert.Equal(t, tc.expected, result, "parseId(%q) should return %d", tc.input, tc.expected)
	}
}

// TestGetIdWithLargeNumberOfKeys tests GetId with a large number of keys.
func TestGetIdWithLargeNumberOfKeys(t *testing.T) {
	alloc := openTestAlloc(t, Options{CommitInterval: 50 * time.Millisecond})
	defer alloc.Close()

	numKeys := 100
	ids := make(map[string]string)

	for i := 0; i < numKeys; i++ {
		key := []byte("large-test-key-" + strconv.Itoa(i))
		id, err := alloc.GetId(key)
		assert.NoError(t, err, "GetId failed for key %d", i)
		ids[string(key)] = id
	}

	idSet := make(map[string]bool)
	for _, id := range ids {
		idSet[id] = true
	}
	assert.Equal(t, numKeys, len(idSet), "Expected all IDs to be unique")

	// Requesting the same keys again returns the same IDs.
	for i := 0; i < numKeys; i++ {
		key := []byte("large-test-key-" + strconv.Itoa(i))
		id, err := alloc.GetId(key)
		assert.NoError(t, err, "GetId failed for existing key %d", i)
		assert.Equal(t, ids[string(key)], id, "Expected consistent ID for key %s", string(key))
	}
}

// TestGetIdPersistence tests that IDs persist across reopen.
func TestGetIdPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idtable.db")
	testKey := []byte("persistence-test-key")

	var firstId string
	{
		alloc, err := Open(path, Options{CommitInterval: 50 * time.Millisecond})
		assert.NoError(t, err, "Open failed")
		firstId, err = alloc.GetId(testKey)
		assert.NoError(t, err, "GetId failed")
		alloc.Close() // flushes pending on close
	}

	{
		alloc, err := Open(path, Options{CommitInterval: 50 * time.Millisecond})
		assert.NoError(t, err, "Open failed on reopen")
		defer alloc.Close()
		secondId, err := alloc.GetId(testKey)
		assert.NoError(t, err, "GetId failed on reopen")
		assert.Equal(t, firstId, secondId, "ID should be persistent across restarts")
	}
}
