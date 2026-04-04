package symbols

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/internal/core/invertedindex"
	"github.com/codetrek/haystack/internal/core/pebble"
	"github.com/codetrek/haystack/internal/testutil"
	"github.com/codetrek/haystack/internal/utils/queue"
)

// testEnv holds all resources created during test setup so they can
// be torn down cleanly in reverse order.
type testEnv struct {
	*testutil.Env
}

// setupTestEnv creates a temporary Pebble database, starts an MPSC queue,
// and initialises both invertedindex and symbols packages.
// Call env.teardown() in a defer.
func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	env := testutil.SetupEnv(t, "TestSymQueue")

	// Ensure the symbols feature flag is enabled for tests.
	conf.Get().Symbols.EnableFeature = true

	// Init inverted index first (symbols.Create depends on it).
	if err := invertedindex.Init(env.DB, env.Mpsc); err != nil {
		env.TeardownBase()
		t.Fatalf("failed to init inverted index: %v", err)
	}

	// Init symbols package -- sets the package-level globals.
	if err := Init(env.DB, env.Mpsc); err != nil {
		invertedindex.CloseAndWait()
		env.TeardownBase()
		t.Fatalf("failed to init symbols: %v", err)
	}

	return &testEnv{Env: env}
}

// teardown shuts down everything in reverse init order:
//
//	symbols -> invertedindex -> mpsc queue -> pebble db -> temp dir
func (e *testEnv) teardown() {
	e.T.Helper()

	// 1. symbols package
	if mpsc != nil {
		CloseAndWait()
	}

	// 2. inverted index
	invertedindex.CloseAndWait()

	// 3. base resources (queue → db → temp dir)
	e.TeardownBase()
}

// mustCreateWorkspace creates a workspace via Create() and fails the test on error.
func mustCreateWorkspace(t *testing.T, workspaceId int) {
	t.Helper()
	if err := Create(workspaceId, "test-workspace"); err != nil {
		t.Fatalf("failed to create workspace %d: %v", workspaceId, err)
	}
}

// setupClosedDbEnv properly tears down the full test environment, then sets
// the package-level db to a freshly-opened-and-closed database and mpsc to a
// running queue, so that callers can exercise the db.IsClosed() early-return
// paths without causing panics in background goroutines.
// Returns a cleanup function that must be deferred.
func setupClosedDbEnv(t *testing.T) func() {
	t.Helper()

	// Ensure the symbols feature flag is enabled.
	conf.Get().Symbols.EnableFeature = true

	tmpDir, err := os.MkdirTemp("", "haystack-closed-db-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	closedDB, err := pebble.OpenDB(filepath.Join(tmpDir, "closed"), 0)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to open pebble for closed-db test: %v", err)
	}
	// Immediately close it so IsClosed() returns true.
	closedDB.Close()

	q := queue.NewMpsc("TestClosedDbQueue")
	q.Start()

	// Install into package globals.
	db = closedDB
	mpsc = q

	return func() {
		db = nil
		mpsc = nil
		q.Stop()
		os.RemoveAll(tmpDir)
	}
}
