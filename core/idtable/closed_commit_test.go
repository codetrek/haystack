package idtable

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTryCommitAfterClose verifies the closed-state guard on the periodic-commit
// path. With the standalone bbolt backend the Allocator OWNS its database and
// Close stops the commit ticker BEFORE closing the db, so the old "tick commits
// against a store closed out from under us" race (which panicked "pebble: closed"
// under -race) is structurally impossible. This pins the remaining guard: driving
// tryCommit after Close returns a clean error and never panics.
func TestTryCommitAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idtable.db")
	alloc, err := Open(path, Options{CommitInterval: time.Hour})
	require.NoError(t, err)

	// Stage a pending allocation so tryCommit would have work to flush.
	_, err = alloc.GetId([]byte("alpha"))
	require.NoError(t, err)
	require.Greater(t, len(alloc.pending), 0, "pending must be non-empty to exercise the flush path")

	alloc.Close() // stops the ticker, flushes, then closes the bbolt db

	// Drive the exact periodic-tick code path after Close. It must return a clean
	// error (db is closed) and must not panic.
	require.NotPanics(t, func() {
		alloc.mu.Lock()
		defer alloc.mu.Unlock()
		if err := alloc.tryCommit(); err == nil {
			t.Error("tryCommit after Close should return an error")
		}
	}, "tryCommit after Close must not panic")

	// A second Close is also safe.
	require.NotPanics(t, func() { alloc.Close() }, "double Close must not panic")
}

// TestPeriodicTickWithTinyInterval runs the real background ticker at a tiny
// interval while allocations are staged, then Closes. Close must coordinate the
// ticker shutdown cleanly (no panic, no deadlock) — the bbolt backend owns its
// db so there is no external close to race.
func TestPeriodicTickWithTinyInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idtable.db")
	alloc, err := Open(path, Options{CommitInterval: time.Millisecond})
	require.NoError(t, err)

	_, err = alloc.GetId([]byte("beta"))
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond) // let the ticker fire many times

	require.NotPanics(t, func() { alloc.Close() }, "Close during active ticking must not panic")
}
