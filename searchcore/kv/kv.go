package kv

// Store is the key-value store interface used throughout haystack.
// It is implemented by pebblekv.PebbleDB.
//
// GetIncrementalId and ScheduleCompact are part of the interface to keep Store
// a drop-in replacement for the original Pebble wrapper relied on by the
// invertedindex and workspace packages (GetIncrementalId backs ID allocation;
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

// Batch is the write-batch interface used throughout haystack.
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
