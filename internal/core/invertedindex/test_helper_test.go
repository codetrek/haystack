package invertedindex

import (
	"fmt"
	"testing"

	"github.com/codetrek/haystack/internal/testutil"
	"github.com/codetrek/haystack/searchcore/kv"
)

// testEnv holds all resources created during test setup so they can
// be torn down cleanly in reverse order.
type testEnv struct {
	*testutil.Env
	idx *Index
}

// setupTestEnv creates a temporary Pebble database, starts an MPSC queue,
// and initialises the invertedindex package.
// Call env.teardown() in a defer.
func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	env := testutil.SetupEnv(t, "TestInvertedQueue")

	// Reset the test-injection seams to their defaults.
	NewBatch = func(database kv.Store) kv.Batch {
		return database.NewBatch(MaxBatchSize)
	}
	writeInvertedIndex = func(batch kv.Batch, tableId int, kw string, docids []string, key []byte) {
		uniqueDocids := removeDuplicatesEfficiently(docids)
		content := encodeInvertedValue(uniqueDocids)
		if len(key) == 0 {
			key = encodeInvertedKey(tableId, kw, len(docids))
		}
		batch.Put(key, content)
	}

	opts := Options{
		FlushTicker:        FlushTicker,
		FlushWaitTimeout:   FlushWaitTimeout,
		FlushWaitBatchSize: FlushWaitBatchSize,
		FlushCooldown:      FlushCooldown,
	}
	idx, err := New(env.DB, env.Mpsc, opts)
	if err != nil {
		env.TeardownBase()
		t.Fatalf("failed to init inverted index: %v", err)
	}

	// Also set the legacy singleton so any code that still calls the package-level
	// helpers (e.g. tests that directly call flushPendingWrites via forceFlush) works.
	_legacyIdx = idx

	return &testEnv{Env: env, idx: idx}
}

// teardown shuts down everything in reverse init order.
func (e *testEnv) teardown() {
	e.T.Helper()

	// 1. inverted index
	e.idx.CloseAndWait()
	_legacyIdx = nil

	// 2. base resources (queue → db → temp dir)
	e.TeardownBase()
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
func simulateClosedDB() (restore func()) {
	// Operates on _legacyIdx.db (which is the same as env.idx.db in tests).
	orig := _legacyIdx.db
	_legacyIdx.db = closedDB{}
	return func() { _legacyIdx.db = orig }
}
