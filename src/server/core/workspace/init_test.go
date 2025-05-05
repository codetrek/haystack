package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/server/core/fulltext"
	"github.com/ai-microsoft/haystack/server/core/storage"
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

	// Initialize storage
	db, err := storage.Open(tempDir)
	defer db.Close()
	err = fulltext.Init(db)
	if err != nil {
		t.Fatalf("Storage Init failed: %v", err)
	}
	defer fulltext.CloseAndWait()

	// Create test workspace data
	workspaceData := map[string]interface{}{
		"id":               "test-workspace",
		"path":             "/test/path",
		"useGlobalFilters": true,
		"createdAt":        time.Now().Format(time.RFC3339),
	}
	workspaceJSON, err := json.Marshal(workspaceData)
	if err != nil {
		t.Fatalf("Failed to marshal workspace data: %v", err)
	}

	// Save workspace
	fulltext.SaveWorkspace("test-workspace", string(workspaceJSON))

	// Initialize workspace manager
	err = Init()
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Verify workspace is loaded correctly
	ws, err := GetByPath("/test/path")
	if err != nil {
		t.Fatalf("Failed to get workspace: %v", err)
	}
	if ws.ID != "test-workspace" {
		t.Errorf("Workspace ID mismatch, got %s, want test-workspace", ws.ID)
	}
}

func TestWorkspaceManagement(t *testing.T) {
	// Set up a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "haystack-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Set configuration
	conf.Get().Global.DataPath = tempDir

	// Initialize storage
	db, err := storage.Open(tempDir)
	defer db.Close()
	err = fulltext.Init(db)
	if err != nil {
		t.Fatalf("Storage Init failed: %v", err)
	}
	defer fulltext.CloseAndWait()

	// Initialize workspace manager
	err = Init()
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Test creating a workspace
	workspacePath := filepath.Join(tempDir, "test-workspace")
	err = os.MkdirAll(workspacePath, 0755)
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
	if ws2.ID != ws.ID {
		t.Errorf("Workspace ID mismatch, got %s, want %s", ws2.ID, ws.ID)
	}

	ws3, err := Get(ws.ID)
	if err != nil {
		t.Fatalf("Failed to get workspace by ID: %v", err)
	}
	if ws3.ID != ws.ID {
		t.Errorf("Workspace ID mismatch, got %s, want %s", ws3.ID, ws.ID)
	}

	// Test getting all workspaces
	allWorkspaces := GetAll()
	found := false
	for _, w := range allWorkspaces {
		if w.ID == ws.ID {
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
	err = Delete(ws.ID)
	if err != nil {
		t.Fatalf("Failed to delete workspace: %v", err)
	}

	// Verify workspace is deleted
	_, err = Get(ws.ID)
	if err == nil {
		t.Error("Workspace was not deleted")
	}
}

func TestWorkspaceConcurrency(t *testing.T) {
	// Set up a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "haystack-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Set configuration
	conf.Get().Global.DataPath = tempDir

	// Initialize storage
	db, err := storage.Open(tempDir)
	defer db.Close()
	err = fulltext.Init(db)
	if err != nil {
		t.Fatalf("Storage Init failed: %v", err)
	}
	defer fulltext.CloseAndWait()

	// Initialize workspace manager
	err = Init()
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Create multiple workspace directories
	workspacePaths := make([]string, 10)
	for i := 0; i < 10; i++ {
		workspacePaths[i] = filepath.Join(tempDir, "test-workspace", string(rune('a'+i)))
		err = os.MkdirAll(workspacePaths[i], 0755)
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
		go func(id string) {
			defer accessWg.Done()
			_, err := Get(id)
			if err != nil {
				t.Errorf("Failed to get workspace: %v", err)
			}
		}(ws.ID)
	}
	accessWg.Wait()
}
