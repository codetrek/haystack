package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/internal/core/documents"
	"github.com/codetrek/haystack/internal/core/invertedindex"
	"github.com/codetrek/haystack/internal/core/storage"
	"github.com/codetrek/haystack/internal/core/symbols"
	"github.com/codetrek/haystack/internal/core/workspace/internal"
	"github.com/codetrek/haystack/searchcore/queue"
)

// setupFullEnv initializes all required subsystems for testing manage.go functions.
func setupFullEnv(t *testing.T) (cleanup func(), tempDir string) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "haystack-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	conf.Get().Global.DataPath = tempDir

	db, _ := storage.Open(filepath.Join(tempDir, "data"), 0)

	mpsc := queue.NewMpsc("TestQueue")
	mpsc.Start()

	invertedindex.Init(db, mpsc)
	documents.Init(db, mpsc, invertedindex.GetLegacy())
	symbols.Init(db, mpsc, invertedindex.GetLegacy())

	err = Init(db)
	if err != nil {
		db.Close()
		mpsc.Stop()
		os.RemoveAll(tempDir)
		t.Fatalf("Init failed: %v", err)
	}

	cleanup = func() {
		symbols.CloseAndWait()
		documents.CloseAndWait()
		invertedindex.CloseAndWait()
		mpsc.Stop()
		db.Close()
		os.RemoveAll(tempDir)
	}

	return cleanup, tempDir
}

func TestInit(t *testing.T) {
	// Set up a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "haystack-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Set configuration
	conf.Get().Global.DataPath = tempDir

	// Initialize storage
	db, _ := storage.Open(filepath.Join(tempDir, "data"), 0)
	defer db.Close()

	mpsc := queue.NewMpsc("TestQueue")
	mpsc.Start()
	defer mpsc.Stop()

	err = documents.Init(db, mpsc, nil)
	if err != nil {
		t.Fatalf("Storage Init failed: %v", err)
	}
	defer documents.CloseAndWait()

	workspacdId := 1
	// Create test workspace data
	workspaceData := map[string]interface{}{
		"id":               workspacdId,
		"path":             "/test/path",
		"useGlobalFilters": true,
		"createdAt":        time.Now().Format(time.RFC3339),
	}
	workspaceJson, err := json.Marshal(workspaceData)
	if err != nil {
		t.Fatalf("Failed to marshal workspace data: %v", err)
	}
	db.Put(internal.EncodeWorkspaceKey(workspacdId), []byte(workspaceJson))

	// Initialize workspace manager
	err = Init(db)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Verify workspace is loaded correctly
	ws, err := GetByPath("/test/path")
	if err != nil {
		t.Fatalf("Failed to get workspace: %v", err)
	}
	if ws.Id != workspacdId {
		t.Errorf("Workspace ID mismatch, got %d, want test-workspace", ws.Id)
	}
}

func TestInitWithMalformedData(t *testing.T) {
	// Test that Init handles malformed workspace JSON gracefully
	tempDir, err := os.MkdirTemp("", "haystack-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	conf.Get().Global.DataPath = tempDir

	db, _ := storage.Open(filepath.Join(tempDir, "data"), 0)
	defer db.Close()

	// Store malformed JSON
	db.Put(internal.EncodeWorkspaceKey(1), []byte("this is not valid json{{{"))

	// Also store a valid workspace
	validData := map[string]interface{}{
		"id":   2,
		"path": "/valid/path",
	}
	validJSON, _ := json.Marshal(validData)
	db.Put(internal.EncodeWorkspaceKey(2), validJSON)

	err = Init(db)
	if err != nil {
		t.Fatalf("Init should not fail on malformed data: %v", err)
	}

	// The malformed workspace should be skipped
	_, err = Get(1)
	if err == nil {
		t.Error("Malformed workspace should not be loaded")
	}

	// The valid workspace should be loaded
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
	workspacePaths := make([]string, 10)
	for i := 0; i < 10; i++ {
		workspacePaths[i] = filepath.Join(tempDir, "test-workspace", string(rune('a'+i)))
		err := os.MkdirAll(workspacePaths[i], 0755)
		if err != nil {
			t.Fatalf("Failed to create test workspace directory: %v", err)
		}
	}

	// Concurrently create workspaces
	var createWg sync.WaitGroup
	for _, path := range workspacePaths {
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

	// Set up CountByWorkspaceFunc to return a known value
	CountByWorkspaceFunc = func(wsId int) int {
		if wsId == ws.Id {
			return 99
		}
		return 0
	}
	defer func() { CountByWorkspaceFunc = nil }()

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
				t.Errorf("TotalFiles = %d, want 99 (from CountByWorkspaceFunc)", w.TotalFiles)
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
