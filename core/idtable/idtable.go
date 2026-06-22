// Package idtable allocates stable, compact int64 identifiers for arbitrary
// byte keys, backed by a self-owned bbolt file with an LRU cache and background
// commit loop. It is a standalone component: each Allocator owns its own bbolt
// database (no shared key-value store, no key-type prefixes), so it can be used
// by independent subsystems (the document indexer, the vector store) without
// colliding in a shared namespace.
package idtable

import (
	"encoding/binary"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	DefaultLRUCacheSize   = 200_000
	DefaultCommitInterval = 1 * time.Second

	// InvalidId is returned by DecodeId for a malformed (too-short) docid string.
	InvalidId = int64(-1)

	// LegacyKeyTypeNextId / LegacyKeyTypeKey are the key-type prefix bytes the
	// previous shared-kv.Store-backed allocator used by default. They are retained
	// only as the default migration-source parameters for MigrateFromKV; the
	// standalone bbolt layout uses buckets, not prefixes.
	LegacyKeyTypeNextId = byte(28)
	LegacyKeyTypeKey    = byte(29)

	// openTimeout bounds how long Open waits for the bbolt file lock before
	// failing, instead of blocking forever when another process holds it.
	openTimeout = 5 * time.Second
)

// Bucket / meta-key names for the on-disk layout. These are stable: changing
// them after data has been written makes previously allocated ids unreachable.
var (
	bucketKeys = []byte("keys") // raw key -> 8-byte big-endian id
	bucketMeta = []byte("meta") // metadata bucket
	metaNextId = []byte("nextId")
)

// Options configures an Allocator. Zero-value fields fall back to defaults.
type Options struct {
	LRUCacheSize   int           // default DefaultLRUCacheSize (200000)
	CommitInterval time.Duration // default DefaultCommitInterval (1s)
}

// Allocator maps arbitrary keys to stable, compact int64 ids (returned as
// 8-byte big-endian strings), LRU-cached and persisted to a self-owned bbolt
// database, with a background commit loop.
type Allocator struct {
	mu     sync.Mutex
	db     *bolt.DB
	closed bool
	// lru has its own internal locking, but every Allocator access to it already
	// happens while holding a.mu, so its lock is effectively redundant here.
	lru *lruCache
	// pending holds key->id allocations made since the last commit. It is the
	// authoritative read-your-own-writes buffer: the LRU is bounded and may evict
	// an uncommitted entry, so GetId/Lookup consult pending too before allocating
	// a (duplicate) id. Cleared on every successful commit.
	pending       map[string]int64
	pendingNextId bool
	nextId        int64

	commitInterval time.Duration
	closing        chan bool
	done           chan bool
}

// Open creates and starts an Allocator backed by a bbolt file at path, creating
// it if necessary. Zero-value Options fields fall back to defaults.
func Open(path string, opts Options) (*Allocator, error) {
	if opts.LRUCacheSize == 0 {
		opts.LRUCacheSize = DefaultLRUCacheSize
	}
	if opts.CommitInterval == 0 {
		opts.CommitInterval = DefaultCommitInterval
	}

	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: openTimeout})
	if err != nil {
		return nil, fmt.Errorf("idtable: open %s: %w", path, err)
	}

	// Ensure buckets exist and read the persisted nextId.
	var nextId int64 = 1
	err = db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketKeys); err != nil {
			return err
		}
		mb, err := tx.CreateBucketIfNotExists(bucketMeta)
		if err != nil {
			return err
		}
		if v := mb.Get(metaNextId); v != nil {
			nextId = decodeId(v)
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	if nextId < 0 {
		db.Close()
		return nil, fmt.Errorf("idtable: invalid nextId value: %d, database malformed", nextId)
	}

	a := &Allocator{
		db:             db,
		lru:            newLRUCache(opts.LRUCacheSize),
		pending:        make(map[string]int64),
		nextId:         nextId,
		commitInterval: opts.CommitInterval,
		closing:        make(chan bool),
		done:           make(chan bool),
	}

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

	if a.closed {
		return "", fmt.Errorf("database is closed")
	}

	// Hot path: keep string(key) inline in the LRU lookup so the compiler's
	// no-alloc map-key optimization applies — hoisting it to a variable would
	// force an allocation of the key string on every cached hit.
	if v, ok := a.lru.Get(string(key)); ok {
		return EncodeId(v), nil
	}
	sk := string(key)
	// pending is authoritative for uncommitted allocations (robust to LRU eviction).
	if v, ok := a.pending[sk]; ok {
		a.lru.Put(sk, v)
		return EncodeId(v), nil
	}

	id, found, err := a.readCommitted(key)
	if err != nil {
		return "", err
	}
	if found {
		a.lru.Put(sk, id)
		return EncodeId(id), nil
	}

	// Allocate a new id.
	id = a.nextId
	a.nextId++
	a.lru.Put(sk, id)
	a.pending[sk] = id
	a.pendingNextId = true
	return EncodeId(id), nil
}

// Lookup resolves a key to its id WITHOUT allocating: it returns found=false for
// a key that has never been assigned an id. It consults the LRU and the pending
// (uncommitted) buffer before the durable store. Safe for concurrent use.
func (a *Allocator) Lookup(key []byte) (id int64, found bool, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return 0, false, fmt.Errorf("database is closed")
	}

	if v, ok := a.lru.Get(string(key)); ok { // inline string(key): no-alloc map-key path
		return v, true, nil
	}
	sk := string(key)
	if v, ok := a.pending[sk]; ok {
		return v, true, nil
	}

	id, found, err = a.readCommitted(key)
	if err != nil {
		return 0, false, err
	}
	if found {
		a.lru.Put(sk, id)
	}
	return id, found, nil
}

// readCommitted reads a committed key->id entry from bbolt. Callers hold a.mu.
func (a *Allocator) readCommitted(key []byte) (id int64, found bool, err error) {
	err = a.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(bucketKeys).Get(key); v != nil {
			id = decodeId(v)
			found = true
		}
		return nil
	})
	if err != nil {
		return 0, false, fmt.Errorf("idtable: lookup failed: %w", err)
	}
	return id, found, nil
}

// Close flushes any pending writes, stops the background commit goroutine, and
// closes the bbolt database.
//
// The mutex is released while waiting for the background goroutine to exit: that
// goroutine's periodic-commit branch acquires a.mu, so holding the lock across
// the <-done wait would deadlock if the timer fired concurrently.
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
	a.closed = true
	if a.db != nil {
		a.db.Close()
		a.db = nil
	}
}

// CrashRelease drops the Allocator's OS-held resources the way a process kill
// would: it stops the background commit goroutine and closes the bbolt file
// WITHOUT the orderly final commit, so uncommitted (pending) allocations are
// discarded and the file lock is released for a same-process reopen. It exists
// for crash-recovery tests that simulate an abrupt termination; production code
// uses Close. Safe to call more than once and after Close.
func (a *Allocator) CrashRelease() {
	a.mu.Lock()
	if a.closing == nil {
		// Already cleanly closed or crash-released: just ensure the db is gone.
		a.closed = true
		if a.db != nil {
			a.db.Close()
			a.db = nil
		}
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
	// Deliberately NO tryCommit: pending allocations are discarded, mimicking a
	// crash that never flushed the lazy batch.
	a.closed = true
	if a.db != nil {
		a.db.Close()
		a.db = nil
	}
}

// Commit synchronously flushes any pending key→id mappings and the nextId
// counter to the bbolt database (durable: bbolt fsyncs on transaction commit).
// It is the public, on-demand counterpart to the lazy commit tick and the
// Close-time flush: callers that must make the id allocations durable at a
// precise point (e.g. before truncating an external write-ahead log that is the
// only other record of those mappings) call this to close the durability gap.
// It is safe for concurrent use and is a no-op when nothing is pending.
func (a *Allocator) Commit() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("database is closed")
	}
	return a.tryCommit()
}

// tryCommit flushes the pending allocations and nextId in a single bbolt
// transaction if anything is pending. Callers must hold a.mu.
//
// It re-checks the closed state before driving the write: the periodic-commit
// tick and the Close-time flush both reach here, and committing against a closed
// db would error — when closed we skip and return a clean error instead,
// consistent with GetId/Commit's fail-closed guards. The check sits under a.mu
// (held by every caller), so it cannot race a concurrent close-then-commit.
func (a *Allocator) tryCommit() error {
	if a.closed || a.db == nil {
		return fmt.Errorf("database is closed")
	}
	if len(a.pending) == 0 && !a.pendingNextId {
		return nil
	}
	err := a.db.Update(func(tx *bolt.Tx) error {
		kb := tx.Bucket(bucketKeys)
		for k, id := range a.pending {
			// A fresh slice per Put: bbolt retains the value slice by reference
			// until the transaction commits, so a reused buffer would make every
			// entry take the last-written value.
			if err := kb.Put([]byte(k), toBytes(id)); err != nil {
				return err
			}
		}
		if a.pendingNextId {
			if err := tx.Bucket(bucketMeta).Put(metaNextId, toBytes(a.nextId)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Reset pending only after a durable commit. Keep the map allocated for reuse.
	for k := range a.pending {
		delete(a.pending, k)
	}
	a.pendingNextId = false
	return nil
}

func parseId(id string) int64 {
	v, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return -1
	}
	return v
}

// decodeId reads an 8-byte big-endian id value; a short slice yields -1.
func decodeId(v []byte) int64 {
	if len(v) < 8 {
		return -1
	}
	return int64(binary.BigEndian.Uint64(v))
}

func toBytes(v int64) []byte {
	id := make([]byte, 8) // int64 is 8 bytes
	binary.BigEndian.PutUint64(id, uint64(v))
	return id
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
