package documents

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// GetDocumentPath
// ---------------------------------------------------------------------------

func TestGetDocumentPath_Exists(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	doc := &Document{
		ID:      "d1",
		RelPath: "src/app.go",
		Words:   []string{"app"},
	}
	err := env.St.SaveNewDocuments(1, []*Document{doc})
	if !assert.NoError(t, err) {
		return
	}

	path := env.St.GetDocumentPath(1, "d1")
	assert.Equal(t, "src/app.go", path)
}

func TestGetDocumentPath_Missing(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	path := env.St.GetDocumentPath(1, "nonexistent")
	assert.Empty(t, path, "missing doc should return empty path")
}

// ---------------------------------------------------------------------------
// ScanFiles
// ---------------------------------------------------------------------------

func TestScanFiles_NormalScan(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	docs := []*Document{
		{ID: "a", RelPath: "a.go", Words: []string{"a"}},
		{ID: "b", RelPath: "b.go", Words: []string{"b"}},
		{ID: "c", RelPath: "c.go", Words: []string{"c"}},
	}
	err := env.St.SaveNewDocuments(1, docs)
	if !assert.NoError(t, err) {
		return
	}

	collected := map[string]string{}
	env.St.ScanFiles(1, func(docid, relPath string) bool {
		collected[docid] = relPath
		return true
	})

	assert.Len(t, collected, 3)
	assert.Equal(t, "a.go", collected["a"])
	assert.Equal(t, "b.go", collected["b"])
	assert.Equal(t, "c.go", collected["c"])
}

func TestScanFiles_CallbackReturnsFalseStops(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	docs := []*Document{
		{ID: "a", RelPath: "a.go", Words: []string{"a"}},
		{ID: "b", RelPath: "b.go", Words: []string{"b"}},
		{ID: "c", RelPath: "c.go", Words: []string{"c"}},
	}
	err := env.St.SaveNewDocuments(1, docs)
	if !assert.NoError(t, err) {
		return
	}

	count := 0
	env.St.ScanFiles(1, func(docid, relPath string) bool {
		count++
		return false // stop after first
	})

	assert.Equal(t, 1, count, "scan should stop after callback returns false")
}

func TestScanFiles_DeletedDocNotScanned(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	docs := []*Document{
		{ID: "keep", RelPath: "keep.go", Words: []string{"keep"}},
		{ID: "del", RelPath: "del.go", Words: []string{"del"}},
	}
	err := env.St.SaveNewDocuments(1, docs)
	if !assert.NoError(t, err) {
		return
	}

	err = env.St.DeleteDocument(1, "del")
	if !assert.NoError(t, err) {
		return
	}

	collected := map[string]string{}
	env.St.ScanFiles(1, func(docid, relPath string) bool {
		collected[docid] = relPath
		return true
	})

	assert.Len(t, collected, 1, "deleted doc should not appear in scan")
	assert.Equal(t, "keep.go", collected["keep"])
}

func TestScanFiles_EmptyWorkspace(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	count := 0
	env.St.ScanFiles(1, func(docid, relPath string) bool {
		count++
		return true
	})
	assert.Equal(t, 0, count, "empty workspace should yield no scan results")
}

// ---------------------------------------------------------------------------
// ScanFiles – empty docid branch (DecodeDocumentPathKey returns "")
// ---------------------------------------------------------------------------

func TestScanFiles_EmptyDocIdSkipped(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	// Save a normal document so we have something to scan
	docs := []*Document{
		{ID: "real", RelPath: "real.go", Words: []string{"w"}},
	}
	err := env.St.SaveNewDocuments(1, docs)
	if !assert.NoError(t, err) {
		return
	}

	// Insert a key with the right prefix but an empty docid.
	// EncodeDocumentPathKey(1, "") produces the prefix "\x0d1|", which
	// is also a valid key with an empty docid after the pipe separator.
	// DecodeDocumentPathKey will return docid="" for this key, triggering
	// the `if docid == ""` branch that should skip the entry.
	badKey := EncodeDocumentPathKey(1, "")
	err = env.DB.Put(badKey, []byte("phantom.go"))
	if !assert.NoError(t, err) {
		return
	}

	collected := map[string]string{}
	env.St.ScanFiles(1, func(docid, relPath string) bool {
		collected[docid] = relPath
		return true
	})

	// Only the real document should appear; the empty-docid entry should be skipped
	assert.Len(t, collected, 1, "empty-docid entry should be skipped by ScanFiles")
	assert.Equal(t, "real.go", collected["real"])
}
