package idtable

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTryCommitOnClose deterministically exercises tryCommit's flush path. A
// long CommitInterval keeps the periodic-commit goroutine from firing, so the
// pending allocations are still present when Close() calls tryCommit — making
// the flush path reliably covered.
func TestTryCommitOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idtable.db")

	alloc, err := Open(path, Options{CommitInterval: time.Hour})
	require.NoError(t, err)
	id, err := alloc.GetId([]byte("alpha")) // stages a pending allocation
	require.NoError(t, err)
	require.Len(t, id, 8)
	alloc.Close() // tryCommit flushes the pending allocation

	// Reopen: the id must have been persisted by the close-time commit.
	alloc2, err := Open(path, Options{CommitInterval: time.Hour})
	require.NoError(t, err)
	got, err := alloc2.GetId([]byte("alpha"))
	require.NoError(t, err)
	assert.Equal(t, id, got, "id should survive the close-time commit")
	alloc2.Close()
}
