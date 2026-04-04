package invertedindex

import (
	"fmt"
	"testing"
	"time"

	"github.com/codetrek/haystack/internal/testutil"
	"github.com/codetrek/haystack/server/core/pebble"
)

// testEnv holds all resources created during test setup so they can
// be torn down cleanly in reverse order.
type testEnv struct {
	*testutil.Env
}

// setupTestEnv creates a temporary Pebble database, starts an MPSC queue,
// and initialises the invertedindex package.
// Call env.teardown() in a defer.
func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	env := testutil.SetupEnv(t, "TestInvertedQueue")

	// Reset all package-level state to ensure test isolation.
	// This prevents leakage from tests that mock these globals
	// (e.g. keywords_merger_test mocking NewBatch, writeInvertedIndex).
	pendingWrites = map[int]*PendingTableWrites{}
	pendingDeletes = map[int]*PendingTableWrites{}
	lastFlushWriteTime = time.Now()
	lastFlushDeleteTime = time.Now()
	NewBatch = func(database pebble.DB) pebble.Batch {
		return database.NewBatch(MaxBatchSize)
	}

	if err := Init(env.DB, env.Mpsc); err != nil {
		env.TeardownBase()
		t.Fatalf("failed to init inverted index: %v", err)
	}

	return &testEnv{Env: env}
}

// teardown shuts down everything in reverse init order.
func (e *testEnv) teardown() {
	e.T.Helper()

	// 1. inverted index
	CloseAndWait()

	// 2. base resources (queue → db → temp dir)
	e.TeardownBase()
}

// closedDB implements pebble.DB but always returns errors.
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
func (closedDB) NewBatch(maxBatchSize int32) pebble.Batch     { return nil }
func (closedDB) Scan([]byte, func([]byte, []byte) bool) error { return fmt.Errorf("closed") }
func (closedDB) ScanRange([]byte, []byte, func([]byte, []byte) bool) error {
	return fmt.Errorf("closed")
}

// simulateClosedDB replaces the package-level db with a closedDB stub and
// returns a restore function that puts the original db back.
func simulateClosedDB() (restore func()) {
	orig := db
	db = closedDB{}
	return func() { db = orig }
}
