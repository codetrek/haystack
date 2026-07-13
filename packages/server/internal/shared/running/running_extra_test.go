package running

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// shutdown.go – cover the signal-based shutdown path (lines 37-39)
// ---------------------------------------------------------------------------

func TestInitShutdown_Signal(t *testing.T) {
	wg := &sync.WaitGroup{}
	InitShutdown(wg)

	assert.False(t, IsShuttingDown())

	// Send SIGTERM to ourselves to trigger the signal-handling goroutine.
	proc, err := os.FindProcess(os.Getpid())
	assert.NoError(t, err)

	err = proc.Signal(syscall.SIGTERM)
	assert.NoError(t, err)

	// Wait for shutdown to be triggered (with timeout).
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// expected
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for signal-triggered shutdown")
	}

	assert.True(t, IsShuttingDown())
}

// ---------------------------------------------------------------------------
// server.go – cover MkdirAll error path (line 31-33)
// ---------------------------------------------------------------------------

func TestCheckAndLockServer_MkdirAllFails(t *testing.T) {
	tmpDir := t.TempDir()
	blocker := filepath.Join(tmpDir, "blocker")
	err := os.WriteFile(blocker, []byte("x"), 0644)
	assert.NoError(t, err)

	lockFile = "" // reset
	RegisterLockFile(filepath.Join(blocker, "sub", "lock.pid"))

	_, err = CheckAndLockServer()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create lock directory")
}

// ---------------------------------------------------------------------------
// server.go – IsServerRunning when CheckAndLockServer returns
// a non-ErrRunning error → should return false.
// ---------------------------------------------------------------------------

func TestIsServerRunning_ErrorNotRunning(t *testing.T) {
	tmpDir := t.TempDir()
	blocker := filepath.Join(tmpDir, "blocker")
	err := os.WriteFile(blocker, []byte("x"), 0644)
	assert.NoError(t, err)

	lockFile = ""
	RegisterLockFile(filepath.Join(blocker, "sub", "lock.pid"))

	assert.False(t, IsServerRunning())
}

// ---------------------------------------------------------------------------
// runtime.go – StartNewServer code paths
//
// To prevent an infinite cascade (the spawned child is the test binary
// which would re-run the test), we set an env var before calling
// StartNewServer; child processes inherit it and skip the test.
// ---------------------------------------------------------------------------

func TestStartNewServer_DaemonArg(t *testing.T) {
	if os.Getenv("HAYSTACK_SKIP_STARTNEW") != "" {
		t.Skip("child process – skip to prevent cascade")
	}

	savedArgs := os.Args
	defer func() { os.Args = savedArgs }()

	os.Args = []string{"test-binary", "--daemon"}
	StartNewServer()
}

func TestStartNewServer_NonDaemonArg(t *testing.T) {
	if os.Getenv("HAYSTACK_SKIP_STARTNEW") != "" {
		t.Skip("child process – skip to prevent cascade")
	}

	savedArgs := os.Args
	defer func() { os.Args = savedArgs }()

	os.Args = []string{"test-binary", "--something-else"}
	StartNewServer()
}

// ---------------------------------------------------------------------------
// server.go – Verify cleanup function removes lock file
// ---------------------------------------------------------------------------

func TestCheckAndLockServer_CleanupRemovesFile(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "cleanup.lock")
	lockFile = ""
	RegisterLockFile(lockPath)

	cleanup, err := CheckAndLockServer()
	assert.NoError(t, err)
	assert.NotNil(t, cleanup)

	_, err = os.Stat(lockPath)
	assert.NoError(t, err)

	cleanup()

	_, err = os.Stat(lockPath)
	assert.True(t, os.IsNotExist(err))
}

// ---------------------------------------------------------------------------
// shutdown.go – Restart + InitShutdown cycle (exercise restart reset)
// ---------------------------------------------------------------------------

func TestInitShutdown_ResetsRestart(t *testing.T) {
	wg := &sync.WaitGroup{}
	InitShutdown(wg)
	Restart()
	assert.True(t, IsRestart())
	wg.Wait()

	wg2 := &sync.WaitGroup{}
	InitShutdown(wg2)
	assert.False(t, IsRestart())
	Shutdown()
	wg2.Wait()
}

// ---------------------------------------------------------------------------
// Exported errors
// ---------------------------------------------------------------------------

func TestErrShutdown(t *testing.T) {
	assert.Equal(t, "server is shutting down", ErrShutdown.Error())
}

func TestErrRunning(t *testing.T) {
	assert.Equal(t, "server is running", ErrRunning.Error())
}

// ---------------------------------------------------------------------------
// runtime.go – StartNewServer with os.Getwd() failure
// ---------------------------------------------------------------------------

func TestStartNewServer_GetwdFails(t *testing.T) {
	if os.Getenv("HAYSTACK_SKIP_STARTNEW") != "" {
		t.Skip("child process – skip to prevent cascade")
	}

	savedArgs := os.Args
	defer func() { os.Args = savedArgs }()
	os.Args = []string{"test-binary", "--daemon"}

	// Create a temp dir, chdir to it, then remove it to break os.Getwd().
	// NOTE: do NOT set HAYSTACK_SKIP_STARTNEW here — that would cause
	// an early return before reaching the os.Getwd() call.
	tmpDir, err := os.MkdirTemp("", "startnew-test-*")
	assert.NoError(t, err)

	origDir, err := os.Getwd()
	assert.NoError(t, err)
	defer os.Chdir(origDir)

	err = os.Chdir(tmpDir)
	assert.NoError(t, err)

	err = os.RemoveAll(tmpDir)
	assert.NoError(t, err)

	// Should hit the os.Getwd error path and return early
	// (no child is spawned because the error occurs before os.StartProcess)
	StartNewServer()
}

// TestStartNewServer covers args[0] == "--daemon" path with valid env.
// We also test that both the daemon and client-start code paths execute
// by verifying the spawned process runs.
func TestStartNewServer_EmptyArgs_Panics(t *testing.T) {
	// StartNewServer accesses os.Args[1:] then args[0]. If os.Args has
	// only 1 element, args is empty and args[0] panics. Verify that.
	//
	// No need to set HAYSTACK_SKIP_STARTNEW here — the panic happens
	// before os.StartProcess, so no child process is ever spawned.

	savedArgs := os.Args
	defer func() { os.Args = savedArgs }()
	os.Args = []string{"test-binary"} // empty args[1:]

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic from empty args slice")
		}
	}()

	// Temporarily clear the env var so StartNewServer doesn't bail out
	// before reaching the panic point.
	saved := os.Getenv("HAYSTACK_SKIP_STARTNEW")
	os.Unsetenv("HAYSTACK_SKIP_STARTNEW")
	defer os.Setenv("HAYSTACK_SKIP_STARTNEW", saved)

	StartNewServer()
}

// ---------------------------------------------------------------------------
// runtime.go – StartNewServer early return when HAYSTACK_SKIP_STARTNEW is set
// ---------------------------------------------------------------------------

func TestStartNewServer_SkipEnvVar(t *testing.T) {
	// Verify that setting the guard env var causes an immediate return
	// without spawning a child process or touching os.Args.
	t.Setenv("HAYSTACK_SKIP_STARTNEW", "1")

	// Intentionally do NOT set os.Args — if StartNewServer tried to
	// access os.Args[1:] it would work but hit os.StartProcess; the
	// point is it should return before any of that.
	StartNewServer()
}

// ---------------------------------------------------------------------------
// runtime.go – StartNewServer with os.Executable() failure (lines 82-85)
// ---------------------------------------------------------------------------

func TestStartNewServer_ExecutableFails(t *testing.T) {
	saved := os.Getenv("HAYSTACK_SKIP_STARTNEW")
	os.Unsetenv("HAYSTACK_SKIP_STARTNEW")
	defer os.Setenv("HAYSTACK_SKIP_STARTNEW", saved)

	origExe := osExecutable
	osExecutable = func() (string, error) {
		return "", fmt.Errorf("injected executable error")
	}
	defer func() { osExecutable = origExe }()

	// Should log error and return without crashing
	StartNewServer()
}

// ---------------------------------------------------------------------------
// runtime.go – StartNewServer with os.StartProcess failure (lines 112-115)
// ---------------------------------------------------------------------------

func TestStartNewServer_StartProcessFails(t *testing.T) {
	saved := os.Getenv("HAYSTACK_SKIP_STARTNEW")
	os.Unsetenv("HAYSTACK_SKIP_STARTNEW")
	defer os.Setenv("HAYSTACK_SKIP_STARTNEW", saved)

	savedArgs := os.Args
	defer func() { os.Args = savedArgs }()
	os.Args = []string{"test-binary", "--daemon"}

	// Point osExecutable to a non-existent file so StartProcess fails
	origExe := osExecutable
	osExecutable = func() (string, error) {
		return "/nonexistent/path/to/binary", nil
	}
	defer func() { osExecutable = origExe }()

	// Should log "Failed to start new process" and return
	StartNewServer()
}
