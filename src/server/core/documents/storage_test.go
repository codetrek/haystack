package documents

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetrek/haystack/conf"
	"github.com/codetrek/haystack/server/core/storage"
	"github.com/codetrek/haystack/utils/queue"
	"github.com/stretchr/testify/assert"
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
	db, _ := storage.Open(filepath.Join(tempDir, "data"), 0)

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
	db, _ := storage.Open(filepath.Join(tempDir, "data"), 0)

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

// ---------------------------------------------------------------------------
// Create + GetWorkspace (cache behaviour)
// ---------------------------------------------------------------------------

func TestCreateAndGetWorkspace(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 42)

	ws, err := GetWorkspace(42)
	if !assert.NoError(t, err) {
		return
	}
	if !assert.NotNil(t, ws) {
		return
	}
	assert.Equal(t, 42, ws.WorkspaceId)
	assert.Equal(t, "test-workspace", ws.Desc)

	// Second call should come from cache
	ws2, err := GetWorkspace(42)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, ws, ws2, "second GetWorkspace should return cached value")
}

func TestGetWorkspace_NonExistent(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	_, err := GetWorkspace(999)
	assert.Error(t, err, "non-existent workspace should return error")
}

// ---------------------------------------------------------------------------
// Delete workspace
// ---------------------------------------------------------------------------

func TestDelete_MarksWorkspaceDeleted(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	// Save a document so we can verify it gets cleaned up
	doc := &Document{
		ID:      "d1",
		RelPath: "file.go",
		Words:   []string{"word"},
	}
	err := SaveNewDocuments(1, []*Document{doc})
	if !assert.NoError(t, err) {
		return
	}

	// Verify document exists before delete
	got, err := GetDocument(1, "d1", false)
	if !assert.NoError(t, err) {
		return
	}
	if !assert.NotNil(t, got) {
		return
	}

	// Delete workspace
	err = Delete(1)
	if !assert.NoError(t, err) {
		return
	}

	// Workspace should be marked as deleted
	assert.True(t, isWorkspaceDeleted(1))

	// New saves to the deleted workspace should fail
	doc2 := &Document{ID: "d2", RelPath: "y.go", Words: []string{"y"}}
	err = SaveNewDocuments(1, []*Document{doc2})
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// isWorkspaceDeleted / markWorkspaceDeleted
// ---------------------------------------------------------------------------

func TestIsWorkspaceDeleted(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	assert.False(t, isWorkspaceDeleted(99))

	markWorkspaceDeleted(99)
	assert.True(t, isWorkspaceDeleted(99))
}
