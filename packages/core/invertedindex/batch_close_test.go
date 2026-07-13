package invertedindex

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codetrek/haystack/packages/core/kv"
	"github.com/codetrek/haystack/packages/core/kv/pebblekv"
	"github.com/codetrek/haystack/packages/core/queue"
)

// recStore wraps a real kv.Store, delegating every method EXCEPT NewBatch, which
// returns a recBatch so the test can observe Close() on every batch created.
// Wrapping at the STORE captures both the three newBatch(idx.db) sites (flush
// writes, flush deletes, merge) AND DeleteTable's direct idx.db.NewBatch(0),
// which bypasses the newBatch package seam. batchCloses counts Close() across all
// batches it hands out.
type recStore struct {
	kv.Store
	batchCloses *atomic.Int64
}

func (s recStore) NewBatch(maxBatchSize int32) kv.Batch {
	return &recBatch{Batch: s.Store.NewBatch(maxBatchSize), closes: s.batchCloses}
}

// recBatch embeds a real kv.Batch and overrides Close to increment the shared
// counter (modelled on mockBatchWriteWithFuncs.Close) before delegating to the
// embedded real Close.
type recBatch struct {
	kv.Batch
	closes *atomic.Int64
}

func (b *recBatch) Close() error {
	b.closes.Add(1)
	return b.Batch.Close()
}

// TestBatchSitesCloseCommittedBatch proves every batch-creating site Closes its
// committed batch (returning it to pebble's pool). Driving forceFlush (flush
// writes + flush deletes), a synchronous merge, and DeleteTable exercises all
// four sites; a store-level recorder observes Close on each. Before I5 no site
// Closes (count 0 → fail); after I5 each op Closes at least once (count >= 4).
func TestBatchSitesCloseCommittedBatch(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-ii-batchclose-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(tempDir)

	real, err := pebblekv.Open(filepath.Join(tempDir, "data"), 0)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}

	var closes atomic.Int64
	store := recStore{Store: real, batchCloses: &closes}

	q := queue.NewMpsc("TestBatchCloseQueue")
	q.Start()

	// Reset the package test-injection seams to their production defaults so the
	// three newBatch sites actually reach recStore.NewBatch — prior tests
	// (setupTestEnv, newBoundedIndex, the merger tests) reassign them, and a
	// mutated newBatch seam would bypass recStore.NewBatch. DeleteTable's direct
	// NewBatch(0) is captured regardless.
	newBatch = func(db kv.Store) kv.Batch { return db.NewBatch(MaxBatchSize) }
	writeInvertedIndex = defaultWriteInvertedIndex

	// Disable the periodic ticker (FlushTicker = 1h) so the only batches created
	// are the ones this test drives explicitly — single-goroutine and race-free.
	idx, err := New(store, q, Options{FlushTicker: time.Hour})
	if err != nil {
		q.Stop()
		real.Close()
		t.Fatalf("new index: %v", err)
	}
	defer func() {
		idx.CloseAndWait()
		q.Stop()
		real.Close()
	}()

	const tableId = 7
	idx.updateIndex(tableId, makeDocID("d1"), []string{"kw"})

	// (a) forceFlush -> flushPendingWrites + flushPendingDeletes = 2 batches.
	forceFlush(idx)

	// (b) a synchronous merge via the queue = 1 batch. pendingWrites is now empty
	// (drained by forceFlush), so mergeKeywordTask.Run does not early-return.
	if err := idx.q.RunTask(&mergeKeywordTask{
		idx:     idx,
		merging: merging{NextIter: string(DefaultKeyTypeRow)},
	}); err != nil {
		t.Fatalf("merge RunTask: %v", err)
	}

	// (c) DeleteTable = 1 batch (idx.db.NewBatch(0)).
	if err := idx.DeleteTable(tableId); err != nil {
		t.Fatalf("DeleteTable: %v", err)
	}

	// Each of the four batch-creating ops must Close its committed batch. Before
	// I5 the count is 0; after I5 it is >= 1 per op (>= 4 total).
	if got := closes.Load(); got < 4 {
		t.Fatalf("batch Close count = %d, want >= 4 (one per flush-writes, flush-deletes, merge, DeleteTable)", got)
	}
}
