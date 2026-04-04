package indexer

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/internal/core/workspace"
)

func TestGetLangFromFilename_JavaScript(t *testing.T) {
	cases := map[string]string{
		"app.js":        "javascript",
		"component.jsx": "javascript",
		"APP.JS":        "javascript",
		"file.JSX":      "javascript",
	}
	for file, want := range cases {
		got := GetLangFromFilename(file)
		if got != want {
			t.Errorf("GetLangFromFilename(%q) = %q, want %q", file, got, want)
		}
	}
}

func TestGetLangFromFilename_TypeScript(t *testing.T) {
	cases := map[string]string{
		"app.ts":        "typescript",
		"component.tsx": "typescript",
		"FILE.TS":       "typescript",
	}
	for file, want := range cases {
		got := GetLangFromFilename(file)
		if got != want {
			t.Errorf("GetLangFromFilename(%q) = %q, want %q", file, got, want)
		}
	}
}

func TestGetLangFromFilename_Python(t *testing.T) {
	for _, file := range []string{"main.py", "script.PY"} {
		got := GetLangFromFilename(file)
		if got != "python" {
			t.Errorf("GetLangFromFilename(%q) = %q, want python", file, got)
		}
	}
}

func TestGetLangFromFilename_Rust(t *testing.T) {
	got := GetLangFromFilename("lib.rs")
	if got != "rust" {
		t.Errorf("GetLangFromFilename(lib.rs) = %q, want rust", got)
	}
}

func TestGetLangFromFilename_Go(t *testing.T) {
	got := GetLangFromFilename("main.go")
	if got != "go" {
		t.Errorf("GetLangFromFilename(main.go) = %q, want go", got)
	}
}

func TestGetLangFromFilename_CPlusPlus(t *testing.T) {
	cppFiles := []string{
		"main.cc", "lib.cpp", "module.cxx",
		"header.h", "header.hh", "header.hxx", "header.hpp",
	}
	for _, file := range cppFiles {
		got := GetLangFromFilename(file)
		if got != "c++" {
			t.Errorf("GetLangFromFilename(%q) = %q, want c++", file, got)
		}
	}
}

func TestGetLangFromFilename_C(t *testing.T) {
	got := GetLangFromFilename("main.c")
	if got != "c" {
		t.Errorf("GetLangFromFilename(main.c) = %q, want c", got)
	}
}

func TestGetLangFromFilename_CSharp(t *testing.T) {
	got := GetLangFromFilename("Program.cs")
	if got != "C#" {
		t.Errorf("GetLangFromFilename(Program.cs) = %q, want C#", got)
	}
}

func TestGetLangFromFilename_Ruby(t *testing.T) {
	got := GetLangFromFilename("app.rb")
	if got != "ruby" {
		t.Errorf("GetLangFromFilename(app.rb) = %q, want ruby", got)
	}
}

func TestGetLangFromFilename_Java(t *testing.T) {
	got := GetLangFromFilename("Main.java")
	if got != "Java" {
		t.Errorf("GetLangFromFilename(Main.java) = %q, want Java", got)
	}
}

func TestGetLangFromFilename_PHP(t *testing.T) {
	got := GetLangFromFilename("index.php")
	if got != "php" {
		t.Errorf("GetLangFromFilename(index.php) = %q, want php", got)
	}
}

func TestGetLangFromFilename_Swift(t *testing.T) {
	got := GetLangFromFilename("App.swift")
	if got != "swift" {
		t.Errorf("GetLangFromFilename(App.swift) = %q, want swift", got)
	}
}

func TestGetLangFromFilename_UnrecognizedExtension(t *testing.T) {
	unknowns := []string{
		"data.csv", "config.yaml", "file.toml", "readme.md",
		"style.css", "Makefile", "Dockerfile",
	}
	for _, file := range unknowns {
		got := GetLangFromFilename(file)
		if got != "" {
			t.Errorf("GetLangFromFilename(%q) = %q, want empty string", file, got)
		}
	}
}

func TestGetLangFromFilename_NoExtension(t *testing.T) {
	got := GetLangFromFilename("Makefile")
	if got != "" {
		t.Errorf("GetLangFromFilename(Makefile) = %q, want empty string", got)
	}
}

func TestGetLangFromFilename_PathWithDirectories(t *testing.T) {
	got := GetLangFromFilename("src/server/main.go")
	if got != "go" {
		t.Errorf("GetLangFromFilename(src/server/main.go) = %q, want go", got)
	}
}

func TestGetLangFromFilename_DotFile(t *testing.T) {
	got := GetLangFromFilename(".gitignore")
	if got != "" {
		t.Errorf("GetLangFromFilename(.gitignore) = %q, want empty string", got)
	}
}

func TestGetLangFromFilename_DoubleDotExtension(t *testing.T) {
	// e.g. "test.spec.ts" - should pick up .ts
	got := GetLangFromFilename("test.spec.ts")
	if got != "typescript" {
		t.Errorf("GetLangFromFilename(test.spec.ts) = %q, want typescript", got)
	}
}

// ---------------------------------------------------------------------------
// NewSymbolParser
// ---------------------------------------------------------------------------

func TestNewSymbolParser_ReturnsInitialized(t *testing.T) {
	sp := NewSymbolParser()
	if sp == nil {
		t.Fatal("NewSymbolParser() returned nil")
	}
	if sp.ch == nil {
		t.Error("channel should be initialized")
	}
	if sp.stop == nil {
		t.Error("stop channel should be initialized")
	}
	if sp.done == nil {
		t.Error("done channel should be initialized")
	}
	if sp.cacheMap == nil {
		t.Error("cacheMap should be initialized")
	}
	if sp.flushTimer == nil {
		t.Error("flushTimer should be initialized")
	}
	if sp.ctags != "" {
		t.Errorf("ctags should be empty initially, got %q", sp.ctags)
	}
}

func TestNewSymbolParser_ChannelBufferSize(t *testing.T) {
	sp := NewSymbolParser()
	cap := cap(sp.ch)
	if cap != 32 {
		t.Errorf("expected channel buffer size 32, got %d", cap)
	}
}

func TestNewSymbolParser_EmptyCacheMap(t *testing.T) {
	sp := NewSymbolParser()
	if len(sp.cacheMap) != 0 {
		t.Errorf("cacheMap should start empty, got %d entries", len(sp.cacheMap))
	}
}

// ---------------------------------------------------------------------------
// SymbolParser.Start - disabled feature
// ---------------------------------------------------------------------------

func TestSymbolParserStart_DisabledFeature(t *testing.T) {
	sp := NewSymbolParser()
	var wg sync.WaitGroup

	// Disable symbols feature
	orig := conf.Get().Symbols.EnableFeature
	conf.Get().Symbols.EnableFeature = false
	defer func() { conf.Get().Symbols.EnableFeature = orig }()

	sp.Start(&wg)

	// When disabled, Start should not add to waitgroup - wg should resolve immediately
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success - no goroutines were started
	case <-time.After(1 * time.Second):
		t.Fatal("Start with disabled feature should not start goroutines")
	}
}

// ---------------------------------------------------------------------------
// SymbolParser.Stop - disabled feature
// ---------------------------------------------------------------------------

func TestSymbolParserStop_DisabledFeature(t *testing.T) {
	// When feature is disabled, Stop should be a no-op
	orig := conf.Get().Symbols.EnableFeature
	conf.Get().Symbols.EnableFeature = false
	defer func() { conf.Get().Symbols.EnableFeature = orig }()

	sp := NewSymbolParser()
	// Should not panic
	sp.Stop()
}

// ---------------------------------------------------------------------------
// SymbolParser.Add - disabled feature
// ---------------------------------------------------------------------------

func TestSymbolParserAdd_DisabledFeature(t *testing.T) {
	orig := conf.Get().Symbols.EnableFeature
	conf.Get().Symbols.EnableFeature = false
	defer func() { conf.Get().Symbols.EnableFeature = orig }()

	sp := NewSymbolParser()
	ws := &workspace.Workspace{Id: 1, Path: "/tmp/test"}

	// Should not panic and should not add to cache
	sp.Add(ws, "main.go")

	if len(sp.cacheMap) != 0 {
		t.Error("Add should not cache when feature is disabled")
	}
}

func TestSymbolParserAdd_NoCtagsPath(t *testing.T) {
	orig := conf.Get().Symbols.EnableFeature
	conf.Get().Symbols.EnableFeature = true
	defer func() { conf.Get().Symbols.EnableFeature = orig }()

	sp := NewSymbolParser()
	sp.ctags = "" // No ctags configured
	ws := &workspace.Workspace{Id: 1, Path: "/tmp/test"}

	// Should not panic and should not add to cache
	sp.Add(ws, "main.go")

	if len(sp.cacheMap) != 0 {
		t.Error("Add should not cache when ctags path is empty")
	}
}

// ---------------------------------------------------------------------------
// SymbolParser.flushCache
// ---------------------------------------------------------------------------

func TestSymbolParserFlushCache_EmptyCache(t *testing.T) {
	sp := NewSymbolParser()
	// Should not panic on empty cache
	sp.flushCache()
	if len(sp.cacheMap) != 0 {
		t.Error("cacheMap should still be empty after flushing empty cache")
	}
}

func TestSymbolParserFlushCache_WithEmptyFiles(t *testing.T) {
	sp := NewSymbolParser()
	ws := &workspace.Workspace{Id: 1, Path: "/tmp/test"}

	sp.cacheMutex.Lock()
	sp.cacheMap[ws] = []string{} // Empty file list
	sp.cacheMutex.Unlock()

	sp.flushCache()

	// When totalFiles == 0, flushCache returns early WITHOUT clearing the map.
	// The map still contains the entry with empty files.
	sp.cacheMutex.Lock()
	defer sp.cacheMutex.Unlock()
	if len(sp.cacheMap) != 1 {
		t.Errorf("expected cacheMap to still have 1 entry (early return), got %d", len(sp.cacheMap))
	}
}

func TestSymbolParserFlushCache_WithFiles(t *testing.T) {
	sp := NewSymbolParser()
	ws := &workspace.Workspace{Id: 1, Path: "/tmp/test"}

	sp.cacheMutex.Lock()
	sp.cacheMap[ws] = []string{"main.go", "lib.go"}
	sp.cacheMutex.Unlock()

	// Drain the channel in background to avoid blocking
	done := make(chan struct{})
	go func() {
		batch := <-sp.ch
		if batch.Workspace.Id != 1 {
			t.Errorf("expected workspace id 1, got %d", batch.Workspace.Id)
		}
		if len(batch.Files) != 2 {
			t.Errorf("expected 2 files, got %d", len(batch.Files))
		}
		close(done)
	}()

	sp.flushCache()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("flushCache did not send batch within 2 seconds")
	}
}

func TestSymbolParserFlushCache_MultipleWorkspaces(t *testing.T) {
	sp := NewSymbolParser()
	ws1 := &workspace.Workspace{Id: 1, Path: "/tmp/a"}
	ws2 := &workspace.Workspace{Id: 2, Path: "/tmp/b"}

	sp.cacheMutex.Lock()
	sp.cacheMap[ws1] = []string{"a.go"}
	sp.cacheMap[ws2] = []string{"b.go"}
	sp.cacheMutex.Unlock()

	// Drain the channel for both batches
	received := make(chan ParseBatch, 2)
	go func() {
		for i := 0; i < 2; i++ {
			batch := <-sp.ch
			received <- batch
		}
		close(received)
	}()

	sp.flushCache()

	// Collect results
	timeout := time.After(2 * time.Second)
	count := 0
	for {
		select {
		case _, ok := <-received:
			if !ok {
				goto done
			}
			count++
		case <-timeout:
			t.Fatal("timeout waiting for batches")
		}
	}
done:
	if count != 2 {
		t.Errorf("expected 2 batches, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// MaxBatchSize constant
// ---------------------------------------------------------------------------

func TestMaxBatchSize_Value(t *testing.T) {
	if MaxBatchSize != 1000 {
		t.Errorf("MaxBatchSize = %d, want 1000", MaxBatchSize)
	}
}

// ---------------------------------------------------------------------------
// ParseBatch struct
// ---------------------------------------------------------------------------

func TestParseBatch_Fields(t *testing.T) {
	ws := &workspace.Workspace{Id: 5, Path: "/tmp/batch"}
	pb := ParseBatch{
		Workspace: ws,
		Files:     []string{"a.go", "b.py", "c.rs"},
	}

	if pb.Workspace.Id != 5 {
		t.Errorf("workspace id: got %d, want 5", pb.Workspace.Id)
	}
	if len(pb.Files) != 3 {
		t.Errorf("files count: got %d, want 3", len(pb.Files))
	}
}

// ---------------------------------------------------------------------------
// getCtagsPath - basic test (won't find ctags in test env)
// ---------------------------------------------------------------------------

func TestGetCtagsPath_NoCtags(t *testing.T) {
	// Clear configured path
	orig := conf.Get().BinPath.CTags
	conf.Get().BinPath.CTags = ""
	defer func() { conf.Get().BinPath.CTags = orig }()

	_, err := getCtagsPath()
	// In test environment without ctags, this should return error
	if err == nil {
		// If it succeeds, ctags happens to be installed - that's fine
		t.Log("ctags found (expected in some environments)")
	}
}

func TestGetCtagsPath_WithConfiguredPath(t *testing.T) {
	orig := conf.Get().BinPath.CTags
	conf.Get().BinPath.CTags = "/nonexistent/ctags"
	defer func() { conf.Get().BinPath.CTags = orig }()

	_, err := getCtagsPath()
	if err == nil {
		t.Error("expected error for non-existent ctags path")
	}
}

// ---------------------------------------------------------------------------
// SymbolParser.Add - caching behavior with enabled feature + ctags
// ---------------------------------------------------------------------------

func TestSymbolParserAdd_CachesFiles(t *testing.T) {
	orig := conf.Get().Symbols.EnableFeature
	conf.Get().Symbols.EnableFeature = true
	defer func() { conf.Get().Symbols.EnableFeature = orig }()

	sp := NewSymbolParser()
	sp.ctags = "/fake/ctags" // Pretend ctags exists for caching logic

	ws := &workspace.Workspace{Id: 1, Path: "/tmp/test"}
	sp.Add(ws, "main.go")
	sp.Add(ws, "lib.go")

	sp.cacheMutex.Lock()
	files := sp.cacheMap[ws]
	sp.cacheMutex.Unlock()

	if len(files) != 2 {
		t.Errorf("expected 2 cached files, got %d", len(files))
	}
}

func TestSymbolParserAdd_MultipleWorkspaces(t *testing.T) {
	orig := conf.Get().Symbols.EnableFeature
	conf.Get().Symbols.EnableFeature = true
	defer func() { conf.Get().Symbols.EnableFeature = orig }()

	sp := NewSymbolParser()
	sp.ctags = "/fake/ctags"

	ws1 := &workspace.Workspace{Id: 1, Path: "/tmp/a"}
	ws2 := &workspace.Workspace{Id: 2, Path: "/tmp/b"}

	sp.Add(ws1, "a.go")
	sp.Add(ws2, "b.go")
	sp.Add(ws1, "c.go")

	sp.cacheMutex.Lock()
	files1 := sp.cacheMap[ws1]
	files2 := sp.cacheMap[ws2]
	sp.cacheMutex.Unlock()

	if len(files1) != 2 {
		t.Errorf("ws1: expected 2 cached files, got %d", len(files1))
	}
	if len(files2) != 1 {
		t.Errorf("ws2: expected 1 cached file, got %d", len(files2))
	}
}

// ---------------------------------------------------------------------------
// SymbolParser concurrent cache safety
// ---------------------------------------------------------------------------

func TestSymbolParserAdd_ConcurrentSafety(t *testing.T) {
	orig := conf.Get().Symbols.EnableFeature
	conf.Get().Symbols.EnableFeature = true
	defer func() { conf.Get().Symbols.EnableFeature = orig }()

	sp := NewSymbolParser()
	sp.ctags = "/fake/ctags"

	ws := &workspace.Workspace{Id: 1, Path: "/tmp/test"}

	var wg sync.WaitGroup
	const n = 50

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sp.Add(ws, "file.go")
		}(i)
	}

	wg.Wait()

	sp.cacheMutex.Lock()
	count := len(sp.cacheMap[ws])
	sp.cacheMutex.Unlock()

	if count != n {
		t.Errorf("expected %d cached files, got %d", n, count)
	}
}

// ---------------------------------------------------------------------------
// processFileBatch — exercises file grouping and temp file logic
// ---------------------------------------------------------------------------

func TestProcessFileBatch_NoRecognizedLanguage(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	sp := NewSymbolParser()
	sp.ctags = "/fake/ctags" // Will not be called since no files have recognized language

	batch := ParseBatch{
		Workspace: ws,
		Files:     []string{"readme.md", "config.yaml", "Makefile"},
	}

	err = sp.processFileBatch(batch)
	if err != nil {
		t.Errorf("processFileBatch with no recognized language files: %v", err)
	}
}

func TestProcessFileBatch_EmptyBatch(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	sp := NewSymbolParser()
	sp.ctags = "/fake/ctags"

	batch := ParseBatch{
		Workspace: ws,
		Files:     []string{},
	}

	err = sp.processFileBatch(batch)
	if err != nil {
		t.Errorf("processFileBatch with empty batch: %v", err)
	}
}

func TestProcessFileBatch_MixedFiles(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()

	// Create actual files to make stat work
	for _, name := range []string{"main.go", "lib.py", "readme.md"} {
		fpath := filepath.Join(wsDir, name)
		if err := os.WriteFile(fpath, []byte("content"), 0644); err != nil {
			t.Fatalf("write file %s: %v", name, err)
		}
	}

	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	sp := NewSymbolParser()
	// ctags is invalid, so parseFunction will fail, but that's handled gracefully
	sp.ctags = "/nonexistent/ctags"

	batch := ParseBatch{
		Workspace: ws,
		Files:     []string{"main.go", "lib.py", "readme.md"},
	}

	// Should not panic — errors from parseFunction are logged but not fatal
	err = sp.processFileBatch(batch)
	// err may or may not be nil depending on temp dir creation
	_ = err
}

// ---------------------------------------------------------------------------
// processFileBatch — with fake ctags producing JSON (successful parse path)
// ---------------------------------------------------------------------------

func TestProcessFileBatch_SuccessfulParseFunctions(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	// Create Go files with actual content
	goFile1 := filepath.Join(wsDir, "main.go")
	goFile2 := filepath.Join(wsDir, "lib.go")
	if err := os.WriteFile(goFile1, []byte("package main\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.WriteFile(goFile2, []byte("package main\nfunc helper() {}\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Create fake ctags that outputs JSON with found functions for main.go
	// but NOT for lib.go — so lib.go exercises the "missing from docs" path
	fakeCtags := filepath.Join(t.TempDir(), "ctags")
	script := "#!/bin/sh\ncat <<'JSONEOF'\n" +
		`{"_type": "tag", "name": "main", "path": "` + goFile1 + `", "kind": "function", "line": 2, "signature": "()"}` + "\n" +
		"JSONEOF\n"
	if err := os.WriteFile(fakeCtags, []byte(script), 0755); err != nil {
		t.Fatalf("write fake ctags: %v", err)
	}

	sp := NewSymbolParser()
	sp.ctags = fakeCtags

	batch := ParseBatch{
		Workspace: ws,
		Files:     []string{"main.go", "lib.go"},
	}

	err = sp.processFileBatch(batch)
	if err != nil {
		t.Errorf("processFileBatch: %v", err)
	}
}

// ---------------------------------------------------------------------------
// processFileBatch — tmpDir already exists (stat passes, skip MkdirAll)
// ---------------------------------------------------------------------------

func TestProcessFileBatch_ExistingTmpDir(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	// Pre-create the tmp directory so the stat check passes (no MkdirAll needed)
	// Use the same path calculation as processFileBatch
	dataPath := conf.Get().Global.DataPath
	tmpDir := filepath.Join(dataPath, "data", "tmp")
	// workspace ID is an int; just create the whole tree
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		t.Fatalf("pre-create tmpDir parent: %v", err)
	}
	// Create workspace-specific tmp dir by walking the same path as source code
	wsTmpDir := filepath.Join(tmpDir, func() string {
		// Simple int to string without importing strconv
		s := ""
		id := ws.Id
		if id == 0 {
			return "0"
		}
		for id > 0 {
			s = string(rune('0'+id%10)) + s
			id /= 10
		}
		return s
	}())
	if err := os.MkdirAll(wsTmpDir, 0755); err != nil {
		t.Fatalf("pre-create wsTmpDir: %v", err)
	}

	goFile := filepath.Join(wsDir, "test.go")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	fakeCtags := filepath.Join(t.TempDir(), "ctags")
	if err := os.WriteFile(fakeCtags, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write fake ctags: %v", err)
	}

	sp := NewSymbolParser()
	sp.ctags = fakeCtags

	batch := ParseBatch{
		Workspace: ws,
		Files:     []string{"test.go"},
	}

	err = sp.processFileBatch(batch)
	if err != nil {
		t.Errorf("processFileBatch with existing tmpDir: %v", err)
	}
}

// ---------------------------------------------------------------------------
// parseFunction — no "kind" field in JSON (exercises kind !ok branch)
// ---------------------------------------------------------------------------

func TestParseFunction_NoKindField(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	goFilePath := filepath.Join(wsDir, "nokind.go")
	if err := os.WriteFile(goFilePath, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	fakeCtags := filepath.Join(t.TempDir(), "ctags")
	// Output JSON without "kind" field
	script := "#!/bin/sh\ncat <<'JSONEOF'\n" +
		`{"_type": "tag", "name": "NoKind", "path": "` + goFilePath + `", "line": 1}` + "\n" +
		"JSONEOF\n"
	if err := os.WriteFile(fakeCtags, []byte(script), 0755); err != nil {
		t.Fatalf("write fake ctags: %v", err)
	}

	inputFile := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(inputFile, []byte(goFilePath+"\n"), 0644); err != nil {
		t.Fatalf("write input file: %v", err)
	}

	docs, err := parseFunction(fakeCtags, inputFile, "go", wsDir)
	if err != nil {
		t.Fatalf("parseFunction: %v", err)
	}

	// No entries should be included (kind is not "function"/"method"/"prototype")
	totalFunctions := 0
	for _, doc := range docs {
		totalFunctions += len(doc.Functions)
	}
	if totalFunctions != 0 {
		t.Errorf("expected 0 functions when kind is missing, got %d", totalFunctions)
	}
}

// ---------------------------------------------------------------------------
// parseFunction — relPath calculation with workspace prefix
// ---------------------------------------------------------------------------

func TestParseFunction_RelPathExtraction(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	subDir := filepath.Join(wsDir, "src")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	goFilePath := filepath.Join(subDir, "handler.go")
	if err := os.WriteFile(goFilePath, []byte("package src\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	fakeCtags := filepath.Join(t.TempDir(), "ctags")
	script := "#!/bin/sh\ncat <<'JSONEOF'\n" +
		`{"_type": "tag", "name": "Handle", "path": "` + goFilePath + `", "kind": "function", "line": 2, "signature": "()"}` + "\n" +
		"JSONEOF\n"
	if err := os.WriteFile(fakeCtags, []byte(script), 0755); err != nil {
		t.Fatalf("write fake ctags: %v", err)
	}

	inputFile := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(inputFile, []byte(goFilePath+"\n"), 0644); err != nil {
		t.Fatalf("write input file: %v", err)
	}

	docs, err := parseFunction(fakeCtags, inputFile, "go", wsDir)
	if err != nil {
		t.Fatalf("parseFunction: %v", err)
	}

	if len(docs) == 0 {
		t.Fatal("expected at least one DocFunction")
	}

	// Check that relPath is relative to workspace
	for _, doc := range docs {
		if doc.RelPath == goFilePath {
			t.Errorf("relPath should be relative, got absolute: %s", doc.RelPath)
		}
	}
}

// ---------------------------------------------------------------------------
// flushCache — workspace with only empty file lists (exercises skip in loop)
// ---------------------------------------------------------------------------

func TestSymbolParserFlushCache_MixedEmptyAndNonEmpty(t *testing.T) {
	sp := NewSymbolParser()
	ws1 := &workspace.Workspace{Id: 1, Path: "/tmp/a"}
	ws2 := &workspace.Workspace{Id: 2, Path: "/tmp/b"}

	sp.cacheMutex.Lock()
	sp.cacheMap[ws1] = []string{}       // Empty — should be skipped in the loop
	sp.cacheMap[ws2] = []string{"x.go"} // Non-empty — should be sent
	sp.cacheMutex.Unlock()

	// Drain channel for the non-empty batch
	done := make(chan struct{})
	go func() {
		batch := <-sp.ch
		if batch.Workspace.Id != 2 {
			t.Errorf("expected workspace id 2, got %d", batch.Workspace.Id)
		}
		close(done)
	}()

	sp.flushCache()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("flushCache did not send batch within 2 seconds")
	}
}

// ---------------------------------------------------------------------------
// SymbolParser.Add — batch size trigger
// ---------------------------------------------------------------------------

func TestSymbolParserAdd_BatchSizeTrigger(t *testing.T) {
	orig := conf.Get().Symbols.EnableFeature
	conf.Get().Symbols.EnableFeature = true
	defer func() { conf.Get().Symbols.EnableFeature = orig }()

	sp := NewSymbolParser()
	sp.ctags = "/fake/ctags"

	ws := &workspace.Workspace{Id: 1, Path: "/tmp/test"}

	// Drain channel in background
	received := make(chan ParseBatch, 2)
	go func() {
		for batch := range sp.ch {
			received <- batch
		}
	}()

	// Add MaxBatchSize files to trigger a flush
	for i := 0; i < MaxBatchSize; i++ {
		sp.Add(ws, "file.go")
	}

	// Should have received a batch
	select {
	case batch := <-received:
		if len(batch.Files) != MaxBatchSize {
			t.Errorf("expected batch of %d files, got %d", MaxBatchSize, len(batch.Files))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected batch to be sent when MaxBatchSize is reached")
	}

	// Cache should be cleared for this workspace
	sp.cacheMutex.Lock()
	remaining := len(sp.cacheMap[ws])
	sp.cacheMutex.Unlock()

	if remaining != 0 {
		t.Errorf("expected 0 remaining cached files after batch flush, got %d", remaining)
	}
}

// ---------------------------------------------------------------------------
// SymbolParser Start/Stop with enabled feature + fake ctags
// ---------------------------------------------------------------------------

func TestSymbolParserStartStop_EnabledFeature(t *testing.T) {
	sp := NewSymbolParser()
	var wg sync.WaitGroup
	orig := conf.Get().Symbols.EnableFeature
	origW := conf.Get().Server.SymbolParserWorkers
	conf.Get().Symbols.EnableFeature = true
	conf.Get().Server.SymbolParserWorkers = 1
	defer func() { conf.Get().Symbols.EnableFeature = orig; conf.Get().Server.SymbolParserWorkers = origW }()

	fakeCtags := filepath.Join(t.TempDir(), "ctags")
	os.WriteFile(fakeCtags, []byte("#!/bin/sh\nexit 0\n"), 0755)
	origC := conf.Get().BinPath.CTags
	conf.Get().BinPath.CTags = fakeCtags
	defer func() { conf.Get().BinPath.CTags = origC }()

	sp.Start(&wg)
	if sp.ctags == "" {
		t.Error("ctags should be set")
	}
	sp.Stop()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

func TestSymbolParserStartStop_CtagsNotFound(t *testing.T) {
	sp := NewSymbolParser()
	var wg sync.WaitGroup
	orig := conf.Get().Symbols.EnableFeature
	conf.Get().Symbols.EnableFeature = true
	defer func() { conf.Get().Symbols.EnableFeature = orig }()
	origC := conf.Get().BinPath.CTags
	conf.Get().BinPath.CTags = "/nonexistent/ctags"
	defer func() { conf.Get().BinPath.CTags = origC }()

	sp.Start(&wg)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("timeout")
	}
}

func TestSymbolParserRun_ProcessesBatch(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()
	sp := NewSymbolParser()
	var wg sync.WaitGroup
	orig := conf.Get().Symbols.EnableFeature
	origW := conf.Get().Server.SymbolParserWorkers
	conf.Get().Symbols.EnableFeature = true
	conf.Get().Server.SymbolParserWorkers = 1
	defer func() { conf.Get().Symbols.EnableFeature = orig; conf.Get().Server.SymbolParserWorkers = origW }()

	fakeCtags := filepath.Join(t.TempDir(), "ctags")
	os.WriteFile(fakeCtags, []byte("#!/bin/sh\nexit 0\n"), 0755)
	origC := conf.Get().BinPath.CTags
	conf.Get().BinPath.CTags = fakeCtags
	defer func() { conf.Get().BinPath.CTags = origC }()

	sp.Start(&wg)
	wsDir := t.TempDir()
	ws, _ := workspace.Create(wsDir)
	os.WriteFile(filepath.Join(wsDir, "main.go"), []byte("package main\n"), 0644)
	sp.Add(ws, "main.go")
	sp.flushCache()
	time.Sleep(500 * time.Millisecond)
	sp.Stop()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

// ---------------------------------------------------------------------------
// parseFunction — fake ctags tests
// ---------------------------------------------------------------------------

func TestParseFunction_WithFakeCtagsOutput(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()
	wsDir := t.TempDir()
	goFile := filepath.Join(wsDir, "main.go")
	os.WriteFile(goFile, []byte("package main\nfunc hello() {}\nfunc world() {}\n"), 0644)
	fakeCtags := filepath.Join(t.TempDir(), "ctags")
	script := "#!/bin/sh\ncat <<'JSONEOF'\n" +
		`{"_type":"tag","name":"hello","path":"` + goFile + `","kind":"function","line":2}` + "\n" +
		`{"_type":"tag","name":"world","path":"` + goFile + `","kind":"function","line":3}` + "\n" +
		"JSONEOF\n"
	os.WriteFile(fakeCtags, []byte(script), 0755)
	inputFile := filepath.Join(t.TempDir(), "input.txt")
	os.WriteFile(inputFile, []byte(goFile+"\n"), 0644)
	docs, err := parseFunction(fakeCtags, inputFile, "go", wsDir)
	if err != nil {
		t.Fatalf("parseFunction: %v", err)
	}
	total := 0
	for _, d := range docs {
		total += len(d.Functions)
	}
	if total < 2 {
		t.Errorf("expected >=2 functions, got %d", total)
	}
}

func TestParseFunction_SkipsAnonymous(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()
	wsDir := t.TempDir()
	goFile := filepath.Join(wsDir, "a.go")
	os.WriteFile(goFile, []byte("package main\n"), 0644)
	fakeCtags := filepath.Join(t.TempDir(), "ctags")
	script := "#!/bin/sh\ncat <<'JSONEOF'\n" +
		`{"name":"__anon1","path":"` + goFile + `","kind":"function","line":5}` + "\n" +
		`{"name":"real","path":"` + goFile + `","kind":"function","line":10}` + "\n" +
		"JSONEOF\n"
	os.WriteFile(fakeCtags, []byte(script), 0755)
	inputFile := filepath.Join(t.TempDir(), "input.txt")
	os.WriteFile(inputFile, []byte(goFile+"\n"), 0644)
	docs, err := parseFunction(fakeCtags, inputFile, "go", wsDir)
	if err != nil {
		t.Fatalf("parseFunction: %v", err)
	}
	for _, d := range docs {
		for _, fn := range d.Functions {
			if fn.Name == "__anon1" {
				t.Error("anonymous function should be skipped")
			}
		}
	}
}

func TestParseFunction_MalformedJSON(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()
	wsDir := t.TempDir()
	goFile := filepath.Join(wsDir, "b.go")
	os.WriteFile(goFile, []byte("package main\n"), 0644)
	fakeCtags := filepath.Join(t.TempDir(), "ctags")
	script := "#!/bin/sh\ncat <<'JSONEOF'\n{bad json}\n" +
		`{"name":"valid","path":"` + goFile + `","kind":"function","line":1}` + "\nJSONEOF\n"
	os.WriteFile(fakeCtags, []byte(script), 0755)
	inputFile := filepath.Join(t.TempDir(), "input.txt")
	os.WriteFile(inputFile, []byte(goFile+"\n"), 0644)
	docs, err := parseFunction(fakeCtags, inputFile, "go", wsDir)
	if err != nil {
		t.Fatalf("parseFunction: %v", err)
	}
	total := 0
	for _, d := range docs {
		total += len(d.Functions)
	}
	if total != 1 {
		t.Errorf("expected 1 valid function, got %d", total)
	}
}

func TestParseFunction_MethodKind(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()
	wsDir := t.TempDir()
	goFile := filepath.Join(wsDir, "m.go")
	os.WriteFile(goFile, []byte("package main\n"), 0644)
	fakeCtags := filepath.Join(t.TempDir(), "ctags")
	script := "#!/bin/sh\ncat <<'JSONEOF'\n" +
		`{"name":"Method","path":"` + goFile + `","kind":"method","line":5}` + "\n" +
		`{"name":"Var","path":"` + goFile + `","kind":"variable","line":3}` + "\n" +
		"JSONEOF\n"
	os.WriteFile(fakeCtags, []byte(script), 0755)
	inputFile := filepath.Join(t.TempDir(), "input.txt")
	os.WriteFile(inputFile, []byte(goFile+"\n"), 0644)
	docs, err := parseFunction(fakeCtags, inputFile, "go", wsDir)
	if err != nil {
		t.Fatalf("parseFunction: %v", err)
	}
	total := 0
	for _, d := range docs {
		total += len(d.Functions)
	}
	if total != 1 {
		t.Errorf("expected 1 method, got %d", total)
	}
}

func TestParseFunction_CtagsError(t *testing.T) {
	fakeCtags := filepath.Join(t.TempDir(), "ctags")
	os.WriteFile(fakeCtags, []byte("#!/bin/sh\nexit 1\n"), 0755)
	inputFile := filepath.Join(t.TempDir(), "input.txt")
	os.WriteFile(inputFile, []byte("/file.go\n"), 0644)
	_, err := parseFunction(fakeCtags, inputFile, "go", "/tmp")
	if err == nil {
		t.Error("expected error when ctags fails")
	}
}

func TestParseFunction_EmptyOutput(t *testing.T) {
	fakeCtags := filepath.Join(t.TempDir(), "ctags")
	os.WriteFile(fakeCtags, []byte("#!/bin/sh\n"), 0755)
	inputFile := filepath.Join(t.TempDir(), "input.txt")
	os.WriteFile(inputFile, []byte("/file.go\n"), 0644)
	docs, err := parseFunction(fakeCtags, inputFile, "go", "/tmp")
	if err != nil {
		t.Fatalf("parseFunction: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("expected 0, got %d", len(docs))
	}
}

func TestProcessFileBatch_WithFakeCtags(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()
	wsDir := t.TempDir()
	ws, _ := workspace.Create(wsDir)
	os.WriteFile(filepath.Join(wsDir, "main.go"), []byte("package main\n"), 0644)
	fakeCtags := filepath.Join(t.TempDir(), "ctags")
	os.WriteFile(fakeCtags, []byte("#!/bin/sh\nexit 0\n"), 0755)
	sp := NewSymbolParser()
	sp.ctags = fakeCtags
	err := sp.processFileBatch(ParseBatch{Workspace: ws, Files: []string{"main.go"}})
	if err != nil {
		t.Errorf("processFileBatch: %v", err)
	}
}

func TestGetCtagsPath_ValidPath(t *testing.T) {
	fakeCtags := filepath.Join(t.TempDir(), "ctags")
	os.WriteFile(fakeCtags, []byte("#!/bin/sh\n"), 0755)
	orig := conf.Get().BinPath.CTags
	conf.Get().BinPath.CTags = fakeCtags
	defer func() { conf.Get().BinPath.CTags = orig }()
	path, err := getCtagsPath()
	if err != nil {
		t.Errorf("error: %v", err)
	}
	if path != fakeCtags {
		t.Errorf("expected %q, got %q", fakeCtags, path)
	}
}
