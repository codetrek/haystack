package workspace

import (
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/codetrek/haystack/conf"
	"github.com/codetrek/haystack/shared/types"
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

	// Test AddTotalFiles
	ws.AddTotalFiles(5)
	if ws.TotalFiles != 5 {
		t.Errorf("AddTotalFiles failed, got %d, want 5", ws.TotalFiles)
	}

	// Test StartIndexing
	err := ws.StartIndexing()
	if err != nil {
		t.Fatalf("StartIndexing failed: %v", err)
	}

	// Test AddIndexingFiles and AddIndexingTotalFiles
	ws.AddIndexingTotalFiles(10)
	ws.AddIndexingFiles(3)
	status := ws.GetIndexingStatus()
	if status == nil {
		t.Fatal("Indexing status is nil")
	}
	if status.TotalFiles != 10 {
		t.Errorf("AddIndexingTotalFiles failed, got %d, want 10", status.TotalFiles)
	}
	if status.IndexedFiles != 3 {
		t.Errorf("AddIndexingFiles failed, got %d, want 3", status.IndexedFiles)
	}

	// Test GetTotalFiles
	totalFiles := ws.GetTotalFiles()
	if totalFiles != 5 {
		t.Errorf("GetTotalFiles failed, got %d, want 5", totalFiles)
	}

	// Test UpdateLastFullSync
	ws.UpdateLastFullSync()
	if ws.indexingStatus != nil {
		t.Error("UpdateLastFullSync failed to clear indexing status")
	}
	if ws.TotalFiles != 10 {
		t.Errorf("UpdateLastFullSync failed to update total files, got %d, want 10", ws.TotalFiles)
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

func TestWorkspaceTotalFilesConcurrency(t *testing.T) {
	ws := &Workspace{
		Id:               98,
		Path:             "/test/path",
		UseGlobalFilters: true,
		TotalFiles:       0,
	}

	// Test concurrent access
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ws.AddTotalFiles(1)
			ws.GetTotalFiles()
		}()
	}
	wg.Wait()

	if ws.TotalFiles != 100 {
		t.Errorf("Concurrent access failed, got %d, want 100", ws.TotalFiles)
	}
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

func TestAddIndexingTotalFiles_NoIndexingStatus(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test"}
	ws.AddIndexingTotalFiles(5)
	if ws.indexingStatus != nil {
		t.Error("AddIndexingTotalFiles should not create indexing status")
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

func TestGetTotalFiles_FallbackToIndexingStatus(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test", TotalFiles: 0}
	if err := ws.StartIndexing(); err != nil {
		t.Fatalf("StartIndexing failed: %v", err)
	}
	ws.AddIndexingTotalFiles(42)
	total := ws.GetTotalFiles()
	if total != 42 {
		t.Errorf("GetTotalFiles = %d, want 42 (fallback to indexing status)", total)
	}
}

func TestGetTotalFiles_PrefersTotalFiles(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test", TotalFiles: 100}
	if err := ws.StartIndexing(); err != nil {
		t.Fatalf("StartIndexing failed: %v", err)
	}
	ws.AddIndexingTotalFiles(50)
	total := ws.GetTotalFiles()
	if total != 100 {
		t.Errorf("GetTotalFiles = %d, want 100 (should prefer TotalFiles)", total)
	}
}

func TestGetTotalFiles_NoIndexingStatusZero(t *testing.T) {
	ws := &Workspace{Id: 1, Path: "/test", TotalFiles: 0}
	total := ws.GetTotalFiles()
	if total != 0 {
		t.Errorf("GetTotalFiles = %d, want 0", total)
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
	before := ws.TotalFiles
	ws.UpdateLastFullSync()
	if ws.TotalFiles != before {
		t.Errorf("TotalFiles changed from %d to %d without indexing status", before, ws.TotalFiles)
	}
	if ws.LastFullSync.IsZero() {
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
