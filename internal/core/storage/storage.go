package storage

import (
	"log"
	"os"
	"path/filepath"

	"github.com/codetrek/haystack/core/kv"
	"github.com/codetrek/haystack/core/kv/pebblekv"
)

// StorageVersion names the on-disk KV directory. Bump it on any breaking
// on-disk format change to force a clean reindex into a fresh directory; add the
// previous version to cleanup's list so the stale DB is removed. 1.5 switched the
// inverted-index posting-row values from fixed 8-byte big-endian docids to a
// delta-varint encoding, which the 1.4 decoder cannot read. 1.6 replaces the
// pebble-backed inverted index with the segment-based invertedstore (a breaking
// change to the `index` store) and drops the documents doc-words keyspace (a
// breaking change to the `data` store) — both require a fresh reindex.
const StorageVersion = "1.6"

// Cleanup removes the stale on-disk DB directories (previous StorageVersions and
// the first-gen un-versioned `index` dir) under storagePath. storage.Open runs it
// for the `data` store; the index root needs it run explicitly now that the
// pebble `index` store is gone (the invertedstore is NOT opened via storage.Open,
// so its caller invokes Cleanup on the index root to reclaim the dead pebble
// inverted-index version dirs — including the just-superseded "1.5" pebble index).
func Cleanup(storagePath string) {
	// Perform cleanup tasks here, such as removing old files or directories
	log.Printf("[Storage] Cleaning up storage path: %s", storagePath)
	cleanupList := []string{
		"index", // It's the first version of the index, we can safely remove it now
		"1.0",
		"1.1",
		"1.2",
		"1.3",
		"1.4", // pre-delta-varint posting-value format; superseded by 1.5
		"1.5", // pebble-backed inverted index / doc-words keyspace; superseded by 1.6 (invertedstore)
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

	// The shared KV uses pebblekv's default NoSync commit mode: every batch Commit
	// otherwise does a synchronous WAL fsync (pebble.Sync), the dominant indexing
	// cost — a real /workspace/linux index ran ~3x faster without it. This whole DB
	// (inverted index + documents store + collection catalog) is a derived cache
	// rebuilt by re-scanning the workspace, so relaxed durability is sound: NoSync
	// still writes the WAL, so a plain restart recovers it from the OS page cache;
	// only an OS-level crash / power loss loses the un-fsynced tail, which a re-scan
	// rebuilds. (Pass OpenWithOptions{Sync: true} for a store of record.)
	db, err := pebblekv.Open(dbPath, cacheSize)
	if err != nil {
		return nil, err
	}

	go cleanup(storagePath)
	return db, nil
}

// cleanup is the unexported alias kept so the post-Open goroutine reads naturally;
// it forwards to the exported Cleanup used by the index-root caller.
func cleanup(storagePath string) { Cleanup(storagePath) }
