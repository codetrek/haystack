package idtable

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/codetrek/haystack/packages/core/kv/pebblekv"
	"github.com/stretchr/testify/require"
)

// TestTryCommitAfterStoreClosed reproduces the -race BLOCKER: a periodic
// commit tick that fires AFTER the backing KV has been closed out from under
// the allocator (e.g. a test's t.Cleanup closes the store while a 5s tick is
// in flight) must NOT panic with "pebble: closed". tryCommit drives
// batch.Commit() on the now-closed pebble; without a closed-store re-check
// under the allocator lock it panics.
//
// We simulate the in-flight tick deterministically: stage a non-empty batch,
// close the store, then invoke the exact tick code path (tryCommit under
// a.mu). It must return a clean error (no panic, no flush against closed KV).
func TestTryCommitAfterStoreClosed(t *testing.T) {
	dir := t.TempDir()
	store, err := pebblekv.Open(filepath.Join(dir, "data"), 0)
	require.NoError(t, err)

	// Long interval so the real background tick never races this test; we drive
	// the tick path by hand.
	alloc, err := New(store, Options{CommitInterval: time.Hour})
	require.NoError(t, err)

	// Stage pending writes so the batch is non-empty (tryCommit would Commit).
	_, err = alloc.GetId([]byte("alpha"))
	require.NoError(t, err)
	require.Greater(t, alloc.batch.Count(), int32(0), "batch must be non-empty to exercise Commit")

	// Close the KV out from under the allocator (mimics external t.Cleanup).
	require.NoError(t, store.Close())
	require.True(t, store.IsClosed())

	// Drive the exact periodic-tick code path. Before the fix this panics with
	// "pebble: closed"; after the fix it must return without panicking and
	// without committing against the closed store.
	require.NotPanics(t, func() {
		alloc.mu.Lock()
		defer alloc.mu.Unlock()
		if err := alloc.tryCommit(); err != nil {
			// A clean error is acceptable; a panic is not.
			t.Logf("tryCommit returned clean error on closed store: %v", err)
		}
	}, "tryCommit on a closed store must not panic")

	// Close() must also be safe after the store is already closed.
	require.NotPanics(t, func() { alloc.Close() }, "Close after store close must not panic")
}

// TestPeriodicTickAfterStoreClosed exercises the real background ticker firing
// after the store is closed: with a tiny interval the ticker reliably fires
// while/after the KV is closed. It must not panic the process.
func TestPeriodicTickAfterStoreClosed(t *testing.T) {
	dir := t.TempDir()
	store, err := pebblekv.Open(filepath.Join(dir, "data"), 0)
	require.NoError(t, err)

	// Tiny interval so the ticker fires many times.
	alloc, err := New(store, Options{CommitInterval: time.Millisecond})
	require.NoError(t, err)

	// Stage pending writes so each tick has something to Commit.
	_, err = alloc.GetId([]byte("beta"))
	require.NoError(t, err)

	// Close the store under the allocator without calling alloc.Close() first,
	// then let the ticker fire repeatedly against the closed KV.
	require.NoError(t, store.Close())
	time.Sleep(20 * time.Millisecond) // many ticks cross the closed store

	// If a tick committed against the closed pebble, the process would have
	// already panicked. Reaching here and closing cleanly proves the fix.
	require.NotPanics(t, func() { alloc.Close() })
}
