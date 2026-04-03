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

func TestCleanup_RemovesOldVersionDirs(t *testing.T) {
	tmpDir := t.TempDir()
	storagePath := filepath.Join(tmpDir, "storage")
	os.MkdirAll(storagePath, 0755)

	// Old version directories that cleanup should remove
	oldDirs := []string{"index", "1.0", "1.1", "1.2", "1.3"}
	for _, name := range oldDirs {
		dirPath := filepath.Join(storagePath, name)
		os.MkdirAll(dirPath, 0755)
		// Put a file inside to verify recursive removal
		os.WriteFile(filepath.Join(dirPath, "data.sst"), []byte("dummy"), 0644)
	}

	// A directory that should NOT be removed (current version)
	currentDir := filepath.Join(storagePath, StorageVersion)
	os.MkdirAll(currentDir, 0755)
	os.WriteFile(filepath.Join(currentDir, "data.sst"), []byte("current"), 0644)

	// An unrelated directory that should NOT be removed
	otherDir := filepath.Join(storagePath, "other")
	os.MkdirAll(otherDir, 0755)

	cleanup(storagePath)

	// All old version directories should be gone
	for _, name := range oldDirs {
		dirPath := filepath.Join(storagePath, name)
		_, err := os.Stat(dirPath)
		assert.True(t, os.IsNotExist(err), "expected %s to be removed, but it still exists", name)
	}

	// Current version directory should still exist
	_, err := os.Stat(currentDir)
	assert.NoError(t, err, "current version directory should not be removed")

	// Unrelated directory should still exist
	_, err = os.Stat(otherDir)
	assert.NoError(t, err, "unrelated directory should not be removed")
}

func TestCleanup_NoOldDirs(t *testing.T) {
	tmpDir := t.TempDir()
	storagePath := filepath.Join(tmpDir, "storage")
	os.MkdirAll(storagePath, 0755)

	// Only the current version exists, nothing to clean up — should not panic
	currentDir := filepath.Join(storagePath, StorageVersion)
	os.MkdirAll(currentDir, 0755)

	cleanup(storagePath)

	// Current version should still be intact
	_, err := os.Stat(currentDir)
	assert.NoError(t, err)
}

func TestCleanup_PartialOldDirs(t *testing.T) {
	tmpDir := t.TempDir()
	storagePath := filepath.Join(tmpDir, "storage")
	os.MkdirAll(storagePath, 0755)

	// Only some old dirs exist
	existing := []string{"1.0", "1.2"}
	for _, name := range existing {
		os.MkdirAll(filepath.Join(storagePath, name), 0755)
	}
	absent := []string{"index", "1.1", "1.3"}

	cleanup(storagePath)

	for _, name := range existing {
		_, err := os.Stat(filepath.Join(storagePath, name))
		assert.True(t, os.IsNotExist(err), "expected %s to be removed", name)
	}
	for _, name := range absent {
		_, err := os.Stat(filepath.Join(storagePath, name))
		assert.True(t, os.IsNotExist(err), "%s should still not exist", name)
	}
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
