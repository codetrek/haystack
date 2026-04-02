package documents

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/codetrek/haystack/conf"
	"github.com/codetrek/haystack/server/core/invertedindex"
	"github.com/codetrek/haystack/server/core/pebble"
	"github.com/codetrek/haystack/server/core/storage"
	"github.com/codetrek/haystack/utils/queue"
)

// testEnv holds all resources created during test setup so they can
// be torn down cleanly in reverse order.
type testEnv struct {
	t       *testing.T
	tempDir string
	db      pebble.DB
	mpsc    *queue.Mpsc
}

// setupTestEnv creates a temporary Pebble database, starts an MPSC queue,
// and initialises both invertedindex and documents packages.
// Call the returned cleanup function (or env.teardown) in a defer.
func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "haystack-doc-test-*")
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

	q := queue.NewMpsc("TestDocQueue")
	q.Start()

	// Init inverted index first (documents.Create depends on it).
	if err := invertedindex.Init(database, q); err != nil {
		q.Stop()
		database.Close()
		os.RemoveAll(tempDir)
		t.Fatalf("failed to init inverted index: %v", err)
	}

	// Init documents package – sets the package-level globals.
	if err := Init(database, q); err != nil {
		invertedindex.CloseAndWait()
		q.Stop()
		database.Close()
		os.RemoveAll(tempDir)
		t.Fatalf("failed to init documents: %v", err)
	}

	return &testEnv{
		t:       t,
		tempDir: tempDir,
		db:      database,
		mpsc:    q,
	}
}

// teardown shuts down everything in reverse init order:
//
//	documents -> invertedindex -> mpsc queue -> pebble db -> temp dir
func (e *testEnv) teardown() {
	e.t.Helper()

	// 1. documents package
	CloseAndWait()

	// 2. inverted index
	invertedindex.CloseAndWait()

	// 3. mpsc queue
	e.mpsc.Stop()

	// 4. database
	e.db.Close()

	// 5. temp directory
	os.RemoveAll(e.tempDir)
}

// mustCreateWorkspace creates a workspace via Create() and fails the test on error.
func mustCreateWorkspace(t *testing.T, workspaceId int) {
	t.Helper()
	if err := Create(workspaceId, "test-workspace"); err != nil {
		t.Fatalf("failed to create workspace %d: %v", workspaceId, err)
	}
}
