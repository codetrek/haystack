package pebble

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestOpenDB_And_BasicOps(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := OpenDB(tmpDir+"/testdb", 4*1024*1024)
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
	db, err := OpenDB(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)

	assert.False(t, db.IsClosed())
	db.Close()
	assert.True(t, db.IsClosed())
}

func TestDB_CloseDouble(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := OpenDB(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	assert.NoError(t, db.Close())
	assert.Error(t, db.Close()) // second close should error
}

func TestDB_OpsAfterClose(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := OpenDB(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	db.Close()

	assert.Error(t, db.Put([]byte("k"), []byte("v")))
	_, err = db.Get([]byte("k"))
	assert.Error(t, err)
	assert.Error(t, db.Delete([]byte("k")))
}

func TestDB_GetIncrementalId(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := OpenDB(tmpDir+"/testdb", 4*1024*1024)
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
	db, err := OpenDB(tmpDir+"/testdb", 4*1024*1024)
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

func TestDB_ScanRange(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := OpenDB(tmpDir+"/testdb", 4*1024*1024)
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
	db, err := OpenDB(tmpDir+"/testdb", 4*1024*1024)
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
	db, err := OpenDB(tmpDir+"/testdb", 4*1024*1024)
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
	db, err := OpenDB(tmpDir+"/testdb", 4*1024*1024)
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
	db, err := OpenDB(tmpDir+"/testdb", 4*1024*1024)
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
	db, err := OpenDB(tmpDir+"/testdb", 4*1024*1024)
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
	db, err := OpenDB(tmpDir+"/testdb", 4*1024*1024)
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
	db, err := OpenDB(tmpDir+"/testdb", 4*1024*1024)
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
	db, err := OpenDB(tmpDir+"/testdb", 4*1024*1024)
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
	db, err := OpenDB(tmpDir+"/testdb", 4*1024*1024)
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
	db, err := OpenDB(tmpDir+"/testdb", 4*1024*1024)
	assert.NoError(t, err)
	defer db.Close()

	batch := db.NewBatch(0)
	assert.NoError(t, batch.Close())
}
