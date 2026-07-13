package idtable

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/codetrek/haystack/packages/core/kv/pebblekv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTryCommitOnClose deterministically exercises tryCommit's flush path. A
// long CommitInterval keeps the periodic-commit goroutine from firing, so the
// pending batch is still non-empty when Close() calls tryCommit — making the
// Commit/Reset path reliably covered (the default-interval tests race the
// background goroutine, which can empty the batch first).
func TestTryCommitOnClose(t *testing.T) {
	dir := t.TempDir()
	store, err := pebblekv.Open(filepath.Join(dir, "data"), 0)
	require.NoError(t, err)
	defer store.Close()

	alloc, err := New(store, Options{CommitInterval: time.Hour})
	require.NoError(t, err)
	id, err := alloc.GetId([]byte("alpha")) // queues writes into the batch
	require.NoError(t, err)
	require.Len(t, id, 8)
	alloc.Close() // tryCommit flushes the non-empty batch

	// Reopen: the id must have been persisted by the close-time commit.
	alloc2, err := New(store, Options{CommitInterval: time.Hour})
	require.NoError(t, err)
	got, err := alloc2.GetId([]byte("alpha"))
	require.NoError(t, err)
	assert.Equal(t, id, got, "id should survive the close-time commit")
	alloc2.Close()
}
