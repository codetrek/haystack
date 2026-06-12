package documents

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCountByCollection_Empty(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	count := env.St.CountByCollection(1)
	assert.Equal(t, 0, count, "empty workspace should have 0 documents")
}

func TestCountByCollection_WithDocuments(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	// Save some documents
	docs := []*Document{
		{ID: "doc1", RelPath: "a.go", Words: []string{"hello"}, PathWords: []string{"a"}},
		{ID: "doc2", RelPath: "b.go", Words: []string{"world"}, PathWords: []string{"b"}},
		{ID: "doc3", RelPath: "c.go", Words: []string{"foo"}, PathWords: []string{"c"}},
	}
	err := env.St.SaveNewDocuments(1, docs)
	if !assert.NoError(t, err) {
		return
	}

	count := env.St.CountByCollection(1)
	assert.Equal(t, 3, count, "should count 3 documents")
}

func TestCountByCollection_MultipleWorkspaces(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)
	mustCreateWorkspace(t, env.St, 2)

	// Save 2 docs in workspace 1
	err := env.St.SaveNewDocuments(1, []*Document{
		{ID: "doc1", RelPath: "a.go", Words: []string{"a"}, PathWords: []string{"a"}},
		{ID: "doc2", RelPath: "b.go", Words: []string{"b"}, PathWords: []string{"b"}},
	})
	if !assert.NoError(t, err) {
		return
	}

	// Save 1 doc in workspace 2
	err = env.St.SaveNewDocuments(2, []*Document{
		{ID: "doc3", RelPath: "c.go", Words: []string{"c"}, PathWords: []string{"c"}},
	})
	if !assert.NoError(t, err) {
		return
	}

	assert.Equal(t, 2, env.St.CountByCollection(1), "workspace 1 should have 2 documents")
	assert.Equal(t, 1, env.St.CountByCollection(2), "workspace 2 should have 1 document")
}

func TestCountByCollection_NonExistentWorkspace(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	count := env.St.CountByCollection(999)
	assert.Equal(t, 0, count, "non-existent workspace should have 0 documents")
}

func TestCountByCollection_AfterDelete(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	// Save 2 docs
	err := env.St.SaveNewDocuments(1, []*Document{
		{ID: "doc1", RelPath: "a.go", Words: []string{"a"}, PathWords: []string{"a"}},
		{ID: "doc2", RelPath: "b.go", Words: []string{"b"}, PathWords: []string{"b"}},
	})
	if !assert.NoError(t, err) {
		return
	}

	assert.Equal(t, 2, env.St.CountByCollection(1), "should have 2 documents before delete")

	// Delete one document
	err = env.St.DeleteDocument(1, "doc1")
	if !assert.NoError(t, err) {
		return
	}

	assert.Equal(t, 1, env.St.CountByCollection(1), "should have 1 document after deleting one")
}

func TestCountByCollection_AfterWorkspaceDelete(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	// Save docs
	err := env.St.SaveNewDocuments(1, []*Document{
		{ID: "doc1", RelPath: "a.go", Words: []string{"a"}, PathWords: []string{"a"}},
		{ID: "doc2", RelPath: "b.go", Words: []string{"b"}, PathWords: []string{"b"}},
	})
	if !assert.NoError(t, err) {
		return
	}

	assert.Equal(t, 2, env.St.CountByCollection(1), "should have 2 documents before workspace delete")

	// Delete the entire workspace
	err = env.St.Delete(1)
	if !assert.NoError(t, err) {
		return
	}

	assert.Equal(t, 0, env.St.CountByCollection(1), "should have 0 documents after workspace delete")
}

func TestCountByCollection_IncrementsBatchCorrectly(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, env.St, 1)

	// Save first batch
	err := env.St.SaveNewDocuments(1, []*Document{
		{ID: "doc1", RelPath: "a.go", Words: []string{"a"}, PathWords: []string{"a"}},
	})
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, 1, env.St.CountByCollection(1), "should have 1 after first batch")

	// Save second batch
	err = env.St.SaveNewDocuments(1, []*Document{
		{ID: "doc2", RelPath: "b.go", Words: []string{"b"}, PathWords: []string{"b"}},
		{ID: "doc3", RelPath: "c.go", Words: []string{"c"}, PathWords: []string{"c"}},
	})
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, 3, env.St.CountByCollection(1), "should have 3 after second batch")
}
