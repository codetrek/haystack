package idtable

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLookup covers the non-allocating Lookup path: an unknown key is found=false
// without allocating, a committed key resolves via the store, and Lookup fails
// closed after Close.
func TestLookup(t *testing.T) {
	store, err := openTestStore(t, t.TempDir())
	require.NoError(t, err)
	a, err := New(store, Options{CommitInterval: time.Hour})
	require.NoError(t, err)

	_, found, err := a.Lookup([]byte("missing"))
	require.NoError(t, err)
	assert.False(t, found, "never-allocated key must be found=false")
	assert.Equal(t, int64(1), a.nextId, "Lookup must not allocate")

	idStr, err := a.GetId([]byte("k"))
	require.NoError(t, err)
	require.NoError(t, a.Commit())
	a.lru.Delete("k") // force the store-read path

	id, found, err := a.Lookup([]byte("k"))
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, DecodeId(idStr), id)

	a.Close()
	if _, _, err := a.Lookup([]byte("k")); err == nil {
		t.Fatal("Lookup after Close must fail closed")
	}
}

// TestCrashRelease covers the crash-simulation path: it discards the uncommitted
// batch and detaches WITHOUT closing the caller's store. A fresh allocator over the
// same store must not see the dropped allocation, and the store stays usable.
func TestCrashRelease(t *testing.T) {
	store, err := openTestStore(t, t.TempDir())
	require.NoError(t, err)
	a, err := New(store, Options{CommitInterval: time.Hour}) // long interval: no auto-tick
	require.NoError(t, err)

	if _, err := a.GetId([]byte("uncommitted")); err != nil { // staged in the batch only
		t.Fatal(err)
	}
	a.CrashRelease() // discard the batch + detach; must NOT close the store

	assert.False(t, store.IsClosed(), "CrashRelease must not close the caller's store")

	a2, err := New(store, Options{CommitInterval: time.Hour})
	require.NoError(t, err)
	defer a2.Close()
	_, found, err := a2.Lookup([]byte("uncommitted"))
	require.NoError(t, err)
	assert.False(t, found, "an uncommitted allocation dropped by CrashRelease must not survive")

	assert.NotPanics(t, func() { a.CrashRelease() }, "CrashRelease must be safe to call again")
	require.NoError(t, store.Close())
}
