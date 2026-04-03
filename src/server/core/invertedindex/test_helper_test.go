package invertedindex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/codetrek/haystack/conf"
	"github.com/codetrek/haystack/server/core/pebble"
	"github.com/codetrek/haystack/server/core/storage"
	"github.com/codetrek/haystack/utils/queue"
)

// testEnv holds all resources created during test setup so they can
// be torn down cleanly in reverse order.
type testEnv struct {
	t       *testing.T
	tempDir string
	DB      pebble.DB
	mpsc    *queue.Mpsc
}

// setupTestEnv creates a temporary Pebble database, starts an MPSC queue,
// and initialises the invertedindex package.
// Call env.teardown() in a defer.
func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "haystack-inverted-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	// Point conf at our temp dir so storage.Open writes there.
	conf.Get().Global.DataPath = tempDir

	database, err := storage.Open(filepath.Join(tempDir, "data"), 0)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("failed to open storage: %v", err)
	}

	q := queue.NewMpsc("TestInvertedQueue")
	q.Start()

	if err := Init(database, q); err != nil {
		q.Stop()
		database.Close()
		os.RemoveAll(tempDir)
		t.Fatalf("failed to init inverted index: %v", err)
	}

	return &testEnv{
		t:       t,
		tempDir: tempDir,
		DB:      database,
		mpsc:    q,
	}
}

// teardown shuts down everything in reverse init order.
func (e *testEnv) teardown() {
	e.t.Helper()

	// 1. inverted index
	CloseAndWait()

	// 2. mpsc queue
	e.mpsc.Stop()

	// 3. database
	e.DB.Close()

	// 4. temp directory
	os.RemoveAll(e.tempDir)
}
