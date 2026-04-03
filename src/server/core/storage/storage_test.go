package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOpen(t *testing.T) {
	tmpDir := t.TempDir()
	storagePath := filepath.Join(tmpDir, "testdata")

	db, err := Open(storagePath, 4*1024*1024)
	assert.NoError(t, err)
	assert.NotNil(t, db)
	defer db.Close()

	// Version file should exist
	version, err := os.ReadFile(filepath.Join(storagePath, "version"))
	assert.NoError(t, err)
	assert.Equal(t, StorageVersion, string(version))
}

func TestOpen_DefaultCacheSize(t *testing.T) {
	tmpDir := t.TempDir()
	storagePath := filepath.Join(tmpDir, "testdata")

	db, err := Open(storagePath, 0) // 0 should use default
	assert.NoError(t, err)
	assert.NotNil(t, db)
	db.Close()
}

func TestOpen_BasicOps(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(filepath.Join(tmpDir, "data"), 4*1024*1024)
	assert.NoError(t, err)
	defer db.Close()

	assert.NoError(t, db.Put([]byte("k"), []byte("v")))
	val, err := db.Get([]byte("k"))
	assert.NoError(t, err)
	assert.Equal(t, []byte("v"), val)
}

func TestStorageVersion(t *testing.T) {
	assert.Equal(t, "1.4", StorageVersion)
}

func TestIsKeyType(t *testing.T) {
	assert.True(t, IsKeyType(string([]byte{KeyTypeWorkspace, 'a'}), KeyTypeWorkspace))
	assert.False(t, IsKeyType(string([]byte{KeyTypeDocMeta, 'a'}), KeyTypeWorkspace))
	assert.False(t, IsKeyType("", KeyTypeWorkspace))
}

func TestKeyTypeConstants(t *testing.T) {
	// Verify no collisions between key types
	types := []byte{
		KeyTypeWorkspaceIncrId, KeyTypeWorkspace,
		KeyTypeDocWorkspace, KeyTypeDocWords, KeyTypeDocMeta, KeyTypeDocPath,
		KeyTypeInvertedRow, KeyTypeInvertedTable, KeyTypeInvertedNextTableId,
		KeyTypeIdTableNextId, KeyTypeIdTableKey,
		KeyTypeSymbol, KeyTypeSymbolDocFunctions, KeyTypeSymbolWords,
	}
	seen := map[byte]bool{}
	for _, kt := range types {
		assert.NotEqual(t, byte(0), kt, "key type should not be 0")
		assert.False(t, seen[kt], "duplicate key type: %d", kt)
		seen[kt] = true
	}
}
