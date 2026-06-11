package invertedindex

import (
	"context"
	"time"

	"github.com/codetrek/haystack/searchcore/kv"
	"github.com/codetrek/haystack/searchcore/queue"
)

// Options holds tunables for the Index.
// Zero values select sensible defaults.
type Options struct {
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

	// FlushCooldown is the minimum interval between flush passes.
	// Default: 1 second.
	FlushCooldown time.Duration

	// MaxInvertedIndexSize caps the number of doc-ids stored per index row.
	// Default: 1000.
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
	return 1000
}

// Index is the instance-based inverted index.
type Index struct {
	db   kv.Store
	q    queue.Queue
	opts Options

	// pending writes/deletes — accessed only from the single-threaded mpsc queue
	pendingWrites      map[int]*PendingTableWrites
	lastFlushWriteTime time.Time

	pendingDeletes      map[int]*PendingTableWrites
	lastFlushDeleteTime time.Time

	// flush goroutine lifecycle
	cancelFlush context.CancelFunc

	// keywords merger
	merger *KeywordsMerger
}

// New creates and starts a new Index.
func New(store kv.Store, q queue.Queue, opts Options) (*Index, error) {
	idx := &Index{
		db:                  store,
		q:                   q,
		opts:                opts,
		pendingWrites:       map[int]*PendingTableWrites{},
		lastFlushWriteTime:  time.Now(),
		pendingDeletes:      map[int]*PendingTableWrites{},
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

	idx.merger = &KeywordsMerger{idx: idx}
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
