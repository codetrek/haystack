package pebblekv

import (
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	pebbledb "github.com/cockroachdb/pebble"
	"github.com/stretchr/testify/assert"
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
