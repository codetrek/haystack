package idtable

import (
	"encoding/binary"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/stretchr/testify/assert"
)

// TestCloseWhenNotInitialized ensures Close on a zero-value Allocator does not panic.
func TestCloseWhenNotInitialized(t *testing.T) {
	a := &Allocator{}
	a.Close() // should be a no-op
}

// TestGetIdDBReadPath covers the path where the key exists in the durable store
// but not in the LRU cache (cache miss, db hit).
func TestGetIdDBReadPath(t *testing.T) {
	alloc := openTestAlloc(t, Options{CommitInterval: 50 * time.Millisecond})
	defer alloc.Close()

	key := []byte("db-read-test-key")
	id1, err := alloc.GetId(key)
	assert.NoError(t, err)

	// Commit so the entry is visible via the durable store, then evict it from the
	// LRU (and it is gone from pending after commit) so the next GetId reads bbolt.
	assert.NoError(t, alloc.Commit())
	alloc.lru.Delete(string(key))

	id2, err := alloc.GetId(key)
	assert.NoError(t, err)
	assert.Equal(t, id1, id2)
}

// TestPeriodicCommit covers the background goroutine's timer-triggered tryCommit path.
func TestPeriodicCommit(t *testing.T) {
	alloc := openTestAlloc(t, Options{CommitInterval: 50 * time.Millisecond})
	defer alloc.Close()

	for i := 0; i < 5; i++ {
		_, err := alloc.GetId([]byte("periodic-key-" + strconv.Itoa(i)))
		assert.NoError(t, err)
	}

	// Wait long enough for the background goroutine's timer to fire.
	time.Sleep(200 * time.Millisecond)

	for i := 0; i < 5; i++ {
		_, err := alloc.GetId([]byte("periodic-key-" + strconv.Itoa(i)))
		assert.NoError(t, err)
	}
}

// TestCloseDuringPeriodicCommits exercises the Close() path while the background
// goroutine is actively firing its commit timer at a very small interval. This
// reproduces the deadlock that occurs if Close holds a.mu while waiting for the
// goroutine to exit (the goroutine's commit branch also acquires a.mu). It must
// finish well within the test timeout and is most meaningful under -race.
func TestCloseDuringPeriodicCommits(t *testing.T) {
	for iter := 0; iter < 20; iter++ {
		alloc, err := Open(filepath.Join(t.TempDir(), "idtable.db"), Options{CommitInterval: time.Microsecond})
		assert.NoError(t, err)

		var wg sync.WaitGroup
		stop := make(chan struct{})
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				i := 0
				for {
					select {
					case <-stop:
						return
					default:
						_, _ = alloc.GetId([]byte("k-" + strconv.Itoa(w) + "-" + strconv.Itoa(i)))
						i++
					}
				}
			}(w)
		}

		time.Sleep(2 * time.Millisecond) // let the timer fire many times

		// Close must not deadlock even though the commit goroutine is active.
		close(stop)
		wg.Wait()
		alloc.Close()
	}
}

// TestGetId_PendingSurvivesLRUEviction pins the pending buffer's reason to exist:
// an uncommitted allocation that is evicted from a full LRU must still resolve
// from `pending` — it must NOT be re-allocated a second (duplicate) id.
func TestGetId_PendingSurvivesLRUEviction(t *testing.T) {
	// LRUCacheSize 1 so the second alloc evicts the first; long interval so
	// nothing is committed (the entries stay only in pending + the LRU).
	a := openTestAlloc(t, Options{LRUCacheSize: 1, CommitInterval: time.Hour})
	defer a.Close()

	first, err := a.GetId([]byte("a")) // id 1: in LRU + pending (uncommitted)
	assert.NoError(t, err)
	_, err = a.GetId([]byte("b")) // id 2: evicts "a" from the size-1 LRU
	assert.NoError(t, err)
	assert.Equal(t, int64(3), a.nextId, "exactly two ids allocated so far")

	// "a" is gone from the LRU and never committed → only `pending` can answer.
	again, err := a.GetId([]byte("a"))
	assert.NoError(t, err)
	assert.Equal(t, first, again, "evicted-but-uncommitted key must keep its id")
	assert.Equal(t, int64(3), a.nextId, "must not allocate a duplicate id for a pending key")
}

func TestGetIdMultipleCallsSameKey(t *testing.T) {
	alloc := openTestAlloc(t, Options{CommitInterval: 50 * time.Millisecond})
	defer alloc.Close()

	key := []byte("same-key")

	// Repeated GetId returns the same id (cache/pending hit).
	id1, err := alloc.GetId(key)
	assert.NoError(t, err)
	id2, err := alloc.GetId(key)
	assert.NoError(t, err)
	assert.Equal(t, id1, id2)

	// Commit, evict from cache, then call again (durable read path).
	assert.NoError(t, alloc.Commit())
	alloc.lru.Delete(string(key))
	id3, err := alloc.GetId(key)
	assert.NoError(t, err)
	assert.Equal(t, id1, id3)
}

// TestGetIdWithExistingNextId covers Open when the db already has a nextId value.
func TestGetIdWithExistingNextId(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idtable.db")

	// Pre-seed nextId=100 directly into the meta bucket.
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	assert.NoError(t, err)
	seed := make([]byte, 8)
	binary.BigEndian.PutUint64(seed, 100)
	err = db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(bucketMeta)
		if err != nil {
			return err
		}
		return b.Put(metaNextId, seed)
	})
	assert.NoError(t, err)
	assert.NoError(t, db.Close())

	alloc, err := Open(path, Options{CommitInterval: 50 * time.Millisecond})
	assert.NoError(t, err)
	defer alloc.Close()
	assert.Equal(t, int64(100), alloc.nextId)

	// The next id assigned should be 100, advancing nextId to 101.
	id, err := alloc.GetId([]byte("key-after-seed"))
	assert.NoError(t, err)
	assert.Equal(t, uint64(100), binary.BigEndian.Uint64([]byte(id)))
	assert.Equal(t, int64(101), alloc.nextId)
}

// TestCloseCommitsPendingBatch verifies that Close flushes the pending allocations.
func TestCloseCommitsPendingBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idtable.db")

	alloc, err := Open(path, Options{CommitInterval: 50 * time.Millisecond})
	assert.NoError(t, err)

	key := []byte("batch-test-key")
	id1, err := alloc.GetId(key)
	assert.NoError(t, err)

	alloc.Close() // flushes pending to the db

	alloc2, err := Open(path, Options{CommitInterval: 50 * time.Millisecond})
	assert.NoError(t, err)
	defer alloc2.Close()

	id2, err := alloc2.GetId(key)
	assert.NoError(t, err)
	assert.Equal(t, id1, id2, "ID should survive close and reopen")
}
