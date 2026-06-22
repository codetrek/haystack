package engine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/codetrek/haystack/core/collection"
	"github.com/codetrek/haystack/core/documents"
	"github.com/codetrek/haystack/core/engine"
	"github.com/codetrek/haystack/core/idtable"
	"github.com/codetrek/haystack/core/invertedindex"
	"github.com/codetrek/haystack/core/kv/pebblekv"
	"github.com/codetrek/haystack/core/queue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// indexedStack is a fully wired core stack with documents indexed, ready
// for engine queries. It exercises the CollectDocuments path end-to-end.
type indexedStack struct {
	idx   *invertedindex.Index
	docs  *documents.Store
	colID int
	ids   map[string]string // relPath -> docID
}

// buildIndexedStack wires pebblekv + queue + idtable + inverted index +
// documents + collection, indexes the given docs (relPath -> words), flushes,
// and returns the pieces needed to run engine queries. t.Cleanup shuts it down.
func buildIndexedStack(t *testing.T, docMap map[string][]string) *indexedStack {
	t.Helper()
	tmpDir := t.TempDir()

	store, err := pebblekv.Open(filepath.Join(tmpDir, "data"), 16<<20)
	require.NoError(t, err)

	q := queue.NewMpsc("engine-test-writes")
	q.Start()

	alloc, err := idtable.Open(filepath.Join(tmpDir, "idtable.db"), idtable.Options{})
	require.NoError(t, err)
	ids := make(map[string]string, len(docMap))
	for relPath := range docMap {
		id, err := alloc.GetId([]byte(relPath))
		require.NoError(t, err)
		ids[relPath] = id
	}
	alloc.Close()

	idx, err := invertedindex.New(store, q, invertedindex.Options{})
	require.NoError(t, err)

	docs, err := documents.New(store, q, idx, documents.Options{})
	require.NoError(t, err)

	cat, err := collection.New(store, docs, collection.Options{})
	require.NoError(t, err)

	col, err := cat.Create("engine-test")
	require.NoError(t, err)

	batch := make([]*documents.Document, 0, len(docMap))
	for relPath, words := range docMap {
		batch = append(batch, &documents.Document{ID: ids[relPath], RelPath: relPath, Words: words})
	}
	require.NoError(t, col.Save(batch))

	// Flush index writes to disk so Search sees them.
	idx.CloseAndWait()

	t.Cleanup(func() {
		docs.CloseAndWait()
		q.Stop()
		_ = store.Close()
		_ = os.RemoveAll(tmpDir)
	})

	return &indexedStack{idx: idx, docs: docs, colID: col.ID(), ids: ids}
}

func (s *indexedStack) collect(t *testing.T, query string) map[int64]struct{} {
	t.Helper()
	eng := engine.New(s.idx, s.docs, s.colID, engine.Options{MaxWildcardLength: 24, MaxKeywordDistance: 32})
	require.NoError(t, eng.Compile(query, false))
	res, err := eng.CollectDocuments()
	require.NoError(t, err)
	return res.DocIds
}

// id decodes the int64 docid for relPath (engine results are keyed by int64).
func (s *indexedStack) id(relPath string) int64 {
	return idtable.DecodeId(s.ids[relPath])
}

func TestEngineCollectDocuments(t *testing.T) {
	s := buildIndexedStack(t, map[string][]string{
		"alpha.go": {"hello", "world", "alpha"},
		"beta.go":  {"hello", "beta", "shared"},
		"gamma.go": {"world", "gamma", "shared"},
	})

	t.Run("single keyword hits all containing docs", func(t *testing.T) {
		got := s.collect(t, "hello")
		assert.Contains(t, got, s.id("alpha.go"))
		assert.Contains(t, got, s.id("beta.go"))
		assert.NotContains(t, got, s.id("gamma.go"))
	})

	t.Run("multiple AND keywords intersect", func(t *testing.T) {
		// hello AND world → only alpha.go has both.
		got := s.collect(t, "hello world")
		assert.Contains(t, got, s.id("alpha.go"))
		assert.NotContains(t, got, s.id("beta.go"))
		assert.NotContains(t, got, s.id("gamma.go"))
	})

	t.Run("OR clauses union and merge", func(t *testing.T) {
		// "alpha | gamma" → alpha.go (via alpha) and gamma.go (via gamma).
		got := s.collect(t, "alpha | gamma")
		assert.Contains(t, got, s.id("alpha.go"))
		assert.Contains(t, got, s.id("gamma.go"))
		assert.NotContains(t, got, s.id("beta.go"))
	})

	t.Run("shared keyword across docs", func(t *testing.T) {
		got := s.collect(t, "shared")
		assert.Contains(t, got, s.id("beta.go"))
		assert.Contains(t, got, s.id("gamma.go"))
	})

	t.Run("keyword absent from index yields no docs", func(t *testing.T) {
		got := s.collect(t, "nonexistentkeyword")
		assert.Empty(t, got)
	})

	t.Run("quoted phrase intersects its keywords", func(t *testing.T) {
		// A quoted phrase becomes one term with multiple keywords, exercising
		// collectWithKeywords' multi-keyword intersection loop. Only alpha.go
		// has both "hello" and "world".
		got := s.collect(t, `"hello world"`)
		assert.Contains(t, got, s.id("alpha.go"))
		assert.NotContains(t, got, s.id("beta.go"))
		assert.NotContains(t, got, s.id("gamma.go"))
	})

	t.Run("wildcard suffix prunes wild-only docs", func(t *testing.T) {
		// "hello*world" → keyword "hello" {alpha,beta}, wildcard "world"
		// {alpha,gamma}; gamma is in WildDocIds but not DocIds, so it's pruned
		// (exercises the WildDocIds delete branch).
		got := s.collect(t, "hello*world")
		assert.Contains(t, got, s.id("alpha.go"))
		assert.Contains(t, got, s.id("beta.go"))
	})

	t.Run("unknown collection id yields no docs", func(t *testing.T) {
		// Exercises the GetCollection-error branch in term.collectDocuments.
		eng := engine.New(s.idx, s.docs, 999999, engine.Options{MaxWildcardLength: 24, MaxKeywordDistance: 32})
		require.NoError(t, eng.Compile("hello", false))
		res, err := eng.CollectDocuments()
		require.NoError(t, err)
		assert.Empty(t, res.DocIds)
	})
}

// TestEngineString covers Engine.String / andClause.String / term.String.
func TestEngineString(t *testing.T) {
	eng := engine.New(nil, nil, 0, engine.Options{MaxWildcardLength: 4, MaxKeywordDistance: 4})
	require.NoError(t, eng.Compile("hello world | foo", false))
	// 2 OR clauses; first has 2 AND terms, second has 1.
	assert.Equal(t, "hello AND world | foo", eng.String())
}

// TestEngineCollect_NilStores exercises the nil-store guard paths in the
// collect chain (docs/idx not initialised).
func TestEngineCollect_NilStores(t *testing.T) {
	eng := engine.New(nil, nil, 0, engine.Options{MaxWildcardLength: 4, MaxKeywordDistance: 4})
	require.NoError(t, eng.Compile("hello world", false))
	res, err := eng.CollectDocuments()
	require.NoError(t, err)
	assert.Empty(t, res.DocIds)
}
