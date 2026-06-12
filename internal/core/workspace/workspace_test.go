package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/internal/core/storage"
	"github.com/codetrek/haystack/internal/shared/types"
	"github.com/codetrek/haystack/searchcore/collection"
	"github.com/codetrek/haystack/searchcore/documents"
	"github.com/codetrek/haystack/searchcore/queue"
)

// newTestStore creates a transient documents.Store for use in workspace unit
// tests. It returns the store and a cleanup function.
func newTestStoreWithCount(t *testing.T, wsId int, count int) (st *documents.Store, cleanup func()) {
	t.Helper()
	tempDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tempDir, "data"), 0)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	q := queue.NewMpsc("test-ws-queue")
	q.Start()
	st, err = documents.New(db, q, nil, documents.Options{})
	if err != nil {
		q.Stop()
		db.Close()
		t.Fatalf("documents.New: %v", err)
	}
	if err := st.Create(wsId, "test"); err != nil {
		st.CloseAndWait()
		q.Stop()
		db.Close()
		t.Fatalf("st.Create: %v", err)
	}
	if count > 0 {
		docs := make([]*documents.Document, count)
		for i := 0; i < count; i++ {
			docs[i] = &documents.Document{
				ID:      fmt.Sprintf("d%d", i),
				RelPath: fmt.Sprintf("f%d.go", i),
				Words:   []string{"w"},
			}
		}
		if err := st.SaveNewDocuments(wsId, docs); err != nil {
			st.CloseAndWait()
			q.Stop()
			db.Close()
			t.Fatalf("st.SaveNewDocuments: %v", err)
		}
	}
	cleanup = func() {
		st.CloseAndWait()
		q.Stop()
		db.Close()
	}
	return st, cleanup
}

func TestWorkspaceMethods(t *testing.T) {
	// Create a test workspace
	ws := &Workspace{
		Id:               99,
		Path:             "/test/path",
		UseGlobalFilters: true,
		CreatedAt:        time.Now(),
		LastAccessed:     time.Now(),
		LastFullSync:     time.Now(),
	}

	// Test StartIndexing
	err := ws.StartIndexing()
	if err != nil {
		t.Fatalf("StartIndexing failed: %v", err)
	}

	// Test AddIndexingFiles
	ws.AddIndexingFiles(3)
	status := ws.GetIndexingProgress()
	if status == nil {
		t.Fatal("Indexing status is nil")
	}
	if status.IndexedFiles != 3 {
		t.Errorf("AddIndexingFiles failed, got %d, want 3", status.IndexedFiles)
	}

	// Test GetTotalFiles by injecting a real store with 42 documents.
	st42, cleanSt42 := newTestStoreWithCount(t, ws.Id, 42)
	defer cleanSt42()
	old := docStoreInst
	SetDocStore(st42)
	defer func() { SetDocStore(old) }()
	totalFiles := ws.GetTotalFiles()
	if totalFiles != 42 {
		t.Errorf("GetTotalFiles failed, got %d, want 42", totalFiles)
	}

	// Test UpdateLastFullSync
	ws.UpdateLastFullSync()
	if ws.indexingProgress != nil {
		t.Error("UpdateLastFullSync failed to clear indexing progress")
	}

	// Test GetFilters
	filters := ws.GetFilters()
	if !reflect.DeepEqual(filters, conf.Get().Server.Filters) {
		t.Error("GetFilters failed to return global filters")
	}

	// Test SetDeleted and IsDeleted
	ws.SetDeleted()
	if !ws.IsDeleted() {
		t.Error("SetDeleted/IsDeleted failed")
	}

	// Test Serialize
	_, err = ws.Serialize()
	if err == nil {
		t.Error("Serialize should fail for deleted workspace")
	}
}

func TestGetTotalFiles_WithFunc(t *testing.T) {
	ws := &Workspace{Id: 7, Path: "/test"}

	st, cleanup := newTestStoreWithCount(t, 7, 100)
	defer cleanup()
	old := docStoreInst
	SetDocStore(st)
	defer func() { SetDocStore(old) }()

	total := ws.GetTotalFiles()
	if total != 100 {
		t.Errorf("GetTotalFiles = %d, want 100", total)
	}
}

func TestGetTotalFiles_WithoutFunc(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test"}

	old := docStoreInst
	SetDocStore(nil)
	defer func() { SetDocStore(old) }()

	total := ws.GetTotalFiles()
	if total != 0 {
		t.Errorf("GetTotalFiles = %d, want 0 when docStoreInst is nil", total)
	}
}

func TestGetTotalFiles_Concurrency(t *testing.T) {
	ws := &Workspace{
		Id:               98,
		Path:             "/test/path",
		UseGlobalFilters: true,
	}

	st, cleanup := newTestStoreWithCount(t, 98, 42)
	defer cleanup()
	old := docStoreInst
	SetDocStore(st)
	defer func() { SetDocStore(old) }()

	// Test concurrent access to GetTotalFiles
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			total := ws.GetTotalFiles()
			if total != 42 {
				t.Errorf("Concurrent GetTotalFiles = %d, want 42", total)
			}
		}()
	}
	wg.Wait()
}

func TestWorkspaceFilters(t *testing.T) {
	// Set up a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "haystack-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Set configuration
	conf.Get().Global.DataPath = tempDir

	// Create a test workspace
	ws := &Workspace{
		Id:               97,
		Path:             "/test/path",
		UseGlobalFilters: false,
		Filters: &types.Filters{
			Include: []string{"*.go"},
			Exclude: types.Exclude{Customized: []string{"*.test"}},
		},
	}

	// Test GetFilters returns custom filters
	filters := ws.GetFilters()
	if len(filters.Include) == 0 || filters.Include[0] != "*.go" || len(filters.Exclude.Customized) == 0 || filters.Exclude.Customized[0] != "*.test" {
		t.Error("GetFilters failed to return custom filters")
	}

	// Test GetFilters returns global filters
	ws.UseGlobalFilters = true
	filters = ws.GetFilters()
	if !reflect.DeepEqual(filters, conf.Get().Server.Filters) {
		t.Error("GetFilters failed to return global filters")
	}
}

func TestAddSymbolParsedFiles(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test"}

	// When no indexing progress, should be a no-op
	ws.AddSymbolParsedFiles(5)
	if ws.indexingProgress != nil {
		t.Error("AddSymbolParsedFiles should not create indexing progress")
	}

	// Start indexing, then add
	if err := ws.StartIndexing(); err != nil {
		t.Fatalf("StartIndexing failed: %v", err)
	}

	ws.AddSymbolParsedFiles(3)
	ws.AddSymbolParsedFiles(2)

	status := ws.GetIndexingProgress()
	if status == nil {
		t.Fatal("Indexing status should not be nil")
	}
	if status.SymbolParsedFiles != 5 {
		t.Errorf("SymbolParsedFiles = %d, want 5", status.SymbolParsedFiles)
	}
}

func TestAddIndexingFiles_NoIndexingProgress(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test"}
	ws.AddIndexingFiles(5)
	if ws.indexingProgress != nil {
		t.Error("AddIndexingFiles should not create indexing progress")
	}
}

func TestStartIndexing_AlreadyIndexing(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test"}
	if err := ws.StartIndexing(); err != nil {
		t.Fatalf("First StartIndexing failed: %v", err)
	}
	err := ws.StartIndexing()
	if err == nil {
		t.Error("StartIndexing should fail when already indexing")
	}
}

func TestSerialize_Success(t *testing.T) {
	ws := &Workspace{
		Id:               5,
		Path:             "/test/serialize",
		UseGlobalFilters: true,
		CreatedAt:        time.Now(),
		LastAccessed:     time.Now(),
	}
	data, err := ws.Serialize()
	if err != nil {
		t.Fatalf("Serialize failed: %v", err)
	}
	if len(data) == 0 {
		t.Error("Serialize returned empty data")
	}
}

func TestIsDeleted_Default(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test"}
	if ws.IsDeleted() {
		t.Error("New workspace should not be deleted")
	}
}

func TestUpdateLastFullSync_NoIndexingProgress(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test"}
	ws.UpdateLastFullSync()
	if ws.GetLastFullSync().IsZero() {
		t.Error("LastFullSync should be set after UpdateLastFullSync")
	}
}

func TestUpdateLastFullSync_ClearsIndexingProgress(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test"}
	if err := ws.StartIndexing(); err != nil {
		t.Fatalf("StartIndexing failed: %v", err)
	}
	ws.UpdateLastFullSync()
	if ws.indexingProgress != nil {
		t.Error("UpdateLastFullSync should clear indexing progress")
	}
	if ws.GetLastFullSync().IsZero() {
		t.Error("LastFullSync should be set after UpdateLastFullSync")
	}
}

func TestGetFilters_NilFilters_NotGlobal(t *testing.T) {
	ws := &Workspace{
		Id:               1,
		Path:             "/test",
		UseGlobalFilters: false,
		Filters:          nil,
	}
	filters := ws.GetFilters()
	globalFilters := conf.Get().Server.Filters
	if !reflect.DeepEqual(filters, globalFilters) {
		t.Error("GetFilters with nil Filters should return global filters")
	}
}

func TestGetFilters_CustomFilters_EmptyInclude(t *testing.T) {
	globalFilters := conf.Get().Server.Filters
	ws := &Workspace{
		Id:               1,
		Path:             "/test",
		UseGlobalFilters: false,
		Filters: &types.Filters{
			Include: []string{},
			Exclude: types.Exclude{UseGitIgnore: true, Customized: []string{"*.log"}},
		},
	}
	filters := ws.GetFilters()
	if !reflect.DeepEqual(filters.Include, globalFilters.Include) {
		t.Errorf("Empty Include should fallback to global, got %v", filters.Include)
	}
	if len(filters.Exclude.Customized) == 0 || filters.Exclude.Customized[0] != "*.log" {
		t.Errorf("Exclude should remain custom, got %v", filters.Exclude.Customized)
	}
}

func TestGetFilters_CustomFilters_NoGitIgnoreNoExclude(t *testing.T) {
	globalFilters := conf.Get().Server.Filters
	ws := &Workspace{
		Id:               1,
		Path:             "/test",
		UseGlobalFilters: false,
		Filters: &types.Filters{
			Include: []string{"*.go", "*.js"},
			Exclude: types.Exclude{UseGitIgnore: false, Customized: []string{}},
		},
	}
	filters := ws.GetFilters()
	if len(filters.Include) != 2 || filters.Include[0] != "*.go" {
		t.Errorf("Include should be custom, got %v", filters.Include)
	}
	if !reflect.DeepEqual(filters.Exclude.Customized, globalFilters.Exclude.Customized) {
		t.Errorf("Exclude.Customized should fallback to global, got %v", filters.Exclude.Customized)
	}
}

func TestGetFilters_CustomFilters_WithGitIgnoreNoCustomized(t *testing.T) {
	ws := &Workspace{
		Id:               1,
		Path:             "/test",
		UseGlobalFilters: false,
		Filters: &types.Filters{
			Include: []string{"*.go"},
			Exclude: types.Exclude{UseGitIgnore: true, Customized: []string{}},
		},
	}
	filters := ws.GetFilters()
	if len(filters.Exclude.Customized) != 0 {
		t.Errorf("Exclude.Customized should stay empty when UseGitIgnore=true, got %v", filters.Exclude.Customized)
	}
}

func TestGetFilters_CustomFilters_BothPopulated(t *testing.T) {
	ws := &Workspace{
		Id:               1,
		Path:             "/test",
		UseGlobalFilters: false,
		Filters: &types.Filters{
			Include: []string{"*.rs"},
			Exclude: types.Exclude{UseGitIgnore: false, Customized: []string{"target/"}},
		},
	}
	filters := ws.GetFilters()
	if filters.Include[0] != "*.rs" {
		t.Errorf("Include should be custom, got %v", filters.Include)
	}
	if filters.Exclude.Customized[0] != "target/" {
		t.Errorf("Exclude.Customized should be custom, got %v", filters.Exclude.Customized)
	}
}

func TestSave_Deleted(t *testing.T) {
	ws := &Workspace{
		Id:               1,
		Path:             "/test/save-deleted",
		UseGlobalFilters: true,
	}
	ws.SetDeleted()

	err := ws.Save()
	if err == nil {
		t.Fatal("Save should fail for deleted workspace")
	}
}

// TestSave_CatalogPutError verifies that Save returns an error when the
// underlying catalog.Save fails (closed database).
func TestSave_CatalogPutError(t *testing.T) {
	tempDir := t.TempDir()
	conf.Get().Global.DataPath = tempDir

	db, err := storage.Open(filepath.Join(tempDir, "data"), 0)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}

	q := queue.NewMpsc("test-save-q")
	q.Start()

	// Use nil invertedindex so there are no background goroutines that require
	// the DB to stay open after we close it.
	st, err := documents.New(db, q, nil, documents.Options{})
	if err != nil {
		q.Stop()
		db.Close()
		t.Fatalf("documents.New: %v", err)
	}

	cat, err := collection.New(db, st, collection.Options{})
	if err != nil {
		st.CloseAndWait()
		q.Stop()
		db.Close()
		t.Fatalf("collection.New: %v", err)
	}

	oldCat := catalog
	catalog = cat
	defer func() { catalog = oldCat }()

	// Create a workspace record so catalog has id=1 in its in-memory index.
	col, err := cat.Create("/test/save-dbput")
	if err != nil {
		st.CloseAndWait()
		q.Stop()
		db.Close()
		t.Fatalf("cat.Create: %v", err)
	}
	meta := col.Meta()

	ws := &Workspace{
		Id:           meta.ID,
		Path:         "/test/save-dbput",
		CreatedAt:    time.Now(),
		LastAccessed: time.Now(),
	}

	// Drain the queue and stop all background workers before closing the DB.
	st.CloseAndWait()
	q.Stop()

	// Now close the DB so that db.Put() returns an error on the next Save.
	db.Close()

	err = ws.Save()
	if err == nil {
		t.Fatal("Save should return error when db.Put fails on closed database")
	}
}
