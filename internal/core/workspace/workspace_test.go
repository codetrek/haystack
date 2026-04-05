package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/internal/core/storage"
	"github.com/codetrek/haystack/internal/core/workspace/internal"
	"github.com/codetrek/haystack/internal/shared/types"
)

func TestWorkspaceMethods(t *testing.T) {
	// Create a test workspace
	ws := &Workspace{
		Id:               99,
		Path:             "/test/path",
		UseGlobalFilters: true,
		TotalFiles:       0,
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
	status := ws.GetIndexingStatus()
	if status == nil {
		t.Fatal("Indexing status is nil")
	}
	if status.IndexedFiles != 3 {
		t.Errorf("AddIndexingFiles failed, got %d, want 3", status.IndexedFiles)
	}

	// Test GetTotalFiles with CountByWorkspaceFunc set
	CountByWorkspaceFunc = func(wsId int) int { return 42 }
	defer func() { CountByWorkspaceFunc = nil }()
	totalFiles := ws.GetTotalFiles()
	if totalFiles != 42 {
		t.Errorf("GetTotalFiles failed, got %d, want 42", totalFiles)
	}

	// Test UpdateLastFullSync
	ws.UpdateLastFullSync()
	if ws.indexingStatus != nil {
		t.Error("UpdateLastFullSync failed to clear indexing status")
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

	CountByWorkspaceFunc = func(wsId int) int {
		if wsId == 7 {
			return 100
		}
		return 0
	}
	defer func() { CountByWorkspaceFunc = nil }()

	total := ws.GetTotalFiles()
	if total != 100 {
		t.Errorf("GetTotalFiles = %d, want 100", total)
	}
}

func TestGetTotalFiles_WithoutFunc(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test"}

	old := CountByWorkspaceFunc
	CountByWorkspaceFunc = nil
	defer func() { CountByWorkspaceFunc = old }()

	total := ws.GetTotalFiles()
	if total != 0 {
		t.Errorf("GetTotalFiles = %d, want 0 when CountByWorkspaceFunc is nil", total)
	}
}

func TestGetTotalFiles_Concurrency(t *testing.T) {
	ws := &Workspace{
		Id:               98,
		Path:             "/test/path",
		UseGlobalFilters: true,
	}

	CountByWorkspaceFunc = func(wsId int) int { return 42 }
	defer func() { CountByWorkspaceFunc = nil }()

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

	// When no indexing status, should be a no-op
	ws.AddSymbolParsedFiles(5)
	if ws.indexingStatus != nil {
		t.Error("AddSymbolParsedFiles should not create indexing status")
	}

	// Start indexing, then add
	if err := ws.StartIndexing(); err != nil {
		t.Fatalf("StartIndexing failed: %v", err)
	}

	ws.AddSymbolParsedFiles(3)
	ws.AddSymbolParsedFiles(2)

	status := ws.GetIndexingStatus()
	if status == nil {
		t.Fatal("Indexing status should not be nil")
	}
	if status.SymbolParsedFiles != 5 {
		t.Errorf("SymbolParsedFiles = %d, want 5", status.SymbolParsedFiles)
	}
}

func TestAddIndexingFiles_NoIndexingStatus(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test"}
	ws.AddIndexingFiles(5)
	if ws.indexingStatus != nil {
		t.Error("AddIndexingFiles should not create indexing status")
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
		TotalFiles:       10,
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

func TestUpdateLastFullSync_NoIndexingStatus(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test", TotalFiles: 50}
	ws.UpdateLastFullSync()
	// TotalFiles should remain unchanged (it's no longer overwritten)
	if ws.TotalFiles != 50 {
		t.Errorf("TotalFiles changed to %d, should stay 50", ws.TotalFiles)
	}
	if ws.GetLastFullSync().IsZero() {
		t.Error("LastFullSync should be set after UpdateLastFullSync")
	}
}

func TestUpdateLastFullSync_ClearsIndexingStatus(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test"}
	if err := ws.StartIndexing(); err != nil {
		t.Fatalf("StartIndexing failed: %v", err)
	}
	ws.UpdateLastFullSync()
	if ws.indexingStatus != nil {
		t.Error("UpdateLastFullSync should clear indexing status")
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

func TestSave_DbPutError(t *testing.T) {
	// Set up a temporary directory and open a real DB.
	tempDir, err := os.MkdirTemp("", "haystack-test-save-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	conf.Get().Global.DataPath = tempDir

	db, err := storage.Open(filepath.Join(tempDir, "data"), 0)
	if err != nil {
		t.Fatalf("Failed to open storage: %v", err)
	}

	// Initialize the internal package with this DB.
	internal.Init(db)

	ws := &Workspace{
		Id:               1,
		Path:             "/test/save-dbput",
		UseGlobalFilters: true,
		CreatedAt:        time.Now(),
		LastAccessed:     time.Now(),
	}

	// Close the DB so that db.Put() returns an error.
	db.Close()

	err = ws.Save()
	if err == nil {
		t.Fatal("Save should return error when db.Put fails on closed database")
	}
}
