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
		batch.Put(key, encodeInvertedValue(docids))
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
		bounded.Add(1, int64(i), []string{"kw"})
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
		unbounded.Add(1, int64(i), []string{"kw"})
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

// ---------------------------------------------------------------------------
// Flush age heuristic (I2): UpdatedAt as int64 unix-nanos. These pin BOTH
// directions of the edited age comparison on the write AND delete sides via
// periodic flushes (false,false) — never forceFlush, whose force=true short-
// circuits the age term. The aged cases direct-seed UpdatedAt as an int64, which
// is the compile RED until relatedDocs.UpdatedAt becomes int64, and pin the
// sign + nanosecond scale of the age math.
// ---------------------------------------------------------------------------

// TestFlushWriteYoungEntrySurvives seeds the write cache via the PRODUCTION
// updateIndex path (pinning the .UnixNano() scale end-to-end) and asserts a
// young, sub-batch-size entry is SKIPPED by a periodic flush (the count=0 skip
// branch at pending_writes.go:104).
func TestFlushWriteYoungEntrySurvives(t *testing.T) {
	// FlushWaitTimeout=1h makes "young" deterministic: the seed->flush gap can
	// never exceed 1h, so a scheduler stall (slow/loaded CI) cannot age the
	// entry out and drain it (kills the flake). The .UnixNano() scale stays
	// pinned despite the large timeout: a .Unix() (seconds) scale bug would make
	// nowNanos-UpdatedAt ~1.75e18 ns (~55 years) >> 1h -> the entry would drain
	// -> this survives-assert would fail.
	idx, cleanup := newBoundedIndex(t, Options{FlushWaitTimeout: time.Hour})
	defer cleanup()

	idx.updateIndex(7, makeDocID("d1"), []string{"kw"})

	// Cooldown satisfied so the flush actually runs; the entry is still young.
	idx.lastFlushWriteTime = time.Now().Add(-time.Hour)
	idx.flushPendingWrites(false, false)

	wp := idx.pendingWrites[7]
	if wp == nil {
		t.Fatalf("expected pending write table to survive a periodic flush, got nil")
	}
	if _, ok := wp.InvertedIndex["kw"]; !ok {
		t.Fatalf("expected young entry 'kw' to survive a periodic flush")
	}
}

// TestFlushWriteAgedEntryDrains direct-seeds a small write entry with UpdatedAt
// WELL past the 3s write timeout (as int64 nanos) and asserts a periodic flush
// DRAINS it — the FALSE branch that pins the age math's sign and nanosecond
// scale (a swapped operand or seconds-vs-nanos regression fails here).
func TestFlushWriteAgedEntryDrains(t *testing.T) {
	idx, cleanup := newBoundedIndex(t, Options{} /* unbounded, ticker disabled */)
	defer cleanup()

	docid := makeDocID("d1")
	wp := idx.getPendingWrite(7)
	wp.InvertedIndex["kw"] = relatedDocs{
		DocIds:    []int64{docid},
		UpdatedAt: time.Now().Add(-time.Hour).UnixNano(),
	}
	idx.pendingWritePostings += 1 // mirror what updateIndex would have bumped

	idx.lastFlushWriteTime = time.Now().Add(-time.Hour) // cooldown satisfied
	pre := idx.pendingWritePostings
	idx.flushPendingWrites(false, false)

	if wp2 := idx.pendingWrites[7]; wp2 != nil {
		if _, ok := wp2.InvertedIndex["kw"]; ok {
			t.Fatalf("expected aged entry 'kw' to drain on a periodic flush")
		}
	}
	if idx.pendingWritePostings >= pre {
		t.Fatalf("expected write posting counter to drop after drain: pre=%d post=%d", pre, idx.pendingWritePostings)
	}
	if got := searchDocCount(idx, 7, "kw"); got != 1 {
		t.Fatalf("expected drained entry written to the store, Search found %d, want 1", got)
	}
}

// TestFlushDeleteYoungEntrySurvives seeds the delete cache via the PRODUCTION
// removeIndex path and asserts a young, sub-batch-size entry is SKIPPED by a
// periodic flush (the count=0 skip branch at pending_writes.go:159).
func TestFlushDeleteYoungEntrySurvives(t *testing.T) {
	// FlushDeleteWaitTimeout=1h makes "young" deterministic: the seed->flush gap
	// can never exceed 1h, so a scheduler stall (slow/loaded CI) cannot age the
	// entry out and drain it (kills the flake). The .UnixNano() scale stays
	// pinned despite the large timeout: a .Unix() (seconds) scale bug would make
	// nowNanos-UpdatedAt ~1.75e18 ns (~55 years) >> 1h -> the entry would drain
	// -> this survives-assert would fail.
	idx, cleanup := newBoundedIndex(t, Options{FlushDeleteWaitTimeout: time.Hour})
	defer cleanup()

	idx.removeIndex(7, makeDocID("d1"), []string{"kw"})

	idx.lastFlushDeleteTime = time.Now().Add(-time.Hour) // cooldown satisfied
	idx.flushPendingDeletes(false, false, MaxInvertedIndexSize)

	wp := idx.pendingDeletes[7]
	if wp == nil {
		t.Fatalf("expected pending delete table to survive a periodic flush, got nil")
	}
	if _, ok := wp.InvertedIndex["kw"]; !ok {
		t.Fatalf("expected young delete entry 'kw' to survive a periodic flush")
	}
}

// TestFlushDeleteAgedEntryDrains seeds a doc in the store, then direct-seeds a
// small delete entry with UpdatedAt WELL past the 5s delete timeout (int64
// nanos) and asserts a periodic flush DRAINS it and removes the doc from the
// store — the delete-side FALSE branch pinning sign + nanosecond scale.
func TestFlushDeleteAgedEntryDrains(t *testing.T) {
	idx, cleanup := newBoundedIndex(t, Options{} /* unbounded, ticker disabled */)
	defer cleanup()

	docid := makeDocID("d1")
	idx.updateIndex(7, docid, []string{"kw"})
	forceFlush(idx)
	if got := searchDocCount(idx, 7, "kw"); got != 1 {
		t.Fatalf("seed: expected 1 doc in store, got %d", got)
	}

	wp := idx.getPendingDelete(7)
	wp.InvertedIndex["kw"] = relatedDocs{
		DocIds:    []int64{docid},
		UpdatedAt: time.Now().Add(-time.Hour).UnixNano(),
	}
	idx.pendingDeletePostings += 1 // mirror what removeIndex would have bumped

	idx.lastFlushDeleteTime = time.Now().Add(-time.Hour) // cooldown satisfied
	pre := idx.pendingDeletePostings
	idx.flushPendingDeletes(false, false, MaxInvertedIndexSize)

	if wp2 := idx.pendingDeletes[7]; wp2 != nil {
		if _, ok := wp2.InvertedIndex["kw"]; ok {
			t.Fatalf("expected aged delete entry 'kw' to drain on a periodic flush")
		}
	}
	if idx.pendingDeletePostings >= pre {
		t.Fatalf("expected delete posting counter to drop after drain: pre=%d post=%d", pre, idx.pendingDeletePostings)
	}
	if got := searchDocCount(idx, 7, "kw"); got != 0 {
		t.Fatalf("expected doc removed from the store after aged delete drain, got %d, want 0", got)
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
		idx.Add(1, int64(i), []string{"kw"})
	}
	forceFlush(idx)
	if got := searchDocCount(idx, 1, "kw"); got != n {
		t.Fatalf("seed: Search found %d, want %d", got, n)
	}

	// Delete them one by one (newKeywords=nil routes to removeIndex). Crossing
	// the bound must force a delete flush, so the buffered deletes stay bounded
	// and removals reach the store without an explicit flush.
	for i := 0; i < n; i++ {
		idx.Delete(1, int64(i))
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
