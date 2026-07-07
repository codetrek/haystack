// Package kv defines the Store and Batch interfaces used by core packages
// to decouple storage from any specific key-value engine implementation.
package kv

// Store is the key-value store interface used by core packages.
// It is implemented by pebblekv.PebbleDB.
//
// GetIncrementalId and ScheduleCompact are part of the interface to keep Store
// a drop-in replacement for the original Pebble wrapper relied on by the
// invertedindex and collection packages (GetIncrementalId backs ID allocation;
// ScheduleCompact lets the keyword merger trigger compaction after large
// rewrites). Alternative backends must provide both: a backend without a native
// compaction concept may implement ScheduleCompact as a no-op, but
// GetIncrementalId must deliver the monotonically increasing semantics those
// callers depend on.
type Store interface {
	Get(key []byte) ([]byte, error)
	Put(key, value []byte) error
	Delete(key []byte) error
	NewBatch(maxBatchSize int32) Batch
	// Scan invokes cb for every key with the given prefix. The key and value
	// slices passed to cb are only valid for the duration of that call; callers
	// that need to retain them beyond the callback must copy them. Returning
	// false from cb stops the scan.
	Scan(prefix []byte, cb func(key, value []byte) bool) error
	// ScanRange invokes cb for every key in [begin, end) (end exclusive). As with
	// Scan, the key and value slices passed to cb are only valid for the duration
	// of that call; callers must copy them to retain. Returning false from cb
	// stops the scan.
	ScanRange(begin, end []byte, cb func(key, value []byte) bool) error
	GetIncrementalId(key []byte) (int, error)
	ScheduleCompact()
	Close() error
	IsClosed() bool
}

// Batch is the write-batch interface used by core packages.
// It is implemented by pebblekv.PebbleBatch.
type Batch interface {
	Put(key, value []byte) error
	Delete(key []byte) error
	DeleteRange(start, end []byte) error
	DeletePrefix(prefix []byte) error
	Commit() error
	Reset()
	// Close releases the resources held by the batch. It must be called if a
	// batch is discarded without calling Commit; Commit handles its own cleanup,
	// so Close after a successful Commit is a no-op but still safe to call.
	// Failing to Close an uncommitted batch may leak underlying resources.
	Close() error
	Count() int32
}

// Snapshotter is an OPTIONAL capability. A backend that can serve a consistent,
// point-in-time read view implements it in addition to Store. Callers obtain the
// view by type-asserting a Store: sn, ok := store.(kv.Snapshotter).
type Snapshotter interface {
	// Snapshot returns a read-only view frozen at the current committed state.
	// Reads issued through it are unaffected by writes committed after the call.
	// The caller MUST Close the returned Snapshot to release the resources it
	// pins (see the Snapshot.Close contract). On error it returns (nil, err) — a
	// literal untyped nil Snapshot, never a typed-nil pointer. It is safe to call
	// concurrently with Store writes and other reads.
	Snapshot() (Snapshot, error)
}

// Snapshot is a read-only, point-in-time view over a Store. Its read methods
// mirror Store's (same key/value copy and callback slice-validity semantics), and
// a single Snapshot is safe for concurrent Get/Scan/ScanRange from multiple
// goroutines (each call opens its own reader/iterator).
type Snapshot interface {
	Get(key []byte) ([]byte, error)
	Scan(prefix []byte, cb func(key, value []byte) bool) error
	ScanRange(begin, end []byte, cb func(key, value []byte) bool) error
	// Close releases the view. It MUST be called (typically deferred) and MUST
	// happen before the parent Store is closed. Close is idempotent and safe to
	// call more than once (a second call is a no-op returning nil), consistent
	// with pebblekv's existing repeat-safe Close (PebbleBatch.Close). Reads
	// through the Snapshot after its Close are undefined; callers must not do that.
	Close() error
}
