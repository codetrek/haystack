package fulltext

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/server/core/storage"
	"github.com/ai-microsoft/haystack/utils/queue"
)

func TestInit(t *testing.T) {
	// Set up a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "haystack-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Set configuration
	conf.Get().Global.DataPath = tempDir

	// Test initialization
	db, _ := storage.Open(tempDir, 0)

	mpsc := queue.NewMpsc("TestQueue")
	mpsc.Start()
	defer mpsc.Stop()

	err = Init(db, mpsc)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Verify if the storage directory was created
	storagePath := filepath.Join(tempDir, "data")
	if _, err := os.Stat(storagePath); os.IsNotExist(err) {
		t.Errorf("Storage directory was not created")
	}

	// Verify the version file
	versionPath := filepath.Join(storagePath, "version")
	versionData, err := os.ReadFile(versionPath)
	if err != nil {
		t.Errorf("Failed to read version file: %v", err)
	}
	if string(versionData) != storage.StorageVersion {
		t.Errorf("Version mismatch, got %s, want %s", string(versionData), storage.StorageVersion)
	}

	// Verify if the database is open
	if db == nil {
		t.Error("Database was not initialized")
	}

	// Cleanup
	CloseAndWait()
	db.Close()
}

func TestCloseAndWait(t *testing.T) {
	// Set up a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "haystack-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Set configuration
	conf.Get().Global.DataPath = tempDir

	// Initialize
	db, _ := storage.Open(tempDir, 0)

	mpsc := queue.NewMpsc("TestQueue")
	mpsc.Start()

	err = Init(db, mpsc)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Test closing
	done := make(chan struct{})
	go func() {
		CloseAndWait()
		db.Close()
		close(done)
		mpsc.Stop()
	}()

	// Wait for closing to complete or timeout
	select {
	case <-done:
		// Normal closure
	case <-time.After(5 * time.Second):
		t.Error("CloseAndWait timed out")
	}

	// Verify if the database is closed
	if !db.IsClosed() {
		t.Error("Database was not closed")
	}
}
