package server

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/codetrek/haystack/conf"
	"github.com/codetrek/haystack/server/core/invertedindex"
	"github.com/codetrek/haystack/server/indexer"
	"github.com/codetrek/haystack/shared/running"
)

// TestZZ_Run exercises the full Run() happy path. This file is named
// "zz_*" so Go processes it AFTER server_test.go (alphabetically).
//
// Before calling Run(), it resets the indexer's package-level singletons
// via indexer.ResetForTest() because TestServerEndToEnd (in server_test.go)
// shuts them down via running.Shutdown(), closing channels that cannot
// be reopened.
func TestZZ_Run(t *testing.T) {
	origWriter := log.Writer()
	origFlags := log.Flags()
	defer func() {
		log.SetOutput(origWriter)
		log.SetFlags(origFlags)
	}()

	// Reset indexer singletons so Run() can start fresh after
	// TestServerEndToEnd has shut everything down.
	indexer.ResetForTest()

	// Reset the default HTTP mux to avoid duplicate pattern registration
	// panic (Go 1.22+ enforces unique patterns on DefaultServeMux).
	http.DefaultServeMux = http.NewServeMux()

	tempDir := t.TempDir()

	testWorkspace := filepath.Join(tempDir, "workspace")
	assert.NoError(t, os.MkdirAll(testWorkspace, 0755))
	assert.NoError(t, os.WriteFile(
		filepath.Join(testWorkspace, "hello.go"),
		[]byte("package main\nfunc main() {}\n"),
		0644,
	))

	conf.Get().Global.DataPath = filepath.Join(tempDir, "data")
	conf.Get().Global.Port = 19876
	conf.Get().Global.SocketPath = ""
	conf.Get().Server.CacheSize = 8 * 1024 * 1024
	conf.Get().Server.LoggingStdout = false
	conf.Get().ForTest.Path = testWorkspace

	invertedindex.FlushTicker = 50 * time.Millisecond
	invertedindex.FlushWaitTimeout = 1 * time.Microsecond
	invertedindex.FlushWaitBatchSize = 10

	running.RegisterLockFile(filepath.Join(tempDir, "server.lock"))

	done := make(chan struct{})
	go func() {
		defer close(done)
		Run()
	}()

	time.Sleep(1 * time.Second)

	running.Shutdown()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("server did not stop within 15s timeout")
	}

	logFile := filepath.Join(tempDir, "data", "logs", "server.log")
	data, err := os.ReadFile(logFile)
	assert.NoError(t, err, "server log file should exist")
	logOutput := string(data)
	assert.Contains(t, logOutput, "Starting haystack server")
	assert.Contains(t, logOutput, "Haystack server stopped")
}
