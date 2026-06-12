package idtable

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/codetrek/haystack/searchcore/kv"
	"github.com/codetrek/haystack/searchcore/kv/pebblekv"
	"github.com/stretchr/testify/assert"
)

// openTestStore opens a pebble-backed kv.Store in a temporary directory.
func openTestStore(t *testing.T, dir string) (kv.Store, error) {
	t.Helper()
	return pebblekv.Open(filepath.Join(dir, "data"), 0)
}

// TestNew tests the New function with various scenarios.
func TestNew(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-idtable-test-*")
	assert.NoError(t, err, "Failed to create temp dir")
	defer os.RemoveAll(tempDir)

	store, err := openTestStore(t, tempDir)
	assert.NoError(t, err, "Failed to open kv store")
	defer store.Close()

	// Test initial initialization.
	alloc, err := New(store, Options{CommitInterval: 50 * time.Millisecond})
	assert.NoError(t, err, "New failed")
	assert.NotNil(t, alloc)

	// Verify nextId is initialized to 1.
	assert.Equal(t, int64(1), alloc.nextId, "Expected nextId to be 1")
	alloc.Close()

	// Test re-initialization with existing data: seed a specific nextId.
	testNextId := int64(42)
	err = store.Put([]byte{DefaultKeyTypeNextId}, []byte(strconv.FormatInt(testNextId, 10)))
	assert.NoError(t, err, "Failed to put nextId")

	alloc2, err := New(store, Options{CommitInterval: 50 * time.Millisecond})
	assert.NoError(t, err, "Re-New failed")
	assert.NotNil(t, alloc2)
	assert.Equal(t, testNextId, alloc2.nextId, "Expected nextId to be loaded correctly")
	alloc2.Close()
}

// TestNewWithInvalidData tests New with corrupted data.
func TestNewWithInvalidData(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-idtable-test-*")
	assert.NoError(t, err, "Failed to create temp dir")
	defer os.RemoveAll(tempDir)

	store, err := openTestStore(t, tempDir)
	assert.NoError(t, err, "Failed to open kv store")
	defer store.Close()

	// Put invalid nextId value.
	err = store.Put([]byte{DefaultKeyTypeNextId}, []byte("invalid"))
	assert.NoError(t, err, "Failed to put invalid nextId")

	_, err = New(store, Options{})
	assert.Error(t, err, "Expected New to fail with invalid nextId data")

	// Test with negative nextId.
	err = store.Put([]byte{DefaultKeyTypeNextId}, []byte("-5"))
	assert.NoError(t, err, "Failed to put negative nextId")

	_, err = New(store, Options{})
	assert.Error(t, err, "Expected New to fail with negative nextId")
}

// TestGetId tests the GetId method.
func TestGetId(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-idtable-test-*")
	assert.NoError(t, err, "Failed to create temp dir")
	defer os.RemoveAll(tempDir)

	store, err := openTestStore(t, tempDir)
	assert.NoError(t, err, "Failed to open kv store")
	defer store.Close()

	alloc, err := New(store, Options{CommitInterval: 50 * time.Millisecond})
	assert.NoError(t, err, "New failed")
	defer alloc.Close()

	// Test getting ID for a new key.
	testKey := []byte("test-key-1")
	id1, err := alloc.GetId(testKey)
	assert.NoError(t, err, "GetId failed")

	// Verify the ID is returned as binary encoded bytes.
	assert.Equal(t, 8, len(id1), "Expected ID length to be 8 bytes")

	// Convert the binary ID back to int64 to verify.
	idValue := binary.BigEndian.Uint64([]byte(id1))
	assert.Equal(t, uint64(1), idValue, "Expected first ID to be 1")

	// Test getting ID for the same key should return the same ID.
	id2, err := alloc.GetId(testKey)
	assert.NoError(t, err, "GetId failed for existing key")
	assert.Equal(t, id1, id2, "Expected same ID for same key")

	// Test getting ID for a different key should return a different ID.
	testKey2 := []byte("test-key-2")
	id3, err := alloc.GetId(testKey2)
	assert.NoError(t, err, "GetId failed for second key")

	assert.NotEqual(t, id1, id3, "Expected different IDs for different keys")

	// Verify the second ID is incremented.
	idValue3 := binary.BigEndian.Uint64([]byte(id3))
	assert.Equal(t, uint64(2), idValue3, "Expected second ID to be 2")
}

// TestGetIdConcurrency tests GetId under concurrent access.
func TestGetIdConcurrency(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-idtable-test-*")
	assert.NoError(t, err, "Failed to create temp dir")
	defer os.RemoveAll(tempDir)

	store, err := openTestStore(t, tempDir)
	assert.NoError(t, err, "Failed to open kv store")
	defer store.Close()

	alloc, err := New(store, Options{CommitInterval: 50 * time.Millisecond})
	assert.NoError(t, err, "New failed")
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

	// Verify all IDs are unique.
	idSet := make(map[string]bool)
	for _, id := range allResults {
		idSet[id] = true
	}

	expectedIds := numGoroutines * numKeysPerGoroutine
	assert.Equal(t, expectedIds, len(idSet), "Expected all IDs to be unique")
}

// TestGetIdWithClosedStore tests GetId with a closed store.
func TestGetIdWithClosedStore(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-idtable-test-*")
	assert.NoError(t, err, "Failed to create temp dir")
	defer os.RemoveAll(tempDir)

	store, err := openTestStore(t, tempDir)
	assert.NoError(t, err, "Failed to open kv store")

	alloc, err := New(store, Options{CommitInterval: 50 * time.Millisecond})
	assert.NoError(t, err, "New failed")
	alloc.Close()
	store.Close()

	testKey := []byte("test-key")
	_, err = alloc.GetId(testKey)
	assert.Error(t, err, "Expected GetId to fail with closed store")
}

// TestEncodeIncrIdKey tests the encodeIncrIdKey method.
func TestEncodeIncrIdKey(t *testing.T) {
	a := &Allocator{keyTypeNextId: DefaultKeyTypeNextId}
	key := a.encodeIncrIdKey()

	expectedKey := []byte{DefaultKeyTypeNextId}
	assert.Equal(t, len(expectedKey), len(key), "Expected key length to match")
	assert.Equal(t, DefaultKeyTypeNextId, key[0], "Expected correct key type")
}

// TestEncodeIdKey tests the encodeIdKey method.
func TestEncodeIdKey(t *testing.T) {
	a := &Allocator{keyTypeKey: DefaultKeyTypeKey}
	testKey := []byte("test-key")
	encodedKey := a.encodeIdKey(testKey)

	expectedLength := 1 + len(testKey)
	assert.Equal(t, expectedLength, len(encodedKey), "Expected correct encoded key length")
	assert.Equal(t, DefaultKeyTypeKey, encodedKey[0], "Expected correct key type")

	keyContent := encodedKey[1:]
	assert.Equal(t, string(testKey), string(keyContent), "Expected key content to match")
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
	tempDir, err := os.MkdirTemp("", "haystack-idtable-test-*")
	assert.NoError(t, err, "Failed to create temp dir")
	defer os.RemoveAll(tempDir)

	store, err := openTestStore(t, tempDir)
	assert.NoError(t, err, "Failed to open kv store")
	defer store.Close()

	alloc, err := New(store, Options{CommitInterval: 50 * time.Millisecond})
	assert.NoError(t, err, "New failed")
	defer alloc.Close()

	numKeys := 100
	ids := make(map[string]string)

	for i := 0; i < numKeys; i++ {
		key := []byte("large-test-key-" + strconv.Itoa(i))
		id, err := alloc.GetId(key)
		assert.NoError(t, err, "GetId failed for key %d", i)

		keyStr := string(key)
		if existingId, exists := ids[keyStr]; exists {
			assert.Equal(t, existingId, id, "Key %s should get consistent ID", keyStr)
		}
		ids[keyStr] = id
	}

	idSet := make(map[string]bool)
	for _, id := range ids {
		idSet[id] = true
	}
	assert.Equal(t, numKeys, len(idSet), "Expected all IDs to be unique")

	// Test that requesting the same keys again returns the same IDs.
	for i := 0; i < numKeys; i++ {
		key := []byte("large-test-key-" + strconv.Itoa(i))
		id, err := alloc.GetId(key)
		assert.NoError(t, err, "GetId failed for existing key %d", i)

		expectedId := ids[string(key)]
		assert.Equal(t, expectedId, id, "Expected consistent ID for key %s", string(key))
	}
}

// TestGetIdPersistence tests that IDs persist across restarts.
func TestGetIdPersistence(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-idtable-test-*")
	assert.NoError(t, err, "Failed to create temp dir")
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "data")
	testKey := []byte("persistence-test-key")

	var firstId string
	{
		store, err := pebblekv.Open(dbPath, 0)
		assert.NoError(t, err, "Failed to open kv store")

		alloc, err := New(store, Options{CommitInterval: 50 * time.Millisecond})
		assert.NoError(t, err, "New failed")

		firstId, err = alloc.GetId(testKey)
		assert.NoError(t, err, "GetId failed")

		alloc.Close()
		store.Close()
	}

	{
		store, err := pebblekv.Open(dbPath, 0)
		assert.NoError(t, err, "Failed to reopen kv store")
		defer store.Close()

		alloc, err := New(store, Options{CommitInterval: 50 * time.Millisecond})
		assert.NoError(t, err, "New failed on reopen")
		defer alloc.Close()

		secondId, err := alloc.GetId(testKey)
		assert.NoError(t, err, "GetId failed on reopen")

		assert.Equal(t, firstId, secondId, "ID should be persistent across restarts")
	}
}
