package idtable

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/codetrek/haystack/core/kv/pebblekv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCommit_DurableBeforeClose covers the public synchronous Commit: it must
// flush the pending key→id batch + nextId counter to the KV (fsync'd) so the
// allocations survive a crash that never reaches Close. A long CommitInterval
// keeps the background tick from firing, so Commit is the only flush.
func TestCommit_DurableBeforeClose(t *testing.T) {
	dir := t.TempDir()
	store, err := pebblekv.Open(filepath.Join(dir, "data"), 0)
	require.NoError(t, err)
	defer store.Close()

	alloc, err := New(store, Options{CommitInterval: time.Hour})
	require.NoError(t, err)
	id, err := alloc.GetId([]byte("alpha")) // stages key→id + nextId into the batch
	require.NoError(t, err)
	require.Len(t, id, 8)

	// Synchronous Commit makes it durable WITHOUT Close.
	require.NoError(t, alloc.Commit())
	// A second Commit with an empty batch is a no-op (no error).
	require.NoError(t, alloc.Commit())

	// Reopen over the same store (no Close of alloc) — the mapping must be present.
	alloc2, err := New(store, Options{CommitInterval: time.Hour})
	require.NoError(t, err)
	got, err := alloc2.GetId([]byte("alpha"))
	require.NoError(t, err)
	assert.Equal(t, id, got, "id must survive via Commit, not Close")
	alloc2.Close()
}

// TestCommit_OnClosedStoreErrors covers Commit's closed-store guard.
func TestCommit_OnClosedStoreErrors(t *testing.T) {
	dir := t.TempDir()
	store, err := pebblekv.Open(filepath.Join(dir, "data"), 0)
	require.NoError(t, err)

	alloc, err := New(store, Options{CommitInterval: time.Hour})
	require.NoError(t, err)
	require.NoError(t, store.Close()) // close the backing store under the allocator

	if err := alloc.Commit(); err == nil {
		t.Fatal("Commit on a closed store should error")
	}
	alloc.Close()
}
