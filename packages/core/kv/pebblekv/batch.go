package pebblekv

import (
	"fmt"
	"sync/atomic"

	"github.com/cockroachdb/pebble"
)

// pebbleBatchWriter is an internal interface satisfied by *pebble.Batch,
// enabling substitution in tests.
type pebbleBatchWriter interface {
	Set(key, value []byte, opts *pebble.WriteOptions) error
	Delete(key []byte, opts *pebble.WriteOptions) error
	DeleteRange(start, end []byte, opts *pebble.WriteOptions) error
	Commit(o *pebble.WriteOptions) error
	Reset()
	Close() error
}

// PebbleBatch is a Pebble-backed implementation of kv.Batch. It buffers
// write operations and applies them atomically on Commit. When maxBatchSize
// is greater than zero, the batch auto-commits (and resets) once the operation
// count reaches that limit.
type PebbleBatch struct {
	batch pebbleBatchWriter

	// maxBatchSize is the maximum number of operations in the batch, count is the number of operations in the batch
	// The batch will be committed silently when the count reaches maxBatchSize, and a new batch will be created
	maxBatchSize int32
	count        atomic.Int32

	// commitOpts is the WriteOptions used by Commit (inherited from the opening
	// PebbleDB). Nil means pebble.NoSync (the default), so a directly constructed
	// batch is non-syncing.
	commitOpts *pebble.WriteOptions
}

// Put adds a key-value pair to the batch
func (b *PebbleBatch) Put(key, value []byte) error {
	if err := b.batch.Set(key, value, nil); err != nil {
		return err
	}

	return b.increaseAndTryCommit()
}

// Delete adds a delete operation to the batch
func (b *PebbleBatch) Delete(key []byte) error {
	if err := b.batch.Delete(key, nil); err != nil {
		return err
	}

	return b.increaseAndTryCommit()
}

// DeleteRange deletes a range of keys in the batch
func (b *PebbleBatch) DeleteRange(start, end []byte) error {
	if err := b.batch.DeleteRange(start, end, nil); err != nil {
		return err
	}

	return b.increaseAndTryCommit()
}

// DeletePrefix deletes all keys with the given prefix in the batch
func (b *PebbleBatch) DeletePrefix(prefix []byte) error {
	// The exclusive end must be the prefix successor (keyUpperBound), NOT
	// append(prefix, 0xff): the latter leaves any key of the form prefix+0xff+...
	// undeleted (e.g. DeleteTable could not remove a keyword whose first byte is
	// 0xff). Mirrors the same fix in Scan.
	end := keyUpperBound(prefix)
	if end == nil {
		// prefix is all-0xff (unreachable for the inverted index's key-type
		// prefixes, which start with a small key-type byte); best-effort bound.
		end = append(append([]byte{}, prefix...), 0xff)
	}
	// Call the underlying writer directly (not the public DeleteRange wrapper)
	// so the auto-commit counter advances by exactly one per DeletePrefix.
	if err := b.batch.DeleteRange(prefix, end, nil); err != nil {
		return err
	}

	return b.increaseAndTryCommit()
}

// Commit commits the batch to the database
func (b *PebbleBatch) Commit() error {
	b.count.Store(0)
	opts := b.commitOpts
	if opts == nil {
		opts = pebble.NoSync
	}
	return b.batch.Commit(opts)
}

// Reset resets the batch for reuse
func (b *PebbleBatch) Reset() {
	b.count.Store(0)
	b.batch.Reset()
}

// Close closes the batch
func (b *PebbleBatch) Close() error {
	return b.batch.Close()
}

func (b *PebbleBatch) Count() int32 {
	return b.count.Load()
}

func (b *PebbleBatch) increaseAndTryCommit() error {
	if b.maxBatchSize <= 0 {
		return nil
	}

	b.count.Add(1)
	if b.count.Load() >= b.maxBatchSize {
		err := b.Commit()
		if err != nil {
			return fmt.Errorf("failed to commit batch: %v", err)
		}
		b.Reset()
	}

	return nil
}
