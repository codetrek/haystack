package server

import (
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/codetrek/haystack/core/invertedindex"
	"github.com/codetrek/haystack/core/kv"
	"github.com/codetrek/haystack/core/queue"
	"github.com/codetrek/haystack/server/internal/conf"
	"github.com/codetrek/haystack/server/internal/shared/running"
)

// TestInitLog_Stdout verifies initLog configures stdout logging.
func TestInitLog_Stdout(t *testing.T) {
	origWriter := log.Writer()
	origFlags := log.Flags()
	defer func() {
		log.SetOutput(origWriter)
		log.SetFlags(origFlags)
	}()

	conf.Get().Server.LoggingStdout = true

	initLog()

	assert.Equal(t, log.LstdFlags, log.Flags())
	assert.Equal(t, os.Stdout, log.Writer())
}

// TestInitLog_File verifies initLog configures file-based logging via lumberjack.
func TestInitLog_File(t *testing.T) {
	origWriter := log.Writer()
	origFlags := log.Flags()
	defer func() {
		log.SetOutput(origWriter)
		log.SetFlags(origFlags)
	}()

	tempDir := t.TempDir()
	conf.Get().Server.LoggingStdout = false
	conf.Get().Global.DataPath = tempDir

	initLog()
	defer closeLog() // release the lumberjack file handle before t.TempDir cleanup

	assert.Equal(t, log.LstdFlags, log.Flags())

	// Write a log message and verify it appears in the log file
	testMsg := "test_log_message_for_file_logging_12345"
	log.Println(testMsg)

	// Lumberjack writes synchronously, but give a tiny buffer
	time.Sleep(50 * time.Millisecond)

	logFile := filepath.Join(tempDir, "logs", "server.log")
	data, err := os.ReadFile(logFile)
	assert.NoError(t, err, "log file should exist after writing")
	assert.Contains(t, string(data), testMsg)
}

// TestRun_DataStorageError tests the run() error path when data storage fails to open.
func TestRun_DataStorageError(t *testing.T) {
	// Block data-storage init portably by putting a FILE where the "data" DB dir is
	// expected (same pattern as TestRun_IndexStorageError below).
	//
	// This previously set DataPath = "/dev/null/impossible": on POSIX that is
	// uncreatable (/dev/null is a file, so a subdir under it fails with ENOTDIR), so
	// run() failed fast at data init. On Windows there is no /dev/null device, so the
	// path resolves to C:\dev\null\impossible — which IS creatable, so data init
	// SUCCEEDED, run() fell through to starting the HTTP server and blocked forever
	// (hanging `go test ./...` and littering C:\dev\null\impossible).
	tempDir := t.TempDir()
	conf.Get().Global.DataPath = tempDir
	conf.Get().Server.CacheSize = 8 * 1024 * 1024

	dataPath := filepath.Join(tempDir, "data")
	assert.NoError(t, os.WriteFile(dataPath, []byte("blocker"), 0644))

	err := run()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error initializing data storage")
}

// TestRun_IndexStorageError tests the run() error path when index storage fails to open.
func TestRun_IndexStorageError(t *testing.T) {
	tempDir := t.TempDir()

	conf.Get().Global.DataPath = tempDir
	conf.Get().Server.CacheSize = 8 * 1024 * 1024

	indexPath := filepath.Join(tempDir, "index")
	os.WriteFile(indexPath, []byte("blocker"), 0644)

	err := run()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error initializing index storage")
}

// TestRun_LockError tests Run() when CheckAndLockServer fails (line 36-38).
func TestRun_LockError(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a blocker file so MkdirAll fails inside CheckAndLockServer.
	blocker := filepath.Join(tmpDir, "blocker")
	err := os.WriteFile(blocker, []byte("x"), 0644)
	assert.NoError(t, err)

	running.ResetLockFileForTest()
	running.RegisterLockFile(filepath.Join(blocker, "sub", "lock.pid"))

	// Run() should log the error and return without crashing.
	Run()
}

// TestRun_RunError tests Run() when run() returns an error (line 47-48).
func TestRun_RunError(t *testing.T) {
	restore := saveAndMockInits()
	defer restore()

	// Make invertedindexInit fail so run() returns an error.
	invertedindexInit = func(_ kv.Store, _ *queue.Mpsc) (*invertedindex.Index, error) {
		return nil, errFake
	}

	setupRunEnv(t)

	// Ensure CheckAndLockServer succeeds by using a valid lock path.
	lockPath := filepath.Join(t.TempDir(), "test.lock")
	running.ResetLockFileForTest()
	running.RegisterLockFile(lockPath)

	// Run() should log the error from run() and return without crashing.
	Run()
}
