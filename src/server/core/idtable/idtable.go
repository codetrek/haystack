package idtable

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/codetrek/haystack/server/core/pebble"
	"github.com/codetrek/haystack/server/core/storage"
)

// LRUCacheSize defines the maximum number of entries in the LRU cache.
// 200,000 was chosen as a balance between memory usage and cache hit rate for typical workloads.
const LRUCacheSize = 20_0000
const BatchSize = 100

var (
	mu    sync.Mutex
	db    pebble.DB
	batch pebble.Batch

	lru    *LRUCache
	nextId int64

	closing chan bool
	done    chan bool
)

func EncodeIncrIdKey() []byte {
	return []byte(fmt.Sprintf("%c", storage.KeyTypeIdTableNextId))
}

func EncodeIdKey(key []byte) []byte {
	return []byte(fmt.Sprintf("%c%s", storage.KeyTypeIdTableKey, key))
}

func parseId(id string) int64 {
	v, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return -1
	}

	return v
}

func tryCommit() {
	if batch.Count() > 0 {
		batch.Commit()
		batch.Reset()
	}
}

func Init(database pebble.DB) error {
	if db != nil {
		return fmt.Errorf("already initialized")
	}

	v, err := database.Get(EncodeIncrIdKey())
	if err != nil {
		return err
	}

	if v == nil {
		nextId = 1
	} else {
		nextId = parseId(string(v))
	}

	if nextId < 0 {
		return fmt.Errorf("invalid nextId value: %d, database malformed", nextId)
	}

	db = database
	lru = NewLRUCache(LRUCacheSize)
	batch = db.NewBatch(BatchSize)

	closing = make(chan bool)
	done = make(chan bool)
	go func() {
		for {
			select {
			case <-time.After(5 * time.Second):
				mu.Lock()
				tryCommit()
				mu.Unlock()
			case <-closing:
				close(done)
				return
			}
		}
	}()

	return nil
}

func Close() {
	mu.Lock()
	defer mu.Unlock()
	if closing == nil {
		return
	}

	close(closing)
	<-done
	closing = nil
	done = nil

	tryCommit()
	lru.Clear()

	db = nil
}

func toBytes(v int64) []byte {
	id := make([]byte, 8) // int64 is 8 bytes
	binary.BigEndian.PutUint64(id, uint64(v))
	return id
}

func fromBytes(buf []byte) int64 {
	return int64(binary.BigEndian.Uint64(buf))
}

func GetId(key []byte) (string, error) {
	mu.Lock()
	defer mu.Unlock()

	if db == nil || db.IsClosed() {
		return "", fmt.Errorf("database is closed")
	}

	if v, ok := lru.Get(string(key)); ok {
		return string(toBytes(v)), nil
	}

	v, err := db.Get(EncodeIdKey(key))
	if err != nil {
		return "", fmt.Errorf("failed to get id for key %s: %v", key, err)
	}

	if v != nil {
		lru.Put(string(key), fromBytes(v))
		return string(v), nil
	}

	id := make([]byte, 8) // int64 is 8 bytes
	binary.BigEndian.PutUint64(id, uint64(nextId))
	lru.Put(string(key), nextId)

	nextId++

	batch.Put(EncodeIncrIdKey(), []byte(strconv.FormatInt(nextId, 10)))
	batch.Put(EncodeIdKey(key), id)

	return string(id), nil
}
