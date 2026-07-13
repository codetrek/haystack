// Package pebblekv provides a Pebble-backed implementation of kv.Store.
package pebblekv

import (
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
	"github.com/codetrek/haystack/packages/core/kv"
)

// pebbleStore is an internal interface satisfied by *pebble.DB, enabling
// substitution in tests to exercise error paths.
type pebbleStore interface {
	Set(key, value []byte, opts *pebble.WriteOptions) error
	Get(key []byte) ([]byte, io.Closer, error)
	Delete(key []byte, opts *pebble.WriteOptions) error
	NewBatch() *pebble.Batch
	NewIter(o *pebble.IterOptions) (*pebble.Iterator, error)
	NewSnapshot() *pebble.Snapshot
	Compact(start, end []byte, parallelize bool) error
	Close() error
}

// var _ kv.Snapshotter = (*PebbleDB)(nil) asserts at compile time that PebbleDB
// implements the optional kv.Snapshotter capability (in addition to kv.Store).
var _ kv.Snapshotter = (*PebbleDB)(nil)

// pebbleIterable and pebbleGetter are the minimal read surfaces the shared scan
// and get helpers need. Both the whole DB (*pebble.DB via pebbleStore) and a
// point-in-time snapshot (pebbleSnapshotReader) satisfy them, so PebbleDB and
// pebbleSnapshot share a single source of truth for the scan/get logic (notably
// the subtle keyUpperBound / prefix-HasPrefix bound, which has regressed before).
type pebbleIterable interface {
	NewIter(o *pebble.IterOptions) (*pebble.Iterator, error)
}
type pebbleGetter interface {
	Get(key []byte) ([]byte, io.Closer, error)
}

// PebbleDB is a Pebble-backed implementation of kv.Store. It wraps an
// underlying Pebble database and tracks whether it has been closed.
type PebbleDB struct {
	path   string
	db     pebbleStore
	closed atomic.Bool

	// writeOpts is the WriteOptions applied to Set/Delete and batch Commit
	// (pebble.Sync by default; pebble.NoSync when opened with NoSync/DisableWAL).
	writeOpts *pebble.WriteOptions
}

// OpenOptions configures a pebblekv store. The zero value is the DEFAULT mode:
// WAL on, commits NOT fsync'd (NoSync). Opt into durability with Sync, or drop
// the WAL entirely with DisableWAL.
type OpenOptions struct {
	CacheSize int64
	// DisableWAL turns off the write-ahead log entirely: writes live only in the
	// memtable until it flushes to an SSTable, so an unclean shutdown loses the
	// un-flushed tail. ONLY safe for a derived, rebuildable store (e.g. an index
	// that can be rebuilt from source) — never for a store of record.
	DisableWAL bool
	// Sync requests a synchronous WAL fsync on every Set/Delete/batch Commit
	// (pebble.Sync). The DEFAULT (unset) is NoSync: the WAL is still written but
	// not fsync'd, so a clean restart recovers it from the OS page cache and only
	// an OS-level crash / power loss can lose the un-synced tail. Set Sync for a
	// store of record. Ignored when DisableWAL is set (there is no WAL to sync).
	Sync bool
}

// Open opens a Pebble database at the default WAL mode — WAL on, commits not
// fsync'd (NoSync). Use OpenWithOptions to request Sync or DisableWAL.
func Open(path string, cacheSize int64) (kv.Store, error) {
	return OpenWithOptions(path, OpenOptions{CacheSize: cacheSize})
}

// OpenWithOptions opens a Pebble database with explicit WAL/sync control.
func OpenWithOptions(path string, o OpenOptions) (kv.Store, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %v", err)
	}

	// Configure Pebble options
	opts := &pebble.Options{
		Cache: pebble.NewCache(o.CacheSize),

		DisableWAL: o.DisableWAL,

		WALMinSyncInterval: func() time.Duration {
			// Sync the WAL every 500us to avoid latency spikes.
			// Allow more operations to arrive and reduce IO operations
			return 500 * time.Microsecond
		},
		// Allow more files to be open
		MaxOpenFiles: 8192,

		// Set write buffer size to 8MB
		MemTableSize: 4 * 1024 * 1024,
		// Set max memtable count to 2
		MemTableStopWritesThreshold: 2,

		// The count of L0 files necessary to trigger an L0 compaction.
		L0CompactionFileThreshold: 1024,
		// Set L0 compaction threshold to 8
		L0CompactionThreshold: 12,
		// Set L0 stop writes threshold to 12
		L0StopWritesThreshold: 18,
		// Enable bloom filter
		Levels: []pebble.LevelOptions{
			{
				BlockSize:    32 * 1024,
				FilterPolicy: bloom.FilterPolicy(10),
			},
		},
		MaxConcurrentCompactions: func() int {
			return 2
		},
	}

	db, err := pebble.Open(absPath, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open pebble: %v", err)
	}

	// Default to NoSync (skip the per-commit fsync); opt into durability with
	// Sync. With the WAL disabled there is nothing to sync, so Sync is ignored.
	writeOpts := pebble.NoSync
	if o.Sync && !o.DisableWAL {
		writeOpts = pebble.Sync
	}

	// Create a new DB instance
	pdb := &PebbleDB{
		path:      absPath,
		db:        db,
		closed:    atomic.Bool{},
		writeOpts: writeOpts,
	}

	return pdb, nil
}

func (d *PebbleDB) ScheduleCompact() {
	go func() {
		if d.IsClosed() {
			return
		}
		start := time.Now()
		log.Println("[Pebble] Compacting database...")
		d.db.Compact([]byte{0}, []byte{0xff}, false)
		log.Println("[Pebble] Compact done, took", time.Since(start))
	}()
}

// GetIncrementalId returns a monotonically increasing ID for the given key.
// The current value stored at key is returned, and the stored value is then
// incremented by one for the next call.
//
// The first time a key is seen it returns 0 and stores 1 back. On each
// subsequent call it returns the previously stored value (1, 2, 3, ...) and
// stores the next value.
//
// On error it returns -1 together with a non-nil error: either the underlying
// Get failed, or the stored value could not be parsed as an integer. If the
// database is closed it returns -1 and a non-nil error.
func (d *PebbleDB) GetIncrementalId(key []byte) (int, error) {
	if d.IsClosed() {
		return -1, fmt.Errorf("database is closed")
	}

	str, err := d.Get(key)
	if err != nil {
		return -1, err
	}

	var nextId int = 0
	if str != nil {
		i, err := strconv.Atoi(string(str))
		if err != nil {
			return -1, err
		}
		nextId = i
	}

	d.Put(key, []byte(strconv.Itoa(nextId+1)))

	return nextId, nil
}

// Close closes the database.
//
// All snapshots obtained via Snapshot() MUST be Closed before Close is called:
// pebble's DB.Close returns a "leaked snapshots" error if any snapshot is still
// open. PebbleDB does not track or auto-close snapshots on Close — the ordering
// is the caller's contract (see kv.Snapshot.Close).
func (d *PebbleDB) Close() error {
	if d.IsClosed() {
		return fmt.Errorf("database is closed")
	}

	d.closed.Store(true)

	if d.db != nil {
		if err := d.db.Close(); err != nil {
			return fmt.Errorf("failed to close pebble: %v", err)
		}
		d.db = nil
	}
	return nil
}

func (d *PebbleDB) IsClosed() bool {
	return d.closed.Load()
}

// Put stores a key-value pair
func (d *PebbleDB) Put(key, value []byte) error {
	if d.IsClosed() {
		return fmt.Errorf("database is closed")
	}

	// Use default write options (sync=true)
	if err := d.db.Set(key, value, d.writeOpts); err != nil {
		return fmt.Errorf("failed to put data: %v", err)
	}
	return nil
}

// Get retrieves the value for a key
func (d *PebbleDB) Get(key []byte) ([]byte, error) {
	if d.IsClosed() {
		return nil, fmt.Errorf("database is closed")
	}

	// Read directly from the DB
	return getCopy(d.db, key)
}

// getCopy reads key from r and returns a freshly-copied value (the source slice
// may be invalidated once the pebble Closer is closed), mapping pebble.ErrNotFound
// to (nil, nil). It is the single source of truth for the get semantics shared by
// PebbleDB.Get and pebbleSnapshot.Get.
func getCopy(r pebbleGetter, key []byte) ([]byte, error) {
	value, closer, err := r.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get data: %v", err)
	}
	defer closer.Close()

	// Make a copy of the value since the original slice may be invalidated
	return append([]byte{}, value...), nil
}

// Delete removes a key-value pair
func (d *PebbleDB) Delete(key []byte) error {
	if d.IsClosed() {
		return fmt.Errorf("database is closed")
	}

	// Use default write options (sync=true)
	if err := d.db.Delete(key, d.writeOpts); err != nil {
		return fmt.Errorf("failed to delete data: %v", err)
	}
	return nil
}

// NewBatch creates a new write batch for atomically applying multiple operations.
// maxBatchSize is the maximum number of operations in a single batch; set 0 to disable the limit.
func (d *PebbleDB) NewBatch(maxBatchSize int32) kv.Batch {
	return &PebbleBatch{
		batch:        d.db.NewBatch(),
		maxBatchSize: maxBatchSize,
		count:        atomic.Int32{},
		commitOpts:   d.writeOpts,
	}
}

// keyUpperBound returns the smallest key strictly greater than every key that
// begins with prefix — the correct exclusive upper bound for a prefix scan. It
// always copies (it never aliases or mutates prefix, unlike append(prefix, ...))
// and correctly handles prefixes ending in 0xff. A nil result means the prefix
// is all-0xff and therefore has no finite upper bound (the caller's prefix check
// terminates the scan instead).
func keyUpperBound(prefix []byte) []byte {
	end := make([]byte, len(prefix))
	copy(end, prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] != 0xff {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}

// Scan performs a range scan over the database
// key and value will INVALIDATE after the callback
// so make sure to copy them if you need to use them later
// The callback should return true to continue scanning or false to stop
func (d *PebbleDB) Scan(prefix []byte, cb func(key, value []byte) bool) error {
	if d.IsClosed() {
		return fmt.Errorf("database is closed")
	}

	return scanPrefix(d.db, prefix, cb)
}

// scanPrefix iterates every key of r that begins with prefix, invoking cb with
// each key/value (valid only for that call). Returning false from cb stops the
// scan. It is the single source of truth for the prefix-scan semantics shared by
// PebbleDB.Scan and pebbleSnapshot.Scan.
func scanPrefix(r pebbleIterable, prefix []byte, cb func(key, value []byte) bool) error {
	// Create an iterator with the prefix. The upper bound must be the prefix
	// successor (keyUpperBound), NOT append(prefix, 0xff): the latter excludes
	// any key of the form prefix+0xff+... — e.g. an inverted-index keyword
	// containing 0xff right after a search prefix — silently dropping it.
	iter, err := r.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: keyUpperBound(prefix),
	})
	if err != nil {
		return fmt.Errorf("failed to create iterator: %v", err)
	}
	defer iter.Close()

	prefixStr := string(prefix)
	for iter.First(); iter.Valid(); iter.Next() {
		if !strings.HasPrefix(string(iter.Key()), prefixStr) {
			// If the key doesn't start with the prefix, break the loop
			break
		}
		// We're not going to copy silently for performance reasons.
		// It's user's responsibility to copy the key and value if they need to use them later.
		//
		// key := append([]byte{}, iter.Key()...)
		// value := append([]byte{}, iter.Value()...)

		if continueScan := cb(iter.Key(), iter.Value()); !continueScan {
			break
		}
	}
	return nil
}

// Scan performs a range scan over the database
// key and value will INVALIDATE after the callback
// so make sure to copy them if you need to use them later
// The callback should return true to continue scanning or false to stop
func (d *PebbleDB) ScanRange(begin []byte, end []byte, cb func(key, value []byte) bool) error {
	if d.IsClosed() {
		return fmt.Errorf("database is closed")
	}

	return scanRange(d.db, begin, end, cb)
}

// scanRange iterates every key of r in [begin, end) (end exclusive), invoking cb
// with each key/value (valid only for that call). Returning false from cb stops
// the scan. It is the single source of truth for the range-scan semantics shared
// by PebbleDB.ScanRange and pebbleSnapshot.ScanRange.
func scanRange(r pebbleIterable, begin []byte, end []byte, cb func(key, value []byte) bool) error {
	// Create an iterator with the prefix
	iter, err := r.NewIter(&pebble.IterOptions{
		LowerBound: begin,
		UpperBound: end,
	})
	if err != nil {
		return fmt.Errorf("failed to create iterator: %v", err)
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		// We're not going to copy silently for performance reasons.
		// It's user's responsibility to copy the key and value if they need to use them later.
		//
		// key := append([]byte{}, ...)
		// value := append([]byte{}, ...)

		if continueScan := cb(iter.Key(), iter.Value()); !continueScan {
			break
		}
	}
	return nil
}

// pebbleSnapshotReader is the read surface of a *pebble.Snapshot that
// pebbleSnapshot depends on. Declaring it as an interface (rather than using
// *pebble.Snapshot directly) lets tests substitute a fake to drive the snapshot
// read/close error branches, exactly as errPebbleStore does for PebbleDB.
type pebbleSnapshotReader interface {
	Get(key []byte) ([]byte, io.Closer, error)
	NewIter(o *pebble.IterOptions) (*pebble.Iterator, error)
	Close() error
}

// pebbleSnapshot is a read-only, point-in-time kv.Snapshot backed by a
// *pebble.Snapshot. Its Get/Scan/ScanRange delegate to the same shared helpers
// as PebbleDB, so their semantics are byte-identical. A pebbleSnapshot has no
// independent closed-state: closing the parent DB while it is open is a caller
// contract violation (see PebbleDB.Close / kv.Snapshot.Close).
type pebbleSnapshot struct {
	snap pebbleSnapshotReader
}

func (s *pebbleSnapshot) Get(key []byte) ([]byte, error) {
	return getCopy(s.snap, key)
}

func (s *pebbleSnapshot) Scan(prefix []byte, cb func(key, value []byte) bool) error {
	return scanPrefix(s.snap, prefix, cb)
}

func (s *pebbleSnapshot) ScanRange(begin, end []byte, cb func(key, value []byte) bool) error {
	return scanRange(s.snap, begin, end, cb)
}

// Close releases the snapshot. It is idempotent: the reader field is nil'd
// UNCONDITIONALLY on the first call (before/regardless of the underlying Close's
// result), so a later Close is a safe no-op returning nil and never re-invokes
// the underlying Close — which would panic on a raw pebble.Snapshot double-close.
// Consistent with pebblekv's repeat-safe PebbleBatch.Close.
func (s *pebbleSnapshot) Close() error {
	snap := s.snap
	s.snap = nil
	if snap == nil {
		return nil
	}
	return snap.Close()
}

// Snapshot returns a consistent, point-in-time read view of the DB, satisfying
// the optional kv.Snapshotter capability. On a closed DB it returns a non-nil
// error together with a literal-nil kv.Snapshot (never a typed-nil
// *pebbleSnapshot, which would defeat a caller's sn == nil check). The IsClosed
// guard is required, not cosmetic: pebble's DB.NewSnapshot panics with ErrClosed
// on a closed *pebble.DB (and Close nils d.db), so the guard must return the
// error before ever touching d.db — exactly like Get/Scan/ScanRange. It is safe
// to call concurrently with writes and other reads.
func (d *PebbleDB) Snapshot() (kv.Snapshot, error) {
	if d.IsClosed() {
		return nil, fmt.Errorf("database is closed")
	}

	return &pebbleSnapshot{snap: d.db.NewSnapshot()}, nil
}
