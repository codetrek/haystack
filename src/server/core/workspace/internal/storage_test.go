package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetrek/haystack/conf"
	"github.com/codetrek/haystack/server/core/storage"
)

func TestWorkspaceStorage(t *testing.T) {
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

	err = Init(db)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Test saving a workspace
	workspaceID := 1
	workspaceData := map[string]interface{}{
		"id":        workspaceID,
		"path":      "/test/path",
		"createdAt": time.Now().Format(time.RFC3339),
	}
	workspaceJSON, err := json.Marshal(workspaceData)
	if err != nil {
		t.Fatalf("Failed to marshal workspace data: %v", err)
	}

	Save(workspaceID, string(workspaceJSON))

	// Test getting workspaces
	workspaces, err := ScanAll()
	if err != nil {
		t.Fatalf("Failed to get all workspaces: %v", err)
	}

	found := false
	for k, v := range workspaces {
		if k == workspaceID {
			found = true
			if v != string(workspaceJSON) {
				t.Errorf("Workspace data mismatch, got %s, want %s", v, string(workspaceJSON))
			}
			break
		}
	}
	if !found {
		t.Error("Saved workspace not found in GetAllWorkspaces")
	}

	// Test deleting a workspace
	Delete(workspaceID)

	// Verify workspace is deleted
	workspaces, err = ScanAll()
	if err != nil {
		t.Fatalf("Failed to get all workspaces: %v", err)
	}

	for k, _ := range workspaces {
		if k == workspaceID {
			t.Error("Workspace was not deleted")
			break
		}
	}
}

func TestGetIncreasedWorkspaceID(t *testing.T) {
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

	err = Init(db)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Test getting increased workspace ID
	ids := make(map[int]bool)
	for i := 0; i < 10; i++ {
		id, err := GetNextId()
		if err != nil {
			t.Fatalf("Failed to get increased workspace ID: %v", err)
		}
		if ids[id] {
			t.Errorf("Duplicate workspace ID generated: %d", id)
		}
		ids[id] = true
	}
}

func setupDB(t *testing.T) func() {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "haystack-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	conf.Get().Global.DataPath = tempDir
	d, _ := storage.Open(filepath.Join(tempDir, "data"), 0)
	if err := Init(d); err != nil {
		d.Close()
		os.RemoveAll(tempDir)
		t.Fatalf("Init failed: %v", err)
	}
	return func() {
		d.Close()
		os.RemoveAll(tempDir)
	}
}

func TestGet_Success(t *testing.T) {
	cleanup := setupDB(t)
	defer cleanup()

	workspaceID := 42
	wsJSON := `{"id":42,"path":"/test/get"}`
	if err := Save(workspaceID, wsJSON); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	got, err := Get(workspaceID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got != wsJSON {
		t.Errorf("Get returned %q, want %q", got, wsJSON)
	}
}

func TestGet_NotFound(t *testing.T) {
	cleanup := setupDB(t)
	defer cleanup()

	val, err := Get(9999)
	// Pebble may return empty string without error for missing keys
	if err != nil {
		// error path is also valid
		return
	}
	if val != "" {
		t.Errorf("Get for non-existent workspace should return empty string, got %q", val)
	}
}

func TestScanAll_Empty(t *testing.T) {
	cleanup := setupDB(t)
	defer cleanup()

	all, err := ScanAll()
	if err != nil {
		t.Fatalf("ScanAll failed: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("ScanAll on empty db returned %d workspaces, want 0", len(all))
	}
}

func TestSave_And_ScanAll_Multiple(t *testing.T) {
	cleanup := setupDB(t)
	defer cleanup()

	for i := 1; i <= 5; i++ {
		data := map[string]interface{}{
			"id":   i,
			"path": "/test/path/" + string(rune('a'+i-1)),
		}
		j, _ := json.Marshal(data)
		if err := Save(i, string(j)); err != nil {
			t.Fatalf("Save(%d) failed: %v", i, err)
		}
	}

	all, err := ScanAll()
	if err != nil {
		t.Fatalf("ScanAll failed: %v", err)
	}
	if len(all) != 5 {
		t.Errorf("ScanAll returned %d workspaces, want 5", len(all))
	}
}

func TestDelete_NonExistentKey(t *testing.T) {
	cleanup := setupDB(t)
	defer cleanup()

	err := Delete(12345)
	if err != nil {
		t.Errorf("Delete of non-existent key should not error, got: %v", err)
	}
}
