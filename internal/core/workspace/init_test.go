package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/codetrek/haystack/core/collection"
	"github.com/codetrek/haystack/core/documents"
	"github.com/codetrek/haystack/core/invertedstore"
	"github.com/codetrek/haystack/core/kv"
	"github.com/codetrek/haystack/core/queue"
	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/internal/core/storage"
	"github.com/codetrek/haystack/internal/core/symbols"
)

// setupCatalog is a test helper: runs migration, creates collection.Catalog + documents.Store.
// Returns the catalog, documents store, queue, and a cleanup func.
func setupCatalog(t *testing.T, db kv.Store) (cat *collection.Catalog, st *documents.Store, mpsc *queue.Mpsc, idx *invertedstore.Store, cleanup func()) {
	t.Helper()

	mpsc = queue.NewMpsc("test-catalog-q")
	mpsc.Start()

	var err error
	idx, err = invertedstore.Open(filepath.Join(conf.Get().Global.DataPath, "index", storage.StorageVersion, "invertedstore"), mpsc, invertedstore.Options{})
	if err != nil {
		mpsc.Stop()
		t.Fatalf("invertedstore.Open: %v", err)
	}

	st, err = documents.New(db, mpsc, idx, documents.Options{})
	if err != nil {
		idx.CloseAndWait()
		mpsc.Stop()
		t.Fatalf("documents.New: %v", err)
	}

	cat, err = collection.New(db, st, collection.Options{})
	if err != nil {
		st.CloseAndWait()
		idx.CloseAndWait()
		mpsc.Stop()
		t.Fatalf("collection.New: %v", err)
	}

	cleanup = func() {
		st.CloseAndWait()
		idx.CloseAndWait()
		mpsc.Stop()
	}
	return cat, st, mpsc, idx, cleanup
}

// setupFullEnv initializes all required subsystems for testing manage.go functions.
func setupFullEnv(t *testing.T) (cleanup func(), tempDir string) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "haystack-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	conf.Get().Global.DataPath = tempDir

	db, _ := storage.Open(filepath.Join(tempDir, "data"), 0)

	cat, st, mpsc, idx, catCleanup := setupCatalog(t, db)
	SetDocStore(st)

	if err := symbols.Init(db, mpsc, idx); err != nil {
		catCleanup()
		db.Close()
		os.RemoveAll(tempDir)
		t.Fatalf("symbols.Init failed: %v", err)
	}

	if err := Init(cat); err != nil {
		symbols.CloseAndWait()
		catCleanup()
		db.Close()
		os.RemoveAll(tempDir)
		t.Fatalf("Init failed: %v", err)
	}

	cleanup = func() {
		SetDocStore(nil)
		symbols.CloseAndWait()
		catCleanup()
		db.Close()
		os.RemoveAll(tempDir)
	}

	return cleanup, tempDir
}

func TestInit(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	conf.Get().Global.DataPath = tempDir

	db, _ := storage.Open(filepath.Join(tempDir, "data"), 0)
	defer db.Close()

	mpsc := queue.NewMpsc("TestQueue")
	mpsc.Start()
	defer mpsc.Stop()

	st, err := documents.New(db, mpsc, nil, documents.Options{})
	if err != nil {
		t.Fatalf("documents.New failed: %v", err)
	}
	defer st.CloseAndWait()

	// Seed a new-format record directly so collection.New will pick it up.
	workspaceID := 1
	// Write incr-id counter first.
	db.Put([]byte{collection.DefaultKeyTypeIncrId}, []byte("1"))

	rec := collection.Record{
		ID:           workspaceID,
		Name:         "/test/path",
		CreatedAt:    time.Now(),
		LastAccessed: time.Now(),
		Extra:        encodeExtra(true, nil),
	}
	recJSON, _ := json.Marshal(rec)
	recKey := []byte(fmt.Sprintf("%c%d", collection.DefaultKeyTypeRecord, workspaceID))
	db.Put(recKey, recJSON)

	cat, err := collection.New(db, st, collection.Options{})
	if err != nil {
		t.Fatalf("collection.New: %v", err)
	}

	err = Init(cat)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	ws, err := GetByPath("/test/path")
	if err != nil {
		t.Fatalf("Failed to get workspace: %v", err)
	}
	if ws.Id != workspaceID {
		t.Errorf("Workspace ID mismatch, got %d, want %d", ws.Id, workspaceID)
	}
}

func TestInitWithMalformedData(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	conf.Get().Global.DataPath = tempDir

	db, _ := storage.Open(filepath.Join(tempDir, "data"), 0)
	defer db.Close()

	mpsc := queue.NewMpsc("TestQueue")
	mpsc.Start()
	defer mpsc.Stop()

	st, err := documents.New(db, mpsc, nil, documents.Options{})
	if err != nil {
		t.Fatalf("documents.New failed: %v", err)
	}
	defer st.CloseAndWait()

	// Store malformed JSON at key-type-2 record slot 1.
	malformedKey := []byte(fmt.Sprintf("%c%d", collection.DefaultKeyTypeRecord, 1))
	db.Put(malformedKey, []byte("this is not valid json{{{"))

	// Store a valid new-format record at slot 2.
	validRec := collection.Record{
		ID:           2,
		Name:         "/valid/path",
		CreatedAt:    time.Now(),
		LastAccessed: time.Now(),
	}
	validJSON, _ := json.Marshal(validRec)
	validKey := []byte(fmt.Sprintf("%c%d", collection.DefaultKeyTypeRecord, 2))
	db.Put(validKey, validJSON)
	// Also set incr-id counter to 2 so future allocations don't collide.
	db.Put([]byte{collection.DefaultKeyTypeIncrId}, []byte("2"))

	cat, err := collection.New(db, st, collection.Options{})
	if err != nil {
		t.Fatalf("collection.New: %v", err)
	}

	err = Init(cat)
	if err != nil {
		t.Fatalf("Init should not fail on malformed data: %v", err)
	}

	// The malformed workspace should be skipped.
	_, err = Get(1)
	if err == nil {
		t.Error("Malformed workspace should not be loaded")
	}

	// The valid workspace should be loaded.
	ws, err := Get(2)
	if err != nil {
		t.Fatalf("Valid workspace should be loaded: %v", err)
	}
	if ws.Path != "/valid/path" {
		t.Errorf("Path mismatch, got %q, want %q", ws.Path, "/valid/path")
	}
}

func TestWorkspaceManagement(t *testing.T) {
	cleanup, tempDir := setupFullEnv(t)
	defer cleanup()

	// Test creating a workspace
	workspacePath := filepath.Join(tempDir, "test-workspace")
	err := os.MkdirAll(workspacePath, 0755)
	if err != nil {
		t.Fatalf("Failed to create test workspace directory: %v", err)
	}

	ws, err := Create(workspacePath)
	if err != nil {
		t.Fatalf("Failed to create workspace: %v", err)
	}

	// Test getting a workspace
	ws2, err := GetByPath(workspacePath)
	if err != nil {
		t.Fatalf("Failed to get workspace by path: %v", err)
	}
	if ws2.Id != ws.Id {
		t.Errorf("Workspace ID mismatch, got %d, want %d", ws2.Id, ws.Id)
	}

	ws3, err := Get(ws.Id)
	if err != nil {
		t.Fatalf("Failed to get workspace by ID: %v", err)
	}
	if ws3.Id != ws.Id {
		t.Errorf("Workspace ID mismatch, got %d, want %d", ws3.Id, ws.Id)
	}

	// Test getting all workspaces
	allWorkspaces := GetAll()
	found := false
	for _, w := range allWorkspaces {
		if w.Id == ws.Id {
			found = true
			break
		}
	}
	if !found {
		t.Error("Created workspace not found in GetAll")
	}

	// Test getting all workspace paths
	allPaths := GetAllPaths()
	found = false
	for _, path := range allPaths {
		if path == workspacePath {
			found = true
			break
		}
	}
	if !found {
		t.Error("Created workspace path not found in GetAllPaths")
	}

	// Test deleting a workspace
	err = Delete(ws.Id)
	if err != nil {
		t.Fatalf("Failed to delete workspace: %v", err)
	}

	// Verify workspace is deleted
	_, err = Get(ws.Id)
	if err == nil {
		t.Error("Workspace was not deleted")
	}
}

func TestWorkspaceConcurrency(t *testing.T) {
	cleanup, tempDir := setupFullEnv(t)
	defer cleanup()

	// Create multiple workspace directories
	wsPaths := make([]string, 10)
	for i := 0; i < 10; i++ {
		wsPaths[i] = filepath.Join(tempDir, "test-workspace", string(rune('a'+i)))
		err := os.MkdirAll(wsPaths[i], 0755)
		if err != nil {
			t.Fatalf("Failed to create test workspace directory: %v", err)
		}
	}

	// Concurrently create workspaces
	var createWg sync.WaitGroup
	for _, path := range wsPaths {
		createWg.Add(1)
		go func(p string) {
			defer createWg.Done()
			_, err := Create(p)
			if err != nil {
				t.Errorf("Failed to create workspace: %v", err)
			}
		}(path)
	}
	createWg.Wait()

	// Verify all workspaces were created successfully
	allWorkspaces := GetAll()
	if len(allWorkspaces) != 10 {
		t.Errorf("Expected 10 workspaces, got %d", len(allWorkspaces))
	}

	// Concurrently access workspaces
	var accessWg sync.WaitGroup
	for _, ws := range allWorkspaces {
		accessWg.Add(1)
		go func(id int) {
			defer accessWg.Done()
			_, err := Get(id)
			if err != nil {
				t.Errorf("Failed to get workspace: %v", err)
			}
		}(ws.Id)
	}
	accessWg.Wait()
}

// --- Additional tests for uncovered paths ---

func TestMove_Success(t *testing.T) {
	cleanup, tempDir := setupFullEnv(t)
	defer cleanup()

	// Create workspace directory
	wsPath := filepath.Join(tempDir, "ws-move-src")
	os.MkdirAll(wsPath, 0755)

	ws, err := Create(wsPath)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Create target directory
	newPath := filepath.Join(tempDir, "ws-move-dst")
	os.MkdirAll(newPath, 0755)

	moved, err := Move(ws.Id, newPath)
	if err != nil {
		t.Fatalf("Move failed: %v", err)
	}
	if moved.Path != newPath {
		t.Errorf("Move: path = %q, want %q", moved.Path, newPath)
	}

	// Old path should not resolve
	_, err = GetByPath(wsPath)
	if err == nil {
		t.Error("Old path should not resolve after move")
	}

	// New path should resolve
	found, err := GetByPath(newPath)
	if err != nil {
		t.Fatalf("New path should resolve: %v", err)
	}
	if found.Id != ws.Id {
		t.Errorf("ID mismatch after move: got %d, want %d", found.Id, ws.Id)
	}
}

func TestMove_NotFound(t *testing.T) {
	cleanup, tempDir := setupFullEnv(t)
	defer cleanup()

	newPath := filepath.Join(tempDir, "ws-new")
	os.MkdirAll(newPath, 0755)

	_, err := Move(999999, newPath)
	if err == nil {
		t.Error("Move should fail for non-existent workspace")
	}
}

func TestMove_DeletedWorkspace(t *testing.T) {
	cleanup, tempDir := setupFullEnv(t)
	defer cleanup()

	wsPath := filepath.Join(tempDir, "ws-del")
	os.MkdirAll(wsPath, 0755)

	ws, err := Create(wsPath)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Delete the workspace
	Delete(ws.Id)

	newPath := filepath.Join(tempDir, "ws-del-new")
	os.MkdirAll(newPath, 0755)

	_, err = Move(ws.Id, newPath)
	if err == nil {
		t.Error("Move should fail for deleted workspace")
	}
}

func TestMove_PathAlreadyExists(t *testing.T) {
	cleanup, tempDir := setupFullEnv(t)
	defer cleanup()

	wsPath1 := filepath.Join(tempDir, "ws-exists-1")
	wsPath2 := filepath.Join(tempDir, "ws-exists-2")
	os.MkdirAll(wsPath1, 0755)
	os.MkdirAll(wsPath2, 0755)

	ws1, _ := Create(wsPath1)
	Create(wsPath2)

	// Try to move ws1 to ws2's path
	_, err := Move(ws1.Id, wsPath2)
	if err == nil {
		t.Error("Move should fail when target path already has a workspace")
	}
}

func TestMove_RelativePath(t *testing.T) {
	cleanup, tempDir := setupFullEnv(t)
	defer cleanup()

	wsPath := filepath.Join(tempDir, "ws-rel")
	os.MkdirAll(wsPath, 0755)

	ws, _ := Create(wsPath)

	_, err := Move(ws.Id, "relative/path")
	if err == nil {
		t.Error("Move should fail for relative path")
	}
}

func TestMove_PathDoesNotExist(t *testing.T) {
	cleanup, tempDir := setupFullEnv(t)
	defer cleanup()

	wsPath := filepath.Join(tempDir, "ws-noexist-src")
	os.MkdirAll(wsPath, 0755)

	ws, _ := Create(wsPath)

	_, err := Move(ws.Id, filepath.Join(tempDir, "nonexistent-dir"))
	if err == nil {
		t.Error("Move should fail for non-existent target path")
	}
}

func TestMove_PathIsFile(t *testing.T) {
	cleanup, tempDir := setupFullEnv(t)
	defer cleanup()

	wsPath := filepath.Join(tempDir, "ws-isfile-src")
	os.MkdirAll(wsPath, 0755)

	ws, _ := Create(wsPath)

	// Create a file (not a directory)
	filePath := filepath.Join(tempDir, "not-a-dir.txt")
	os.WriteFile(filePath, []byte("hello"), 0644)

	_, err := Move(ws.Id, filePath)
	if err == nil {
		t.Error("Move should fail when target is a file, not a directory")
	}
}

func TestCreate_DuplicatePath(t *testing.T) {
	cleanup, tempDir := setupFullEnv(t)
	defer cleanup()

	wsPath := filepath.Join(tempDir, "ws-dup")
	os.MkdirAll(wsPath, 0755)

	_, err := Create(wsPath)
	if err != nil {
		t.Fatalf("First Create failed: %v", err)
	}

	_, err = Create(wsPath)
	if err == nil {
		t.Error("Create should fail for duplicate path")
	}
}

func TestCreate_RelativePath(t *testing.T) {
	cleanup, _ := setupFullEnv(t)
	defer cleanup()

	_, err := Create("relative/path")
	if err == nil {
		t.Error("Create should fail for relative path")
	}
}

func TestCreate_PathDoesNotExist(t *testing.T) {
	cleanup, tempDir := setupFullEnv(t)
	defer cleanup()

	_, err := Create(filepath.Join(tempDir, "nonexistent-dir-create"))
	if err == nil {
		t.Error("Create should fail for non-existent path")
	}
}

func TestCreate_PathIsFile(t *testing.T) {
	cleanup, tempDir := setupFullEnv(t)
	defer cleanup()

	filePath := filepath.Join(tempDir, "a-file.txt")
	os.WriteFile(filePath, []byte("content"), 0644)

	_, err := Create(filePath)
	if err == nil {
		t.Error("Create should fail when path is a file")
	}
}

func TestDelete_NotFound(t *testing.T) {
	cleanup, _ := setupFullEnv(t)
	defer cleanup()

	err := Delete(888888)
	if err == nil {
		t.Error("Delete should fail for non-existent workspace")
	}
}

func TestGetByPath_NotFound(t *testing.T) {
	cleanup, _ := setupFullEnv(t)
	defer cleanup()

	_, err := GetByPath("/non/existent/path")
	if err == nil {
		t.Error("GetByPath should fail for non-existent path")
	}
}

func TestGet_NotFound(t *testing.T) {
	cleanup, _ := setupFullEnv(t)
	defer cleanup()

	_, err := Get(777777)
	if err == nil {
		t.Error("Get should fail for non-existent ID")
	}
}

func TestGetAll_WithIndexingProgress(t *testing.T) {
	cleanup, tempDir := setupFullEnv(t)
	defer cleanup()

	wsPath := filepath.Join(tempDir, "ws-indexing")
	os.MkdirAll(wsPath, 0755)

	ws, err := Create(wsPath)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Pre-populate 99 documents in the real store so CountByCollection returns 99.
	docs := make([]*documents.Document, 99)
	for i := 0; i < 99; i++ {
		docs[i] = &documents.Document{
			ID:      fmt.Sprintf("doc%d", i),
			RelPath: fmt.Sprintf("file%d.go", i),
			Words:   []string{"word"},
		}
	}
	if err := docStoreInst.SaveNewDocuments(ws.Id, docs); err != nil {
		t.Fatalf("SaveNewDocuments failed: %v", err)
	}

	// Start indexing
	err = ws.StartIndexing()
	if err != nil {
		t.Fatalf("StartIndexing failed: %v", err)
	}

	all := GetAll()
	found := false
	for _, w := range all {
		if w.Id == ws.Id {
			found = true
			if !w.Indexing {
				t.Error("Workspace should be marked as indexing")
			}
			if w.TotalFiles != 99 {
				t.Errorf("TotalFiles = %d, want 99", w.TotalFiles)
			}
			break
		}
	}
	if !found {
		t.Error("Workspace not found in GetAll")
	}
}

func TestGetAll_Sorted(t *testing.T) {
	cleanup, tempDir := setupFullEnv(t)
	defer cleanup()

	// Create several workspaces
	for i := 0; i < 5; i++ {
		p := filepath.Join(tempDir, "ws-sort", string(rune('a'+i)))
		os.MkdirAll(p, 0755)
		_, err := Create(p)
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
	}

	all := GetAll()
	for i := 1; i < len(all); i++ {
		if all[i].Id < all[i-1].Id {
			t.Errorf("GetAll not sorted by ID: %d < %d", all[i].Id, all[i-1].Id)
		}
	}
}
