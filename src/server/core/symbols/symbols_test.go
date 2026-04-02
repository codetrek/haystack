package symbols

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetrek/haystack/conf"
	"github.com/codetrek/haystack/server/core/storage"
	"github.com/codetrek/haystack/utils/queue"
	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

func TestInit(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-sym-init-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	conf.Get().Global.DataPath = tempDir
	database, _ := storage.Open(filepath.Join(tempDir, "data"), 0)

	q := queue.NewMpsc("TestInitQueue")
	q.Start()
	defer q.Stop()

	err = Init(database, q)
	assert.NoError(t, err)

	// Verify package globals are set
	assert.NotNil(t, db)
	assert.NotNil(t, mpsc)

	// Cleanup: reset globals
	CloseAndWait()
	database.Close()
}

// ---------------------------------------------------------------------------
// CloseAndWait
// ---------------------------------------------------------------------------

func TestCloseAndWait(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "haystack-sym-close-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	conf.Get().Global.DataPath = tempDir
	database, _ := storage.Open(filepath.Join(tempDir, "data"), 0)

	q := queue.NewMpsc("TestCloseQueue")
	q.Start()

	err = Init(database, q)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	done := make(chan struct{})
	go func() {
		CloseAndWait()
		close(done)
	}()

	select {
	case <-done:
		// Normal closure
	case <-time.After(5 * time.Second):
		t.Error("CloseAndWait timed out")
	}

	// Verify package globals are cleared
	assert.Nil(t, db)
	assert.Nil(t, mpsc)

	database.Close()
	q.Stop()
}

// ---------------------------------------------------------------------------
// Create + GetSymbolTable / GetSymbolWordsTable
// ---------------------------------------------------------------------------

func TestCreateAndGetSymbolTable(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 42)

	st, err := GetSymbolTable(42)
	if !assert.NoError(t, err) {
		return
	}
	if !assert.NotNil(t, st) {
		return
	}
	assert.Equal(t, 42, st.WorkspaceId)
	assert.Equal(t, "test-workspace", st.Desc)
}

func TestCreateAndGetSymbolWordsTable(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 7)

	swt, err := GetSymbolWordsTable(7)
	if !assert.NoError(t, err) {
		return
	}
	if !assert.NotNil(t, swt) {
		return
	}
	assert.Equal(t, 7, swt.WorkspaceId)
	assert.Equal(t, "test-workspace", swt.Desc)
}

func TestCreate_TwoTablesHaveDifferentInvertedIds(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	st, err := GetSymbolTable(1)
	if !assert.NoError(t, err) {
		return
	}

	swt, err := GetSymbolWordsTable(1)
	if !assert.NoError(t, err) {
		return
	}

	assert.NotEqual(t, st.InvertedId, swt.InvertedId,
		"symbol table and words table should have different inverted index IDs")
}

func TestGetSymbolTable_NonExistent(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	_, err := GetSymbolTable(999)
	assert.Error(t, err, "non-existent workspace should return error")
}

func TestGetSymbolWordsTable_NonExistent(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	_, err := GetSymbolWordsTable(999)
	assert.Error(t, err, "non-existent workspace should return error")
}

// ---------------------------------------------------------------------------
// Delete workspace
// ---------------------------------------------------------------------------

func TestDelete_RemovesWorkspace(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	// Add some functions first
	funcs := []DocFunction{
		{
			ID:      "doc1",
			RelPath: "main.go",
			Functions: []Function{
				{Name: "main", Line: 1},
			},
		},
	}
	err := AddFunctions(1, funcs)
	if !assert.NoError(t, err) {
		return
	}

	// Verify functions exist
	got, err := GetDocFunctions(1, "doc1")
	if !assert.NoError(t, err) {
		return
	}
	assert.Len(t, got, 1)

	// Delete workspace
	err = Delete(1)
	assert.NoError(t, err)
}

func TestDelete_FeatureDisabled(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	// Disable feature flag
	origFlag := conf.Get().Symbols.EnableFeature
	conf.Get().Symbols.EnableFeature = false
	defer func() { conf.Get().Symbols.EnableFeature = origFlag }()

	mustCreateWorkspace(t, 1)

	err := Delete(1)
	assert.NoError(t, err, "should silently return nil when feature disabled")
}

// ---------------------------------------------------------------------------
// AddFunctions + GetDocFunctions
// ---------------------------------------------------------------------------

func TestAddFunctions_SingleDoc(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	funcs := []DocFunction{
		{
			ID:      "doc1",
			RelPath: "main.go",
			Functions: []Function{
				{Name: "main", Line: 1},
				{Name: "init", Line: 5},
				{Name: "helper", Line: 10},
			},
		},
	}

	err := AddFunctions(1, funcs)
	if !assert.NoError(t, err) {
		return
	}

	got, err := GetDocFunctions(1, "doc1")
	if !assert.NoError(t, err) {
		return
	}
	assert.Len(t, got, 3)

	// Build map of name->lines for order-independent verification
	nameLines := make(map[string][]int)
	for _, f := range got {
		nameLines[f.Name] = append(nameLines[f.Name], f.Line)
	}
	assert.Contains(t, nameLines, "main")
	assert.Contains(t, nameLines, "init")
	assert.Contains(t, nameLines, "helper")
}

func TestAddFunctions_MultipleDocuments(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	funcs := []DocFunction{
		{
			ID:      "doc1",
			RelPath: "a.go",
			Functions: []Function{
				{Name: "funcA", Line: 1},
			},
		},
		{
			ID:      "doc2",
			RelPath: "b.go",
			Functions: []Function{
				{Name: "funcB", Line: 2},
			},
		},
	}

	err := AddFunctions(1, funcs)
	if !assert.NoError(t, err) {
		return
	}

	got1, err := GetDocFunctions(1, "doc1")
	if !assert.NoError(t, err) {
		return
	}
	assert.Len(t, got1, 1)
	assert.Equal(t, "funcA", got1[0].Name)

	got2, err := GetDocFunctions(1, "doc2")
	if !assert.NoError(t, err) {
		return
	}
	assert.Len(t, got2, 1)
	assert.Equal(t, "funcB", got2[0].Name)
}

func TestAddFunctions_SameFunctionMultipleLines(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	// Same function name on multiple lines (overloads, partials, etc.)
	funcs := []DocFunction{
		{
			ID:      "doc1",
			RelPath: "main.go",
			Functions: []Function{
				{Name: "process", Line: 10},
				{Name: "process", Line: 25},
			},
		},
	}

	err := AddFunctions(1, funcs)
	if !assert.NoError(t, err) {
		return
	}

	got, err := GetDocFunctions(1, "doc1")
	if !assert.NoError(t, err) {
		return
	}
	assert.Len(t, got, 2)
	// Both entries should have name "process"
	for _, f := range got {
		assert.Equal(t, "process", f.Name)
	}
}

func TestAddFunctions_UpdateExisting(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	// First add
	funcs1 := []DocFunction{
		{
			ID:      "doc1",
			RelPath: "main.go",
			Functions: []Function{
				{Name: "oldFunc", Line: 1},
			},
		},
	}
	err := AddFunctions(1, funcs1)
	if !assert.NoError(t, err) {
		return
	}

	// Update with new functions
	funcs2 := []DocFunction{
		{
			ID:      "doc1",
			RelPath: "main.go",
			Functions: []Function{
				{Name: "newFunc", Line: 1},
				{Name: "anotherFunc", Line: 5},
			},
		},
	}
	err = AddFunctions(1, funcs2)
	if !assert.NoError(t, err) {
		return
	}

	got, err := GetDocFunctions(1, "doc1")
	if !assert.NoError(t, err) {
		return
	}
	assert.Len(t, got, 2)

	names := make(map[string]bool)
	for _, f := range got {
		names[f.Name] = true
	}
	assert.True(t, names["newFunc"])
	assert.True(t, names["anotherFunc"])
	assert.False(t, names["oldFunc"], "old function should be replaced")
}

func TestAddFunctions_EmptyFunctions_DeletesKey(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	// Add some functions first
	funcs := []DocFunction{
		{
			ID:      "doc1",
			RelPath: "main.go",
			Functions: []Function{
				{Name: "foo", Line: 1},
			},
		},
	}
	err := AddFunctions(1, funcs)
	if !assert.NoError(t, err) {
		return
	}

	// Now add with empty functions list (should delete the key)
	funcs2 := []DocFunction{
		{
			ID:        "doc1",
			RelPath:   "main.go",
			Functions: []Function{},
		},
	}
	err = AddFunctions(1, funcs2)
	if !assert.NoError(t, err) {
		return
	}

	got, err := GetDocFunctions(1, "doc1")
	if !assert.NoError(t, err) {
		return
	}
	assert.Empty(t, got, "empty functions should result in deleted entry")
}

// ---------------------------------------------------------------------------
// GetDocFunctions
// ---------------------------------------------------------------------------

func TestGetDocFunctions_NonExistent(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	got, err := GetDocFunctions(1, "nonexistent")
	// Pebble wrapper returns nil,nil for missing keys
	assert.NoError(t, err)
	assert.Empty(t, got)
}

// ---------------------------------------------------------------------------
// DeleteDocument
// ---------------------------------------------------------------------------

func TestDeleteDocument_RemovesFunctions(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	funcs := []DocFunction{
		{
			ID:      "doc1",
			RelPath: "main.go",
			Functions: []Function{
				{Name: "foo", Line: 1},
				{Name: "bar", Line: 5},
			},
		},
	}
	err := AddFunctions(1, funcs)
	if !assert.NoError(t, err) {
		return
	}

	// Verify functions exist
	got, err := GetDocFunctions(1, "doc1")
	if !assert.NoError(t, err) {
		return
	}
	assert.Len(t, got, 2)

	// Delete document
	err = DeleteDocument(1, "doc1")
	if !assert.NoError(t, err) {
		return
	}

	// Verify functions are gone (Pebble returns nil,nil for missing keys)
	got, err = GetDocFunctions(1, "doc1")
	assert.NoError(t, err)
	assert.Empty(t, got)
}

func TestDeleteDocument_FeatureDisabled(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	// Disable feature flag
	origFlag := conf.Get().Symbols.EnableFeature
	conf.Get().Symbols.EnableFeature = false
	defer func() { conf.Get().Symbols.EnableFeature = origFlag }()

	mustCreateWorkspace(t, 1)

	err := DeleteDocument(1, "doc1")
	assert.NoError(t, err, "should silently return nil when feature disabled")
}

func TestDeleteDocument_NonExistentDoc(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	// Deleting a non-existent document should not error
	err := DeleteDocument(1, "nonexistent")
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// saveDocFunctions (internal, same package)
// ---------------------------------------------------------------------------

func TestSaveDocFunctions_GroupsByName(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	// Use AddFunctions to exercise saveDocFunctions internally
	funcs := []DocFunction{
		{
			ID:      "doc1",
			RelPath: "main.go",
			Functions: []Function{
				{Name: "process", Line: 10},
				{Name: "process", Line: 20},
				{Name: "handler", Line: 30},
			},
		},
	}
	err := AddFunctions(1, funcs)
	if !assert.NoError(t, err) {
		return
	}

	got, err := GetDocFunctions(1, "doc1")
	if !assert.NoError(t, err) {
		return
	}

	// Verify we have all entries
	assert.Len(t, got, 3)

	// Count by name
	nameCount := make(map[string]int)
	for _, f := range got {
		nameCount[f.Name]++
	}
	assert.Equal(t, 2, nameCount["process"])
	assert.Equal(t, 1, nameCount["handler"])
}

// ---------------------------------------------------------------------------
// Multiple workspaces
// ---------------------------------------------------------------------------

func TestMultipleWorkspaces_Isolation(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)
	mustCreateWorkspace(t, 2)

	// Add functions to workspace 1
	funcs1 := []DocFunction{
		{
			ID:        "doc1",
			RelPath:   "a.go",
			Functions: []Function{{Name: "ws1Func", Line: 1}},
		},
	}
	err := AddFunctions(1, funcs1)
	if !assert.NoError(t, err) {
		return
	}

	// Add functions to workspace 2
	funcs2 := []DocFunction{
		{
			ID:        "doc1",
			RelPath:   "a.go",
			Functions: []Function{{Name: "ws2Func", Line: 1}},
		},
	}
	err = AddFunctions(2, funcs2)
	if !assert.NoError(t, err) {
		return
	}

	// Verify workspace isolation
	got1, err := GetDocFunctions(1, "doc1")
	if !assert.NoError(t, err) {
		return
	}
	assert.Len(t, got1, 1)
	assert.Equal(t, "ws1Func", got1[0].Name)

	got2, err := GetDocFunctions(2, "doc1")
	if !assert.NoError(t, err) {
		return
	}
	assert.Len(t, got2, 1)
	assert.Equal(t, "ws2Func", got2[0].Name)
}
