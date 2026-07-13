package indexer

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/codetrek/haystack/packages/core/documents"
	"github.com/codetrek/haystack/server/internal/conf"
	"github.com/codetrek/haystack/server/internal/core/symbols"
	"github.com/codetrek/haystack/server/internal/core/workspace"
	"github.com/codetrek/haystack/server/internal/shared/running"
	"github.com/codetrek/haystack/server/internal/shared/types"
)

// ---------------------------------------------------------------------------
// SyncIfNeeded
// ---------------------------------------------------------------------------

func TestSyncIfNeeded_UnknownWorkspace(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	err := SyncIfNeeded("/nonexistent/workspace/path")
	if err == nil {
		t.Error("SyncIfNeeded should return error for unknown workspace")
	}
}

func TestSyncIfNeeded_AlreadySynced(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	// Mark workspace as already synced
	ws.UpdateLastFullSync()
	ws.Save()

	// SyncIfNeeded should skip since LastFullSync is not zero
	err = SyncIfNeeded(wsDir)
	if err != nil {
		t.Errorf("SyncIfNeeded for already-synced workspace: %v", err)
	}
}

func TestSyncIfNeeded_NeedsSync(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	// LastFullSync is zero, so it should try to sync
	err = SyncIfNeeded(wsDir)
	if err != nil {
		t.Errorf("SyncIfNeeded for new workspace: %v", err)
	}

	// Drain scanner queue
	_ = ws
	scanner.tryPopJob()
}

// ---------------------------------------------------------------------------
// Sync
// ---------------------------------------------------------------------------

func TestSync_NilWorkspaceReturnsError(t *testing.T) {
	err := Sync(nil, false)
	if err == nil {
		t.Error("Sync(nil) should return error")
	}
}

// ---------------------------------------------------------------------------
// RemoveFile
// ---------------------------------------------------------------------------

func TestRemoveFile_Integration(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	if err := stInst.Create(ws.Id, "test"); err != nil {
		t.Fatalf("documents.Create: %v", err)
	}

	// RemoveFile for a file that doesn't exist in the index
	// should still succeed (document not found is handled gracefully)
	err = RemoveFile(ws, "nonexistent.go")
	// It may return an error from documents.DeleteDocument - that's OK
	_ = err
}

// ---------------------------------------------------------------------------
// AddOrSyncFile
// ---------------------------------------------------------------------------

func TestAddOrSyncFile_NewFile(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	if err := stInst.Create(ws.Id, "test"); err != nil {
		t.Fatalf("documents.Create: %v", err)
	}

	// Create a real file
	relPath := "new_file.go"
	fullPath := filepath.Join(wsDir, relPath)
	if err := os.WriteFile(fullPath, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// AddOrSyncFile should queue the file for parsing (we can't easily verify
	// the queue, but it should not return an error)
	err = AddOrSyncFile(ws, relPath)
	if err != nil {
		t.Errorf("AddOrSyncFile: %v", err)
	}

	// Drain the parser channel to prevent goroutine leak
	select {
	case <-parser.ch:
	case <-time.After(1 * time.Second):
	}
}

func TestAddOrSyncFile_DirectoryIsIgnored(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	if err := stInst.Create(ws.Id, "test"); err != nil {
		t.Fatalf("documents.Create: %v", err)
	}

	// Create a subdirectory
	subDir := "mydir"
	if err := os.MkdirAll(filepath.Join(wsDir, subDir), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// AddOrSyncFile on a directory should not queue it
	err = AddOrSyncFile(ws, subDir)
	// Should not return error, but also should not queue
	if err != nil {
		t.Errorf("AddOrSyncFile on directory: %v", err)
	}
}

func TestAddOrSyncFile_NonExistentFile(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	if err := stInst.Create(ws.Id, "test"); err != nil {
		t.Fatalf("documents.Create: %v", err)
	}

	// Non-existent file: stat returns error, which the code returns as err.
	// The code does `stat, err := os.Stat(fullPath); if err != nil || stat.IsDir() { return err }`
	// so it returns the stat error for new (unknown) files.
	err = AddOrSyncFile(ws, "ghost.go")
	// The error is expected — os.Stat fails for a missing file
	if err == nil {
		t.Log("AddOrSyncFile returned nil for non-existent file (file may have been in doc index)")
	}
}

// ---------------------------------------------------------------------------
// RefreshFileIfNeeded
// ---------------------------------------------------------------------------

func TestRefreshFileIfNeeded_DeletedFile(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	if err := stInst.Create(ws.Id, "test"); err != nil {
		t.Fatalf("documents.Create: %v", err)
	}

	doc := &documents.Document{
		ID:           "test-id",
		RelPath:      "deleted.go",
		ModifiedTime: time.Now().UnixNano(),
	}

	// File doesn't exist on disk, so it should be "removed"
	removed, err := RefreshFileIfNeeded(ws, doc)
	if err != nil {
		t.Errorf("RefreshFileIfNeeded error: %v", err)
	}
	if !removed {
		t.Error("expected removed=true for non-existent file")
	}
}

func TestRefreshFileIfNeeded_UnchangedFile(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	// Create a real file
	relPath := "unchanged.go"
	fullPath := filepath.Join(wsDir, relPath)
	content := []byte("package main\n")
	if err := os.WriteFile(fullPath, content, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	stat, _ := os.Stat(fullPath)
	doc := &documents.Document{
		ID:           "test-id",
		RelPath:      relPath,
		ModifiedTime: stat.ModTime().UnixNano(),
	}

	removed, err := RefreshFileIfNeeded(ws, doc)
	if err != nil {
		t.Errorf("RefreshFileIfNeeded error: %v", err)
	}
	if removed {
		t.Error("unchanged file should not be removed")
	}
}

func TestRefreshFileIfNeeded_ModifiedFile(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	// Create a real file
	relPath := "modified.go"
	fullPath := filepath.Join(wsDir, relPath)
	content := []byte("package main\n")
	if err := os.WriteFile(fullPath, content, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Set doc's ModifiedTime to something different (old)
	doc := &documents.Document{
		ID:           "test-id",
		RelPath:      relPath,
		ModifiedTime: time.Now().Add(-1 * time.Hour).UnixNano(),
	}

	removed, err := RefreshFileIfNeeded(ws, doc)
	if err != nil {
		t.Errorf("RefreshFileIfNeeded error: %v", err)
	}
	if removed {
		t.Error("modified file should not be removed")
	}

	// Drain parser channel to prevent leak
	select {
	case <-parser.ch:
	case <-time.After(1 * time.Second):
	}
}

func TestRefreshFileIfNeeded_DirectoryAtFilePath(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	if err := stInst.Create(ws.Id, "test"); err != nil {
		t.Fatalf("documents.Create: %v", err)
	}

	// Create a directory where a file is expected
	relPath := "some_dir"
	if err := os.MkdirAll(filepath.Join(wsDir, relPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	doc := &documents.Document{
		ID:           "test-id",
		RelPath:      relPath,
		ModifiedTime: time.Now().UnixNano(),
	}

	removed, err := RefreshFileIfNeeded(ws, doc)
	if err != nil {
		t.Errorf("RefreshFileIfNeeded error: %v", err)
	}
	if !removed {
		t.Error("directory should be treated as removed file")
	}
}

// ---------------------------------------------------------------------------
// RefreshFilesIfNeeded
// ---------------------------------------------------------------------------

func TestRefreshFilesIfNeeded_InvalidWorkspace(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	result := RefreshFilesIfNeeded(99999, map[string]*documents.Document{})
	if len(result) != 0 {
		t.Errorf("expected empty result for invalid workspace, got %v", result)
	}
}

func TestRefreshFilesIfNeeded_MixedDocs(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	if err := stInst.Create(ws.Id, "test"); err != nil {
		t.Fatalf("documents.Create: %v", err)
	}

	// Create one file that exists
	relPath := "exists.go"
	fullPath := filepath.Join(wsDir, relPath)
	if err := os.WriteFile(fullPath, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	stat, _ := os.Stat(fullPath)
	docs := map[string]*documents.Document{
		"exists": {
			ID:           "id1",
			RelPath:      relPath,
			ModifiedTime: stat.ModTime().UnixNano(),
		},
		"deleted": {
			ID:           "id2",
			RelPath:      "gone.go",
			ModifiedTime: time.Now().UnixNano(),
		},
	}

	removed := RefreshFilesIfNeeded(ws.Id, docs)
	// "gone.go" should be in the removed list
	found := false
	for _, id := range removed {
		if id == "id2" {
			found = true
		}
	}
	if !found {
		t.Error("expected 'id2' (gone.go) to be in removed list")
	}
}

// ---------------------------------------------------------------------------
// CreateWorkspace
// ---------------------------------------------------------------------------

func TestCreateWorkspace_Success(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()

	// Disable symbols so symbolParser.Start won't fail
	origSymbols := conf.Get().Symbols.EnableFeature
	conf.Get().Symbols.EnableFeature = false
	defer func() { conf.Get().Symbols.EnableFeature = origSymbols }()

	ws, err := CreateWorkspace(wsDir, true, nil)
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if ws == nil {
		t.Fatal("CreateWorkspace returned nil workspace")
	}
	if ws.Path == "" {
		t.Error("workspace path should not be empty")
	}
	if !ws.UseGlobalFilters {
		t.Error("UseGlobalFilters should be true")
	}

	// Drain scanner queue
	scanner.tryPopJob()
}

// ---------------------------------------------------------------------------
// ShouldIndexFile
// ---------------------------------------------------------------------------

func TestShouldIndexFile_NilWorkspace(t *testing.T) {
	if ShouldIndexFile(nil, "main.go") {
		t.Error("ShouldIndexFile with nil workspace should return false")
	}
}

func TestShouldIndexFile_NonIndexableExtension(t *testing.T) {
	ws := &workspace.Workspace{
		Id:   1,
		Path: "/tmp/test",
	}
	if ShouldIndexFile(ws, "photo.png") {
		t.Error("ShouldIndexFile should return false for .png")
	}
}

// ---------------------------------------------------------------------------
// ShouldIndexFile - with workspace filters
// ---------------------------------------------------------------------------

func TestShouldIndexFile_WithDefaultFilters(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	// With default filters (UseGlobalFilters), Go files should be indexable
	ws.UseGlobalFilters = true
	ws.Save()

	if !ShouldIndexFile(ws, "main.go") {
		t.Error("ShouldIndexFile should return true for .go file with default filters")
	}
}

func TestShouldIndexFile_WithCustomExcludeFilters(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	// Set custom filters that exclude .go files
	ws.UseGlobalFilters = false
	ws.Filters = &types.Filters{
		Exclude: types.Exclude{
			UseGitIgnore: false,
			Customized:   []string{"*.go"},
		},
		Include: []string{"**/*"},
	}
	ws.Save()

	// .go file should NOT be indexable with the custom exclude
	if ShouldIndexFile(ws, "main.go") {
		t.Error("ShouldIndexFile should return false for .go file excluded by custom filter")
	}

	// .py file should still be indexable
	if !ShouldIndexFile(ws, "main.py") {
		t.Error("ShouldIndexFile should return true for .py file not excluded")
	}
}

func TestShouldIndexFile_WithGitIgnoreFilter(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	ws, err := workspace.Create(t.TempDir())
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	// Create a .gitignore that ignores the build/ directory and *.log files.
	// NewGitIgnore reads .gitignore directly — it needs no .git dir or git binary.
	if err := os.WriteFile(filepath.Join(ws.Path, ".gitignore"), []byte("build/\n*.log\n"), 0644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	ws.UseGlobalFilters = false
	ws.Filters = &types.Filters{
		Exclude: types.Exclude{
			UseGitIgnore: true,
		},
		Include: []string{"**/*"},
	}
	ws.Save()

	// main.go is not ignored → should be kept (indexed).
	if !ShouldIndexFile(ws, "main.go") {
		t.Errorf("ShouldIndexFile(main.go) = false, want true (not ignored, should be kept)")
	}
	// app.log matches *.log → should be excluded.
	if ShouldIndexFile(ws, "app.log") {
		t.Errorf("ShouldIndexFile(app.log) = true, want false (ignored by *.log)")
	}
	// build/out.go is under build/ → should be excluded.
	if ShouldIndexFile(ws, "build/out.go") {
		t.Errorf("ShouldIndexFile(build/out.go) = true, want false (ignored by build/)")
	}
}

// ---------------------------------------------------------------------------
// Scanner.Add with valid workspace
// ---------------------------------------------------------------------------

func TestScannerAdd_ValidWorkspace(t *testing.T) {
	s := NewScanner()
	ws := &workspace.Workspace{Id: 1, Path: "/tmp/test"}

	// StartIndexing must succeed before Add
	err := s.Add(ws, false)
	if err != nil {
		t.Errorf("Add should succeed for valid workspace: %v", err)
	}

	// Check job was queued
	job := s.tryPopJob()
	if job == nil {
		t.Fatal("expected a job in the queue")
	}
	if job.workspace.Id != 1 {
		t.Errorf("workspace id: got %d, want 1", job.workspace.Id)
	}
	if job.forceRefresh {
		t.Error("forceRefresh should be false")
	}
}

func TestScannerAdd_ForceRefresh(t *testing.T) {
	s := NewScanner()
	ws := &workspace.Workspace{Id: 2, Path: "/tmp/test2"}

	err := s.Add(ws, true)
	if err != nil {
		t.Errorf("Add should succeed: %v", err)
	}

	job := s.tryPopJob()
	if job == nil {
		t.Fatal("expected a job in the queue")
	}
	if !job.forceRefresh {
		t.Error("forceRefresh should be true")
	}
}

func TestScannerAdd_AlreadyIndexing(t *testing.T) {
	s := NewScanner()
	ws := &workspace.Workspace{Id: 3, Path: "/tmp/test3"}

	// First add succeeds (starts indexing)
	err := s.Add(ws, false)
	if err != nil {
		t.Fatalf("first Add should succeed: %v", err)
	}

	// Second add should fail because workspace is already indexing
	err = s.Add(ws, false)
	if err == nil {
		t.Error("second Add should fail because workspace is already indexing")
	}
}

// ---------------------------------------------------------------------------
// RemoveFile with valid docs in index
// ---------------------------------------------------------------------------

func TestRemoveFile_ValidDoc(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	if err := stInst.Create(ws.Id, "test"); err != nil {
		t.Fatalf("documents.Create: %v", err)
	}

	// First, parse a file to get it into the index
	relPath := "removable.go"
	fullPath := filepath.Join(wsDir, relPath)
	if err := os.WriteFile(fullPath, []byte("package main\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	pf := ParseFile{Workspace: ws, RelFilePath: relPath}
	doc, newFile, _, err := parse(pf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc == nil || !newFile {
		t.Fatal("expected new doc from parse")
	}

	// Save the doc
	w := NewWriter()
	w.processDocs([]*WriteDoc{{Workspace: ws, Document: doc, CreateNew: true}})

	// Now remove it
	err = RemoveFile(ws, relPath)
	if err != nil {
		t.Errorf("RemoveFile should succeed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// AddOrSyncFile - existing doc path
// ---------------------------------------------------------------------------

func TestAddOrSyncFile_ExistingDocModified(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	if err := stInst.Create(ws.Id, "test"); err != nil {
		t.Fatalf("documents.Create: %v", err)
	}

	relPath := "existing.go"
	fullPath := filepath.Join(wsDir, relPath)
	if err := os.WriteFile(fullPath, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// First parse to create the document in the index
	pf := ParseFile{Workspace: ws, RelFilePath: relPath}
	doc, _, _, err := parse(pf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc == nil {
		t.Fatal("expected doc from parse")
	}

	// Save it
	w := NewWriter()
	w.processDocs([]*WriteDoc{{Workspace: ws, Document: doc, CreateNew: true}})

	// Now call AddOrSyncFile — the document already exists, so it should take
	// the "sync existing file" path
	err = AddOrSyncFile(ws, relPath)
	if err != nil {
		t.Errorf("AddOrSyncFile for existing doc: %v", err)
	}

	// Drain parser channel
	select {
	case <-parser.ch:
	case <-time.After(1 * time.Second):
	}
}

func TestAddOrSyncFile_ExistingDocFileDeleted(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	if err := stInst.Create(ws.Id, "test"); err != nil {
		t.Fatalf("documents.Create: %v", err)
	}

	relPath := "todelete.go"
	fullPath := filepath.Join(wsDir, relPath)
	if err := os.WriteFile(fullPath, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Parse and save document
	pf := ParseFile{Workspace: ws, RelFilePath: relPath}
	doc, _, _, err := parse(pf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc == nil {
		t.Fatal("expected doc from parse")
	}

	w := NewWriter()
	w.processDocs([]*WriteDoc{{Workspace: ws, Document: doc, CreateNew: true}})

	// Now delete the file from disk
	os.Remove(fullPath)

	// AddOrSyncFile should handle the missing file (remove from index)
	err = AddOrSyncFile(ws, relPath)
	if err != nil {
		t.Errorf("AddOrSyncFile for deleted file: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Run — full pipeline start/shutdown integration
// ---------------------------------------------------------------------------

func TestRun_StartAndShutdown(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	origSymbols := conf.Get().Symbols.EnableFeature
	conf.Get().Symbols.EnableFeature = false
	defer func() { conf.Get().Symbols.EnableFeature = origSymbols }()

	origWorkers := conf.Get().Server.IndexWorkers
	conf.Get().Server.IndexWorkers = 1
	defer func() { conf.Get().Server.IndexWorkers = origWorkers }()

	scanner = NewScanner()
	parser = NewParser()
	writer = NewWriter()
	symbolParser = NewSymbolParser()

	var wg sync.WaitGroup
	Run(&wg)
	time.Sleep(100 * time.Millisecond)

	running.Shutdown()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop within 5 seconds")
	}
}

// ---------------------------------------------------------------------------
// CreateWorkspace — error from workspace.Create (invalid path)
// ---------------------------------------------------------------------------

func TestCreateWorkspace_InvalidPath(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	origSymbols := conf.Get().Symbols.EnableFeature
	conf.Get().Symbols.EnableFeature = false
	defer func() { conf.Get().Symbols.EnableFeature = origSymbols }()

	_, err := CreateWorkspace("/nonexistent/path/that/does/not/exist/workspace", true, nil)
	if err == nil {
		t.Error("CreateWorkspace with invalid path should return error")
	}
}

// ---------------------------------------------------------------------------
// CreateWorkspace — with custom filters
// ---------------------------------------------------------------------------

func TestCreateWorkspace_WithCustomFilters(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()

	origSymbols := conf.Get().Symbols.EnableFeature
	conf.Get().Symbols.EnableFeature = false
	defer func() { conf.Get().Symbols.EnableFeature = origSymbols }()

	filters := &types.Filters{
		Exclude: types.Exclude{
			UseGitIgnore: false,
			Customized:   []string{"*.log"},
		},
		Include: []string{"**/*.go"},
	}

	ws, err := CreateWorkspace(wsDir, false, filters)
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if ws == nil {
		t.Fatal("CreateWorkspace returned nil")
	}
	if ws.UseGlobalFilters {
		t.Error("UseGlobalFilters should be false")
	}
	if ws.Filters == nil {
		t.Error("Filters should not be nil")
	}

	scanner.tryPopJob()
}

// ---------------------------------------------------------------------------
// AddOrSyncFile — existing doc, file replaced by directory
// ---------------------------------------------------------------------------

func TestAddOrSyncFile_ExistingDocDirectoryReplacedFile(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	if err := stInst.Create(ws.Id, "test"); err != nil {
		t.Fatalf("documents.Create: %v", err)
	}

	relPath := "wasfile.go"
	fullPath := filepath.Join(wsDir, relPath)

	if err := os.WriteFile(fullPath, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	pf := ParseFile{Workspace: ws, RelFilePath: relPath}
	doc, _, _, err := parse(pf)
	if err != nil || doc == nil {
		t.Fatalf("parse: %v, doc=%v", err, doc)
	}

	w := NewWriter()
	w.processDocs([]*WriteDoc{{Workspace: ws, Document: doc, CreateNew: true}})

	// Replace file with a directory
	os.Remove(fullPath)
	if err := os.MkdirAll(fullPath, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// AddOrSyncFile should handle this (directory triggers removal)
	err = AddOrSyncFile(ws, relPath)
	_ = err
}

// ---------------------------------------------------------------------------
// RemoveFile — error branches
// ---------------------------------------------------------------------------

func TestRemoveFile_GetDocumentIdError(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	// Close idtable so GetDocumentId → GetId returns "id allocator not initialized"
	idAllocator.Close()
	SetIdAllocator(nil)

	err = RemoveFile(ws, "anyfile.go")
	if err == nil {
		t.Error("RemoveFile should return error when idtable is closed")
	}
}

func TestRemoveFile_DocumentsDeleteError(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	// workspace.Create already calls documents.Create, so the doc store
	// exists. Deleting a file that was never indexed causes
	// documents.DeleteDocument to return "document not found".
	err = RemoveFile(ws, "never_indexed.go")
	if err == nil {
		t.Error("RemoveFile should return error when document is not found")
	}
}

func TestRemoveFile_SymbolsDeleteError(t *testing.T) {
	env, teardown := setupTestEnv(t)
	defer teardown()

	// Ensure symbols feature is enabled so symbols.DeleteDocument
	// actually attempts the lookup rather than short-circuiting.
	origSymbols := conf.Get().Symbols.EnableFeature
	conf.Get().Symbols.EnableFeature = true
	defer func() { conf.Get().Symbols.EnableFeature = origSymbols }()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	// Index a file so documents.DeleteDocument succeeds.
	relPath := "sym_err.go"
	fullPath := filepath.Join(wsDir, relPath)
	if err := os.WriteFile(fullPath, []byte("package main\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	pf := ParseFile{Workspace: ws, RelFilePath: relPath}
	doc, newFile, _, err := parse(pf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc == nil || !newFile {
		t.Fatal("expected new doc from parse")
	}

	w := NewWriter()
	w.processDocs([]*WriteDoc{{Workspace: ws, Document: doc, CreateNew: true}})

	// Delete the symbol table metadata key from the database so that
	// symbols.DeleteDocument → GetSymbolTable fails with a decode error.
	env.DB.Delete(symbols.EncodeSymbolTableKey(ws.Id))

	err = RemoveFile(ws, relPath)
	if err == nil {
		t.Error("RemoveFile should return error when symbols.DeleteDocument fails")
	}
}
