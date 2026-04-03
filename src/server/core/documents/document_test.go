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

	mustCreateWorkspace(t, 1)

	doc, err := GetDocument(1, "nonexistent", false)
	if !assert.NoError(t, err) {
		return
	}
	assert.Nil(t, doc, "missing doc should return nil")
}

func TestGetDocument_RoundTrip_WithoutWords(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	orig := &Document{
		ID:           "doc1",
		RelPath:      "src/main.go",
		Size:         1234,
		Hash:         "abc123",
		ModifiedTime: 100,
		Words:        []string{"hello", "world"},
		PathWords:    []string{"src", "main", "go"},
	}

	err := SaveNewDocuments(1, []*Document{orig})
	if !assert.NoError(t, err) {
		return
	}

	doc, err := GetDocument(1, "doc1", false)
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
	// Words should be empty when includeWords=false
	assert.Empty(t, doc.Words)
}

func TestGetDocument_RoundTrip_WithWords(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	orig := &Document{
		ID:           "doc1",
		RelPath:      "src/main.go",
		Size:         1234,
		Hash:         "abc123",
		ModifiedTime: 100,
		Words:        []string{"hello", "world"},
	}

	err := SaveNewDocuments(1, []*Document{orig})
	if !assert.NoError(t, err) {
		return
	}

	doc, err := GetDocument(1, "doc1", true)
	if !assert.NoError(t, err) {
		return
	}
	if !assert.NotNil(t, doc) {
		return
	}

	assert.Equal(t, "doc1", doc.ID)
	assert.Equal(t, []string{"hello", "world"}, doc.Words)
}

// ---------------------------------------------------------------------------
// GetDocumentWords
// ---------------------------------------------------------------------------

func TestGetDocumentWords_Missing(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	words, err := GetDocumentWords(1, "nonexistent")
	if !assert.NoError(t, err) {
		return
	}
	assert.Empty(t, words)
}

func TestGetDocumentWords_RoundTrip(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	doc := &Document{
		ID:      "doc1",
		RelPath: "foo.go",
		Words:   []string{"alpha", "beta", "gamma"},
	}

	err := SaveNewDocuments(1, []*Document{doc})
	if !assert.NoError(t, err) {
		return
	}

	words, err := GetDocumentWords(1, "doc1")
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, words)
}

// ---------------------------------------------------------------------------
// SaveNewDocuments
// ---------------------------------------------------------------------------

func TestSaveNewDocuments_PersistsMetaWordsPath(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	doc := &Document{
		ID:      "d1",
		RelPath: "pkg/util.go",
		Size:    42,
		Hash:    "h1",
		Words:   []string{"func", "util"},
	}

	err := SaveNewDocuments(1, []*Document{doc})
	if !assert.NoError(t, err) {
		return
	}

	// Verify meta
	got, err := GetDocument(1, "d1", false)
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

	// Verify words
	words, err := GetDocumentWords(1, "d1")
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, []string{"func", "util"}, words)

	// Verify path
	path := GetDocumentPath(1, "d1")
	assert.Equal(t, "pkg/util.go", path)
}

func TestSaveNewDocuments_MultipleDocuments(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	docs := []*Document{
		{ID: "a", RelPath: "a.go", Words: []string{"one"}},
		{ID: "b", RelPath: "b.go", Words: []string{"two"}},
		{ID: "c", RelPath: "c.go", Words: []string{"three"}},
	}

	err := SaveNewDocuments(1, docs)
	if !assert.NoError(t, err) {
		return
	}

	for _, d := range docs {
		got, err := GetDocument(1, d.ID, true)
		if !assert.NoError(t, err) {
			return
		}
		if !assert.NotNil(t, got, "document %s should exist", d.ID) {
			return
		}
		assert.Equal(t, d.RelPath, got.RelPath)
		assert.Equal(t, d.Words, got.Words)
	}
}

func TestSaveNewDocuments_ClosedDB(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	// Swap in a fake closed DB to trigger the db.IsClosed() early return
	restore := simulateClosedDB()
	defer restore()

	doc := &Document{ID: "d1", RelPath: "x.go", Words: []string{"x"}}
	err := SaveNewDocuments(1, []*Document{doc})
	// SaveNewDocuments returns nil when db is closed (silent skip)
	assert.NoError(t, err, "closed DB should cause a silent skip, not an error")
}

func TestSaveNewDocuments_DeletedWorkspaceRejected(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	// Mark workspace as deleted
	markWorkspaceDeleted(1)

	doc := &Document{ID: "d1", RelPath: "x.go", Words: []string{"x"}}
	err := SaveNewDocuments(1, []*Document{doc})
	assert.Error(t, err, "saving to a deleted workspace should fail")
}

// ---------------------------------------------------------------------------
// UpdateDocuments
// ---------------------------------------------------------------------------

func TestUpdateDocuments_OverwriteExisting(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	orig := &Document{
		ID:      "d1",
		RelPath: "old.go",
		Size:    10,
		Hash:    "oldhash",
		Words:   []string{"old", "word"},
	}
	err := SaveNewDocuments(1, []*Document{orig})
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
	err = UpdateDocuments(1, []*Document{updated})
	if !assert.NoError(t, err) {
		return
	}

	got, err := GetDocument(1, "d1", true)
	if !assert.NoError(t, err) {
		return
	}
	if !assert.NotNil(t, got) {
		return
	}
	assert.Equal(t, "new.go", got.RelPath)
	assert.Equal(t, int64(20), got.Size)
	assert.Equal(t, "newhash", got.Hash)
	assert.Equal(t, []string{"new", "updated"}, got.Words)
}

func TestUpdateDocuments_ClosedDB(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	// Swap in a fake closed DB to trigger the db.IsClosed() early return
	restore := simulateClosedDB()
	defer restore()

	doc := &Document{ID: "d1", RelPath: "x.go", Words: []string{"x"}}
	err := UpdateDocuments(1, []*Document{doc})
	// UpdateDocuments returns an error when db is closed
	assert.Error(t, err, "closed DB should return an error")
}

func TestUpdateDocuments_DeletedWorkspaceRejected(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)
	markWorkspaceDeleted(1)

	doc := &Document{ID: "d1", RelPath: "x.go", Words: []string{"x"}}
	err := UpdateDocuments(1, []*Document{doc})
	assert.Error(t, err, "updating a deleted workspace should fail")
}

func TestUpdateDocuments_NonExistentDocGraceful(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	// Update a document that was never saved -- should not panic,
	// GetDocumentWords returns empty for missing docs so the
	// inverted index diff is simply "add all new words".
	doc := &Document{
		ID:      "ghost",
		RelPath: "ghost.go",
		Words:   []string{"phantom"},
	}
	err := UpdateDocuments(1, []*Document{doc})
	if !assert.NoError(t, err) {
		return
	}

	// The document should now exist (saveDocument is called regardless).
	got, err := GetDocument(1, "ghost", true)
	if !assert.NoError(t, err) {
		return
	}
	if !assert.NotNil(t, got) {
		return
	}
	assert.Equal(t, "ghost.go", got.RelPath)
	assert.Equal(t, []string{"phantom"}, got.Words)
}

// ---------------------------------------------------------------------------
// DeleteDocument
// ---------------------------------------------------------------------------

func TestDeleteDocument_ExistingDoc(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	doc := &Document{
		ID:      "d1",
		RelPath: "del.go",
		Words:   []string{"remove", "me"},
	}
	err := SaveNewDocuments(1, []*Document{doc})
	if !assert.NoError(t, err) {
		return
	}

	err = DeleteDocument(1, "d1")
	if !assert.NoError(t, err) {
		return
	}

	got, err := GetDocument(1, "d1", false)
	if !assert.NoError(t, err) {
		return
	}
	assert.Nil(t, got, "document should be deleted")

	words, err := GetDocumentWords(1, "d1")
	if !assert.NoError(t, err) {
		return
	}
	assert.Empty(t, words, "words should be deleted")

	path := GetDocumentPath(1, "d1")
	assert.Empty(t, path, "path should be deleted")
}

func TestDeleteDocument_ClosedDB(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	// Swap in a fake closed DB to trigger the db.IsClosed() early return
	restore := simulateClosedDB()
	defer restore()

	err := DeleteDocument(1, "d1")
	// DeleteDocument returns nil when db is closed (silent skip)
	assert.NoError(t, err, "closed DB should cause a silent skip, not an error")
}

func TestDeleteDocument_MissingDocReturnsError(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	err := DeleteDocument(1, "nonexistent")
	assert.Error(t, err, "deleting a missing doc should return error")
}
