package documents

import (
	"fmt"
	"testing"

	"github.com/codetrek/haystack/internal/testutil"
	"github.com/codetrek/haystack/searchcore/invertedindex"
	"github.com/codetrek/haystack/searchcore/kv"
)

// testEnv holds all resources created during test setup so they can
// be torn down cleanly in reverse order.
type testEnv struct {
	*testutil.Env
	St  *Store
	idx *invertedindex.Index
}

// setupTestEnv creates a temporary Pebble database, starts an MPSC queue,
// and creates both invertedindex and documents instances.
// Call the returned cleanup function (or env.teardown) in a defer.
func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	env := testutil.SetupEnv(t, "TestDocQueue")

	// Init inverted index first (documents.Create depends on it).
	idx, err := invertedindex.New(env.DB, env.Mpsc, invertedindex.Options{})
	if err != nil {
		env.TeardownBase()
		t.Fatalf("failed to init inverted index: %v", err)
	}

	// Create documents Store instance.
	st, err := New(env.DB, env.Mpsc, idx, Options{})
	if err != nil {
		idx.CloseAndWait()
		env.TeardownBase()
		t.Fatalf("failed to create documents store: %v", err)
	}

	return &testEnv{Env: env, St: st, idx: idx}
}

// teardown shuts down everything in reverse init order:
//
//	documents -> invertedindex -> mpsc queue -> pebble db -> temp dir
func (e *testEnv) teardown() {
	e.T.Helper()

	// 1. documents store
	e.St.CloseAndWait()

	// 2. inverted index
	if e.idx != nil {
		e.idx.CloseAndWait()
	}

	// 3. base resources (queue → db → temp dir)
	e.TeardownBase()
}

// mustCreateWorkspace creates a workspace via st.Create() and fails the test on error.
func mustCreateWorkspace(t *testing.T, st *Store, workspaceId int) {
	t.Helper()
	if err := st.Create(workspaceId, "test-workspace"); err != nil {
		t.Fatalf("failed to create workspace %d: %v", workspaceId, err)
	}
}

// closedDB implements kv.Store but always reports itself as closed.
// This lets tests exercise the db.IsClosed() early-return paths without
// actually closing the underlying Pebble database (which would crash
// background goroutines like the inverted-index flusher).
type closedDB struct{}

func (closedDB) GetIncrementalId([]byte) (int, error)         { return 0, fmt.Errorf("closed") }
func (closedDB) ScheduleCompact()                             {}
func (closedDB) Close() error                                 { return fmt.Errorf("closed") }
func (closedDB) IsClosed() bool                               { return true }
func (closedDB) Put(key, value []byte) error                  { return fmt.Errorf("closed") }
func (closedDB) Get(key []byte) ([]byte, error)               { return nil, fmt.Errorf("closed") }
func (closedDB) Delete(key []byte) error                      { return fmt.Errorf("closed") }
func (closedDB) NewBatch(maxBatchSize int32) kv.Batch         { return nil }
func (closedDB) Scan([]byte, func([]byte, []byte) bool) error { return fmt.Errorf("closed") }
func (closedDB) ScanRange([]byte, []byte, func([]byte, []byte) bool) error {
	return fmt.Errorf("closed")
}

// simulateClosedDB replaces the Store's db with a closedDB stub and returns
// a restore function that puts the original db back. Call the restore function
// in a defer (before teardown) so cleanup works normally.
func simulateClosedDB(st *Store) (restore func()) {
	orig := st.db
	st.db = closedDB{}
	return func() { st.db = orig }
}
