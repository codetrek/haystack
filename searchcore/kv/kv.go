package kv

// Store is the key-value store interface used throughout haystack.
// It is implemented by pebblekv.PebbleDB.
type Store interface {
	Get(key []byte) ([]byte, error)
	Put(key, value []byte) error
	Delete(key []byte) error
	NewBatch(maxBatchSize int32) Batch
	Scan(prefix []byte, cb func(key, value []byte) bool) error
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
	Close() error
	Count() int32
}
