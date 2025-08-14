package pebble

import (
	"fmt"
	"sync/atomic"

	"github.com/cockroachdb/pebble"
)

// Batch represents a batch of operations
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

type PebbleBatch struct {
	db    *PebbleDB
	batch *pebble.Batch

	// maxBatchSize is the maximum number of operations in the batch, count is the number of operations in the batch
	// The batch will be committed silently when the count reaches maxBatchSize, and a new batch will be created
	maxBatchSize int32
	count        atomic.Int32
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
	if err := b.DeleteRange(prefix, append(prefix, 0xFF)); err != nil {
		return err
	}

	return b.increaseAndTryCommit()
}

// Commit commits the batch to the database
func (b *PebbleBatch) Commit() error {
	b.count.Store(0)
	return b.batch.Commit(pebble.Sync)
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
