package symbols

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetrek/haystack/core/invertedindex"
	"github.com/codetrek/haystack/core/kv"
	"github.com/codetrek/haystack/core/kv/pebblekv"
	"github.com/codetrek/haystack/core/queue"
	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/internal/core/storage"
	"github.com/codetrek/haystack/internal/testutil"
)

// testEnv holds all resources created during test setup so they can
// be torn down cleanly in reverse order.
type testEnv struct {
	*testutil.Env
	idx     invertedindex.Indexer
	indexdb kv.Store
}

// setupTestEnv creates a temporary Pebble database, starts an MPSC queue,
// and initialises both the inverted index and symbols packages.
// Call env.teardown() in a defer.
func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	env := testutil.SetupEnv(t, "TestSymQueue")

	// Ensure the symbols feature flag is enabled for tests.
	conf.Get().Symbols.EnableFeature = true

	// Open a dedicated pebble index store (the live server keeps the inverted
	// index in its own `index` store, separate from the `data` store).
	indexdb, err := storage.Open(filepath.Join(env.TempDir, "index"), 0)
	if err != nil {
		env.TeardownBase()
		t.Fatalf("failed to open index storage: %v", err)
	}

	// Init inverted index first (symbols.Create depends on it). Wrap the
	// pebble-backed *Index in the adapter so the test exercises the SAME live
	// backend the production server wires (invertedindex.New + NewIndexerAdapter).
	// Fast-flush options so posting writes reach pebble promptly — the pebble
	// GetDocs/Search read only flushed rows, so the deadlock tests poll for the
	// posting to land (see waitForDocPosting) rather than block on the 1s default.
	index, err := invertedindex.New(indexdb, env.Mpsc, invertedindex.Options{
		FlushTicker:              20 * time.Millisecond,
		FlushWaitTimeout:         1 * time.Microsecond,
		FlushWaitBatchSize:       1,
		FlushDeleteWaitTimeout:   1 * time.Microsecond,
		FlushDeleteWaitBatchSize: 1,
		FlushCooldown:            20 * time.Millisecond,
	})
	if err != nil {
		indexdb.Close()
		env.TeardownBase()
		t.Fatalf("failed to init inverted index: %v", err)
	}
	idx := invertedindex.NewIndexerAdapter(index)

	// Init symbols package -- sets the package-level globals.
	if err := Init(env.DB, env.Mpsc, idx); err != nil {
		idx.CloseAndWait()
		indexdb.Close()
		env.TeardownBase()
		t.Fatalf("failed to init symbols: %v", err)
	}

	return &testEnv{Env: env, idx: idx, indexdb: indexdb}
}

// teardown shuts down everything in reverse init order:
//
//	symbols -> inverted index -> index store -> mpsc queue -> pebble db -> temp dir
func (e *testEnv) teardown() {
	e.T.Helper()

	// 1. symbols package
	if mpsc != nil {
		CloseAndWait()
	}

	// 2. inverted index
	e.idx.CloseAndWait()

	// 3. index store
	e.indexdb.Close()

	// 4. base resources (queue → db → temp dir)
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

	closedDB, err := pebblekv.Open(filepath.Join(tmpDir, "closed"), 0)
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
