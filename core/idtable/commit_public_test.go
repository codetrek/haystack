package idtable

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCommit_DurableBeforeClose covers the public synchronous Commit: it must
// flush the pending key→id allocations + nextId counter to the bbolt db (fsync'd
// on transaction commit) so the allocations survive a crash that never reaches
// Close. A long CommitInterval keeps the background tick from firing, so Commit
// is the only flush.
func TestCommit_DurableBeforeClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idtable.db")

	alloc, err := Open(path, Options{CommitInterval: time.Hour})
	require.NoError(t, err)
	id, err := alloc.GetId([]byte("alpha")) // stages a pending allocation + nextId
	require.NoError(t, err)
	require.Len(t, id, 8)

	// Synchronous Commit makes it durable.
	require.NoError(t, alloc.Commit())
	require.Empty(t, alloc.pending, "Commit must drain the pending buffer")
	// A second Commit with nothing pending is a no-op (no error).
	require.NoError(t, alloc.Commit())

	// Close is now a no-op flush (pending already drained by Commit); reopen and
	// verify the mapping persisted via Commit.
	alloc.Close()
	alloc2, err := Open(path, Options{CommitInterval: time.Hour})
	require.NoError(t, err)
	got, err := alloc2.GetId([]byte("alpha"))
	require.NoError(t, err)
	assert.Equal(t, id, got, "id must survive via Commit")
	alloc2.Close()
}

// TestCommit_OnClosedStoreErrors covers Commit's closed-state guard.
func TestCommit_OnClosedStoreErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idtable.db")

	alloc, err := Open(path, Options{CommitInterval: time.Hour})
	require.NoError(t, err)
	alloc.Close() // close the allocator (and its bbolt db)

	if err := alloc.Commit(); err == nil {
		t.Fatal("Commit on a closed allocator should error")
	}
}
