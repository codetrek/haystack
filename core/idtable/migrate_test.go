package idtable

import (
	"encoding/binary"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/codetrek/haystack/core/kv/pebblekv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeLegacyEntry writes a key→id mapping in the legacy kv layout:
// {LegacyKeyTypeKey}+rawKey -> 8-byte big-endian id.
func writeLegacyEntry(t *testing.T, store interface {
	Put(k, v []byte) error
}, rawKey []byte, id int64) {
	t.Helper()
	k := append([]byte{LegacyKeyTypeKey}, rawKey...)
	v := make([]byte, 8)
	binary.BigEndian.PutUint64(v, uint64(id))
	require.NoError(t, store.Put(k, v))
}

// TestMigrateFromKV copies a legacy kv-backed idtable into a bbolt file and
// verifies the migrated mappings + nextId are served by an Allocator opened on it.
func TestMigrateFromKV(t *testing.T) {
	dir := t.TempDir()
	store, err := pebblekv.Open(filepath.Join(dir, "data"), 0)
	require.NoError(t, err)

	// Seed legacy entries + a legacy decimal-string nextId.
	writeLegacyEntry(t, store, []byte("alpha.go"), 1)
	writeLegacyEntry(t, store, []byte("beta.go"), 2)
	writeLegacyEntry(t, store, []byte("dir/gamma.go"), 3)
	require.NoError(t, store.Put([]byte{LegacyKeyTypeNextId}, []byte(strconv.FormatInt(4, 10))))

	dstPath := filepath.Join(dir, "idtable.db")
	require.NoError(t, MigrateFromKV(store, dstPath, LegacyKeyTypeKey, LegacyKeyTypeNextId))
	require.NoError(t, store.Close())

	alloc, err := Open(dstPath, Options{CommitInterval: time.Hour})
	require.NoError(t, err)
	defer alloc.Close()

	// Migrated nextId carried over.
	assert.Equal(t, int64(4), alloc.nextId)

	// Migrated keys resolve to their original ids without allocating.
	for raw, want := range map[string]int64{"alpha.go": 1, "beta.go": 2, "dir/gamma.go": 3} {
		id, found, err := alloc.Lookup([]byte(raw))
		require.NoError(t, err)
		assert.True(t, found, "migrated key %q must be found", raw)
		assert.Equal(t, want, id, "migrated key %q id", raw)
	}
	assert.Equal(t, int64(4), alloc.nextId, "Lookup must not allocate")

	// A brand-new key gets the next id (4).
	got, err := alloc.GetId([]byte("new.go"))
	require.NoError(t, err)
	assert.Equal(t, uint64(4), binary.BigEndian.Uint64([]byte(got)))
}

// TestMigrateFromKV_Idempotent verifies a second migration is a no-op and does
// not clobber entries written after the first migration.
func TestMigrateFromKV_Idempotent(t *testing.T) {
	dir := t.TempDir()
	store, err := pebblekv.Open(filepath.Join(dir, "data"), 0)
	require.NoError(t, err)
	writeLegacyEntry(t, store, []byte("alpha.go"), 1)
	require.NoError(t, store.Put([]byte{LegacyKeyTypeNextId}, []byte("2")))

	dstPath := filepath.Join(dir, "idtable.db")
	require.NoError(t, MigrateFromKV(store, dstPath, LegacyKeyTypeKey, LegacyKeyTypeNextId))

	// Allocate a new key into the bbolt file after migration.
	alloc, err := Open(dstPath, Options{CommitInterval: time.Hour})
	require.NoError(t, err)
	newId, err := alloc.GetId([]byte("postmigrate.go"))
	require.NoError(t, err)
	require.NoError(t, alloc.Commit())
	alloc.Close()

	// Re-run migration: it must skip (dst already populated), leaving the
	// post-migration allocation intact.
	require.NoError(t, MigrateFromKV(store, dstPath, LegacyKeyTypeKey, LegacyKeyTypeNextId))
	require.NoError(t, store.Close())

	alloc2, err := Open(dstPath, Options{CommitInterval: time.Hour})
	require.NoError(t, err)
	defer alloc2.Close()
	got, found, err := alloc2.Lookup([]byte("postmigrate.go"))
	require.NoError(t, err)
	assert.True(t, found, "post-migration entry must survive a second migration")
	assert.Equal(t, newId, EncodeId(got))
}

// TestMigrateFromKV_EmptySource migrates an empty legacy store: nextId defaults
// to 1 and the allocator starts fresh.
func TestMigrateFromKV_EmptySource(t *testing.T) {
	dir := t.TempDir()
	store, err := pebblekv.Open(filepath.Join(dir, "data"), 0)
	require.NoError(t, err)

	dstPath := filepath.Join(dir, "idtable.db")
	require.NoError(t, MigrateFromKV(store, dstPath, LegacyKeyTypeKey, LegacyKeyTypeNextId))
	require.NoError(t, store.Close())

	alloc, err := Open(dstPath, Options{CommitInterval: time.Hour})
	require.NoError(t, err)
	defer alloc.Close()
	assert.Equal(t, int64(1), alloc.nextId)

	got, err := alloc.GetId([]byte("first.go"))
	require.NoError(t, err)
	assert.Equal(t, uint64(1), binary.BigEndian.Uint64([]byte(got)))
}

// TestMigrateFromKV_NilSource guards the nil-store input.
func TestMigrateFromKV_NilSource(t *testing.T) {
	err := MigrateFromKV(nil, filepath.Join(t.TempDir(), "idtable.db"), LegacyKeyTypeKey, LegacyKeyTypeNextId)
	assert.Error(t, err)
}
