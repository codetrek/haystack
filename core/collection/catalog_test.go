package collection_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetrek/haystack/core/collection"
	"github.com/codetrek/haystack/core/documents"
	"github.com/codetrek/haystack/core/invertedindex"
	"github.com/codetrek/haystack/core/kv/pebblekv"
	"github.com/codetrek/haystack/core/queue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Full integration helpers
// ---------------------------------------------------------------------------

type fullEnv struct {
	t       *testing.T
	tempDir string
	db      interface {
		Close() error
	}
	q       *queue.Mpsc
	idx     *invertedindex.Index
	docs    *documents.Store
	catalog *collection.Catalog
}

func setupFull(t *testing.T) *fullEnv {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "collection-test-*")
	require.NoError(t, err)

	db, err := pebblekv.Open(filepath.Join(tempDir, "data"), 0)
	require.NoError(t, err)

	q := queue.NewMpsc("collection-test")
	q.Start()

	idx, err := invertedindex.New(db, q, invertedindex.Options{})
	require.NoError(t, err)

	docs, err := documents.New(db, q, idx, documents.Options{})
	require.NoError(t, err)

	cat, err := collection.New(db, docs, collection.Options{})
	require.NoError(t, err)

	return &fullEnv{
		t:       t,
		tempDir: tempDir,
		db:      db,
		q:       q,
		idx:     idx,
		docs:    docs,
		catalog: cat,
	}
}

func (e *fullEnv) teardown() {
	e.t.Helper()
	e.docs.CloseAndWait()
	if e.idx != nil {
		e.idx.CloseAndWait()
	}
	e.q.Stop()
	e.db.Close()
	os.RemoveAll(e.tempDir)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestCreate_Basic verifies id allocation, persistence, and returned Collection.
func TestCreate_Basic(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	col, err := env.catalog.Create("/workspace/alpha")
	require.NoError(t, err)
	require.NotNil(t, col)

	assert.Equal(t, 0, col.ID())

	meta := col.Meta()
	require.NotNil(t, meta)
	assert.Equal(t, 0, meta.ID)
	assert.Equal(t, "/workspace/alpha", meta.Name)
	assert.False(t, meta.CreatedAt.IsZero(), "CreatedAt should be set")
	assert.False(t, meta.LastAccessed.IsZero(), "LastAccessed should be set")
}

// TestCreate_IdAllocation verifies that ids are allocated sequentially.
func TestCreate_IdAllocation(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	col1, err := env.catalog.Create("/workspace/one")
	require.NoError(t, err)

	col2, err := env.catalog.Create("/workspace/two")
	require.NoError(t, err)

	col3, err := env.catalog.Create("/workspace/three")
	require.NoError(t, err)

	assert.Equal(t, 0, col1.ID())
	assert.Equal(t, 1, col2.ID())
	assert.Equal(t, 2, col3.ID())
}

// TestCreate_DuplicateName verifies that duplicate names return an error.
func TestCreate_DuplicateName(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	_, err := env.catalog.Create("/workspace/dup")
	require.NoError(t, err)

	_, err = env.catalog.Create("/workspace/dup")
	assert.Error(t, err, "duplicate name must return error")
}

// TestCreate_DocTableCreated verifies that documents.Create was called
// (CountByCollection returns 0, not an error — meaning the collection exists).
func TestCreate_DocTableCreated(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	col, err := env.catalog.Create("/workspace/beta")
	require.NoError(t, err)

	// CountByCollection returns 0 for a brand-new collection (not an error).
	count := col.Count()
	assert.Equal(t, 0, count)
}

// TestGet_Found verifies Get by id returns the correct Collection.
func TestGet_Found(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	created, err := env.catalog.Create("/workspace/get-test")
	require.NoError(t, err)

	got, err := env.catalog.Get(created.ID())
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, created.ID(), got.ID())
	assert.Equal(t, "/workspace/get-test", got.Meta().Name)
}

// TestGet_NotFound verifies Get returns an error for unknown ids.
func TestGet_NotFound(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	_, err := env.catalog.Get(9999)
	assert.Error(t, err)
}

// TestGetByName_Found verifies GetByName returns the correct Collection.
func TestGetByName_Found(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	created, err := env.catalog.Create("/workspace/named")
	require.NoError(t, err)

	got, err := env.catalog.GetByName("/workspace/named")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, created.ID(), got.ID())
}

// TestGetByName_NotFound verifies GetByName returns an error for unknown names.
func TestGetByName_NotFound(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	_, err := env.catalog.GetByName("/workspace/does-not-exist")
	assert.Error(t, err)
}

// TestList verifies List returns all created collections.
func TestList(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	names := []string{"/a", "/b", "/c"}
	for _, n := range names {
		_, err := env.catalog.Create(n)
		require.NoError(t, err)
	}

	list := env.catalog.List()
	assert.Len(t, list, 3)

	listed := make(map[string]bool)
	for _, r := range list {
		listed[r.Name] = true
	}
	for _, n := range names {
		assert.True(t, listed[n], "expected %s in list", n)
	}
}

// TestDelete verifies Delete removes the record and doc data.
func TestDelete(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	col, err := env.catalog.Create("/workspace/to-delete")
	require.NoError(t, err)
	id := col.ID()

	err = env.catalog.Delete(id)
	require.NoError(t, err)

	// Get should now fail.
	_, err = env.catalog.Get(id)
	assert.Error(t, err, "Get after Delete should return error")

	// GetByName should also fail.
	_, err = env.catalog.GetByName("/workspace/to-delete")
	assert.Error(t, err, "GetByName after Delete should return error")

	// List should be empty.
	assert.Len(t, env.catalog.List(), 0)
}

// TestDelete_NotFound verifies Delete returns an error for unknown ids.
func TestDelete_NotFound(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	err := env.catalog.Delete(9999)
	assert.Error(t, err)
}

// TestSave_UpdateNameAndExtra verifies Save persists updates to Name/Extra.
func TestSave_UpdateNameAndExtra(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	col, err := env.catalog.Create("/workspace/original")
	require.NoError(t, err)

	r := col.Meta()
	r.Name = "/workspace/renamed"
	r.Extra = []byte(`{"filter":"*.go"}`)

	err = env.catalog.Save(r)
	require.NoError(t, err)

	// Should be findable by new name.
	got, err := env.catalog.GetByName("/workspace/renamed")
	require.NoError(t, err)
	assert.Equal(t, col.ID(), got.ID())
	assert.Equal(t, []byte(`{"filter":"*.go"}`), got.Meta().Extra)

	// Old name must be gone.
	_, err = env.catalog.GetByName("/workspace/original")
	assert.Error(t, err)
}

// TestSave_NotFound verifies Save returns an error when the id doesn't exist.
func TestSave_NotFound(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	r := &collection.Record{ID: 9999, Name: "/workspace/ghost"}
	err := env.catalog.Save(r)
	assert.Error(t, err)
}

// TestIdContinuation verifies that New reloads existing records and the next
// Create continues the id counter.
func TestIdContinuation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "collection-idcont-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "data")

	// --- First lifecycle ---
	{
		db, err := pebblekv.Open(dbPath, 0)
		require.NoError(t, err)
		q := queue.NewMpsc("cont-q")
		q.Start()
		idx, err := invertedindex.New(db, q, invertedindex.Options{})
		require.NoError(t, err)
		docs, err := documents.New(db, q, idx, documents.Options{})
		require.NoError(t, err)
		cat, err := collection.New(db, docs, collection.Options{})
		require.NoError(t, err)

		col1, err := cat.Create("/workspace/first")
		require.NoError(t, err)
		assert.Equal(t, 0, col1.ID())

		col2, err := cat.Create("/workspace/second")
		require.NoError(t, err)
		assert.Equal(t, 1, col2.ID())

		docs.CloseAndWait()
		idx.CloseAndWait()
		q.Stop()
		db.Close()
	}

	// --- Second lifecycle (reopen) ---
	{
		db, err := pebblekv.Open(dbPath, 0)
		require.NoError(t, err)
		q := queue.NewMpsc("cont-q2")
		q.Start()
		idx, err := invertedindex.New(db, q, invertedindex.Options{})
		require.NoError(t, err)
		docs, err := documents.New(db, q, idx, documents.Options{})
		require.NoError(t, err)
		cat, err := collection.New(db, docs, collection.Options{})
		require.NoError(t, err)

		// Existing records should be in-memory after reload.
		assert.Len(t, cat.List(), 2)

		// Next Create should get id 2 (not 0).
		col3, err := cat.Create("/workspace/third")
		require.NoError(t, err)
		assert.Equal(t, 2, col3.ID())

		docs.CloseAndWait()
		idx.CloseAndWait()
		q.Stop()
		db.Close()
	}
}

// TestReload verifies New rebuilds the in-memory index from persisted records.
func TestReload(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "collection-reload-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "data")

	var savedID int

	// --- Write phase ---
	{
		db, err := pebblekv.Open(dbPath, 0)
		require.NoError(t, err)
		q := queue.NewMpsc("reload-q")
		q.Start()
		idx, err := invertedindex.New(db, q, invertedindex.Options{})
		require.NoError(t, err)
		docs, err := documents.New(db, q, idx, documents.Options{})
		require.NoError(t, err)
		cat, err := collection.New(db, docs, collection.Options{})
		require.NoError(t, err)

		col, err := cat.Create("/workspace/persist-me")
		require.NoError(t, err)
		savedID = col.ID()

		docs.CloseAndWait()
		idx.CloseAndWait()
		q.Stop()
		db.Close()
	}

	// --- Reload phase ---
	{
		db, err := pebblekv.Open(dbPath, 0)
		require.NoError(t, err)
		q := queue.NewMpsc("reload-q2")
		q.Start()
		idx, err := invertedindex.New(db, q, invertedindex.Options{})
		require.NoError(t, err)
		docs, err := documents.New(db, q, idx, documents.Options{})
		require.NoError(t, err)
		cat, err := collection.New(db, docs, collection.Options{})
		require.NoError(t, err)

		got, err := cat.Get(savedID)
		require.NoError(t, err)
		assert.Equal(t, "/workspace/persist-me", got.Meta().Name)

		got2, err := cat.GetByName("/workspace/persist-me")
		require.NoError(t, err)
		assert.Equal(t, savedID, got2.ID())

		docs.CloseAndWait()
		idx.CloseAndWait()
		q.Stop()
		db.Close()
	}
}

// TestExtraRoundTrip verifies arbitrary bytes in Extra persist and reload correctly.
func TestExtraRoundTrip(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "collection-extra-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "data")

	extra := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE}

	var savedID int

	// Write.
	{
		db, err := pebblekv.Open(dbPath, 0)
		require.NoError(t, err)
		q := queue.NewMpsc("extra-q")
		q.Start()
		idx, err := invertedindex.New(db, q, invertedindex.Options{})
		require.NoError(t, err)
		docs, err := documents.New(db, q, idx, documents.Options{})
		require.NoError(t, err)
		cat, err := collection.New(db, docs, collection.Options{})
		require.NoError(t, err)

		col, err := cat.Create("/workspace/extra-test")
		require.NoError(t, err)
		savedID = col.ID()

		r := col.Meta()
		r.Extra = extra
		err = cat.Save(r)
		require.NoError(t, err)

		docs.CloseAndWait()
		idx.CloseAndWait()
		q.Stop()
		db.Close()
	}

	// Reload & verify.
	{
		db, err := pebblekv.Open(dbPath, 0)
		require.NoError(t, err)
		q := queue.NewMpsc("extra-q2")
		q.Start()
		idx, err := invertedindex.New(db, q, invertedindex.Options{})
		require.NoError(t, err)
		docs, err := documents.New(db, q, idx, documents.Options{})
		require.NoError(t, err)
		cat, err := collection.New(db, docs, collection.Options{})
		require.NoError(t, err)

		got, err := cat.Get(savedID)
		require.NoError(t, err)
		assert.Equal(t, extra, got.Meta().Extra)

		docs.CloseAndWait()
		idx.CloseAndWait()
		q.Stop()
		db.Close()
	}
}

// TestKeyTypeDefaults verifies zero Options selects bytes 1/2.
func TestKeyTypeDefaults(t *testing.T) {
	// Verify via the exported constants.
	assert.Equal(t, byte(1), collection.DefaultKeyTypeIncrId)
	assert.Equal(t, byte(2), collection.DefaultKeyTypeRecord)
}

// TestKeyTypeZeroMeansDefault verifies that passing zero Options applies defaults.
func TestKeyTypeZeroMeansDefault(t *testing.T) {
	env := setupFull(t) // uses Options{} — all zeros
	defer env.teardown()

	// If key bytes were wrong, Create would fail or List would be empty after reload.
	col, err := env.catalog.Create("/workspace/defaults-check")
	require.NoError(t, err)
	assert.Equal(t, 0, col.ID())
	assert.Len(t, env.catalog.List(), 1)
}

// TestCollection_DocumentDelegation verifies Collection delegates Save/Update/Count/ScanFiles.
func TestCollection_DocumentDelegation(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	col, err := env.catalog.Create("/workspace/docs-test")
	require.NoError(t, err)

	assert.Equal(t, 0, col.Count())

	docs := []*documents.Document{
		{ID: "file1.go", RelPath: "file1.go", Words: []string{"hello", "world"}},
		{ID: "file2.go", RelPath: "file2.go", Words: []string{"foo", "bar"}},
	}

	err = col.Save(docs)
	require.NoError(t, err)

	assert.Equal(t, 2, col.Count())

	// GetDocument
	d, err := col.GetDocument("file1.go")
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Equal(t, "file1.go", d.RelPath)

	// DeleteDocument
	err = col.DeleteDocument("file1.go")
	require.NoError(t, err)
	assert.Equal(t, 1, col.Count())

	// ScanFiles
	var found []string
	col.ScanFiles(func(docid, relPath string) bool {
		found = append(found, docid)
		return true
	})
	assert.Equal(t, []string{"file2.go"}, found)
}

// TestCollection_Update verifies Collection.Update delegates to
// documents.UpdateDocuments and persists the new document (metadata). The
// keyword re-index on Update is covered deterministically in
// invertedindex/forward_test.go (TestForward_UpdateDiffsAgainstStoredSet).
func TestCollection_Update(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	col, err := env.catalog.Create("/workspace/update-test")
	require.NoError(t, err)

	initial := []*documents.Document{
		{ID: "doc1.go", RelPath: "doc1.go", Size: 10, Hash: "h1", Words: []string{"alpha", "beta"}},
	}
	require.NoError(t, col.Save(initial))

	updated := []*documents.Document{
		{ID: "doc1.go", RelPath: "doc1_renamed.go", Size: 20, Hash: "h2", Words: []string{"gamma", "delta"}},
	}
	require.NoError(t, col.Update(updated))

	d, err := col.GetDocument("doc1.go")
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Equal(t, "doc1_renamed.go", d.RelPath)
	assert.Equal(t, int64(20), d.Size)
	assert.Equal(t, "h2", d.Hash)
}

// TestMeta_SnapshotCopy verifies Meta() returns a snapshot (copy) not a live pointer.
func TestMeta_SnapshotCopy(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	col, err := env.catalog.Create("/workspace/snap")
	require.NoError(t, err)

	r1 := col.Meta()
	r2 := col.Meta()

	// Modifying r1 must not affect r2 (they are independent copies).
	r1.Name = "mutated"
	assert.Equal(t, "/workspace/snap", r2.Name)
}

// TestConcurrentCreate verifies concurrent Creates don't race or deadlock.
func TestConcurrentCreate(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	const n = 20
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			_, err := env.catalog.Create("/workspace/concurrent-" + string(rune('a'+i)))
			errs <- err
		}(i)
	}
	for i := 0; i < n; i++ {
		err := <-errs
		assert.NoError(t, err)
	}
	assert.Len(t, env.catalog.List(), n)
}

// TestList_Empty verifies List returns empty slice (not nil) when no collections exist.
func TestList_Empty(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	list := env.catalog.List()
	assert.NotNil(t, list)
	assert.Len(t, list, 0)
}

// TestSave_TimestampRoundtrip verifies time fields survive JSON serialization.
func TestSave_TimestampRoundtrip(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "collection-ts-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "data")

	now := time.Now().UTC().Truncate(time.Second) // truncate for JSON round-trip

	var savedID int

	{
		db, err := pebblekv.Open(dbPath, 0)
		require.NoError(t, err)
		q := queue.NewMpsc("ts-q")
		q.Start()
		idx, err := invertedindex.New(db, q, invertedindex.Options{})
		require.NoError(t, err)
		docs, err := documents.New(db, q, idx, documents.Options{})
		require.NoError(t, err)
		cat, err := collection.New(db, docs, collection.Options{})
		require.NoError(t, err)

		col, err := cat.Create("/workspace/ts-test")
		require.NoError(t, err)
		savedID = col.ID()

		r := col.Meta()
		r.LastFullSync = now
		err = cat.Save(r)
		require.NoError(t, err)

		docs.CloseAndWait()
		idx.CloseAndWait()
		q.Stop()
		db.Close()
	}

	{
		db, err := pebblekv.Open(dbPath, 0)
		require.NoError(t, err)
		q := queue.NewMpsc("ts-q2")
		q.Start()
		idx, err := invertedindex.New(db, q, invertedindex.Options{})
		require.NoError(t, err)
		docs, err := documents.New(db, q, idx, documents.Options{})
		require.NoError(t, err)
		cat, err := collection.New(db, docs, collection.Options{})
		require.NoError(t, err)

		got, err := cat.Get(savedID)
		require.NoError(t, err)
		assert.True(t, now.Equal(got.Meta().LastFullSync), "LastFullSync should round-trip through JSON")

		docs.CloseAndWait()
		idx.CloseAndWait()
		q.Stop()
		db.Close()
	}
}

// TestList_ReturnsCopies verifies that mutating a Record returned by List
// does not affect the Catalog's internal state.
func TestList_ReturnsCopies(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	_, err := env.catalog.Create("/workspace/list-copy")
	require.NoError(t, err)

	list := env.catalog.List()
	require.Len(t, list, 1)

	// Mutate the returned record (both a scalar field and the Extra slice).
	list[0].Name = "mutated"
	list[0].Extra = []byte("injected")

	// A fresh List / Get must still show the original, unmutated values.
	fresh := env.catalog.List()
	require.Len(t, fresh, 1)
	assert.Equal(t, "/workspace/list-copy", fresh[0].Name, "List() must return copies, not live pointers")
	assert.Nil(t, fresh[0].Extra)

	// GetByName must still resolve the original name.
	_, err = env.catalog.GetByName("/workspace/list-copy")
	assert.NoError(t, err)
	_, err = env.catalog.GetByName("mutated")
	assert.Error(t, err, "mutating a List() copy must not register a new name")
}

// TestSave_RenameToExistingNameErrors verifies that renaming a collection to a
// name already owned by another collection returns an error and leaves both
// collections' name index entries intact.
func TestSave_RenameToExistingNameErrors(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	colA, err := env.catalog.Create("/workspace/aaa")
	require.NoError(t, err)
	_, err = env.catalog.Create("/workspace/bbb")
	require.NoError(t, err)

	// Try to rename A to B's name.
	r := colA.Meta()
	r.Name = "/workspace/bbb"
	err = env.catalog.Save(r)
	assert.Error(t, err, "renaming to an existing name must fail")

	// Both original names must still resolve to their original ids.
	gotA, err := env.catalog.GetByName("/workspace/aaa")
	require.NoError(t, err)
	assert.Equal(t, colA.ID(), gotA.ID())

	gotB, err := env.catalog.GetByName("/workspace/bbb")
	require.NoError(t, err)
	assert.NotEqual(t, colA.ID(), gotB.ID(), "B's name must still map to B, not A")

	// A's record must retain its original name (the failed Save must not persist).
	assert.Equal(t, "/workspace/aaa", gotA.Meta().Name)
}

// TestSave_RenameToOwnNameSucceeds verifies that a no-op rename (Name unchanged)
// and a genuine rename to a free name both succeed.
func TestSave_RenameToFreeNameSucceeds(t *testing.T) {
	env := setupFull(t)
	defer env.teardown()

	col, err := env.catalog.Create("/workspace/start")
	require.NoError(t, err)

	r := col.Meta()
	r.Name = "/workspace/end"
	err = env.catalog.Save(r)
	require.NoError(t, err)

	got, err := env.catalog.GetByName("/workspace/end")
	require.NoError(t, err)
	assert.Equal(t, col.ID(), got.ID())

	_, err = env.catalog.GetByName("/workspace/start")
	assert.Error(t, err, "old name must be released after rename")
}

// TestNew_EqualKeyTypesErrors verifies that New rejects Options where the
// incr-id and record key-type bytes collide (after defaults are applied).
func TestNew_EqualKeyTypesErrors(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "collection-eqkey-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	db, err := pebblekv.Open(filepath.Join(tempDir, "data"), 0)
	require.NoError(t, err)
	q := queue.NewMpsc("eqkey-q")
	q.Start()
	defer q.Stop()
	idx, err := invertedindex.New(db, q, invertedindex.Options{})
	require.NoError(t, err)
	defer func() {
		idx.CloseAndWait()
		db.Close()
	}()
	docs, err := documents.New(db, q, idx, documents.Options{})
	require.NoError(t, err)
	defer docs.CloseAndWait()

	// Explicitly equal non-default bytes.
	_, err = collection.New(db, docs, collection.Options{KeyTypeIncrId: 7, KeyTypeRecord: 7})
	assert.Error(t, err, "equal key-type bytes must be rejected")

	// One zero (→ default) colliding with an explicit byte equal to the other default.
	// IncrId zero → 1; Record set to 1 → collision.
	_, err = collection.New(db, docs, collection.Options{KeyTypeRecord: collection.DefaultKeyTypeIncrId})
	assert.Error(t, err, "explicit byte equal to the other default must be rejected")
}

// TestNew_SkipsEmptyNameRecords verifies the defensive guard in loadFromStore:
// a persisted record whose Name is empty (corrupt or un-migrated legacy data)
// must NOT be indexed, so it cannot poison byName[""] and collide with an
// unrelated GetByName("") lookup. A well-formed record alongside it must still
// load normally.
func TestNew_SkipsEmptyNameRecords(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "collection-emptyname-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	db, err := pebblekv.Open(filepath.Join(tempDir, "data"), 0)
	require.NoError(t, err)
	q := queue.NewMpsc("emptyname-q")
	q.Start()
	defer q.Stop()
	idx, err := invertedindex.New(db, q, invertedindex.Options{})
	require.NoError(t, err)
	defer func() {
		idx.CloseAndWait()
		db.Close()
	}()
	docs, err := documents.New(db, q, idx, documents.Options{})
	require.NoError(t, err)
	defer docs.CloseAndWait()

	recordKey := func(id int) []byte {
		return []byte(fmt.Sprintf("%c%d", collection.DefaultKeyTypeRecord, id))
	}

	// id=1: empty-name record (must be skipped).
	emptyRec := collection.Record{ID: 1, Name: ""}
	emptyJSON, _ := json.Marshal(emptyRec)
	require.NoError(t, db.Put(recordKey(1), emptyJSON))

	// id=2: well-formed record (must load).
	goodRec := collection.Record{ID: 2, Name: "/ws/good"}
	goodJSON, _ := json.Marshal(goodRec)
	require.NoError(t, db.Put(recordKey(2), goodJSON))

	cat, err := collection.New(db, docs, collection.Options{})
	require.NoError(t, err)

	// The empty-name record must not be indexed by id...
	_, err = cat.Get(1)
	assert.Error(t, err, "empty-name record must not be loaded by id")

	// ...nor by the empty name.
	_, err = cat.GetByName("")
	assert.Error(t, err, "GetByName(\"\") must not resolve to a poisoned record")

	// The well-formed record must still be available.
	got, err := cat.Get(2)
	require.NoError(t, err)
	assert.Equal(t, "/ws/good", got.Meta().Name)

	// Only the good record appears in List().
	list := cat.List()
	assert.Len(t, list, 1)
}
