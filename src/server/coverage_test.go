package server

import (
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/codetrek/haystack/conf"
	"github.com/codetrek/haystack/server/core/idtable"
	"github.com/codetrek/haystack/server/core/storage"
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
	conf.Get().Global.DataPath = "/dev/null/impossible"
	conf.Get().Server.CacheSize = 8 * 1024 * 1024

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

// TestRun_IdTableInitError tests the run() error path when idtable.Init fails
// (e.g., already initialized from a previous test).
func TestRun_IdTableInitError(t *testing.T) {
	tempDir := t.TempDir()

	conf.Get().Server.CacheSize = 8 * 1024 * 1024

	// Open a separate DB to initialize idtable
	helperPath := filepath.Join(tempDir, "helper")
	helperDb, err := storage.Open(helperPath, conf.Get().Server.CacheSize)
	assert.NoError(t, err)

	err = idtable.Init(helperDb)
	assert.NoError(t, err)
	// Don't close idtable — the next run() call should fail at idtable.Init

	// Point run() at a different data path so storage.Open succeeds
	conf.Get().Global.DataPath = filepath.Join(tempDir, "run_data")

	err = run()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error initializing id table")

	// Clean up
	idtable.Close()
	helperDb.Close()
}
