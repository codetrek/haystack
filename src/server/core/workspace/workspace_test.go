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
