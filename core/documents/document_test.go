package documents

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// GetDocument
// ---------------------------------------------------------------------------

func TestGetDocument_Missing(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	doc, err := env.St.GetDocument(1, "nonexistent")
	if !assert.NoError(t, err) {
		return
	}
	assert.Nil(t, doc, "missing doc should return nil")
}

func TestGetDocument_DbGetError(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	// Swap in a closed DB stub so db.Get() returns an error
	restore := simulateClosedDB(env.St)
	defer restore()

	doc, err := env.St.GetDocument(1, "any-doc")
	assert.Error(t, err, "GetDocument should propagate db.Get error")
	assert.Nil(t, doc)
}

func TestGetDocument_DecodeError(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	// Write invalid (non-JSON) data directly to the document meta key
	// so decodeDocumentMetaValue will fail.
	docid := "corrupt-doc"
	key := env.St.encodeDocumentMetaKey(1, docid)
	err := env.DB.Put(key, []byte("this is not valid json"))
	if !assert.NoError(t, err) {
		return
	}

	doc, err := env.St.GetDocument(1, docid)
	assert.Error(t, err, "GetDocument should propagate decode error")
	assert.Nil(t, doc)
}

func TestGetDocument_RoundTrip_WithoutWords(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	orig := &Document{
		ID:           "doc1",
		RelPath:      "src/main.go",
		Size:         1234,
		Hash:         "abc123",
		ModifiedTime: 100,
		Words:        []string{"hello", "world"},
		PathWords:    []string{"src", "main", "go"},
	}

	err := env.St.SaveNewDocuments(1, []*Document{orig})
	if !assert.NoError(t, err) {
		return
	}

	doc, err := env.St.GetDocument(1, "doc1")
	if !assert.NoError(t, err) {
		return
	}
	if !assert.NotNil(t, doc) {
		return
	}

	assert.Equal(t, "doc1", doc.ID)
	assert.Equal(t, "src/main.go", doc.RelPath)
	assert.Equal(t, int64(1234), doc.Size)
	assert.Equal(t, "abc123", doc.Hash)
	assert.Equal(t, int64(100), doc.ModifiedTime)
	// GetDocument no longer returns keywords
	assert.Empty(t, doc.Words)
}

// ---------------------------------------------------------------------------
// SaveNewDocuments
// ---------------------------------------------------------------------------

func TestSaveNewDocuments_PersistsMetaWordsPath(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	doc := &Document{
		ID:      "d1",
		RelPath: "pkg/util.go",
		Size:    42,
		Hash:    "h1",
		Words:   []string{"func", "util"},
	}

	err := env.St.SaveNewDocuments(1, []*Document{doc})
	if !assert.NoError(t, err) {
		return
	}

	// Verify meta
	got, err := env.St.GetDocument(1, "d1")
	if !assert.NoError(t, err) {
		return
	}
	if !assert.NotNil(t, got) {
		return
	}
	assert.Equal(t, "pkg/util.go", got.RelPath)
	assert.Equal(t, int64(42), got.Size)
	assert.Equal(t, "h1", got.Hash)

	// Verify LastSyncTime was set
	assert.NotZero(t, got.LastSyncTime, "LastSyncTime should be set by saveDocument")

	// Verify path
	path := env.St.GetDocumentPath(1, "d1")
	assert.Equal(t, "pkg/util.go", path)
}

func TestSaveNewDocuments_MultipleDocuments(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	docs := []*Document{
		{ID: "a", RelPath: "a.go", Words: []string{"one"}},
		{ID: "b", RelPath: "b.go", Words: []string{"two"}},
		{ID: "c", RelPath: "c.go", Words: []string{"three"}},
	}

	err := env.St.SaveNewDocuments(1, docs)
	if !assert.NoError(t, err) {
		return
	}

	for _, d := range docs {
		got, err := env.St.GetDocument(1, d.ID)
		if !assert.NoError(t, err) {
			return
		}
		if !assert.NotNil(t, got, "document %s should exist", d.ID) {
			return
		}
		assert.Equal(t, d.RelPath, got.RelPath)
	}
}

func TestSaveNewDocuments_ClosedDB(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	// Swap in a fake closed DB to trigger the db.IsClosed() early return
	restore := simulateClosedDB(env.St)
	defer restore()

	doc := &Document{ID: "d1", RelPath: "x.go", Words: []string{"x"}}
	err := env.St.SaveNewDocuments(1, []*Document{doc})
	// SaveNewDocuments returns nil when db is closed (silent skip)
	assert.NoError(t, err, "closed DB should cause a silent skip, not an error")
}

func TestSaveNewDocuments_DeletedWorkspaceRejected(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	// Mark collection as deleted
	env.St.markCollectionDeleted(1)

	doc := &Document{ID: "d1", RelPath: "x.go", Words: []string{"x"}}
	err := env.St.SaveNewDocuments(1, []*Document{doc})
	assert.Error(t, err, "saving to a deleted workspace should fail")
}

// ---------------------------------------------------------------------------
// UpdateDocuments
// ---------------------------------------------------------------------------

func TestUpdateDocuments_OverwriteExisting(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	orig := &Document{
		ID:      "d1",
		RelPath: "old.go",
		Size:    10,
		Hash:    "oldhash",
		Words:   []string{"old", "word"},
	}
	err := env.St.SaveNewDocuments(1, []*Document{orig})
	if !assert.NoError(t, err) {
		return
	}

	updated := &Document{
		ID:      "d1",
		RelPath: "new.go",
		Size:    20,
		Hash:    "newhash",
		Words:   []string{"new", "updated"},
	}
	err = env.St.UpdateDocuments(1, []*Document{updated})
	if !assert.NoError(t, err) {
		return
	}

	got, err := env.St.GetDocument(1, "d1")
	if !assert.NoError(t, err) {
		return
	}
	if !assert.NotNil(t, got) {
		return
	}
	assert.Equal(t, "new.go", got.RelPath)
	assert.Equal(t, int64(20), got.Size)
	assert.Equal(t, "newhash", got.Hash)
}

func TestUpdateDocuments_ClosedDB(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	// Swap in a fake closed DB to trigger the db.IsClosed() early return
	restore := simulateClosedDB(env.St)
	defer restore()

	doc := &Document{ID: "d1", RelPath: "x.go", Words: []string{"x"}}
	err := env.St.UpdateDocuments(1, []*Document{doc})
	// UpdateDocuments returns an error when db is closed
	assert.Error(t, err, "closed DB should return an error")
}

func TestUpdateDocuments_DeletedWorkspaceRejected(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)
	env.St.markCollectionDeleted(1)

	doc := &Document{ID: "d1", RelPath: "x.go", Words: []string{"x"}}
	err := env.St.UpdateDocuments(1, []*Document{doc})
	assert.Error(t, err, "updating a deleted workspace should fail")
}

func TestUpdateDocuments_NonExistentDocGraceful(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	// Update a document that was never saved -- should not panic,
	// GetDocumentWords returns empty for missing docs so the
	// inverted index diff is simply "add all new words".
	doc := &Document{
		ID:      "ghost",
		RelPath: "ghost.go",
		Words:   []string{"phantom"},
	}
	err := env.St.UpdateDocuments(1, []*Document{doc})
	if !assert.NoError(t, err) {
		return
	}

	// The document should now exist (saveDocument is called regardless).
	got, err := env.St.GetDocument(1, "ghost")
	if !assert.NoError(t, err) {
		return
	}
	if !assert.NotNil(t, got) {
		return
	}
	assert.Equal(t, "ghost.go", got.RelPath)
}

// ---------------------------------------------------------------------------
// DeleteDocument
// ---------------------------------------------------------------------------

func TestDeleteDocument_ExistingDoc(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	doc := &Document{
		ID:      "d1",
		RelPath: "del.go",
		Words:   []string{"remove", "me"},
	}
	err := env.St.SaveNewDocuments(1, []*Document{doc})
	if !assert.NoError(t, err) {
		return
	}

	err = env.St.DeleteDocument(1, "d1")
	if !assert.NoError(t, err) {
		return
	}

	got, err := env.St.GetDocument(1, "d1")
	if !assert.NoError(t, err) {
		return
	}
	assert.Nil(t, got, "document should be deleted")

	path := env.St.GetDocumentPath(1, "d1")
	assert.Empty(t, path, "path should be deleted")
}

func TestDeleteDocument_ClosedDB(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	// Swap in a fake closed DB to trigger the db.IsClosed() early return
	restore := simulateClosedDB(env.St)
	defer restore()

	err := env.St.DeleteDocument(1, "d1")
	// DeleteDocument returns nil when db is closed (silent skip)
	assert.NoError(t, err, "closed DB should cause a silent skip, not an error")
}

func TestDeleteDocument_MissingDocReturnsError(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	err := env.St.DeleteDocument(1, "nonexistent")
	assert.Error(t, err, "deleting a missing doc should return error")
}

func TestDeleteDocument_NonExistentWorkspaceReturnsError(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	// Do NOT create collection 999 — GetCollection should fail
	err := env.St.DeleteDocument(999, "d1")
	assert.Error(t, err, "deleting from a non-existent workspace should return error")
}
