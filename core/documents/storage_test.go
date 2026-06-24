package documents

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetrek/haystack/core/invertedindex"
	"github.com/codetrek/haystack/core/kv/pebblekv"
	"github.com/codetrek/haystack/core/queue"
	"github.com/stretchr/testify/assert"
)

func TestNew(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	db, err := pebblekv.Open(filepath.Join(tempDir, "data"), 0)
	if err != nil {
		t.Fatalf("Failed to open db: %v", err)
	}

	mpsc := queue.NewMpsc("TestQueue")
	mpsc.Start()
	defer mpsc.Stop()

	st, err := New(db, mpsc, nil, Options{})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Verify if the storage directory was created
	storagePath := filepath.Join(tempDir, "data")
	if _, err := os.Stat(storagePath); os.IsNotExist(err) {
		t.Errorf("Storage directory was not created")
	}

	// Verify if the database is open
	if db == nil {
		t.Error("Database was not initialized")
	}

	// Cleanup
	st.CloseAndWait()
	db.Close()
}

func TestCloseAndWait(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	db, err := pebblekv.Open(filepath.Join(tempDir, "data"), 0)
	if err != nil {
		t.Fatalf("Failed to open db: %v", err)
	}

	mpsc := queue.NewMpsc("TestQueue")
	mpsc.Start()

	st, err := New(db, mpsc, nil, Options{})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Test closing
	done := make(chan struct{})
	go func() {
		st.CloseAndWait()
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
// Create – error paths
// ---------------------------------------------------------------------------

func TestCreate_InvertedIndexCreateTableError(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	// Write non-numeric data to the inverted-index next-table-id key.
	// The inverted index's CreateTable calls db.GetIncrementalId which does
	// strconv.Atoi on the stored value — corrupting it makes Atoi fail,
	// which propagates as an error from CreateTable.
	nextTableIdKey := []byte{invertedindex.DefaultKeyTypeNextId}
	err := env.DB.Put(nextTableIdKey, []byte("not-a-number"))
	if !assert.NoError(t, err) {
		return
	}

	err = env.St.Create(100, "should-fail")
	assert.Error(t, err, "Create should fail when invertedindex.CreateTable returns error")
	assert.Contains(t, err.Error(), "failed to create inverted index table")

	// Restore the key so other operations work normally during teardown
	env.DB.Delete(nextTableIdKey)
}

func TestCreate_DbPutError(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	// simulateClosedDB replaces only the documents store's db with a stub
	// that always returns errors. The inverted index's CreateTable() still uses
	// the real DB and will succeed, but db.Put() in Create() will fail.
	restore := simulateClosedDB(env.St)
	defer restore()

	err := env.St.Create(200, "put-should-fail")
	assert.Error(t, err, "Create should fail when db.Put returns error")
}

// ---------------------------------------------------------------------------
// Create + GetCollection (cache behaviour)
// ---------------------------------------------------------------------------

func TestCreateAndGetCollection(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 42)

	ws, err := env.St.GetCollection(42)
	if !assert.NoError(t, err) {
		return
	}
	if !assert.NotNil(t, ws) {
		return
	}
	assert.Equal(t, 42, ws.CollectionID)
	assert.Equal(t, "test-workspace", ws.Desc)

	// Second call should come from cache
	ws2, err := env.St.GetCollection(42)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, ws, ws2, "second GetCollection should return cached value")
}

func TestGetCollection_NonExistent(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	_, err := env.St.GetCollection(999)
	assert.Error(t, err, "non-existent collection should return error")
}

// ---------------------------------------------------------------------------
// Delete workspace
// ---------------------------------------------------------------------------

func TestDelete_MarksWorkspaceDeleted(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	// Save a document so we can verify it gets cleaned up
	doc := &Document{
		ID:      "d1",
		RelPath: "file.go",
		Words:   []string{"word"},
	}
	err := env.St.SaveNewDocuments(1, []*Document{doc})
	if !assert.NoError(t, err) {
		return
	}

	// Verify document exists before delete
	got, err := env.St.GetDocument(1, "d1")
	if !assert.NoError(t, err) {
		return
	}
	if !assert.NotNil(t, got) {
		return
	}

	// Delete workspace
	err = env.St.Delete(1)
	if !assert.NoError(t, err) {
		return
	}

	// Collection should be marked as deleted
	assert.True(t, env.St.isCollectionDeleted(1))

	// New saves to the deleted workspace should fail
	doc2 := &Document{ID: "d2", RelPath: "y.go", Words: []string{"y"}}
	err = env.St.SaveNewDocuments(1, []*Document{doc2})
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// isCollectionDeleted / markCollectionDeleted
// ---------------------------------------------------------------------------

func TestIsCollectionDeleted(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	assert.False(t, env.St.isCollectionDeleted(99))

	env.St.markCollectionDeleted(99)
	assert.True(t, env.St.isCollectionDeleted(99))
}
