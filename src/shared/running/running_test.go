package running

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSetVersion(t *testing.T) {
	// Reset version for test
	version = ""
	SetVersion("1.0.0")
	assert.Equal(t, "1.0.0", Version())
}

func TestSetVersion_OnlyOnce(t *testing.T) {
	version = ""
	SetVersion("1.0.0")
	SetVersion("2.0.0") // should be ignored
	assert.Equal(t, "1.0.0", Version())
}

func TestIsDevVersion(t *testing.T) {
	version = "dev"
	assert.True(t, IsDevVersion())
	version = "1.0.0"
	assert.False(t, IsDevVersion())
}

func TestUserHomeDir(t *testing.T) {
	// Reset for test
	initHomeDir = sync.Once{}
	userHomeDir = ""
	dir := UserHomeDir()
	assert.NotEmpty(t, dir)
	// Second call should return same value
	assert.Equal(t, dir, UserHomeDir())
}

func TestExecutable(t *testing.T) {
	once = sync.Once{}
	executable = ""
	exe := Executable()
	assert.NotEmpty(t, exe)
}

func TestExecutableName(t *testing.T) {
	once = sync.Once{}
	executable = ""
	name := ExecutableName()
	assert.NotEmpty(t, name)
}

func TestExecutablePath(t *testing.T) {
	once = sync.Once{}
	executable = ""
	path := ExecutablePath()
	assert.NotEmpty(t, path)
}

func TestInitShutdown_And_Shutdown(t *testing.T) {
	wg := &sync.WaitGroup{}
	InitShutdown(wg)

	assert.False(t, IsShuttingDown())

	Shutdown()
	assert.True(t, IsShuttingDown())

	wg.Wait()
}

func TestShutdown_Idempotent(t *testing.T) {
	wg := &sync.WaitGroup{}
	InitShutdown(wg)

	Shutdown()
	Shutdown() // should not panic
	wg.Wait()
}

func TestGetShutdown(t *testing.T) {
	wg := &sync.WaitGroup{}
	InitShutdown(wg)

	ctx := GetShutdown()
	assert.NotNil(t, ctx)

	Shutdown()
	wg.Wait()

	select {
	case <-ctx.Done():
		// expected
	default:
		t.Fatal("context should be done after shutdown")
	}
}

func TestWaitingForShutdown(t *testing.T) {
	wg := &sync.WaitGroup{}
	InitShutdown(wg)

	done := make(chan struct{})
	go func() {
		WaitingForShutdown()
		close(done)
	}()

	Shutdown()
	<-done // should not block
	wg.Wait()
}

func TestRestart(t *testing.T) {
	wg := &sync.WaitGroup{}
	InitShutdown(wg)

	assert.False(t, IsRestart())
	Restart()
	assert.True(t, IsRestart())
	assert.True(t, IsShuttingDown())

	wg.Wait()
}

func TestRegisterLockFile(t *testing.T) {
	lockFile = "" // reset
	RegisterLockFile("/tmp/test.lock")
	assert.Equal(t, "/tmp/test.lock", lockFile)
}

func TestRegisterLockFile_OnlyOnce(t *testing.T) {
	lockFile = "" // reset
	RegisterLockFile("/tmp/test1.lock")
	RegisterLockFile("/tmp/test2.lock") // should be ignored
	assert.Equal(t, "/tmp/test1.lock", lockFile)
}

func TestCheckAndLockServer_NoLockFile(t *testing.T) {
	lockFile = "" // reset
	_, err := CheckAndLockServer()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "lock file not registered")
}

func TestCheckAndLockServer_Success(t *testing.T) {
	tmpDir := t.TempDir()
	lockFile = "" // reset
	RegisterLockFile(filepath.Join(tmpDir, "server.lock"))

	cleanup, err := CheckAndLockServer()
	assert.NoError(t, err)
	assert.NotNil(t, cleanup)
	cleanup()
}

func TestCheckAndLockServer_AlreadyLocked(t *testing.T) {
	tmpDir := t.TempDir()
	lockFile = "" // reset
	RegisterLockFile(filepath.Join(tmpDir, "server.lock"))

	cleanup1, err := CheckAndLockServer()
	assert.NoError(t, err)

	// Reset lockFile to same path so second call uses same file
	lockFile = "" // reset
	RegisterLockFile(filepath.Join(tmpDir, "server.lock"))

	_, err = CheckAndLockServer()
	assert.Error(t, err)
	assert.Equal(t, ErrRunning, err)

	cleanup1()
}

func TestIsServerRunning_NotRunning(t *testing.T) {
	tmpDir := t.TempDir()
	lockFile = "" // reset
	RegisterLockFile(filepath.Join(tmpDir, "notrunning.lock"))

	assert.False(t, IsServerRunning())
}

func TestIsServerRunning_Running(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "running.lock")
	lockFile = "" // reset
	RegisterLockFile(lockPath)

	cleanup, err := CheckAndLockServer()
	assert.NoError(t, err)

	lockFile = "" // reset
	RegisterLockFile(lockPath)
	assert.True(t, IsServerRunning())

	cleanup()
}

func TestIsDaemonMode(t *testing.T) {
	// Just verify it doesn't panic - value depends on flag parsing
	_ = IsDaemonMode()
}

func TestNormalizePath_ViaExecutable(t *testing.T) {
	// Verify Executable returns a valid normalized path
	once = sync.Once{}
	executable = ""
	exe := Executable()
	assert.True(t, filepath.IsAbs(exe))

	// Check it exists
	_, err := os.Stat(exe)
	assert.NoError(t, err)
}
