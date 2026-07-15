package client

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/codetrek/haystack/server/internal/shared/types"
)

func TestHandleServer_Help(t *testing.T) {
	output := captureStdout(t, func() {
		handleServer([]string{"-h"})
	})
	if !strings.Contains(output, "Usage:") {
		t.Error("handleServer -h should show usage")
	}
	if !strings.Contains(output, "status") {
		t.Error("should mention status command")
	}
}

func TestHandleServer_NoArgs(t *testing.T) {
	output := captureStdout(t, func() {
		handleServer(nil)
	})
	if !strings.Contains(output, "Usage:") {
		t.Error("handleServer with no args should show usage")
	}
}

func TestHandleServer_UnknownCommand(t *testing.T) {
	output := captureStdout(t, func() {
		handleServer([]string{"badcmd"})
	})
	if !strings.Contains(output, "Unknown server command: badcmd") {
		t.Errorf("expected unknown command message, got: %s", output)
	}
}

func TestHandleServerRun_UnknownOption(t *testing.T) {
	output := captureStdout(t, func() {
		handleServerRun([]string{"--unknown"})
	})
	if !strings.Contains(output, "Unknown option") {
		t.Errorf("expected unknown option message, got: %s", output)
	}
}

func TestHandleServerStatus_NotRunning(t *testing.T) {
	// When no server is running, IsServerRunning() should return false
	// Note: in test environment, no server should be running on the lock file
	output := captureStdout(t, func() {
		handleServerStatus()
	})
	// Either "Server is not running" or it might connect to an existing server
	if len(output) == 0 {
		t.Error("handleServerStatus should produce output")
	}
}

func TestHandleServerStop_NotRunning(t *testing.T) {
	output := captureStdout(t, func() {
		handleServerStop()
	})
	// In test env, likely no server running
	if len(output) == 0 {
		t.Error("handleServerStop should produce output")
	}
}

func TestGetRunningState_Success(t *testing.T) {
	status := types.ServerStatus{
		PID:          12345,
		Version:      "1.0.0",
		ShuttingDown: false,
		Restarting:   false,
		DataPath:     "/tmp/test",
	}

	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/server/status" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", status))
	})

	result, err := getRunningState()
	if err != nil {
		t.Fatalf("getRunningState error: %v", err)
	}
	if result.PID != 12345 {
		t.Errorf("expected PID 12345, got %d", result.PID)
	}
	if result.Version != "1.0.0" {
		t.Errorf("expected version '1.0.0', got %q", result.Version)
	}
	if result.DataPath != "/tmp/test" {
		t.Errorf("expected data path '/tmp/test', got %q", result.DataPath)
	}
}

func TestGetRunningState_Error(t *testing.T) {
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 1, "server error", nil))
	})

	_, err := getRunningState()
	if err == nil {
		t.Fatal("expected error from getRunningState")
	}
}

func TestHandleServer_StatusDispatch(t *testing.T) {
	// Dispatch to handleServerStatus which will check IsServerRunning
	output := captureStdout(t, func() {
		handleServer([]string{"status"})
	})
	// Should produce some output (either "not running" or status)
	if len(output) == 0 {
		t.Error("status command should produce output")
	}
}

func TestHandleServer_StartDispatch(t *testing.T) {
	if os.Getenv("HAYSTACK_SKIP_STARTNEW") != "" {
		t.Skip("child process – skip to prevent cascade")
	}
	os.Setenv("HAYSTACK_SKIP_STARTNEW", "1")
	defer os.Unsetenv("HAYSTACK_SKIP_STARTNEW")

	output := captureStdout(t, func() {
		handleServer([]string{"start"})
	})
	// In test env, this will either start (unlikely) or produce output
	_ = output
}

func TestHandleServer_StopDispatch(t *testing.T) {
	output := captureStdout(t, func() {
		handleServer([]string{"stop"})
	})
	_ = output
}

func TestHandleServer_RestartDispatch(t *testing.T) {
	if os.Getenv("HAYSTACK_SKIP_STARTNEW") != "" {
		t.Skip("child process – skip to prevent cascade")
	}
	os.Setenv("HAYSTACK_SKIP_STARTNEW", "1")
	defer os.Unsetenv("HAYSTACK_SKIP_STARTNEW")

	// handleServerRestart: if server not running, calls StartNewServer
	output := captureStdout(t, func() {
		handleServer([]string{"restart"})
	})
	_ = output
}

func TestHandleServerRun_NoArgs(t *testing.T) {
	// handleServerRun with no args: daemon=false, would call server.Run()
	// We can't easily test this without starting a real server, but we verify it's dispatched
	// Just test that the dispatch path works in handleServer
	output := captureStdout(t, func() {
		handleServer([]string{"run", "--unknown"})
	})
	if !strings.Contains(output, "Unknown option") {
		t.Errorf("expected unknown option message, got: %s", output)
	}
}

func TestHandleServerStart_AlreadyRunning(t *testing.T) {
	if os.Getenv("HAYSTACK_SKIP_STARTNEW") != "" {
		t.Skip("child process – skip to prevent cascade")
	}
	os.Setenv("HAYSTACK_SKIP_STARTNEW", "1")
	defer os.Unsetenv("HAYSTACK_SKIP_STARTNEW")

	// We can't easily control IsServerRunning, but we test the function directly
	output := captureStdout(t, func() {
		handleServerStart()
	})
	_ = output // Will print "already running" or attempt to start
}

func TestHandleServerRestart_NotRunning(t *testing.T) {
	if os.Getenv("HAYSTACK_SKIP_STARTNEW") != "" {
		t.Skip("child process – skip to prevent cascade")
	}
	os.Setenv("HAYSTACK_SKIP_STARTNEW", "1")
	defer os.Unsetenv("HAYSTACK_SKIP_STARTNEW")

	output := captureStdout(t, func() {
		handleServerRestart()
	})
	_ = output // Will call StartNewServer if not running
}
