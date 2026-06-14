package collection

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/codetrek/haystack/searchcore/documents"
	"github.com/codetrek/haystack/searchcore/invertedindex"
	"github.com/codetrek/haystack/searchcore/kv"
	"github.com/codetrek/haystack/searchcore/kv/pebblekv"
	"github.com/codetrek/haystack/searchcore/queue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failStore wraps a kv.Store and injects failures into selected operations,
// so the catalog's error-handling paths can be exercised.
type failStore struct {
	kv.Store
	failGetIncr bool
	failPut     bool
	failDelete  bool
}

func (f *failStore) GetIncrementalId(key []byte) (int, error) {
	if f.failGetIncr {
		return 0, errors.New("injected GetIncrementalId failure")
	}
	return f.Store.GetIncrementalId(key)
}

func (f *failStore) Put(key, value []byte) error {
	if f.failPut {
		return errors.New("injected Put failure")
	}
	return f.Store.Put(key, value)
}

func (f *failStore) Delete(key []byte) error {
	if f.failDelete {
		return errors.New("injected Delete failure")
	}
	return f.Store.Delete(key)
}

// newFailCatalog builds a catalog whose kv.Store is a failStore (the document
// store uses the real store so only the catalog's db ops can be made to fail).
func newFailCatalog(t *testing.T) (*Catalog, *failStore) {
	t.Helper()
	dir := t.TempDir()
	real, err := pebblekv.Open(filepath.Join(dir, "data"), 0)
	require.NoError(t, err)

	q := queue.NewMpsc("collection-fail-test")
	q.Start()
	idx, err := invertedindex.New(real, q, invertedindex.Options{})
	require.NoError(t, err)
	docs, err := documents.New(real, q, idx, documents.Options{})
	require.NoError(t, err)

	fs := &failStore{Store: real}
	cat, err := New(fs, docs, Options{})
	require.NoError(t, err)

	t.Cleanup(func() {
		idx.CloseAndWait()
		docs.CloseAndWait()
		q.Stop()
		_ = real.Close()
	})
	return cat, fs
}

func TestCreate_GetIdError(t *testing.T) {
	cat, fs := newFailCatalog(t)
	fs.failGetIncr = true
	_, err := cat.Create("proj")
	assert.Error(t, err, "Create should fail when id allocation fails")
}

func TestCreate_PersistError(t *testing.T) {
	cat, fs := newFailCatalog(t)
	fs.failPut = true // id allocation succeeds, persistRecord's Put fails
	_, err := cat.Create("proj")
	assert.Error(t, err, "Create should fail when persisting the record fails")
}

func TestDelete_DbError(t *testing.T) {
	cat, fs := newFailCatalog(t)
	col, err := cat.Create("proj") // succeeds: no failures injected yet
	require.NoError(t, err)
	fs.failDelete = true
	assert.Error(t, cat.Delete(col.ID()), "Delete should fail when the db delete fails")
}
