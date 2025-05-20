package internal

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/server/core/storage"
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
	db, _ := storage.Open(tempDir, 0)
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
	db, _ := storage.Open(tempDir, 0)
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
