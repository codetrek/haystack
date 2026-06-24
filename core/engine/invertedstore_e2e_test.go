package engine_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/codetrek/haystack/core/collection"
	"github.com/codetrek/haystack/core/documents"
	"github.com/codetrek/haystack/core/engine"
	"github.com/codetrek/haystack/core/idtable"
	"github.com/codetrek/haystack/core/invertedstore"
	"github.com/codetrek/haystack/core/kv/pebblekv"
	"github.com/codetrek/haystack/core/queue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// invertedstoreStack is the full core stack wired to the PRODUCTION inverted-index
// implementation — invertedstore.Store (not the lossy invertedindex.NewIndexerAdapter
// the other engine tests use). It exercises the real T9 seam end-to-end: documents
// writing through invertedstore, engine searching it back, and the forward-map diff
// delete/edit path (the adapter cannot retract a dropped keyword; invertedstore can).
//
// The queue is NOT stopped between operations and the store is NOT CloseAndWait'd
// until cleanup, so post-write Searches see the in-memory head: after each
// write the test drains the shared queue (q.RunFunc(nop)) so the async index
// applies complete, then searches.
type invertedstoreStack struct {
	q     *queue.Mpsc
	store *invertedstore.Store
	docs  *documents.Store
	cat   *collection.Catalog
	col   *collection.Collection
	alloc *idtable.Allocator
	ids   map[string]string // relPath -> docID (canonical 8-byte string)
}

func newInvertedstoreStack(t *testing.T) *invertedstoreStack {
	t.Helper()
	tmpDir := t.TempDir()

	kvStore, err := pebblekv.Open(filepath.Join(tmpDir, "data"), 16<<20)
	require.NoError(t, err)

	q := queue.NewMpsc("invstore-e2e")
	q.Start()

	// Open the inverted store on a versioned subdir that does NOT exist yet — the
	// production wiring shape — so this also covers Open's MkdirAll.
	store, err := invertedstore.Open(filepath.Join(tmpDir, "1.6", "invertedstore"), q, invertedstore.Options{})
	require.NoError(t, err)

	// documents.New takes the storage-agnostic invertedindex.Indexer; *invertedstore.Store
	// satisfies it natively (no adapter).
	docs, err := documents.New(kvStore, q, store, documents.Options{})
	require.NoError(t, err)

	cat, err := collection.New(kvStore, docs, collection.Options{})
	require.NoError(t, err)

	col, err := cat.Create("invstore-e2e")
	require.NoError(t, err)

	alloc, err := idtable.Open(filepath.Join(tmpDir, "idtable.db"), idtable.Options{})
	require.NoError(t, err)

	s := &invertedstoreStack{
		q:     q,
		store: store,
		docs:  docs,
		cat:   cat,
		col:   col,
		alloc: alloc,
		ids:   map[string]string{},
	}

	t.Cleanup(func() {
		s.alloc.Close()
		s.docs.CloseAndWait()
		s.store.CloseAndWait()
		q.Stop()
		_ = kvStore.Close()
		_ = os.RemoveAll(tmpDir)
	})
	return s
}

// docID resolves (allocating once) the canonical docid string for relPath.
func (s *invertedstoreStack) docID(t *testing.T, relPath string) string {
	t.Helper()
	if id, ok := s.ids[relPath]; ok {
		return id
	}
	id, err := s.alloc.GetId([]byte(relPath))
	require.NoError(t, err)
	s.ids[relPath] = id
	return id
}

// save persists relPath with the given words and returns its int64 docid (engine
// results are keyed by int64).
func (s *invertedstoreStack) save(t *testing.T, relPath string, words []string) int64 {
	t.Helper()
	id := s.docID(t, relPath)
	require.NoError(t, s.col.Save([]*documents.Document{{ID: id, RelPath: relPath, Words: words}}))
	return idtable.DecodeId(id)
}

// drain blocks until every previously-enqueued async index apply has run, so a
// following Search observes the writes.
func (s *invertedstoreStack) drain(t *testing.T) {
	t.Helper()
	require.NoError(t, s.q.RunFunc(func() error { return nil }))
}

// collect runs the engine query and returns the matched int64 docids.
func (s *invertedstoreStack) collect(t *testing.T, query string) map[int64]struct{} {
	t.Helper()
	eng := engine.New(s.store, s.docs, s.col.ID(), engine.Options{MaxWildcardLength: 24, MaxKeywordDistance: 32})
	require.NoError(t, eng.Compile(query, false))
	res, err := eng.CollectDocuments()
	require.NoError(t, err)
	return res.DocIds
}

// TestInvertedStoreE2E_AddSearch wires documents+engine to invertedstore and
// indexes a batch LARGER than the mpsc channel buffer (default 100), then searches
// it back. The large batch is the regression guard for the documents->invertedstore
// deadlock: if the per-doc index notification were enqueued from inside the
// documents worker task, this Save would hang once the buffer overran.
func TestInvertedStoreE2E_AddSearch(t *testing.T) {
	s := newInvertedstoreStack(t)

	const n = 250 // >> the 100-deep queue buffer
	want := make([]int64, 0, n)
	docs := make([]*documents.Document, 0, n)
	for i := 0; i < n; i++ {
		relPath := filepathName(i)
		id := s.docID(t, relPath)
		want = append(want, idtable.DecodeId(id))
		// Every doc shares "common"; each also has a unique "uniqueN".
		docs = append(docs, &documents.Document{
			ID:      id,
			RelPath: relPath,
			Words:   []string{"common", uniqueWord(i)},
		})
	}
	require.NoError(t, s.col.Save(docs))
	s.drain(t)

	// The shared keyword matches every doc.
	got := s.collect(t, "common")
	for _, id := range want {
		assert.Contains(t, got, id, "doc %d missing from 'common' search", id)
	}
	assert.Len(t, got, n)

	// A unique keyword matches exactly its one doc.
	got = s.collect(t, uniqueWord(7))
	assert.Equal(t, map[int64]struct{}{want[7]: {}}, got)
}

// TestInvertedStoreE2E_DeleteRoundTrip indexes docs, deletes one, and confirms it
// disappears from Search — the forward-map-diff delete path that the lossy adapter
// CANNOT do (the adapter's Update passes oldKeywords=nil, so a delete is a posting
// no-op). This is the T9 acceptance the adapter-based tests never exercise.
func TestInvertedStoreE2E_DeleteRoundTrip(t *testing.T) {
	s := newInvertedstoreStack(t)

	a := s.save(t, "a.go", []string{"shared", "alpha"})
	b := s.save(t, "b.go", []string{"shared", "beta"})
	s.drain(t)

	got := s.collect(t, "shared")
	assert.Contains(t, got, a)
	assert.Contains(t, got, b)

	// Delete a.go; it must vanish from BOTH its keywords.
	require.NoError(t, s.col.DeleteDocument(s.docID(t, "a.go")))
	s.drain(t)

	got = s.collect(t, "shared")
	assert.NotContains(t, got, a, "deleted doc still in 'shared'")
	assert.Contains(t, got, b, "surviving doc dropped from 'shared'")

	got = s.collect(t, "alpha")
	assert.NotContains(t, got, a, "deleted doc still in its unique keyword 'alpha'")
}

// TestInvertedStoreE2E_EditRetractsKeyword indexes a doc, then re-saves it with a
// DROPPED keyword, and confirms the dropped keyword no longer matches while a
// retained/added one does. The forward-map diff (full re-post + tombstone the
// removed keyword) is exactly what the adapter cannot do; invertedstore can.
func TestInvertedStoreE2E_EditRetractsKeyword(t *testing.T) {
	s := newInvertedstoreStack(t)

	id := s.save(t, "doc.go", []string{"keep", "drop"})
	s.drain(t)

	assert.Contains(t, s.collect(t, "keep"), id)
	assert.Contains(t, s.collect(t, "drop"), id)

	// Re-save with "drop" removed and "added" introduced.
	require.NoError(t, s.col.Save([]*documents.Document{
		{ID: s.docID(t, "doc.go"), RelPath: "doc.go", Words: []string{"keep", "added"}},
	}))
	s.drain(t)

	assert.Contains(t, s.collect(t, "keep"), id, "retained keyword lost after edit")
	assert.Contains(t, s.collect(t, "added"), id, "added keyword missing after edit")
	assert.NotContains(t, s.collect(t, "drop"), id, "dropped keyword NOT retracted (forward-diff failed)")
}

// filepathName is a stable, unique relPath for doc i.
func filepathName(i int) string { return fmt.Sprintf("dir/file%04d.go", i) }

// uniqueWord is a stable keyword unique to doc i (lower-cased; the index lowercases
// on the prefix-search path).
func uniqueWord(i int) string { return fmt.Sprintf("unique%04d", i) }
