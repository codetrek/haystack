package storage

import (
	"log"
	"os"
	"path/filepath"

	"github.com/ai-microsoft/haystack/server/core/pebble"
)

const StorageVersion = "1.0"

func cleanup(storagePath string) {
	// Perform cleanup tasks here, such as removing old files or directories
	log.Printf("[Storage] Cleaning up storage path: %s", storagePath)
	cleanupList := []string{
		"index", // It's the first version of the index, we can safely remove it now
	}

	for _, item := range cleanupList {
		itemPath := filepath.Join(storagePath, item)
		if _, err := os.Stat(itemPath); err == nil {
			os.RemoveAll(itemPath)
			log.Printf("[Storage] Removed stale DB: %s", itemPath)
		}
	}
}

func Open(dataPath string) (pebble.DB, error) {
	storagePath := filepath.Join(dataPath, "data")

	log.Printf("[Storage] Init storage path: %s", storagePath)

	dbPath := filepath.Join(storagePath, StorageVersion)
	versionPath := filepath.Join(storagePath, "version")

	os.MkdirAll(storagePath, 0755)
	os.WriteFile(versionPath, []byte(StorageVersion), 0644)

	db, err := pebble.OpenDB(dbPath, 16*1024*1024)
	if err != nil {
		return nil, err
	}

	go cleanup(storagePath)
	return db, nil
}
