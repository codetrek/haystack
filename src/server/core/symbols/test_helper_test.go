package symbols

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
// and initialises both invertedindex and symbols packages.
// Call env.teardown() in a defer.
func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "haystack-sym-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	// Point conf at our temp dir so storage.Open writes there.
	conf.Get().Global.DataPath = tempDir

	// Ensure the symbols feature flag is enabled for tests.
	conf.Get().Symbols.EnableFeature = true

	database, err := storage.Open(filepath.Join(tempDir, "data"), 0)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("failed to open storage: %v", err)
	}

	q := queue.NewMpsc("TestSymQueue")
	q.Start()

	// Init inverted index first (symbols.Create depends on it).
	if err := invertedindex.Init(database, q); err != nil {
		q.Stop()
		database.Close()
		os.RemoveAll(tempDir)
		t.Fatalf("failed to init inverted index: %v", err)
	}

	// Init symbols package -- sets the package-level globals.
	if err := Init(database, q); err != nil {
		invertedindex.CloseAndWait()
		q.Stop()
		database.Close()
		os.RemoveAll(tempDir)
		t.Fatalf("failed to init symbols: %v", err)
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
//	symbols -> invertedindex -> mpsc queue -> pebble db -> temp dir
func (e *testEnv) teardown() {
	e.t.Helper()

	// 1. symbols package
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
