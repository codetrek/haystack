// Package testutil provides shared test-environment helpers that eliminate
// duplication across packages that need a temporary Pebble database and
// MPSC queue for integration-style tests.
//
// This is an internal package; only tests inside the haystack module may
// import it.
package testutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/internal/core/storage"
	"github.com/codetrek/haystack/searchcore/kv"
	"github.com/codetrek/haystack/searchcore/queue"
)

// Env holds the resources created by SetupEnv so that callers can access
// them and later tear them down in reverse order.
type Env struct {
	T       *testing.T
	TempDir string
	DB      kv.Store
	Mpsc    *queue.Mpsc
}

// SetupEnv creates a temporary directory, opens a Pebble database inside it,
// and starts an MPSC queue.  The caller is responsible for calling
// env.Teardown() (typically via defer) to release everything.
//
// queueName is used to label the MPSC queue (useful in log output to tell
// queues apart when debugging).
func SetupEnv(t *testing.T, queueName string) *Env {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "haystack-test-*")
	if err != nil {
		t.Fatalf("testutil: failed to create temp dir: %v", err)
	}

	// Point the global conf at our temp dir so storage.Open writes there.
	conf.Get().Global.DataPath = tempDir

	database, err := storage.Open(filepath.Join(tempDir, "data"), 0)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("testutil: failed to open storage: %v", err)
	}

	q := queue.NewMpsc(queueName)
	q.Start()

	return &Env{
		T:       t,
		TempDir: tempDir,
		DB:      database,
		Mpsc:    q,
	}
}

// TeardownBase shuts down the MPSC queue, closes the database, and removes
// the temporary directory.  Callers that initialised higher-level subsystems
// (e.g. invertedindex, documents, symbols) must shut those down *before*
// calling TeardownBase.
func (e *Env) TeardownBase() {
	e.T.Helper()

	// 1. mpsc queue
	e.Mpsc.Stop()

	// 2. database
	e.DB.Close()

	// 3. temp directory
	os.RemoveAll(e.TempDir)
}
