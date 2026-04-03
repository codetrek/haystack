package documents

import (
	"testing"

	"github.com/codetrek/haystack/internal/testutil"
	"github.com/codetrek/haystack/server/core/invertedindex"
)

// testEnv holds all resources created during test setup so they can
// be torn down cleanly in reverse order.
type testEnv struct {
	*testutil.Env
}

// setupTestEnv creates a temporary Pebble database, starts an MPSC queue,
// and initialises both invertedindex and documents packages.
// Call the returned cleanup function (or env.teardown) in a defer.
func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	env := testutil.SetupEnv(t, "TestDocQueue")

	// Init inverted index first (documents.Create depends on it).
	if err := invertedindex.Init(env.DB, env.Mpsc); err != nil {
		env.TeardownBase()
		t.Fatalf("failed to init inverted index: %v", err)
	}

	// Init documents package – sets the package-level globals.
	if err := Init(env.DB, env.Mpsc); err != nil {
		invertedindex.CloseAndWait()
		env.TeardownBase()
		t.Fatalf("failed to init documents: %v", err)
	}

	return &testEnv{Env: env}
}

// teardown shuts down everything in reverse init order:
//
//	documents -> invertedindex -> mpsc queue -> pebble db -> temp dir
func (e *testEnv) teardown() {
	e.T.Helper()

	// 1. documents package
	CloseAndWait()

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
