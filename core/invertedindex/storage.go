package invertedindex

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/codetrek/haystack/core/kv"
	"github.com/codetrek/haystack/core/queue"
)

// Options holds tunables for the Index.
// Zero values select sensible defaults.
//
// The KeyType* fields select the single on-disk key-type prefix byte for each
// kind of key. Byte 0 (NUL) is reserved and cannot be selected by a consumer:
// a zero field means "use the default" (NUL is a poor key prefix, and reserving
// it lets the zero value double as the default sentinel). To use a custom
// prefix, pick any non-zero byte.
type Options struct {
	// KeyTypeRow is the on-disk key-type prefix byte for inverted-index row keys.
	// A zero value selects DefaultKeyTypeRow (20); byte 0 is reserved and cannot
	// be selected. Changing this after data has been written is a breaking
	// on-disk change.
	KeyTypeRow byte

	// KeyTypeTable is the on-disk key-type prefix byte for table-metadata keys.
	// A zero value selects DefaultKeyTypeTable (21); byte 0 is reserved.
	KeyTypeTable byte

	// KeyTypeNextId is the on-disk key-type prefix byte for the next-table-id counter.
	// A zero value selects DefaultKeyTypeNextId (22); byte 0 is reserved.
	KeyTypeNextId byte

	// FlushTicker is how often the flush goroutine enqueues a flush task.
	// Default: 1 second.
	FlushTicker time.Duration

	// FlushWaitTimeout is the time a keyword entry must be older than before
	// being flushed when the batch size threshold is not yet reached.
	// Default: 3 seconds.
	FlushWaitTimeout time.Duration

	// FlushWaitBatchSize is the number of doc-ids at which a keyword entry is
	// flushed regardless of its age.
	// Default: 200.
	FlushWaitBatchSize int

	// FlushDeleteWaitBatchSize is the number of doc-ids at which a pending-delete
	// entry is flushed regardless of its age.
	// Default: 50.
	FlushDeleteWaitBatchSize int

	// FlushDeleteWaitTimeout is the time a pending-delete entry must be older than
	// before being flushed when the batch size threshold is not yet reached.
	// Default: 5 seconds.
	FlushDeleteWaitTimeout time.Duration

	// FlushCooldown is the minimum interval between flush passes.
	// Default: 1 second.
	FlushCooldown time.Duration

	// MaxInvertedIndexSize caps the number of doc-ids stored per index row.
	// Default: 1000 (MaxInvertedIndexSize).
	MaxInvertedIndexSize int
}

func (o *Options) flushTicker() time.Duration {
	if o.FlushTicker > 0 {
		return o.FlushTicker
	}
	return 1 * time.Second
}

func (o *Options) flushWaitTimeout() time.Duration {
	if o.FlushWaitTimeout > 0 {
		return o.FlushWaitTimeout
	}
	return 3 * time.Second
}

func (o *Options) flushWaitBatchSize() int {
	if o.FlushWaitBatchSize > 0 {
		return o.FlushWaitBatchSize
	}
	return 200
}

func (o *Options) flushDeleteWaitBatchSize() int {
	if o.FlushDeleteWaitBatchSize > 0 {
		return o.FlushDeleteWaitBatchSize
	}
	return 50
}

func (o *Options) flushDeleteWaitTimeout() time.Duration {
	if o.FlushDeleteWaitTimeout > 0 {
		return o.FlushDeleteWaitTimeout
	}
	return 5 * time.Second
}

func (o *Options) flushCooldown() time.Duration {
	if o.FlushCooldown > 0 {
		return o.FlushCooldown
	}
	return 1 * time.Second
}

func (o *Options) maxInvertedIndexSize() int {
	if o.MaxInvertedIndexSize > 0 {
		return o.MaxInvertedIndexSize
	}
	return MaxInvertedIndexSize
}

// Index is the instance-based inverted index.
type Index struct {
	db   kv.Store
	q    queue.Queue
	opts Options

	// resolved on-disk key-type bytes (set in New from opts with defaults applied)
	keyTypeRow    byte
	keyTypeTable  byte
	keyTypeNextId byte

	// pending writes/deletes — accessed only from the single-threaded mpsc queue
	pendingWrites      map[int]*pendingTableWrites
	lastFlushWriteTime time.Time

	pendingDeletes      map[int]*pendingTableWrites
	lastFlushDeleteTime time.Time

	// flush goroutine lifecycle
	cancelFlush context.CancelFunc

	// keywords merger
	merger *keywordsMerger

	// keySeq disambiguates inverted-index keys written within the same
	// microsecond: the tick (time.Now().UnixMicro()) is otherwise the sole
	// disambiguator for two rows of the same (tableId,keyword,doccount), so two
	// such writes in one microsecond would collide and one would overwrite the
	// other (data loss, reachable in the merger's rewrite loop). Appended to the
	// key tail, which decode treats as opaque — no on-disk format break.
	keySeq atomic.Uint64
}

// New creates and starts a new Index.
func New(store kv.Store, q queue.Queue, opts Options) (*Index, error) {
	// Apply key-type defaults (zero means "use default").
	if opts.KeyTypeRow == 0 {
		opts.KeyTypeRow = DefaultKeyTypeRow
	}
	if opts.KeyTypeTable == 0 {
		opts.KeyTypeTable = DefaultKeyTypeTable
	}
	if opts.KeyTypeNextId == 0 {
		opts.KeyTypeNextId = DefaultKeyTypeNextId
	}

	idx := &Index{
		db:                  store,
		q:                   q,
		opts:                opts,
		keyTypeRow:          opts.KeyTypeRow,
		keyTypeTable:        opts.KeyTypeTable,
		keyTypeNextId:       opts.KeyTypeNextId,
		pendingWrites:       map[int]*pendingTableWrites{},
		lastFlushWriteTime:  time.Now(),
		pendingDeletes:      map[int]*pendingTableWrites{},
		lastFlushDeleteTime: time.Now(),
	}

	flushCtx, cancelFlush := context.WithCancel(context.Background())
	idx.cancelFlush = cancelFlush

	// Capture locals for the goroutine — must not read idx fields that Close mutates.
	localQ := q
	ticker := opts.flushTicker()
	go func() {
		timer := time.NewTicker(ticker)
		defer timer.Stop()

		for {
			select {
			case <-flushCtx.Done():
				return
			case <-timer.C:
				localQ.Add(&flushPendingWritesTask{
					idx:     idx,
					closing: false,
				})
			}
		}
	}()

	idx.merger = &keywordsMerger{idx: idx}
	idx.merger.Start()

	return idx, nil
}

// CloseAndWait stops the flush ticker, shuts down the merger, and performs a
// final flush — without holding any mutex across blocking waits.
func (idx *Index) CloseAndWait() {
	// 1. Signal the flush goroutine to stop (non-blocking).
	idx.cancelFlush()

	// 2. Shut down + wait for the merger (captures its own channels — safe).
	idx.merger.Shutdown()
	idx.merger.Wait()

	// 3. Final flush via the queue (synchronous RunTask, no mutex needed).
	idx.q.RunTask(&flushPendingWritesTask{
		idx:     idx,
		closing: true,
	})
}
