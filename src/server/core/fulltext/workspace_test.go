package fulltext

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
	db, err := storage.Open(tempDir)
	defer db.Close()

	err = Init(db)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer CloseAndWait()

	// Test saving a workspace
	workspaceID := "test-workspace"
	workspaceData := map[string]interface{}{
		"id":        workspaceID,
		"path":      "/test/path",
		"createdAt": time.Now().Format(time.RFC3339),
	}
	workspaceJSON, err := json.Marshal(workspaceData)
	if err != nil {
		t.Fatalf("Failed to marshal workspace data: %v", err)
	}

	SaveWorkspace(workspaceID, string(workspaceJSON))

	// Test getting workspaces
	workspaces, err := GetAllWorkspaces()
	if err != nil {
		t.Fatalf("Failed to get all workspaces: %v", err)
	}

	found := false
	for _, ws := range workspaces {
		if ws[0] == workspaceID {
			found = true
			if ws[1] != string(workspaceJSON) {
				t.Errorf("Workspace data mismatch, got %s, want %s", ws[1], string(workspaceJSON))
			}
			break
		}
	}
	if !found {
		t.Error("Saved workspace not found in GetAllWorkspaces")
	}

	// Test deleting a workspace
	DeleteWorkspace(workspaceID)

	// Verify workspace is deleted
	workspaces, err = GetAllWorkspaces()
	if err != nil {
		t.Fatalf("Failed to get all workspaces: %v", err)
	}

	for _, ws := range workspaces {
		if ws[0] == workspaceID {
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
	db, err := storage.Open(tempDir)
	defer db.Close()
	err = Init(db)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer CloseAndWait()

	// Test getting increased workspace ID
	ids := make(map[string]bool)
	for i := 0; i < 10; i++ {
		id, err := GetIncreasedWorkspaceID()
		if err != nil {
			t.Fatalf("Failed to get increased workspace ID: %v", err)
		}
		if ids[id] {
			t.Errorf("Duplicate workspace ID generated: %s", id)
		}
		ids[id] = true
	}
}
