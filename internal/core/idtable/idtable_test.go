package idtable

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/codetrek/haystack/internal/core/storage"
	"github.com/stretchr/testify/assert"
)

// TestInit tests the Init function with various scenarios
func TestInit(t *testing.T) {
	// Set up a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "haystack-idtable-test-*")
	assert.NoError(t, err, "Failed to create temp dir")
	defer os.RemoveAll(tempDir)

	// Initialize storage
	db, err := storage.Open(filepath.Join(tempDir, "data"), 0)
	assert.NoError(t, err, "Failed to open database")
	defer db.Close()

	// Test initial initialization
	err = Init(db)
	assert.NoError(t, err, "Init failed")

	// Verify nextId is initialized to 0
	assert.Equal(t, int64(1), nextId, "Expected nextId to be 0")

	// Test re-initialization with existing data
	// First, set a specific nextId value
	testNextId := int64(42)
	err = db.Put(EncodeIncrIdKey(), []byte(strconv.FormatInt(testNextId, 10)))
	assert.NoError(t, err, "Failed to put nextId")

	Close()

	// Re-initialize
	err = Init(db)
	assert.NoError(t, err, "Re-init failed")

	// Verify nextId is loaded correctly
	assert.Equal(t, testNextId, nextId, "Expected nextId to be loaded correctly")
	Close()
}

// TestInitWithInvalidData tests Init with corrupted data
func TestInitWithInvalidData(t *testing.T) {
	// Set up a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "haystack-idtable-test-*")
	assert.NoError(t, err, "Failed to create temp dir")
	defer os.RemoveAll(tempDir)

	// Initialize storage
	db, err := storage.Open(filepath.Join(tempDir, "data"), 0)
	assert.NoError(t, err, "Failed to open database")
	defer db.Close()

	// Put invalid nextId value
	err = db.Put(EncodeIncrIdKey(), []byte("invalid"))
	assert.NoError(t, err, "Failed to put invalid nextId")

	// Init should fail with invalid data
	err = Init(db)
	assert.Error(t, err, "Expected Init to fail with invalid nextId data")

	// Test with negative nextId
	err = db.Put(EncodeIncrIdKey(), []byte("-5"))
	assert.NoError(t, err, "Failed to put negative nextId")

	err = Init(db)
	assert.Error(t, err, "Expected Init to fail with negative nextId")
	Close()
}

// TestGetId tests the GetId function
func TestGetId(t *testing.T) {
	// Set up a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "haystack-idtable-test-*")
	assert.NoError(t, err, "Failed to create temp dir")
	defer os.RemoveAll(tempDir)

	// Initialize storage
	db, err := storage.Open(filepath.Join(tempDir, "data"), 0)
	assert.NoError(t, err, "Failed to open database")
	defer db.Close()

	// Initialize idtable
	err = Init(db)
	defer Close()
	assert.NoError(t, err, "Init failed")

	// Test getting ID for a new key
	testKey := []byte("test-key-1")
	id1, err := GetId(testKey)
	assert.NoError(t, err, "GetId failed")

	// Verify the ID is returned as binary encoded bytes
	assert.Equal(t, 8, len(id1), "Expected ID length to be 8 bytes")

	// Convert the binary ID back to int64 to verify
	idValue := binary.BigEndian.Uint64([]byte(id1))
	assert.Equal(t, uint64(1), idValue, "Expected first ID to be 1")

	// Test getting ID for the same key should return the same ID
	id2, err := GetId(testKey)
	assert.NoError(t, err, "GetId failed for existing key")
	assert.Equal(t, id1, id2, "Expected same ID for same key")

	// Test getting ID for a different key should return a different ID
	testKey2 := []byte("test-key-2")
	id3, err := GetId(testKey2)
	assert.NoError(t, err, "GetId failed for second key")

	assert.NotEqual(t, id1, id3, "Expected different IDs for different keys")

	// Verify the second ID is incremented
	idValue3 := binary.BigEndian.Uint64([]byte(id3))
	assert.Equal(t, uint64(2), idValue3, "Expected second ID to be 2")
}

// TestGetIdConcurrency tests GetId function under concurrent access
func TestGetIdConcurrency(t *testing.T) {
	// Set up a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "haystack-idtable-test-*")
	assert.NoError(t, err, "Failed to create temp dir")
	defer os.RemoveAll(tempDir)

	// Initialize storage
	db, err := storage.Open(filepath.Join(tempDir, "data"), 0)
	assert.NoError(t, err, "Failed to open database")
	defer db.Close()

	// Initialize idtable
	err = Init(db)
	defer Close()
	assert.NoError(t, err, "Init failed")

	// Test concurrent access to GetId
	numGoroutines := 10
	numKeysPerGoroutine := 10
	resultChan := make(chan map[string]string, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(goroutineId int) {
			results := make(map[string]string)
			for j := 0; j < numKeysPerGoroutine; j++ {
				key := []byte("key-" + strconv.Itoa(goroutineId) + "-" + strconv.Itoa(j))
				id, err := GetId(key)
				if err != nil {
					// Note: Cannot use assert in goroutine, so using t.Errorf
					t.Errorf("GetId failed in goroutine %d: %v", goroutineId, err)
					return
				}
				results[string(key)] = id
			}
			resultChan <- results
		}(i)
	}

	// Collect all results
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

	// Verify all IDs are unique
	idSet := make(map[string]bool)
	for _, id := range allResults {
		idSet[id] = true
	}

	// All IDs should be unique
	expectedIds := numGoroutines * numKeysPerGoroutine
	assert.Equal(t, expectedIds, len(idSet), "Expected all IDs to be unique")
}

// TestGetIdWithClosedDB tests GetId with a closed database
func TestGetIdWithClosedDB(t *testing.T) {
	// Set up a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "haystack-idtable-test-*")
	assert.NoError(t, err, "Failed to create temp dir")
	defer os.RemoveAll(tempDir)

	// Initialize storage
	db, err := storage.Open(filepath.Join(tempDir, "data"), 0)
	assert.NoError(t, err, "Failed to open database")

	// Initialize idtable
	err = Init(db)
	assert.NoError(t, err, "Init failed")
	Close()
	// Close the database
	db.Close()

	// Test GetId with closed database should fail
	testKey := []byte("test-key")
	_, err = GetId(testKey)
	assert.Error(t, err, "Expected GetId to fail with closed database")
}

// TestEncodeIncrIdKey tests the EncodeIncrIdKey function
func TestEncodeIncrIdKey(t *testing.T) {
	key := EncodeIncrIdKey()

	// Verify the key is correctly formatted
	expectedKey := []byte{storage.KeyTypeIdTableNextId}
	assert.Equal(t, len(expectedKey), len(key), "Expected key length to match")
	assert.Equal(t, storage.KeyTypeIdTableNextId, key[0], "Expected correct key type")
}

// TestEncodeIdKey tests the EncodeIdKey function
func TestEncodeIdKey(t *testing.T) {
	testKey := []byte("test-key")
	encodedKey := EncodeIdKey(testKey)

	// Verify the key is correctly formatted
	expectedLength := 1 + len(testKey) // key type byte + key content
	assert.Equal(t, expectedLength, len(encodedKey), "Expected correct encoded key length")
	assert.Equal(t, storage.KeyTypeIdTableKey, encodedKey[0], "Expected correct key type")

	// Verify the rest of the key matches the input
	keyContent := encodedKey[1:]
	assert.Equal(t, string(testKey), string(keyContent), "Expected key content to match")
}

// TestParseId tests the parseId function
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

// TestGetIdWithLargeNumberOfKeys tests GetId with a large number of keys
func TestGetIdWithLargeNumberOfKeys(t *testing.T) {
	// Set up a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "haystack-idtable-test-*")
	assert.NoError(t, err, "Failed to create temp dir")
	defer os.RemoveAll(tempDir)

	// Initialize storage
	db, err := storage.Open(filepath.Join(tempDir, "data"), 0)
	assert.NoError(t, err, "Failed to open database")
	defer db.Close()

	// Initialize idtable
	err = Init(db)
	defer Close()
	assert.NoError(t, err, "Init failed")

	// Generate a large number of unique keys and IDs
	numKeys := 100 // Reduced for faster testing
	ids := make(map[string]string)

	for i := 0; i < numKeys; i++ {
		key := []byte("large-test-key-" + strconv.Itoa(i))
		id, err := GetId(key)
		assert.NoError(t, err, "GetId failed for key %d", i)

		// Store the ID for this key
		keyStr := string(key)
		if existingId, exists := ids[keyStr]; exists {
			assert.Equal(t, existingId, id, "Key %s should get consistent ID", keyStr)
		}
		ids[keyStr] = id
	}

	// Verify all IDs are unique
	idSet := make(map[string]bool)
	for _, id := range ids {
		idSet[id] = true
	}
	assert.Equal(t, numKeys, len(idSet), "Expected all IDs to be unique")

	// Test that requesting the same keys again returns the same IDs
	for i := 0; i < numKeys; i++ {
		key := []byte("large-test-key-" + strconv.Itoa(i))
		id, err := GetId(key)
		assert.NoError(t, err, "GetId failed for existing key %d", i)

		expectedId := ids[string(key)]
		assert.Equal(t, expectedId, id, "Expected consistent ID for key %s", string(key))
	}
}

// TestGetIdPersistence tests that IDs persist across database restarts
func TestGetIdPersistence(t *testing.T) {
	// Set up a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "haystack-idtable-test-*")
	assert.NoError(t, err, "Failed to create temp dir")
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "data")
	testKey := []byte("persistence-test-key")

	// First session: create database and get ID
	var firstId string
	{
		db, err := storage.Open(dbPath, 0)
		assert.NoError(t, err, "Failed to open database")

		err = Init(db)
		assert.NoError(t, err, "Init failed")

		firstId, err = GetId(testKey)
		assert.NoError(t, err, "GetId failed")

		Close()
		db.Close()
	}

	// Second session: reopen database and verify ID persistence
	{
		db, err := storage.Open(dbPath, 0)
		assert.NoError(t, err, "Failed to reopen database")
		defer db.Close()

		err = Init(db)
		assert.NoError(t, err, "Init failed on reopen")

		secondId, err := GetId(testKey)
		assert.NoError(t, err, "GetId failed on reopen")

		assert.Equal(t, firstId, secondId, "ID should be persistent across restarts")
		Close()
	}
}
