package client

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/codetrek/haystack/server/internal/shared/types"
)

func TestHandleWorkspace_Help(t *testing.T) {
	output := captureStdout(t, func() {
		handleWorkspace([]string{"-h"})
	})
	if !strings.Contains(output, "Usage:") {
		t.Error("handleWorkspace -h should show usage")
	}
	if !strings.Contains(output, "list") {
		t.Error("should mention list command")
	}
	if !strings.Contains(output, "create") {
		t.Error("should mention create command")
	}
	if !strings.Contains(output, "delete") {
		t.Error("should mention delete command")
	}
	if !strings.Contains(output, "sync") {
		t.Error("should mention sync command")
	}
	if !strings.Contains(output, "move") {
		t.Error("should mention move command")
	}
}

func TestHandleWorkspace_NoArgs(t *testing.T) {
	output := captureStdout(t, func() {
		handleWorkspace(nil)
	})
	if !strings.Contains(output, "Usage:") {
		t.Error("handleWorkspace with no args should show usage")
	}
}

func TestHandleWorkspace_UnknownCommand(t *testing.T) {
	output := captureStdout(t, func() {
		handleWorkspace([]string{"badcmd"})
	})
	if !strings.Contains(output, "Unknown workspace command: badcmd") {
		t.Errorf("expected unknown command message, got: %s", output)
	}
}

func TestHandleWorkspaceList_Success(t *testing.T) {
	now := time.Now()
	workspaces := types.Workspaces{
		Workspaces: []types.Workspace{
			{
				Id:               1,
				Path:             "/home/user/project",
				TotalFiles:       100,
				UseGlobalFilters: true,
				CreatedAt:        now,
				LastAccessed:     now,
				LastFullSync:     now,
				Indexing:         false,
			},
		},
	}

	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspace/list" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", workspaces))
	})

	output := captureStdout(t, func() {
		handleWorkspaceList()
	})
	if !strings.Contains(output, "/home/user/project") {
		t.Errorf("expected workspace path, got: %s", output)
	}
}

func TestHandleWorkspaceList_ServerError(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 1, "error listing", nil))
	})

	output := captureStdout(t, func() {
		handleWorkspaceList()
	})
	if !strings.Contains(output, "Error") {
		t.Errorf("expected error message, got: %s", output)
	}
}

func TestHandleWorkspaceGet_Success(t *testing.T) {
	now := time.Now()
	ws := types.Workspace{
		Id:               1,
		Path:             "/home/user/project",
		TotalFiles:       50,
		UseGlobalFilters: false,
		CreatedAt:        now,
		LastAccessed:     now,
		LastFullSync:     now,
		Indexing:         true,
	}

	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspace/get" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", ws))
	})

	output := captureStdout(t, func() {
		handleWorkspaceGet("/home/user/project")
	})
	if !strings.Contains(output, "/home/user/project") {
		t.Errorf("expected workspace path, got: %s", output)
	}
	if !strings.Contains(output, "Indexing: true") {
		t.Errorf("expected indexing true, got: %s", output)
	}
}

func TestHandleWorkspaceGet_ServerError(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 1, "not found", nil))
	})

	output := captureStdout(t, func() {
		handleWorkspaceGet("/nonexistent")
	})
	if !strings.Contains(output, "Error") {
		t.Errorf("expected error message, got: %s", output)
	}
}

func TestHandleWorkspaceCreate_EmptyPath(t *testing.T) {
	output := captureStdout(t, func() {
		handleWorkspaceCreate("")
	})
	if !strings.Contains(output, "Usage:") {
		t.Errorf("expected usage message for empty path, got: %s", output)
	}
}

func TestHandleWorkspaceCreate_RelativePath(t *testing.T) {
	output := captureStdout(t, func() {
		handleWorkspaceCreate("relative/path")
	})
	if !strings.Contains(output, "Workspace path must be absolute") {
		t.Errorf("expected absolute path error, got: %s", output)
	}
}

func TestHandleWorkspaceCreate_Success(t *testing.T) {
	now := time.Now()
	ws := types.Workspace{
		Id:               2,
		Path:             "/home/user/newproject",
		TotalFiles:       0,
		UseGlobalFilters: true,
		CreatedAt:        now,
		LastAccessed:     now,
		LastFullSync:     now,
	}

	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspace/create" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", ws))
	})

	output := captureStdout(t, func() {
		handleWorkspaceCreate("/home/user/newproject")
	})
	if !strings.Contains(output, "Created workspace") {
		t.Errorf("expected 'Created workspace', got: %s", output)
	}
	if !strings.Contains(output, "/home/user/newproject") {
		t.Errorf("expected path in output, got: %s", output)
	}
}

func TestHandleWorkspaceCreate_ServerError(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 1, "already exists", nil))
	})

	output := captureStdout(t, func() {
		handleWorkspaceCreate("/home/user/existing")
	})
	if !strings.Contains(output, "Error") {
		t.Errorf("expected error message, got: %s", output)
	}
}

func TestHandleWorkspaceDelete_EmptyPath(t *testing.T) {
	output := captureStdout(t, func() {
		handleWorkspaceDelete("")
	})
	if !strings.Contains(output, "Usage:") {
		t.Errorf("expected usage message for empty path, got: %s", output)
	}
}

func TestHandleWorkspaceDelete_Success(t *testing.T) {
	now := time.Now()
	ws := types.Workspace{
		Id:        1,
		Path:      "/home/user/project",
		CreatedAt: now,
	}

	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspace/delete" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", ws))
	})

	output := captureStdout(t, func() {
		handleWorkspaceDelete("/home/user/project")
	})
	if !strings.Contains(output, "Deleted") {
		t.Errorf("expected 'Deleted' in output, got: %s", output)
	}
}

func TestHandleWorkspaceDelete_ServerError(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 1, "not found", nil))
	})

	output := captureStdout(t, func() {
		handleWorkspaceDelete("/home/user/project")
	})
	if !strings.Contains(output, "Error") {
		t.Errorf("expected error message, got: %s", output)
	}
}

func TestHandleWorkspaceSync_EmptyPath(t *testing.T) {
	output := captureStdout(t, func() {
		handleWorkspaceSync("")
	})
	if !strings.Contains(output, "Usage:") {
		t.Errorf("expected usage for empty path, got: %s", output)
	}
}

func TestHandleWorkspaceSync_RelativePath(t *testing.T) {
	output := captureStdout(t, func() {
		handleWorkspaceSync("relative/path")
	})
	if !strings.Contains(output, "Workspace path must be absolute") {
		t.Errorf("expected absolute path error, got: %s", output)
	}
}

func TestHandleWorkspaceSync_Success(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspace/sync" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "syncing", nil))
	})

	output := captureStdout(t, func() {
		handleWorkspaceSync("/home/user/project")
	})
	if !strings.Contains(output, "Message: syncing") {
		t.Errorf("expected sync message, got: %s", output)
	}
}

func TestHandleWorkspaceSync_ServerError(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 1, "sync failed", nil))
	})

	output := captureStdout(t, func() {
		handleWorkspaceSync("/home/user/project")
	})
	if !strings.Contains(output, "Error") {
		t.Errorf("expected error message, got: %s", output)
	}
}

func TestHandleWorkspaceSyncAll_Success(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspace/sync-all" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "all synced", nil))
	})

	output := captureStdout(t, func() {
		handleWorkspaceSyncAll()
	})
	if !strings.Contains(output, "Message: all synced") {
		t.Errorf("expected sync-all message, got: %s", output)
	}
}

func TestHandleWorkspaceSyncAll_ServerError(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 1, "sync failed", nil))
	})

	output := captureStdout(t, func() {
		handleWorkspaceSyncAll()
	})
	if !strings.Contains(output, "Error") {
		t.Errorf("expected error message, got: %s", output)
	}
}

func TestHandleWorkspaceMove_EmptyArgs(t *testing.T) {
	output := captureStdout(t, func() {
		handleWorkspaceMove("", "")
	})
	if !strings.Contains(output, "Usage:") {
		t.Errorf("expected usage message, got: %s", output)
	}
}

func TestHandleWorkspaceMove_EmptyNewPath(t *testing.T) {
	output := captureStdout(t, func() {
		handleWorkspaceMove("1", "")
	})
	if !strings.Contains(output, "Usage:") {
		t.Errorf("expected usage message, got: %s", output)
	}
}

func TestHandleWorkspaceMove_InvalidID(t *testing.T) {
	output := captureStdout(t, func() {
		handleWorkspaceMove("notanumber", "/new/path")
	})
	if !strings.Contains(output, "Invalid workspace ID") {
		t.Errorf("expected invalid ID error, got: %s", output)
	}
}

func TestHandleWorkspaceMove_Success(t *testing.T) {
	now := time.Now()
	ws := types.Workspace{
		Id:           1,
		Path:         "/home/user/newpath",
		CreatedAt:    now,
		LastAccessed: now,
		LastFullSync: now,
	}

	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspace/move" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", ws))
	})

	output := captureStdout(t, func() {
		handleWorkspaceMove("1", "/home/user/newpath")
	})
	if !strings.Contains(output, "Moved") {
		t.Errorf("expected 'Moved' in output, got: %s", output)
	}
	if !strings.Contains(output, "/home/user/newpath") {
		t.Errorf("expected new path in output, got: %s", output)
	}
}

func TestHandleWorkspaceMove_ServerError(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 1, "move failed", nil))
	})

	output := captureStdout(t, func() {
		handleWorkspaceMove("1", "/new/path")
	})
	if !strings.Contains(output, "Error") {
		t.Errorf("expected error message, got: %s", output)
	}
}

func TestPrintWorkspace(t *testing.T) {
	now := time.Now()
	ws := types.Workspace{
		Id:               42,
		Path:             "/home/user/myproject",
		TotalFiles:       1234,
		UseGlobalFilters: true,
		CreatedAt:        now,
		LastAccessed:     now,
		LastFullSync:     now,
		Indexing:         false,
	}

	output := captureStdout(t, func() {
		printWorkspace("Test", ws)
	})
	if !strings.Contains(output, "Test 42:") {
		t.Errorf("expected 'Test 42:', got: %s", output)
	}
	if !strings.Contains(output, "/home/user/myproject") {
		t.Error("expected path in output")
	}
	if !strings.Contains(output, "Use global filters: true") {
		t.Error("expected use global filters in output")
	}
}

func TestPrintWorkspace_EmptyPrefix(t *testing.T) {
	now := time.Now()
	ws := types.Workspace{
		Id:        1,
		Path:      "/tmp",
		CreatedAt: now,
	}

	output := captureStdout(t, func() {
		printWorkspace("", ws)
	})
	if !strings.Contains(output, "1:") {
		t.Errorf("expected '1:' in output, got: %s", output)
	}
}

// Test workspace commands dispatching via handleWorkspace
func TestHandleWorkspace_ListDispatch(t *testing.T) {
	workspaces := types.Workspaces{
		Workspaces: []types.Workspace{},
	}

	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", workspaces))
	})

	// Should not panic
	output := captureStdout(t, func() {
		handleWorkspace([]string{"list"})
	})
	_ = output
}

func TestHandleWorkspace_SyncAllDispatch(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", nil))
	})

	output := captureStdout(t, func() {
		handleWorkspace([]string{"sync-all"})
	})
	_ = output
}

func TestHandleWorkspaceList_InvalidJSON(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Return valid CommonResponse but with invalid data for workspace list
		resp := `{"code":0,"message":"ok","data":"not-a-workspaces-object"}`
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(resp))
	})

	output := captureStdout(t, func() {
		handleWorkspaceList()
	})
	if !strings.Contains(output, "Error") {
		t.Errorf("expected error for invalid workspace JSON, got: %s", output)
	}
}

func TestHandleWorkspaceGet_InvalidJSON(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		resp := `{"code":0,"message":"ok","data":"bad"}`
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(resp))
	})

	output := captureStdout(t, func() {
		handleWorkspaceGet("/some/path")
	})
	if !strings.Contains(output, "Error") {
		t.Errorf("expected error for invalid JSON, got: %s", output)
	}
}

// Test dispatch via handleWorkspace to sub-handlers
func TestHandleWorkspace_CreateDispatch(t *testing.T) {
	output := captureStdout(t, func() {
		handleWorkspace([]string{"create", "relative/path"})
	})
	if !strings.Contains(output, "Workspace path must be absolute") {
		t.Errorf("expected absolute path error, got: %s", output)
	}
}

func TestHandleWorkspace_DeleteDispatch(t *testing.T) {
	output := captureStdout(t, func() {
		handleWorkspace([]string{"delete", ""})
	})
	// empty path handled inside handleWorkspaceDelete
	if !strings.Contains(output, "Usage:") {
		t.Errorf("expected usage message, got: %s", output)
	}
}

func TestHandleWorkspace_SyncDispatch(t *testing.T) {
	output := captureStdout(t, func() {
		handleWorkspace([]string{"sync", "relative"})
	})
	if !strings.Contains(output, "Workspace path must be absolute") {
		t.Errorf("expected absolute path error, got: %s", output)
	}
}

func TestHandleWorkspace_GetDispatch(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 1, "not found", nil))
	})

	output := captureStdout(t, func() {
		handleWorkspace([]string{"get", "/some/path"})
	})
	if !strings.Contains(output, "Error") {
		t.Errorf("expected error, got: %s", output)
	}
}

func TestHandleWorkspace_MoveDispatch(t *testing.T) {
	output := captureStdout(t, func() {
		handleWorkspace([]string{"move", "", ""})
	})
	if !strings.Contains(output, "Usage:") {
		t.Errorf("expected usage message, got: %s", output)
	}
}

func TestHandleWorkspaceCreate_InvalidJSON(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		resp := `{"code":0,"message":"ok","data":"bad"}`
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(resp))
	})

	output := captureStdout(t, func() {
		handleWorkspaceCreate("/valid/abs/path")
	})
	if !strings.Contains(output, "Error") {
		t.Errorf("expected error for invalid JSON, got: %s", output)
	}
}

func TestHandleWorkspaceDelete_InvalidJSON(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		resp := `{"code":0,"message":"ok","data":"bad"}`
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(resp))
	})

	output := captureStdout(t, func() {
		handleWorkspaceDelete("/some/path")
	})
	if !strings.Contains(output, "Error") {
		t.Errorf("expected error for invalid JSON, got: %s", output)
	}
}

func TestHandleWorkspaceMove_InvalidJSON(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		resp := `{"code":0,"message":"ok","data":"bad"}`
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(resp))
	})

	output := captureStdout(t, func() {
		handleWorkspaceMove("1", "/new/path")
	})
	if !strings.Contains(output, "Error") {
		t.Errorf("expected error for invalid JSON, got: %s", output)
	}
}
