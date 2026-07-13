package invertedindex

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/codetrek/haystack/packages/core/kv"
	"github.com/codetrek/haystack/packages/core/kv/pebblekv"
	"github.com/codetrek/haystack/packages/core/queue"
)

// testEnv holds all resources created during test setup so they can
// be torn down cleanly in reverse order.
type testEnv struct {
	T       *testing.T
	TempDir string
	DB      kv.Store
	Mpsc    *queue.Mpsc
	idx     *Index
}

// setupTestEnv creates a temporary Pebble database, starts an MPSC queue,
// and initialises the invertedindex package.
// Call env.teardown() in a defer.
func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "haystack-invertedindex-test-*")
	if err != nil {
		t.Fatalf("setupTestEnv: failed to create temp dir: %v", err)
	}

	database, err := pebblekv.Open(filepath.Join(tempDir, "data"), 0)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("setupTestEnv: failed to open pebble db: %v", err)
	}

	q := queue.NewMpsc("TestInvertedQueue")
	q.Start()

	// Reset the test-injection seams to their defaults.
	newBatch = func(db kv.Store) kv.Batch {
		return db.NewBatch(MaxBatchSize)
	}
	writeInvertedIndex = defaultWriteInvertedIndex

	opts := Options{}
	idx, err := New(database, q, opts)
	if err != nil {
		q.Stop()
		database.Close()
		os.RemoveAll(tempDir)
		t.Fatalf("failed to init inverted index: %v", err)
	}

	return &testEnv{
		T:       t,
		TempDir: tempDir,
		DB:      database,
		Mpsc:    q,
		idx:     idx,
	}
}

// teardown shuts down everything in reverse init order.
func (e *testEnv) teardown() {
	e.T.Helper()

	// 1. inverted index
	e.idx.CloseAndWait()

	// 2. mpsc queue
	e.Mpsc.Stop()

	// 3. database
	e.DB.Close()

	// 4. temp directory
	os.RemoveAll(e.TempDir)
}

// closedDB implements kv.Store but always returns errors.
// This lets tests exercise error paths (e.g. db.GetIncrementalId failure)
// without actually closing the underlying database.
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

// simulateClosedDB replaces the index db with a closedDB stub and
// returns a restore function that puts the original db back.
func simulateClosedDB(idx *Index) (restore func()) {
	orig := idx.db
	idx.db = closedDB{}
	return func() { idx.db = orig }
}
