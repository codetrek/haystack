package idtable

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/codetrek/haystack/searchcore/kv"
)

// Default key-type prefix bytes (match haystack's historical layout for on-disk compatibility).
const (
	DefaultKeyTypeNextId  = byte(28)
	DefaultKeyTypeKey     = byte(29)
	DefaultLRUCacheSize   = 20_0000
	DefaultBatchSize      = int32(100)
	DefaultCommitInterval = 5 * time.Second
)

// Options configures an Allocator. Zero-value fields fall back to defaults.
type Options struct {
	KeyTypeNextId  byte          // default DefaultKeyTypeNextId (28)
	KeyTypeKey     byte          // default DefaultKeyTypeKey (29)
	LRUCacheSize   int           // default DefaultLRUCacheSize (200000)
	BatchSize      int32         // default DefaultBatchSize (100)
	CommitInterval time.Duration // default DefaultCommitInterval (5s)
}

// Allocator maps arbitrary keys to stable, compact int64 ids (returned as 8-byte strings),
// LRU-cached and persisted to the kv.Store, with a background commit loop.
type Allocator struct {
	mu             sync.Mutex
	store          kv.Store
	batch          kv.Batch
	lru            *LRUCache
	nextId         int64
	keyTypeNextId  byte
	keyTypeKey     byte
	commitInterval time.Duration
	closing        chan bool
	done           chan bool
}

// New creates and starts an Allocator. Zero-value Options fields fall back to defaults.
func New(store kv.Store, opts Options) (*Allocator, error) {
	// Fill defaults
	if opts.KeyTypeNextId == 0 {
		opts.KeyTypeNextId = DefaultKeyTypeNextId
	}
	if opts.KeyTypeKey == 0 {
		opts.KeyTypeKey = DefaultKeyTypeKey
	}
	if opts.LRUCacheSize == 0 {
		opts.LRUCacheSize = DefaultLRUCacheSize
	}
	if opts.BatchSize == 0 {
		opts.BatchSize = DefaultBatchSize
	}
	if opts.CommitInterval == 0 {
		opts.CommitInterval = DefaultCommitInterval
	}

	a := &Allocator{
		store:          store,
		keyTypeNextId:  opts.KeyTypeNextId,
		keyTypeKey:     opts.KeyTypeKey,
		commitInterval: opts.CommitInterval,
		closing:        make(chan bool),
		done:           make(chan bool),
	}

	v, err := store.Get(a.encodeIncrIdKey())
	if err != nil {
		return nil, err
	}

	if v == nil {
		a.nextId = 1
	} else {
		a.nextId = parseId(string(v))
	}

	if a.nextId < 0 {
		return nil, fmt.Errorf("invalid nextId value: %d, database malformed", a.nextId)
	}

	a.lru = NewLRUCache(opts.LRUCacheSize)
	a.batch = store.NewBatch(opts.BatchSize)

	go func() {
		for {
			select {
			case <-time.After(a.commitInterval):
				a.mu.Lock()
				a.tryCommit()
				a.mu.Unlock()
			case <-a.closing:
				close(a.done)
				return
			}
		}
	}()

	return a, nil
}

// GetId returns a stable 8-byte string id for the given key, allocating one if
// it does not yet exist. It is safe for concurrent use.
func (a *Allocator) GetId(key []byte) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.store == nil || a.store.IsClosed() {
		return "", fmt.Errorf("database is closed")
	}

	if v, ok := a.lru.Get(string(key)); ok {
		return string(toBytes(v)), nil
	}

	v, err := a.store.Get(a.encodeIdKey(key))
	if err != nil {
		return "", fmt.Errorf("failed to get id for key %s: %v", key, err)
	}

	if v != nil {
		a.lru.Put(string(key), fromBytes(v))
		return string(v), nil
	}

	id := toBytes(a.nextId)
	a.lru.Put(string(key), a.nextId)

	a.nextId++

	a.batch.Put(a.encodeIncrIdKey(), []byte(strconv.FormatInt(a.nextId, 10)))
	a.batch.Put(a.encodeIdKey(key), id)

	return string(id), nil
}

// Close flushes any pending batch writes, stops the background commit goroutine,
// and clears the LRU cache.
func (a *Allocator) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closing == nil {
		return
	}

	close(a.closing)
	<-a.done
	a.closing = nil
	a.done = nil

	a.tryCommit()
	a.lru.Clear()

	a.store = nil
}

// encodeIncrIdKey returns the key used to store nextId.
func (a *Allocator) encodeIncrIdKey() []byte {
	return []byte{a.keyTypeNextId}
}

// encodeIdKey returns the key used to store the id for the given key.
func (a *Allocator) encodeIdKey(key []byte) []byte {
	result := make([]byte, 1+len(key))
	result[0] = a.keyTypeKey
	copy(result[1:], key)
	return result
}

func (a *Allocator) tryCommit() {
	if a.batch.Count() > 0 {
		a.batch.Commit()
		a.batch.Reset()
	}
}

func parseId(id string) int64 {
	v, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return -1
	}
	return v
}

func toBytes(v int64) []byte {
	id := make([]byte, 8) // int64 is 8 bytes
	binary.BigEndian.PutUint64(id, uint64(v))
	return id
}

func fromBytes(buf []byte) int64 {
	return int64(binary.BigEndian.Uint64(buf))
}
