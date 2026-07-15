package client

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codetrek/haystack/server/internal/shared/running"
	"github.com/codetrek/haystack/server/internal/shared/types"
	"github.com/gofrs/flock"
)

// simulateServerRunning registers a lock file and holds an exclusive lock on it
// so that running.IsServerRunning() returns true. Returns a cleanup function.
func simulateServerRunning(t *testing.T) func() {
	t.Helper()
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "server.lock")
	running.ResetLockFileForTest()
	running.RegisterLockFile(lockPath)

	fl := flock.New(lockPath)
	locked, err := fl.TryLock()
	if err != nil || !locked {
		t.Fatalf("failed to acquire lock for simulation: %v", err)
	}
	return func() {
		fl.Unlock()
		os.Remove(lockPath)
		running.ResetLockFileForTest()
	}
}

// skipInChildProcess skips the test if running inside a child process
// spawned by StartNewServer, to prevent an infinite fork cascade.
func skipInChildProcess(t *testing.T) {
	t.Helper()
	if os.Getenv("HAYSTACK_SKIP_STARTNEW") != "" {
		t.Skip("child process – skip to prevent cascade")
	}
}

// --- handleServerStatus: running + success ---
func TestHandleServerStatus_Running_Success(t *testing.T) {
	skipInChildProcess(t)
	cleanup := simulateServerRunning(t)
	defer cleanup()

	status := types.ServerStatus{
		PID: 999, Version: "2.0.0", ShuttingDown: false, Restarting: false, DataPath: "/data",
	}
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", status))
	})

	output := captureStdout(t, func() {
		handleServerStatus()
	})
	if !strings.Contains(output, "999") {
		t.Errorf("expected PID in output, got: %s", output)
	}
	if !strings.Contains(output, "2.0.0") {
		t.Errorf("expected version in output, got: %s", output)
	}
}

// --- handleServerStatus: running + error ---
func TestHandleServerStatus_Running_Error(t *testing.T) {
	skipInChildProcess(t)
	cleanup := simulateServerRunning(t)
	defer cleanup()

	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 1, "fail", nil))
	})

	output := captureStdout(t, func() {
		handleServerStatus()
	})
	if !strings.Contains(output, "Error") {
		t.Errorf("expected error output, got: %s", output)
	}
}

// --- handleServerStart: already running ---
func TestHandleServerStart_AlreadyRunning_Coverage(t *testing.T) {
	skipInChildProcess(t)
	cleanup := simulateServerRunning(t)
	defer cleanup()

	output := captureStdout(t, func() {
		handleServerStart()
	})
	if !strings.Contains(output, "already running") {
		t.Errorf("expected 'already running', got: %s", output)
	}
}

// --- handleServerStop: running + success (server stops quickly) ---
func TestHandleServerStop_Running_Success(t *testing.T) {
	skipInChildProcess(t)
	cleanup := simulateServerRunning(t)

	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		// When the stop request arrives, release the lock so the
		// polling loop inside handleServerStop sees the server stopped.
		cleanup()
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", nil))
	})

	output := captureStdout(t, func() {
		handleServerStop()
	})
	if !strings.Contains(output, "Server stopped") {
		t.Errorf("expected 'Server stopped', got: %s", output)
	}
}

// --- handleServerStop: running + request error ---
func TestHandleServerStop_Running_RequestError(t *testing.T) {
	skipInChildProcess(t)
	cleanup := simulateServerRunning(t)
	defer cleanup()

	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 1, "stop failed", nil))
	})

	output := captureStdout(t, func() {
		handleServerStop()
	})
	if !strings.Contains(output, "Error stopping") {
		t.Errorf("expected error stopping message, got: %s", output)
	}
}

// --- handleServerRestart: running + shutting down ---
func TestHandleServerRestart_Running_ShuttingDown(t *testing.T) {
	skipInChildProcess(t)
	cleanup := simulateServerRunning(t)
	defer cleanup()

	status := types.ServerStatus{ShuttingDown: true}
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", status))
	})

	output := captureStdout(t, func() {
		handleServerRestart()
	})
	if !strings.Contains(output, "shutting down") {
		t.Errorf("expected shutting down message, got: %s", output)
	}
}

// --- handleServerRestart: running + restarting ---
func TestHandleServerRestart_Running_Restarting(t *testing.T) {
	skipInChildProcess(t)
	cleanup := simulateServerRunning(t)
	defer cleanup()

	status := types.ServerStatus{Restarting: true}
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", status))
	})

	output := captureStdout(t, func() {
		handleServerRestart()
	})
	if !strings.Contains(output, "shutting down or restarting") {
		t.Errorf("expected restarting message, got: %s", output)
	}
}

// --- handleServerRestart: running + getRunningState error ---
func TestHandleServerRestart_Running_StatusError(t *testing.T) {
	skipInChildProcess(t)
	cleanup := simulateServerRunning(t)
	defer cleanup()

	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 1, "status error", nil))
	})

	output := captureStdout(t, func() {
		handleServerRestart()
	})
	if !strings.Contains(output, "Error getting server status") {
		t.Errorf("expected error message, got: %s", output)
	}
}

// --- handleServerRestart: running + successful restart ---
func TestHandleServerRestart_Running_Success(t *testing.T) {
	skipInChildProcess(t)
	cleanup := simulateServerRunning(t)
	defer cleanup()

	callCount := 0
	status := types.ServerStatus{ShuttingDown: false, Restarting: false}
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		w.Write(makeCommonResponse(t, 0, "ok", status))
	})

	output := captureStdout(t, func() {
		handleServerRestart()
	})
	if !strings.Contains(output, "Server restarted") {
		t.Errorf("expected 'Server restarted', got: %s", output)
	}
}

// --- handleServerRestart: running + restart request error ---
func TestHandleServerRestart_Running_RestartError(t *testing.T) {
	skipInChildProcess(t)
	cleanup := simulateServerRunning(t)
	defer cleanup()

	status := types.ServerStatus{ShuttingDown: false, Restarting: false}
	callCount := 0
	startMockServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// First call: /server/status - success
			w.WriteHeader(http.StatusOK)
			w.Write(makeCommonResponse(t, 0, "ok", status))
		} else {
			// Second call: /server/restart - error
			w.WriteHeader(http.StatusOK)
			w.Write(makeCommonResponse(t, 1, "restart failed", nil))
		}
	})

	output := captureStdout(t, func() {
		handleServerRestart()
	})
	if !strings.Contains(output, "Error restarting") {
		t.Errorf("expected restart error message, got: %s", output)
	}
}

// --- handleServerRun: daemon mode (calls StartNewServer which will fail gracefully in test) ---
func TestHandleServerRun_Daemon(t *testing.T) {
	skipInChildProcess(t)
	os.Setenv("HAYSTACK_SKIP_STARTNEW", "1")
	defer os.Unsetenv("HAYSTACK_SKIP_STARTNEW")

	// StartNewServer tries os.Executable + os.StartProcess, which will
	// fail or produce a log message in test env. We just need to cover the path.
	output := captureStdout(t, func() {
		handleServerRun([]string{"-d"})
	})
	_ = output // may or may not produce output depending on env
}

// --- Run() coverage ---
func TestRun_NoArgs(t *testing.T) {
	skipInChildProcess(t)
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"haystack"}

	output := captureStdout(t, func() {
		Run()
	})
	if !strings.Contains(output, "Haystack") {
		t.Errorf("expected usage output, got: %s", output)
	}
}

func TestRun_WithCommand(t *testing.T) {
	skipInChildProcess(t)
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"haystack", "version"}

	output := captureStdout(t, func() {
		Run()
	})
	if len(output) == 0 {
		t.Error("expected version output")
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	skipInChildProcess(t)
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"haystack", "bogus"}

	output := captureStdout(t, func() {
		Run()
	})
	if !strings.Contains(output, "Unknown command") {
		t.Errorf("expected unknown command, got: %s", output)
	}
}
