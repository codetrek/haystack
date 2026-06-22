// Package idtable allocates stable, compact int64 identifiers for arbitrary
// byte keys, backed by a kv.Store with an LRU cache and background commit loop.
package idtable

import (
	"encoding/binary"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/codetrek/haystack/core/kv"
)

// Default key-type prefix bytes (match the historical on-disk layout for on-disk compatibility).
const (
	DefaultKeyTypeNextId  = byte(28)
	DefaultKeyTypeKey     = byte(29)
	DefaultLRUCacheSize   = 200_000
	DefaultBatchSize      = int32(100)
	DefaultCommitInterval = 5 * time.Second

	// InvalidId is returned by DecodeId for a malformed (too-short) docid string.
	InvalidId = int64(-1)
)

// Options configures an Allocator. Zero-value fields fall back to defaults.
type Options struct {
	// KeyTypeNextId is the single-byte KV key PREFIX under which the allocator
	// persists its nextId counter. It namespaces the allocator's keys within a
	// shared store. Changing it after data has been written is a breaking
	// on-disk change: previously allocated ids become unreachable.
	// A zero value selects DefaultKeyTypeNextId (28); byte 0 cannot be used as
	// an explicit prefix because it is reserved to mean "use the default".
	KeyTypeNextId byte
	// KeyTypeKey is the single-byte KV key PREFIX under which the allocator
	// persists each key→id mapping. The same breaking-change and zero-value
	// ("use default", DefaultKeyTypeKey 29) constraints as KeyTypeNextId apply.
	KeyTypeKey     byte
	LRUCacheSize   int           // default DefaultLRUCacheSize (200000)
	BatchSize      int32         // default DefaultBatchSize (100)
	CommitInterval time.Duration // default DefaultCommitInterval (5s)
}

// Allocator maps arbitrary keys to stable, compact int64 ids (returned as 8-byte strings),
// LRU-cached and persisted to the kv.Store, with a background commit loop.
type Allocator struct {
	mu    sync.Mutex
	store kv.Store
	batch kv.Batch
	// lru has its own internal locking, but every Allocator access to it
	// (GetId, Close) already happens while holding a.mu, so its lock is
	// effectively redundant here and never contended by this package.
	lru            *lruCache
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

	a.lru = newLRUCache(opts.LRUCacheSize)
	a.batch = store.NewBatch(opts.BatchSize)

	// Capture the channels locally so the goroutine never reads a.closing/a.done,
	// which Close() nils out under a.mu (reading them in the goroutine would race).
	closing, done := a.closing, a.done
	interval := a.commitInterval
	go func() {
		// Use an explicit ticker rather than time.After so Close can stop it:
		// when Close closes `closing`, the goroutine returns and the deferred
		// Stop fires, guaranteeing no tick is delivered after Close drains it.
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				a.mu.Lock()
				if err := a.tryCommit(); err != nil {
					log.Printf("[idtable] periodic commit failed: %v", err)
				}
				a.mu.Unlock()
			case <-closing:
				close(done)
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
//
// The mutex is released while waiting for the background goroutine to exit:
// that goroutine's periodic-commit branch acquires a.mu, so holding the lock
// across the <-done wait would deadlock if the timer fired concurrently.
func (a *Allocator) Close() {
	a.mu.Lock()
	if a.closing == nil {
		a.mu.Unlock()
		return
	}
	closing, done := a.closing, a.done
	a.closing, a.done = nil, nil
	a.mu.Unlock()

	close(closing)
	<-done

	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.tryCommit(); err != nil {
		log.Printf("[idtable] final commit on close failed: %v", err)
	}
	a.lru.Clear()
	a.store = nil
}

// Commit synchronously flushes any pending key→id mappings and the nextId
// counter to the backing kv.Store, fsync'd (the underlying batch commits with
// pebble.Sync). It is the public, on-demand counterpart to the lazy 5s tick and
// the Close-time flush: callers that must make the id allocations durable at a
// precise point (e.g. before truncating an external write-ahead log that is the
// only other record of those mappings) call this to close the durability gap.
// It is safe for concurrent use and is a no-op when nothing is pending.
func (a *Allocator) Commit() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.store == nil || a.store.IsClosed() {
		return fmt.Errorf("database is closed")
	}
	return a.tryCommit()
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

// tryCommit flushes the pending batch if it is non-empty, returning any
// Commit error. Callers must hold a.mu.
//
// It re-checks the store's open state under a.mu immediately before driving
// batch.Commit. The periodic-commit tick and the Close-time flush both reach
// here, and the backing KV can be closed out from under the allocator (e.g. a
// test's t.Cleanup closes the store while a tick is in flight). Committing a
// batch against a closed pebble panics ("pebble: closed"), so when the store is
// closed we skip the commit and return a clean error instead — consistent with
// GetId/Commit's fail-closed guards. The check sits under a.mu (held by every
// caller), so it cannot race a concurrent close-then-commit on this allocator.
func (a *Allocator) tryCommit() error {
	if a.store == nil || a.store.IsClosed() {
		return fmt.Errorf("database is closed")
	}
	if a.batch.Count() > 0 {
		if err := a.batch.Commit(); err != nil {
			return err
		}
		a.batch.Reset()
	}
	return nil
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

// EncodeId returns the canonical on-disk string form of a docid: its 8-byte
// big-endian encoding. This is the exact byte sequence GetId hands out and that
// documents.Store / invertedindex use as a key/value component, so callers that
// hold a docid as an int64 (e.g. search results) can rebuild the string form
// expected by the string-keyed Store APIs.
func EncodeId(id int64) string {
	return string(toBytes(id))
}

// DecodeId is the inverse of EncodeId: it decodes an 8-byte big-endian docid
// string back to its int64 value. A string shorter than 8 bytes is malformed
// (it can never be a docid this package produced) and yields InvalidId.
func DecodeId(s string) int64 {
	if len(s) < 8 {
		return InvalidId
	}
	return int64(binary.BigEndian.Uint64([]byte(s[:8])))
}
