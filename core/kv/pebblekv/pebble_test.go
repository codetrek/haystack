package pebblekv

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pebbledb "github.com/cockroachdb/pebble"
	"github.com/codetrek/haystack/core/kv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errBatchWriter is a mock pebbleBatchWriter that returns errors from every operation.
type errBatchWriter struct {
	err error
}

func (e *errBatchWriter) Set(key, value []byte, opts *pebbledb.WriteOptions) error {
	return e.err
}
func (e *errBatchWriter) Delete(key []byte, opts *pebbledb.WriteOptions) error {
	return e.err
}
func (e *errBatchWriter) DeleteRange(start, end []byte, opts *pebbledb.WriteOptions) error {
	return e.err
}
func (e *errBatchWriter) Commit(o *pebbledb.WriteOptions) error {
	return e.err
}
func (e *errBatchWriter) Reset() {}
func (e *errBatchWriter) Close() error {
	return nil
}

// TestOpenWithOptions_WALModes covers the WAL/sync option paths: the writeOpts
// selection (NoSync by default; Sync only when opted in and the WAL is on) and a
// full Put/Get + batch round-trip through each mode, exercising the batch's
// inherited commitOpts.
func TestOpenWithOptions_WALModes(t *testing.T) {
	cases := []struct {
		name     string
		opts     OpenOptions
		wantSync bool
	}{
		{"default_nosync", OpenOptions{CacheSize: 1 << 20}, false},
		{"sync", OpenOptions{CacheSize: 1 << 20, Sync: true}, true},
		{"disablewal", OpenOptions{CacheSize: 1 << 20, DisableWAL: true}, false},
		{"sync_ignored_under_disablewal", OpenOptions{CacheSize: 1 << 20, Sync: true, DisableWAL: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, err := OpenWithOptions(t.TempDir()+"/db", c.opts)
			assert.NoError(t, err)
			defer db.Close()

			// writeOpts selection (white-box; same package).
			pdb := db.(*PebbleDB)
			if c.wantSync {
				assert.Equal(t, pebbledb.Sync, pdb.writeOpts)
			} else {
				assert.Equal(t, pebbledb.NoSync, pdb.writeOpts)
			}

			// Single-op round-trip.
			assert.NoError(t, db.Put([]byte("k"), []byte("v")))
			got, err := db.Get([]byte("k"))
			assert.NoError(t, err)
			assert.Equal(t, []byte("v"), got)

			// Batch round-trip (Commit uses the inherited commitOpts).
			b := db.NewBatch(0)
			assert.NoError(t, b.Put([]byte("bk"), []byte("bv")))
			assert.NoError(t, b.Delete([]byte("k")))
			assert.NoError(t, b.Commit())

			got, err = db.Get([]byte("bk"))
			assert.NoError(t, err)
			assert.Equal(t, []byte("bv"), got)
			got, err = db.Get([]byte("k"))
			assert.NoError(t, err)
			assert.Nil(t, got)
		})
	}
}

func TestOpen_And_BasicOps(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	assert.NotNil(t, db)
	defer db.Close()

	// Put and Get
	assert.NoError(t, db.Put([]byte("key1"), []byte("value1")))
	val, err := db.Get([]byte("key1"))
	assert.NoError(t, err)
	assert.Equal(t, []byte("value1"), val)

	// Get non-existent key
	val, err = db.Get([]byte("nonexistent"))
	assert.NoError(t, err)
	assert.Nil(t, val)

	// Delete
	assert.NoError(t, db.Delete([]byte("key1")))
	val, err = db.Get([]byte("key1"))
	assert.NoError(t, err)
	assert.Nil(t, val)
}

func TestDB_IsClosed(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)

	assert.False(t, db.IsClosed())
	db.Close()
	assert.True(t, db.IsClosed())
}

func TestDB_CloseDouble(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	assert.NoError(t, db.Close())
	assert.Error(t, db.Close()) // second close should error
}

func TestDB_OpsAfterClose(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	db.Close()

	assert.Error(t, db.Put([]byte("k"), []byte("v")))
	_, err = db.Get([]byte("k"))
	assert.Error(t, err)
	assert.Error(t, db.Delete([]byte("k")))
}

func TestDB_GetIncrementalId(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	defer db.Close()

	id1, err := db.GetIncrementalId([]byte("counter"))
	assert.NoError(t, err)
	assert.Equal(t, 0, id1)

	id2, err := db.GetIncrementalId([]byte("counter"))
	assert.NoError(t, err)
	assert.Equal(t, 1, id2)

	id3, err := db.GetIncrementalId([]byte("counter"))
	assert.NoError(t, err)
	assert.Equal(t, 2, id3)
}

func TestDB_Scan(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	defer db.Close()

	db.Put([]byte("prefix:a"), []byte("1"))
	db.Put([]byte("prefix:b"), []byte("2"))
	db.Put([]byte("prefix:c"), []byte("3"))
	db.Put([]byte("other:x"), []byte("4"))

	count := 0
	err = db.Scan([]byte("prefix:"), func(key, value []byte) bool {
		count++
		return true
	})
	assert.NoError(t, err)
	assert.Equal(t, 3, count)
}

// TestDB_Scan_PrefixFollowedByFF guards the prefix-scan upper bound: every key
// that starts with the prefix must be scanned, including keys whose first byte
// after the prefix is 0xff. The old bound `append(prefix, 0xff)` excluded any
// key of the form prefix+0xff+... (e.g. an inverted-index keyword containing
// 0xff right after a search prefix), silently dropping it from Search results.
func TestDB_Scan_PrefixFollowedByFF(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	defer db.Close()

	prefix := []byte("p|")
	keys := [][]byte{
		[]byte("p|a"),
		{'p', '|', 0xff},
		{'p', '|', 0xff, 'x'},
		{'p', '|', 0xff, 0xff, 'z'},
	}
	for i, k := range keys {
		assert.NoError(t, db.Put(k, []byte{byte(i)}))
	}
	db.Put([]byte("q|other"), []byte("x")) // outside the prefix

	found := map[string]bool{}
	err = db.Scan(prefix, func(key, _ []byte) bool {
		found[string(append([]byte{}, key...))] = true
		return true
	})
	assert.NoError(t, err)
	assert.Equal(t, len(keys), len(found),
		"all keys with the prefix must be scanned, including those with 0xff right after the prefix")
}

// TestDB_DeletePrefix_FFByte mirrors the Scan guard for the delete path:
// DeletePrefix must remove keys whose first byte after the prefix is 0xff. The
// old end-bound append(prefix,0xFF) left them behind (orphan-undeletable).
func TestDB_DeletePrefix_FFByte(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	defer db.Close()

	keys := [][]byte{
		[]byte("p|a"),
		{'p', '|', 0xff},
		{'p', '|', 0xff, 'x'},
	}
	for i, k := range keys {
		assert.NoError(t, db.Put(k, []byte{byte(i)}))
	}
	db.Put([]byte("q|keep"), []byte("x")) // outside the prefix — must survive

	b := db.NewBatch(1000)
	assert.NoError(t, b.DeletePrefix([]byte("p|")))
	assert.NoError(t, b.Commit())

	remaining := 0
	_ = db.Scan([]byte("p|"), func(_, _ []byte) bool { remaining++; return true })
	assert.Equal(t, 0, remaining, "DeletePrefix must remove every p| key, including p|\\xff…")
	v, _ := db.Get([]byte("q|keep"))
	assert.Equal(t, []byte("x"), v, "DeletePrefix must not touch keys outside the prefix")
}

func TestDB_ScanRange(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	defer db.Close()

	db.Put([]byte("a"), []byte("1"))
	db.Put([]byte("b"), []byte("2"))
	db.Put([]byte("c"), []byte("3"))
	db.Put([]byte("d"), []byte("4"))

	count := 0
	err = db.ScanRange([]byte("b"), []byte("d"), func(key, value []byte) bool {
		count++
		return true
	})
	assert.NoError(t, err)
	assert.Equal(t, 2, count) // b, c (d is exclusive upper bound)
}

func TestDB_Scan_StopEarly(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	defer db.Close()

	db.Put([]byte("k:1"), []byte("1"))
	db.Put([]byte("k:2"), []byte("2"))
	db.Put([]byte("k:3"), []byte("3"))

	count := 0
	db.Scan([]byte("k:"), func(key, value []byte) bool {
		count++
		return count < 2 // stop after 2
	})
	assert.Equal(t, 2, count)
}

func TestDB_ScheduleCompact(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)

	// Add some data first
	for i := 0; i < 10; i++ {
		db.Put([]byte(fmt.Sprintf("compact:%d", i)), []byte("value"))
	}

	db.ScheduleCompact()
	// Wait for async compact to finish before closing
	time.Sleep(200 * time.Millisecond)
	db.Close()
}

func TestBatch_BasicOps(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	defer db.Close()

	batch := db.NewBatch(0)
	assert.NoError(t, batch.Put([]byte("bk1"), []byte("bv1")))
	assert.NoError(t, batch.Put([]byte("bk2"), []byte("bv2")))
	assert.NoError(t, batch.Commit())

	val, _ := db.Get([]byte("bk1"))
	assert.Equal(t, []byte("bv1"), val)
}

func TestBatch_Delete(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	defer db.Close()

	db.Put([]byte("dk1"), []byte("dv1"))

	batch := db.NewBatch(0)
	assert.NoError(t, batch.Delete([]byte("dk1")))
	assert.NoError(t, batch.Commit())

	val, _ := db.Get([]byte("dk1"))
	assert.Nil(t, val)
}

func TestBatch_DeleteRange(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	defer db.Close()

	db.Put([]byte("r:a"), []byte("1"))
	db.Put([]byte("r:b"), []byte("2"))
	db.Put([]byte("r:c"), []byte("3"))

	batch := db.NewBatch(0)
	assert.NoError(t, batch.DeleteRange([]byte("r:a"), []byte("r:c")))
	assert.NoError(t, batch.Commit())

	val, _ := db.Get([]byte("r:a"))
	assert.Nil(t, val)
	val, _ = db.Get([]byte("r:b"))
	assert.Nil(t, val)
	val, _ = db.Get([]byte("r:c"))
	assert.Equal(t, []byte("3"), val) // upper bound exclusive
}

func TestBatch_DeletePrefix(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	defer db.Close()

	db.Put([]byte("pfx:1"), []byte("a"))
	db.Put([]byte("pfx:2"), []byte("b"))
	db.Put([]byte("other"), []byte("c"))

	batch := db.NewBatch(0)
	assert.NoError(t, batch.DeletePrefix([]byte("pfx:")))
	assert.NoError(t, batch.Commit())

	val, _ := db.Get([]byte("pfx:1"))
	assert.Nil(t, val)
	val, _ = db.Get([]byte("other"))
	assert.Equal(t, []byte("c"), val)
}

func TestBatch_Reset(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	defer db.Close()

	batch := db.NewBatch(0)
	batch.Put([]byte("rk"), []byte("rv"))
	batch.Reset()
	assert.Equal(t, int32(0), batch.Count())

	batch.Commit() // commit empty
	val, _ := db.Get([]byte("rk"))
	assert.Nil(t, val) // was reset before commit
}

func TestBatch_AutoCommit(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	defer db.Close()

	batch := db.NewBatch(3) // auto-commit after 3 ops
	batch.Put([]byte("ac1"), []byte("v1"))
	batch.Put([]byte("ac2"), []byte("v2"))
	batch.Put([]byte("ac3"), []byte("v3")) // should trigger auto-commit

	// Data should be committed already
	val, _ := db.Get([]byte("ac1"))
	assert.Equal(t, []byte("v1"), val)
}

func TestBatch_Count(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	defer db.Close()

	batch := db.NewBatch(0)
	assert.Equal(t, int32(0), batch.Count())
	batch.Put([]byte("x"), []byte("y"))
	// Count only tracks for auto-commit when maxBatchSize > 0
	batch.Close()
}

func TestBatch_Close(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	defer db.Close()

	batch := db.NewBatch(0)
	assert.NoError(t, batch.Close())
}

func TestDB_ScanAfterClose(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	db.Close()

	err = db.Scan([]byte("k"), func(key, value []byte) bool { return true })
	assert.Error(t, err)
}

func TestDB_ScanRangeAfterClose(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	db.Close()

	err = db.ScanRange([]byte("a"), []byte("z"), func(key, value []byte) bool { return true })
	assert.Error(t, err)
}

func TestDB_GetIncrementalIdAfterClose(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	db.Close()

	_, err = db.GetIncrementalId([]byte("counter"))
	assert.Error(t, err)
}

func TestDB_NewBatchAndOps(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	defer db.Close()

	// Test batch with maxBatchSize > 0 but not enough ops to trigger
	batch := db.NewBatch(10)
	assert.NoError(t, batch.Put([]byte("k1"), []byte("v1")))
	assert.Equal(t, int32(1), batch.Count())
	assert.NoError(t, batch.Delete([]byte("k1")))
	assert.Equal(t, int32(2), batch.Count())
	assert.NoError(t, batch.Commit())
	assert.Equal(t, int32(0), batch.Count())
}

// TestBatch_DeletePrefix_CountAdvancesByOne guards against DeletePrefix
// double-counting against the auto-commit limit: each batch operation,
// including DeletePrefix, must advance Count() by exactly one.
func TestBatch_DeletePrefix_CountAdvancesByOne(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	defer db.Close()

	batch := db.NewBatch(100) // large limit so no auto-commit triggers
	assert.Equal(t, int32(0), batch.Count())

	assert.NoError(t, batch.DeletePrefix([]byte("pfx:")))
	assert.Equal(t, int32(1), batch.Count())

	assert.NoError(t, batch.DeletePrefix([]byte("pfx2:")))
	assert.Equal(t, int32(2), batch.Count())

	// Mix with other ops to confirm consistent per-op increment.
	assert.NoError(t, batch.Put([]byte("k"), []byte("v")))
	assert.Equal(t, int32(3), batch.Count())
	assert.NoError(t, batch.DeleteRange([]byte("a"), []byte("z")))
	assert.Equal(t, int32(4), batch.Count())
}

func TestBatch_PutError(t *testing.T) {
	injectedErr := errors.New("set failed")
	batch := &PebbleBatch{
		batch:        &errBatchWriter{err: injectedErr},
		maxBatchSize: 0,
		count:        atomic.Int32{},
	}

	err := batch.Put([]byte("k"), []byte("v"))
	assert.ErrorIs(t, err, injectedErr)
}

func TestBatch_DeleteError(t *testing.T) {
	injectedErr := errors.New("delete failed")
	batch := &PebbleBatch{
		batch:        &errBatchWriter{err: injectedErr},
		maxBatchSize: 0,
		count:        atomic.Int32{},
	}

	err := batch.Delete([]byte("k"))
	assert.ErrorIs(t, err, injectedErr)
}

func TestBatch_DeleteRangeError(t *testing.T) {
	injectedErr := errors.New("delete range failed")
	batch := &PebbleBatch{
		batch:        &errBatchWriter{err: injectedErr},
		maxBatchSize: 0,
		count:        atomic.Int32{},
	}

	err := batch.DeleteRange([]byte("a"), []byte("z"))
	assert.ErrorIs(t, err, injectedErr)
}

func TestBatch_DeletePrefixError(t *testing.T) {
	injectedErr := errors.New("delete range failed")
	batch := &PebbleBatch{
		batch:        &errBatchWriter{err: injectedErr},
		maxBatchSize: 0,
		count:        atomic.Int32{},
	}

	err := batch.DeletePrefix([]byte("pfx:"))
	assert.ErrorIs(t, err, injectedErr)
}

// errPebbleStore is a mock pebbleStore that returns errors from Set and Delete,
// enabling tests for the error paths in PebbleDB.Put and PebbleDB.Delete.
type errPebbleStore struct {
	err error
}

func (e *errPebbleStore) Set(key, value []byte, opts *pebbledb.WriteOptions) error {
	return e.err
}
func (e *errPebbleStore) Get(key []byte) ([]byte, io.Closer, error) {
	return nil, nil, e.err
}
func (e *errPebbleStore) Delete(key []byte, opts *pebbledb.WriteOptions) error {
	return e.err
}
func (e *errPebbleStore) NewBatch() *pebbledb.Batch { return nil }
func (e *errPebbleStore) NewIter(o *pebbledb.IterOptions) (*pebbledb.Iterator, error) {
	return nil, e.err
}

// NewSnapshot is never dereferenced on errPebbleStore's paths (no test drives
// PebbleDB.Snapshot through this fake); it exists only to satisfy pebbleStore.
func (e *errPebbleStore) NewSnapshot() *pebbledb.Snapshot                   { return nil }
func (e *errPebbleStore) Compact(start, end []byte, parallelize bool) error { return e.err }
func (e *errPebbleStore) Close() error                                      { return nil }

func TestDB_PutErrorFromUnderlying(t *testing.T) {
	injectedErr := errors.New("disk write failed")
	db := &PebbleDB{
		db: &errPebbleStore{err: injectedErr},
	}

	err := db.Put([]byte("key"), []byte("value"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to put data")
	assert.Contains(t, err.Error(), "disk write failed")
}

func TestDB_DeleteErrorFromUnderlying(t *testing.T) {
	injectedErr := errors.New("disk delete failed")
	db := &PebbleDB{
		db: &errPebbleStore{err: injectedErr},
	}

	err := db.Delete([]byte("key"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to delete data")
	assert.Contains(t, err.Error(), "disk delete failed")
}

func TestDB_ScanRange_StopEarly(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	defer db.Close()

	db.Put([]byte("a"), []byte("1"))
	db.Put([]byte("b"), []byte("2"))
	db.Put([]byte("c"), []byte("3"))

	count := 0
	db.ScanRange([]byte("a"), []byte("d"), func(key, value []byte) bool {
		count++
		return count < 2
	})
	assert.Equal(t, 2, count)
}

// ---------------------------------------------------------------------------
// kv.Snapshotter / kv.Snapshot (pebbleSnapshot) tests
// ---------------------------------------------------------------------------

// openSnapTestDB opens a fresh pebblekv store in a temp dir for the snapshot
// tests. It fails the test (not merely records) on error so callers never
// dereference a nil Store.
func openSnapTestDB(t *testing.T) kv.Store {
	t.Helper()
	db, err := Open(t.TempDir()+"/snapdb", 4*1024*1024)
	require.NoError(t, err)
	require.NotNil(t, db)
	return db
}

// mustSnapshot obtains a snapshot via the OPTIONAL kv.Snapshotter capability
// (type-asserted from kv.Store, exactly how a caller acquires one) and fails the
// test if the store does not implement it or Snapshot errors.
func mustSnapshot(t *testing.T, db kv.Store) kv.Snapshot {
	t.Helper()
	ss, ok := db.(kv.Snapshotter)
	require.True(t, ok, "pebblekv store must implement kv.Snapshotter")
	snap, err := ss.Snapshot()
	require.NoError(t, err)
	require.NotNil(t, snap)
	return snap
}

// errSnapReader implements pebbleSnapshotReader and returns the injected error
// from every method (Get/NewIter/Close). It lets the snapshot's read/close error
// branches be driven directly via pebbleSnapshot{snap: errSnapReader{err}} — the
// errPebbleStore fake cannot, because its Close returns nil. Value receivers so
// the field can be nil'd by pebbleSnapshot.Close without aliasing surprises.
type errSnapReader struct {
	err error
}

func (e errSnapReader) Get(key []byte) ([]byte, io.Closer, error) {
	return nil, nil, e.err
}
func (e errSnapReader) NewIter(o *pebbledb.IterOptions) (*pebbledb.Iterator, error) {
	return nil, e.err
}
func (e errSnapReader) Close() error {
	return e.err
}

// TestSnapshot_Consistency pins contract §4.1: reads through a snapshot observe
// exactly the state committed when Snapshot() returned. Writes committed after
// (overwrite, new key, delete) are invisible to it, while the live DB reflects
// them. A snapshot that read the live DB would fail every assertion here.
func TestSnapshot_Consistency(t *testing.T) {
	db := openSnapTestDB(t)
	defer db.Close()

	// Seed pre-snapshot state: k=v1 and an "old" key we will later delete.
	require.NoError(t, db.Put([]byte("k"), []byte("v1")))
	require.NoError(t, db.Put([]byte("old"), []byte("kept")))

	snap := mustSnapshot(t, db)
	defer snap.Close()

	// Mutate the DB AFTER the snapshot: overwrite k, add a new key, delete old.
	require.NoError(t, db.Put([]byte("k"), []byte("v2")))
	require.NoError(t, db.Put([]byte("new"), []byte("fresh")))
	require.NoError(t, db.Delete([]byte("old")))

	// The snapshot still sees the frozen state.
	got, err := snap.Get([]byte("k"))
	assert.NoError(t, err)
	assert.Equal(t, []byte("v1"), got, "snapshot must still read the pre-write value")

	got, err = snap.Get([]byte("new"))
	assert.NoError(t, err)
	assert.Nil(t, got, "snapshot must NOT see a key added after it was taken")

	got, err = snap.Get([]byte("old"))
	assert.NoError(t, err)
	assert.Equal(t, []byte("kept"), got, "snapshot must still see a key deleted after it was taken")

	// The live DB reflects the post-snapshot writes.
	got, err = db.Get([]byte("k"))
	assert.NoError(t, err)
	assert.Equal(t, []byte("v2"), got)
	got, err = db.Get([]byte("new"))
	assert.NoError(t, err)
	assert.Equal(t, []byte("fresh"), got)
	got, err = db.Get([]byte("old"))
	assert.NoError(t, err)
	assert.Nil(t, got)
}

// TestSnapshot_ScanIsolation pins contract §4.1 for Scan/ScanRange: after
// mutations under the prefix (overwrite, add, delete), the snapshot yields the
// EXACT original key/value set — not a count, so a wrong impl that saw the
// mutated value or the added/removed key is caught.
func TestSnapshot_ScanIsolation(t *testing.T) {
	db := openSnapTestDB(t)
	defer db.Close()

	original := map[string]string{
		"p:a": "1",
		"p:b": "2",
		"p:c": "3",
	}
	for k, v := range original {
		require.NoError(t, db.Put([]byte(k), []byte(v)))
	}

	snap := mustSnapshot(t, db)
	defer snap.Close()

	// Mutate under the prefix after the snapshot.
	require.NoError(t, db.Put([]byte("p:a"), []byte("changed"))) // overwrite
	require.NoError(t, db.Put([]byte("p:d"), []byte("4")))       // add
	require.NoError(t, db.Delete([]byte("p:b")))                 // delete

	gotScan := map[string]string{}
	err := snap.Scan([]byte("p:"), func(k, v []byte) bool {
		gotScan[string(k)] = string(v)
		return true
	})
	assert.NoError(t, err)
	assert.Equal(t, original, gotScan, "Scan through the snapshot must yield the frozen set")

	// ScanRange over [p:, p;) spans exactly the p: keys; must match likewise.
	gotRange := map[string]string{}
	err = snap.ScanRange([]byte("p:"), []byte("p;"), func(k, v []byte) bool {
		gotRange[string(k)] = string(v)
		return true
	})
	assert.NoError(t, err)
	assert.Equal(t, original, gotRange, "ScanRange through the snapshot must yield the frozen set")
}

// TestSnapshot_Scan_PrefixFollowedByFF mirrors TestDB_Scan_PrefixFollowedByFF
// through the snapshot path (contract §4.4 / R3): every key with the prefix must
// be scanned, including keys whose first byte after the prefix is 0xff. Asserts
// the EXACT key set (stronger than a count) so a regressed keyUpperBound bound in
// the shared scanPrefix helper is caught on the snapshot path too.
func TestSnapshot_Scan_PrefixFollowedByFF(t *testing.T) {
	db := openSnapTestDB(t)
	defer db.Close()

	prefix := []byte("p|")
	keys := [][]byte{
		[]byte("p|a"),
		{'p', '|', 0xff},
		{'p', '|', 0xff, 'x'},
		{'p', '|', 0xff, 0xff, 'z'},
	}
	for i, k := range keys {
		require.NoError(t, db.Put(k, []byte{byte(i)}))
	}
	require.NoError(t, db.Put([]byte("q|other"), []byte("x"))) // outside the prefix

	snap := mustSnapshot(t, db)
	defer snap.Close()

	want := map[string]bool{}
	for _, k := range keys {
		want[string(k)] = true
	}

	got := map[string]bool{}
	err := snap.Scan(prefix, func(k, _ []byte) bool {
		got[string(append([]byte{}, k...))] = true
		return true
	})
	assert.NoError(t, err)
	assert.Equal(t, want, got,
		"snapshot Scan must yield exactly the prefix keys, including those with 0xff right after the prefix")
}

// TestSnapshot_Get_CopyAndNotFound pins contract §4.3: snapshot Get returns a
// fresh copy (mutating it must not corrupt a subsequent read) and maps a missing
// key to (nil, nil).
func TestSnapshot_Get_CopyAndNotFound(t *testing.T) {
	db := openSnapTestDB(t)
	defer db.Close()

	require.NoError(t, db.Put([]byte("k"), []byte("orig")))

	snap := mustSnapshot(t, db)
	defer snap.Close()

	got, err := snap.Get([]byte("k"))
	assert.NoError(t, err)
	assert.Equal(t, []byte("orig"), got)

	// Mutating the returned slice must not affect a re-Get.
	for i := range got {
		got[i] = 'X'
	}
	again, err := snap.Get([]byte("k"))
	assert.NoError(t, err)
	assert.Equal(t, []byte("orig"), again, "Get must return a copy; mutating it must not corrupt the view")

	// Absent key → (nil, nil).
	absent, err := snap.Get([]byte("missing"))
	assert.NoError(t, err)
	assert.Nil(t, absent)
}

// TestSnapshot_Scan_StopEarly pins contract §4.4: returning false from the cb
// stops iteration for both Scan and ScanRange through the snapshot.
func TestSnapshot_Scan_StopEarly(t *testing.T) {
	db := openSnapTestDB(t)
	defer db.Close()

	for _, k := range []string{"k:1", "k:2", "k:3"} {
		require.NoError(t, db.Put([]byte(k), []byte("v")))
	}

	snap := mustSnapshot(t, db)
	defer snap.Close()

	count := 0
	err := snap.Scan([]byte("k:"), func(k, v []byte) bool {
		count++
		return count < 2 // stop after 2
	})
	assert.NoError(t, err)
	assert.Equal(t, 2, count)

	rcount := 0
	err = snap.ScanRange([]byte("k:1"), []byte("k:9"), func(k, v []byte) bool {
		rcount++
		return rcount < 2 // stop after 2
	})
	assert.NoError(t, err)
	assert.Equal(t, 2, rcount)
}

// TestSnapshot_ConcurrentReads pins contract §4.2(a)+(b): a single snapshot is
// read by N goroutines (Get + Scan) WHILE other goroutines write the DB
// (Put/Delete/add). Every read must observe the frozen state, and the run must
// be race-clean under -race. A wrong impl (shared iterator, or reading the live
// DB) either races or observes a mutated value.
func TestSnapshot_ConcurrentReads(t *testing.T) {
	db := openSnapTestDB(t)
	defer db.Close()

	const nKeys = 50
	frozen := map[string]string{}
	for i := 0; i < nKeys; i++ {
		k := fmt.Sprintf("p:%03d", i)
		v := fmt.Sprintf("v%03d", i)
		frozen[k] = v
		require.NoError(t, db.Put([]byte(k), []byte(v)))
	}
	require.NoError(t, db.Put([]byte("solo"), []byte("frozen-solo")))

	snap := mustSnapshot(t, db)
	defer snap.Close()

	// Writers churn the DB under the same prefix + a growing "extra:" set.
	stop := make(chan struct{})
	var writers sync.WaitGroup
	for w := 0; w < 4; w++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				_ = db.Put([]byte(fmt.Sprintf("p:%03d", i%nKeys)), []byte("MUTATED"))
				_ = db.Put([]byte("solo"), []byte("MUTATED"))
				_ = db.Delete([]byte(fmt.Sprintf("p:%03d", (i+1)%nKeys)))
				_ = db.Put([]byte(fmt.Sprintf("extra:%d", i)), []byte("x"))
			}
		}()
	}

	// Readers repeatedly read the snapshot; each read must match the frozen set.
	var readers sync.WaitGroup
	errCh := make(chan error, 32)
	for r := 0; r < 4; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for iter := 0; iter < 200; iter++ {
				got, err := snap.Get([]byte("solo"))
				if err != nil {
					errCh <- fmt.Errorf("snapshot Get: %w", err)
					return
				}
				if string(got) != "frozen-solo" {
					errCh <- fmt.Errorf("snapshot Get(solo)=%q, want frozen-solo", got)
					return
				}
				seen := map[string]string{}
				if err := snap.Scan([]byte("p:"), func(k, v []byte) bool {
					seen[string(k)] = string(v)
					return true
				}); err != nil {
					errCh <- fmt.Errorf("snapshot Scan: %w", err)
					return
				}
				if !reflect.DeepEqual(seen, frozen) {
					errCh <- fmt.Errorf("snapshot Scan saw a mutated set (%d keys)", len(seen))
					return
				}
			}
		}()
	}

	readers.Wait()
	close(stop)
	writers.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

// TestSnapshot_CloseBeforeDBClose pins contract §4.7 (positive): with the
// snapshot Closed first, the parent DB.Close returns nil.
func TestSnapshot_CloseBeforeDBClose(t *testing.T) {
	db := openSnapTestDB(t)
	require.NoError(t, db.Put([]byte("k"), []byte("v")))

	snap := mustSnapshot(t, db)
	assert.NoError(t, snap.Close())

	assert.NoError(t, db.Close(), "DB.Close must succeed once the snapshot is closed")
}

// TestSnapshot_OpenSnapshotBlocksDBClose pins contract §4.7 / R2 (negative):
// closing the DB while a snapshot is still open returns a non-nil error. It
// wraps pebble's "leaked snapshots" error; asserting on that substring is an
// INTENTIONAL, documented coupling to the pebble behavior the ordering contract
// rests on, so a pebble upgrade that silently weakened it is caught.
func TestSnapshot_OpenSnapshotBlocksDBClose(t *testing.T) {
	db := openSnapTestDB(t)
	require.NoError(t, db.Put([]byte("k"), []byte("v")))

	snap := mustSnapshot(t, db)

	err := db.Close()
	assert.Error(t, err, "DB.Close with an open snapshot must return an error")
	assert.Contains(t, err.Error(), "leaked",
		"DB.Close with an open snapshot must surface pebble's leaked-snapshots error (documented coupling)")

	// Release the snapshot to clean up.
	assert.NoError(t, snap.Close())
}

// TestSnapshot_CloseIdempotent pins contract §4.6: a second Close is a safe
// no-op returning nil and never panics (defends the defer+explicit-Close
// pattern from pebble's double-close panic).
func TestSnapshot_CloseIdempotent(t *testing.T) {
	db := openSnapTestDB(t)
	defer db.Close()

	snap := mustSnapshot(t, db)

	assert.NoError(t, snap.Close())
	assert.NotPanics(t, func() {
		assert.NoError(t, snap.Close(), "second Close must be a no-op returning nil")
	})
}

// TestSnapshot_OnClosedStore pins contract §4.8: Snapshot() on a closed store
// returns a non-nil error AND a LITERAL-nil kv.Snapshot (not a typed-nil
// *pebbleSnapshot, which would defeat a caller's sn == nil check).
func TestSnapshot_OnClosedStore(t *testing.T) {
	db := openSnapTestDB(t)
	require.NoError(t, db.Close())

	ss, ok := db.(kv.Snapshotter)
	require.True(t, ok)

	snap, err := ss.Snapshot()
	assert.Error(t, err)
	assert.True(t, snap == nil, "Snapshot on a closed store must return a literal-nil kv.Snapshot, not a typed nil")
}

// ---------------------------------------------------------------------------
// T4: underlying-error propagation via the errSnapReader fake. These drive the
// error branches of getCopy/scanPrefix/scanRange and pebbleSnapshot.Close that a
// real *pebble.Snapshot cannot be made to hit, closing the deferred go-cov
// CRITICAL markers.
// ---------------------------------------------------------------------------

// TestSnapshot_GetError: a snapshot Get propagates the reader's Get error
// (getCopy's non-NotFound branch: "failed to get data").
func TestSnapshot_GetError(t *testing.T) {
	injectedErr := errors.New("snapshot get failed")
	s := &pebbleSnapshot{snap: errSnapReader{err: injectedErr}}

	_, err := s.Get([]byte("k"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get data")
	assert.Contains(t, err.Error(), "snapshot get failed")
}

// TestSnapshot_ScanError: a snapshot Scan propagates the reader's NewIter error
// (scanPrefix's "failed to create iterator" branch).
func TestSnapshot_ScanError(t *testing.T) {
	injectedErr := errors.New("snapshot newiter failed")
	s := &pebbleSnapshot{snap: errSnapReader{err: injectedErr}}

	err := s.Scan([]byte("p:"), func(k, v []byte) bool { return true })
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create iterator")
	assert.Contains(t, err.Error(), "snapshot newiter failed")
}

// TestSnapshot_ScanRangeError: a snapshot ScanRange propagates the reader's
// NewIter error (scanRange's "failed to create iterator" branch).
func TestSnapshot_ScanRangeError(t *testing.T) {
	injectedErr := errors.New("snapshot newiter failed")
	s := &pebbleSnapshot{snap: errSnapReader{err: injectedErr}}

	err := s.ScanRange([]byte("a"), []byte("z"), func(k, v []byte) bool { return true })
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create iterator")
	assert.Contains(t, err.Error(), "snapshot newiter failed")
}

// TestSnapshot_CloseError: the FIRST Close propagates the reader's Close error
// (pebbleSnapshot.Close returns the captured reader's Close result verbatim).
func TestSnapshot_CloseError(t *testing.T) {
	injectedErr := errors.New("snapshot close failed")
	s := &pebbleSnapshot{snap: errSnapReader{err: injectedErr}}

	err := s.Close()
	assert.ErrorIs(t, err, injectedErr)

	// Idempotency still holds after an erroring Close: the reader was nil'd
	// unconditionally, so a second Close is a no-op returning nil.
	assert.NoError(t, s.Close())
}
