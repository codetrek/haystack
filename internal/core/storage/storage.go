package storage

import (
	"log"
	"os"
	"path/filepath"

	"github.com/codetrek/haystack/searchcore/kv"
	"github.com/codetrek/haystack/searchcore/kv/pebblekv"
)

const StorageVersion = "1.4"

func cleanup(storagePath string) {
	// Perform cleanup tasks here, such as removing old files or directories
	log.Printf("[Storage] Cleaning up storage path: %s", storagePath)
	cleanupList := []string{
		"index", // It's the first version of the index, we can safely remove it now
		"1.0",
		"1.1",
		"1.2",
		"1.3",
	}

	for _, item := range cleanupList {
		itemPath := filepath.Join(storagePath, item)
		if _, err := os.Stat(itemPath); err == nil {
			os.RemoveAll(itemPath)
			log.Printf("[Storage] Removed stale DB: %s", itemPath)
		}
	}
}

func Open(storagePath string, cacheSize int64) (kv.Store, error) {
	log.Printf("[Storage] Init data storage path: %s", storagePath)

	dbPath := filepath.Join(storagePath, StorageVersion)
	versionPath := filepath.Join(storagePath, "version")

	os.MkdirAll(storagePath, 0755)
	os.WriteFile(versionPath, []byte(StorageVersion), 0644)

	if cacheSize <= 0 {
		cacheSize = 8 * 1024 * 1024 // Default cache size
	}

	db, err := pebblekv.Open(dbPath, cacheSize)
	if err != nil {
		return nil, err
	}

	go cleanup(storagePath)
	return db, nil
}
