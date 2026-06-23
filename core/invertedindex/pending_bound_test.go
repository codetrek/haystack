package invertedindex

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetrek/haystack/core/kv"
	"github.com/codetrek/haystack/core/kv/pebblekv"
	"github.com/codetrek/haystack/core/queue"
)

// newBoundedIndex builds an index with caller-supplied Options and the flush
// ticker effectively disabled (FlushTicker = 1h), so the ONLY flushes are the
// forced memory-pressure flush under test and explicit forceFlush/Close. That
// makes the whole test single-goroutine and race-free (no ticker concurrently
// touching pendingWrites). Returns the index and a cleanup func.
func newBoundedIndex(t *testing.T, opts Options) (*Index, func()) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "haystack-ii-bound-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	db, err := pebblekv.Open(filepath.Join(tempDir, "data"), 0)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("open pebble: %v", err)
	}
	q := queue.NewMpsc("TestBoundQueue")
	q.Start()

	// Reset the package test-injection seams (mirrors setupTestEnv).
	newBatch = func(db kv.Store) kv.Batch { return db.NewBatch(MaxBatchSize) }
	writeInvertedIndex = func(batch kv.Batch, tableId int, kw string, docids []int64, key []byte) {
		batch.Put(key, encodeInvertedValue(removeDuplicatesEfficiently(docids)))
	}

	opts.FlushTicker = time.Hour // disable the periodic ticker for the test
	idx, err := New(db, q, opts)
	if err != nil {
		q.Stop()
		db.Close()
		os.RemoveAll(tempDir)
		t.Fatalf("new index: %v", err)
	}
	cleanup := func() {
		idx.CloseAndWait()
		q.Stop()
		db.Close()
		os.RemoveAll(tempDir)
	}
	return idx, cleanup
}

// searchDocCount returns how many docids Search finds for an exact keyword. With
// the ticker disabled, a non-zero count BEFORE any forceFlush proves the data was
// flushed to the store by the memory-pressure path (Search reads the store only,
// not the in-memory buffer).
func searchDocCount(idx *Index, tableId int, kw string) int {
	return len(idx.Search(tableId, kw, -1, nil).DocIds)
}

// TestMaxPendingPostingsForcesFlush pins the core behavior: once buffered postings
// reach the bound, a forced flush drains the cache to the store WITHOUT an explicit
// flush — whereas the unbounded index keeps everything in RAM until forceFlush.
func TestMaxPendingPostingsForcesFlush(t *testing.T) {
	const n = 20

	// Bounded: a tiny budget. Each Update adds 1 docid to "kw"; crossing the
	// bound must flush, so the docs become visible to Search before forceFlush.
	bounded, cleanup := newBoundedIndex(t, Options{MaxPendingPostings: 4})
	defer cleanup()
	for i := 0; i < n; i++ {
		bounded.Update(1, int64(i), []string{"kw"}, nil)
	}
	// The bound guarantees the buffer never holds more than (bound + one update's
	// worth) docids; with single-keyword updates that is < bound+1.
	if bounded.pendingWritePostings > 4 {
		t.Fatalf("bounded: pendingWritePostings=%d exceeded the bound 4", bounded.pendingWritePostings)
	}
	if got := searchDocCount(bounded, 1, "kw"); got == 0 {
		t.Fatalf("bounded: expected docs flushed by memory pressure, Search found 0")
	}

	// Unbounded control: nothing reaches the store until an explicit flush.
	unbounded, cleanup2 := newBoundedIndex(t, Options{} /* zero value: unbounded (opt-in default) */)
	defer cleanup2()
	for i := 0; i < n; i++ {
		unbounded.Update(1, int64(i), []string{"kw"}, nil)
	}
	if unbounded.pendingWritePostings != n {
		t.Fatalf("unbounded: pendingWritePostings=%d, want %d (no forced flush)", unbounded.pendingWritePostings, n)
	}
	if got := searchDocCount(unbounded, 1, "kw"); got != 0 {
		t.Fatalf("unbounded: expected 0 before flush (buffer not searchable), got %d", got)
	}

	// After an explicit flush both must agree on the full, correct result set.
	forceFlush(unbounded)
	forceFlush(bounded)
	if a, b := searchDocCount(bounded, 1, "kw"), searchDocCount(unbounded, 1, "kw"); a != n || b != n {
		t.Fatalf("final result mismatch: bounded=%d unbounded=%d want %d", a, b, n)
	}
}

// TestMaxPendingPostingsAccessor covers the opt-in Option resolution: any
// non-positive value (including the zero value) is unbounded (0); a positive
// value is used verbatim.
func TestMaxPendingPostingsAccessor(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, 0},  // zero value -> unbounded (opt-in default)
		{-1, 0}, // any non-positive -> unbounded
		{5, 5},
	}
	for _, c := range cases {
		if got := (&Options{MaxPendingPostings: c.in}).maxPendingPostings(); got != c.want {
			t.Fatalf("maxPendingPostings(%d)=%d, want %d", c.in, got, c.want)
		}
	}
}

// TestClearPendingWritesKeepsCounters pins that dropping a table's caches (table
// delete) subtracts its buffered docs from the global counters so they stay
// accurate (no leak that would wrongly trip the pressure flush later).
func TestClearPendingWritesKeepsCounters(t *testing.T) {
	idx, cleanup := newBoundedIndex(t, Options{} /* zero value: unbounded (opt-in default) */) // unbounded: keep docs buffered
	defer cleanup()

	idx.updateIndex(7, 1, []string{"a", "b", "c"}) // +3 writes
	idx.removeIndex(7, 2, []string{"x", "y"})      // +2 deletes
	if idx.pendingWritePostings != 3 || idx.pendingDeletePostings != 2 {
		t.Fatalf("before clear: write=%d delete=%d, want 3/2", idx.pendingWritePostings, idx.pendingDeletePostings)
	}

	idx.clearPendingWrites(7)
	if idx.pendingWritePostings != 0 || idx.pendingDeletePostings != 0 {
		t.Fatalf("after clear: write=%d delete=%d, want 0/0", idx.pendingWritePostings, idx.pendingDeletePostings)
	}
}

// TestMaxPendingPostingsForcesDeleteFlush covers the delete-side pressure branch:
// buffered deletes crossing the bound trigger a forced flushPendingDeletes that
// applies the removals to the store without an explicit flush.
func TestMaxPendingPostingsForcesDeleteFlush(t *testing.T) {
	const n = 20
	idx, cleanup := newBoundedIndex(t, Options{MaxPendingPostings: 4})
	defer cleanup()

	// Seed and flush n docs under "kw".
	for i := 0; i < n; i++ {
		idx.Update(1, int64(i), []string{"kw"}, nil)
	}
	forceFlush(idx)
	if got := searchDocCount(idx, 1, "kw"); got != n {
		t.Fatalf("seed: Search found %d, want %d", got, n)
	}

	// Delete them one by one (newKeywords=nil routes to removeIndex). Crossing
	// the bound must force a delete flush, so the buffered deletes stay bounded
	// and removals reach the store without an explicit flush.
	for i := 0; i < n; i++ {
		idx.Update(1, int64(i), nil, []string{"kw"})
	}
	if idx.pendingDeletePostings > 4 {
		t.Fatalf("pendingDeletePostings=%d exceeded the bound 4", idx.pendingDeletePostings)
	}
	if got := searchDocCount(idx, 1, "kw"); got == n {
		t.Fatalf("expected memory-pressure delete flush to remove some docs, still %d", got)
	}

	// Final flush clears the remainder; nothing should be left.
	forceFlush(idx)
	if got := searchDocCount(idx, 1, "kw"); got != 0 {
		t.Fatalf("after final flush: Search found %d, want 0", got)
	}
}
