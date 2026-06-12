package documents

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/codetrek/haystack/searchcore/invertedindex"
	"github.com/codetrek/haystack/searchcore/kv"
	"github.com/codetrek/haystack/searchcore/kv/pebblekv"
	"github.com/codetrek/haystack/searchcore/queue"
)

// testEnv holds all resources created during test setup so they can
// be torn down cleanly in reverse order.
type testEnv struct {
	T       *testing.T
	TempDir string
	DB      kv.Store
	Mpsc    *queue.Mpsc
	St      *Store
	idx     *invertedindex.Index
}

// setupTestEnv creates a temporary Pebble database, starts an MPSC queue,
// and creates both invertedindex and documents instances.
// Call env.teardown() in a defer.
func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "haystack-documents-test-*")
	if err != nil {
		t.Fatalf("setupTestEnv: failed to create temp dir: %v", err)
	}

	database, err := pebblekv.Open(filepath.Join(tempDir, "data"), 0)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("setupTestEnv: failed to open pebble db: %v", err)
	}

	q := queue.NewMpsc("TestDocQueue")
	q.Start()

	// Init inverted index first (documents.Create depends on it).
	idx, err := invertedindex.New(database, q, invertedindex.Options{})
	if err != nil {
		q.Stop()
		database.Close()
		os.RemoveAll(tempDir)
		t.Fatalf("failed to init inverted index: %v", err)
	}

	// Create documents Store instance.
	st, err := New(database, q, idx, Options{})
	if err != nil {
		idx.CloseAndWait()
		q.Stop()
		database.Close()
		os.RemoveAll(tempDir)
		t.Fatalf("failed to create documents store: %v", err)
	}

	return &testEnv{
		T:       t,
		TempDir: tempDir,
		DB:      database,
		Mpsc:    q,
		St:      st,
		idx:     idx,
	}
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

	// 3. mpsc queue
	e.Mpsc.Stop()

	// 4. database
	e.DB.Close()

	// 5. temp directory
	os.RemoveAll(e.TempDir)
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
