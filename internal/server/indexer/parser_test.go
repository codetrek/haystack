package indexer

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/internal/core/documents"
	"github.com/codetrek/haystack/internal/core/symbols"
	"github.com/codetrek/haystack/internal/core/workspace"
	"github.com/codetrek/haystack/internal/shared/running"
	"github.com/codetrek/haystack/internal/testutil"
	"github.com/codetrek/haystack/searchcore/idtable"
	"github.com/codetrek/haystack/searchcore/invertedindex"
)

// setupTestEnv initialises the subsystems required for parsing tests.
// Caller must defer the returned teardown function.
func setupTestEnv(t *testing.T) (env *testutil.Env, teardown func()) {
	t.Helper()
	env = testutil.SetupEnv(t, "indexer-test")

	// Initialize the shutdown context so running.IsShuttingDown() works
	var shutdownWg sync.WaitGroup
	running.InitShutdown(&shutdownWg)

	alloc, err := idtable.New(env.DB, idtable.Options{})
	if err != nil {
		t.Fatalf("idtable.New: %v", err)
	}
	SetIdAllocator(alloc)
	idx, err := invertedindex.New(env.DB, env.Mpsc, invertedindex.Options{})
	if err != nil {
		t.Fatalf("invertedindex.New: %v", err)
	}
	st, err := documents.New(env.DB, env.Mpsc, idx, documents.Options{})
	if err != nil {
		t.Fatalf("documents.New: %v", err)
	}
	SetDocStore(st)
	if err := symbols.Init(env.DB, env.Mpsc, idx); err != nil {
		t.Fatalf("symbols.Init: %v", err)
	}
	if err := workspace.Init(env.DB); err != nil {
		t.Fatalf("workspace.Init: %v", err)
	}

	return env, func() {
		SetDocStore(nil)
		symbols.CloseAndWait()
		st.CloseAndWait()
		idx.CloseAndWait()
		alloc.Close()
		env.TeardownBase()
	}
}

// ---------------------------------------------------------------------------
// NewParser
// ---------------------------------------------------------------------------

func TestNewParser_ReturnsInitialized(t *testing.T) {
	p := NewParser()
	if p == nil {
		t.Fatal("NewParser() returned nil")
	}
	if p.ch == nil {
		t.Error("channel should be initialized")
	}
	if p.stop == nil {
		t.Error("stop channel should be initialized")
	}
	if p.done == nil {
		t.Error("done channel should be initialized")
	}
	if p.workers != 0 {
		t.Errorf("workers should be 0 before Start, got %d", p.workers)
	}
}

// ---------------------------------------------------------------------------
// Parser lifecycle: Start → Stop
// ---------------------------------------------------------------------------

func TestParserStartStop_Lifecycle(t *testing.T) {
	p := NewParser()
	var wg sync.WaitGroup

	// Set up config so workers count is deterministic
	conf.Get().Server.IndexWorkers = 2
	p.Start(&wg)

	if p.workers != 2 {
		t.Errorf("expected 2 workers, got %d", p.workers)
	}

	// Stop should close gracefully
	p.Stop()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("Parser did not stop within 2 seconds")
	}
}

// ---------------------------------------------------------------------------
// Parser.Add - channel send
// ---------------------------------------------------------------------------

func TestParserAdd_SendsToChannel(t *testing.T) {
	p := NewParser()

	ws := &workspace.Workspace{Id: 1, Path: "/tmp/test"}
	go p.Add(ws, "main.go")

	select {
	case file := <-p.ch:
		if file.Workspace.Id != 1 {
			t.Errorf("expected workspace id 1, got %d", file.Workspace.Id)
		}
		if file.RelFilePath != "main.go" {
			t.Errorf("expected main.go, got %s", file.RelFilePath)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Add did not send to channel within 1 second")
	}
}

// ---------------------------------------------------------------------------
// ParseFile struct
// ---------------------------------------------------------------------------

func TestParseFile_Fields(t *testing.T) {
	ws := &workspace.Workspace{Id: 10, Path: "/tmp/pf"}
	pf := ParseFile{Workspace: ws, RelFilePath: "src/lib.go"}
	if pf.Workspace.Id != 10 {
		t.Errorf("workspace id: got %d, want 10", pf.Workspace.Id)
	}
	if pf.RelFilePath != "src/lib.go" {
		t.Errorf("RelFilePath: got %s, want src/lib.go", pf.RelFilePath)
	}
}

// ---------------------------------------------------------------------------
// parse() - unit test with temp files
// ---------------------------------------------------------------------------

func TestParse_NewGoFile(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	// Create a temp workspace directory with a Go file
	wsDir := t.TempDir()
	goContent := []byte(`package main

import "fmt"

func main() {
	fmt.Println("hello")
}
`)
	relPath := "main.go"
	fullPath := filepath.Join(wsDir, relPath)
	if err := os.WriteFile(fullPath, goContent, 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	ws := &workspace.Workspace{Id: 1, Path: wsDir}
	pf := ParseFile{Workspace: ws, RelFilePath: relPath}

	doc, newFile, oversize, err := parse(pf)
	if err != nil {
		t.Fatalf("parse() returned error: %v", err)
	}
	if doc == nil {
		t.Fatal("parse() returned nil document for new file")
	}
	if !newFile {
		t.Error("expected newFile to be true for first parse")
	}
	if oversize {
		t.Error("small file should not be oversize")
	}
	if doc.RelPath != relPath {
		t.Errorf("doc.RelPath = %q, want %q", doc.RelPath, relPath)
	}
	if doc.Size <= 0 {
		t.Errorf("doc.Size should be > 0, got %d", doc.Size)
	}
	if doc.Hash == "" {
		t.Error("doc.Hash should not be empty")
	}
	if doc.ModifiedTime == 0 {
		t.Error("doc.ModifiedTime should not be zero")
	}
	if len(doc.Words) == 0 {
		t.Error("doc.Words should not be empty for a Go file")
	}
	if len(doc.PathWords) == 0 {
		t.Error("doc.PathWords should not be empty")
	}
}

func TestParse_BinaryFileReturnsNil(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	relPath := "data.bin"
	fullPath := filepath.Join(wsDir, relPath)

	// Write binary content that won't pass IsLikelyText
	binData := make([]byte, 200)
	for i := range binData {
		binData[i] = byte(i % 20) // lots of control chars
	}
	if err := os.WriteFile(fullPath, binData, 0644); err != nil {
		t.Fatalf("failed to write binary file: %v", err)
	}

	ws := &workspace.Workspace{Id: 2, Path: wsDir}
	pf := ParseFile{Workspace: ws, RelFilePath: relPath}

	doc, _, _, err := parse(pf)
	if err != nil {
		t.Fatalf("parse() returned error: %v", err)
	}
	if doc != nil {
		t.Error("parse() should return nil document for binary file")
	}
}

func TestParse_OversizeFile(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	relPath := "big.txt"
	fullPath := filepath.Join(wsDir, relPath)

	// Set a very small max file size for testing
	origMaxSize := conf.Get().Server.MaxFileSize
	conf.Get().Server.MaxFileSize = 10 // 10 bytes
	defer func() { conf.Get().Server.MaxFileSize = origMaxSize }()

	content := []byte("This is a file that exceeds the 10-byte limit for testing")
	if err := os.WriteFile(fullPath, content, 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	ws := &workspace.Workspace{Id: 3, Path: wsDir}
	pf := ParseFile{Workspace: ws, RelFilePath: relPath}

	doc, newFile, oversize, err := parse(pf)
	if err != nil {
		t.Fatalf("parse() returned error: %v", err)
	}
	if doc == nil {
		t.Fatal("parse() returned nil document for oversize file (should still create doc)")
	}
	if !newFile {
		t.Error("expected newFile true for first parse")
	}
	if !oversize {
		t.Error("expected oversize to be true")
	}
	if doc.Hash != "" {
		t.Error("oversize files should have empty hash")
	}
	if len(doc.Words) != 0 {
		t.Errorf("oversize files should have empty words, got %d", len(doc.Words))
	}
}

func TestParse_NonExistentFileReturnsError(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws := &workspace.Workspace{Id: 4, Path: wsDir}
	pf := ParseFile{Workspace: ws, RelFilePath: "does_not_exist.go"}

	_, _, _, err := parse(pf)
	if err == nil {
		t.Error("parse() should return error for non-existent file")
	}
}

func TestParse_EmptyTextFile(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	relPath := "empty.txt"
	fullPath := filepath.Join(wsDir, relPath)

	// Write an empty file
	if err := os.WriteFile(fullPath, []byte{}, 0644); err != nil {
		t.Fatalf("failed to write empty file: %v", err)
	}

	ws := &workspace.Workspace{Id: 5, Path: wsDir}
	pf := ParseFile{Workspace: ws, RelFilePath: relPath}

	// Empty files: isProbablyText divides by len(data)=0 which could panic or return false
	// The behavior depends on how the code handles zero-length data
	doc, _, _, err := parse(pf)
	// We just want no panic; whether doc is nil is implementation-dependent
	_ = doc
	_ = err
}

func TestParse_UnchangedFileSkip(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	relPath := "skip.go"
	fullPath := filepath.Join(wsDir, relPath)
	content := []byte("package main\nfunc skip() {}\n")
	if err := os.WriteFile(fullPath, content, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	if err := stInst.Create(ws.Id, "test"); err != nil {
		t.Fatalf("documents.Create: %v", err)
	}

	pf := ParseFile{Workspace: ws, RelFilePath: relPath}

	// First parse creates the doc
	doc1, newFile1, _, err := parse(pf)
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}
	if doc1 == nil || !newFile1 {
		t.Fatal("expected new doc from first parse")
	}

	// Save it to the index so it's found as existing on second parse
	w := NewWriter()
	w.processDocs([]*WriteDoc{{Workspace: ws, Document: doc1, CreateNew: true}})

	// Second parse should return nil (file not changed: same ModifiedTime)
	doc2, _, _, err := parse(pf)
	if err != nil {
		t.Fatalf("second parse: %v", err)
	}
	if doc2 != nil {
		t.Error("second parse should return nil doc (unchanged file)")
	}
}

func TestParse_SameHashSkip(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	relPath := "hashskip.go"
	fullPath := filepath.Join(wsDir, relPath)
	content := []byte("package main\nfunc hash() {}\n")
	if err := os.WriteFile(fullPath, content, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	if err := stInst.Create(ws.Id, "test"); err != nil {
		t.Fatalf("documents.Create: %v", err)
	}

	pf := ParseFile{Workspace: ws, RelFilePath: relPath}

	// First parse
	doc1, _, _, err := parse(pf)
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}
	if doc1 == nil {
		t.Fatal("expected doc from first parse")
	}

	// Save with a fake older ModifiedTime so the mod-time check doesn't skip
	doc1.ModifiedTime = doc1.ModifiedTime - 1000
	w := NewWriter()
	w.processDocs([]*WriteDoc{{Workspace: ws, Document: doc1, CreateNew: true}})

	// Second parse: mod-time differs, but hash is the same — should return nil
	doc2, _, _, err := parse(pf)
	if err != nil {
		t.Fatalf("second parse: %v", err)
	}
	if doc2 != nil {
		t.Error("second parse should return nil doc (same hash)")
	}
}

func TestParse_HTMLFile(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	relPath := "index.html"
	fullPath := filepath.Join(wsDir, relPath)

	htmlContent := []byte(`<!DOCTYPE html>
<html>
<head><title>Test Page</title></head>
<body>
<h1>Hello World</h1>
<p>This is a test HTML page with enough content to be detected as text.</p>
</body>
</html>
`)
	if err := os.WriteFile(fullPath, htmlContent, 0644); err != nil {
		t.Fatalf("failed to write HTML file: %v", err)
	}

	ws := &workspace.Workspace{Id: 6, Path: wsDir}
	pf := ParseFile{Workspace: ws, RelFilePath: relPath}

	doc, newFile, oversize, err := parse(pf)
	if err != nil {
		t.Fatalf("parse() returned error: %v", err)
	}
	if doc == nil {
		t.Fatal("parse() returned nil for HTML file")
	}
	if !newFile {
		t.Error("expected newFile true")
	}
	if oversize {
		t.Error("HTML file should not be oversize")
	}
	if doc.Hash == "" {
		t.Error("doc.Hash should not be empty for text file")
	}
}

func TestParse_JSONFile(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	relPath := "config.json"
	fullPath := filepath.Join(wsDir, relPath)

	jsonContent := []byte(`{
  "name": "test-project",
  "version": "1.0.0",
  "dependencies": {
    "express": "^4.18.0"
  }
}
`)
	if err := os.WriteFile(fullPath, jsonContent, 0644); err != nil {
		t.Fatalf("failed to write JSON file: %v", err)
	}

	ws := &workspace.Workspace{Id: 7, Path: wsDir}
	pf := ParseFile{Workspace: ws, RelFilePath: relPath}

	doc, newFile, _, err := parse(pf)
	if err != nil {
		t.Fatalf("parse() returned error: %v", err)
	}
	if doc == nil {
		t.Fatal("parse() returned nil for JSON file")
	}
	if !newFile {
		t.Error("expected newFile true")
	}
}

// ---------------------------------------------------------------------------
// Parser channel buffering
// ---------------------------------------------------------------------------

func TestParserChannelBufferSize(t *testing.T) {
	p := NewParser()
	// The channel has buffer size 32
	cap := cap(p.ch)
	if cap != 32 {
		t.Errorf("expected parser channel buffer size 32, got %d", cap)
	}
}

// ---------------------------------------------------------------------------
// processFile — integration test
// ---------------------------------------------------------------------------

func TestProcessFile_NewTextFile(t *testing.T) {
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

	relPath := "hello.go"
	fullPath := filepath.Join(wsDir, relPath)
	if err := os.WriteFile(fullPath, []byte("package main\nfunc hello() {}\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	p := NewParser()
	pf := ParseFile{Workspace: ws, RelFilePath: relPath}

	// processFile calls parse, then writer.Add and symbolParser.Add
	// We need to drain the writer channel to avoid blocking
	doneCh := make(chan struct{})
	go func() {
		// Drain writer channel
		select {
		case <-writer.docs:
		case <-time.After(2 * time.Second):
		}
		close(doneCh)
	}()

	err = p.processFile(pf)
	if err != nil {
		t.Errorf("processFile: %v", err)
	}

	<-doneCh
}

func TestProcessFile_NonExistentFileReturnsError(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	p := NewParser()
	pf := ParseFile{Workspace: ws, RelFilePath: "missing.go"}

	err = p.processFile(pf)
	if err == nil {
		t.Error("processFile should return error for missing file")
	}
}

func TestProcessFile_BinaryFileReturnsNil(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	relPath := "binary.dat"
	fullPath := filepath.Join(wsDir, relPath)
	binData := make([]byte, 200)
	for i := range binData {
		binData[i] = byte(i % 20)
	}
	if err := os.WriteFile(fullPath, binData, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	p := NewParser()
	pf := ParseFile{Workspace: ws, RelFilePath: relPath}

	// processFile should return nil error for binary file (doc is nil, skipped)
	err = p.processFile(pf)
	if err != nil {
		t.Errorf("processFile should not error for binary file: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Parser.run — integration test with Start/Add/Stop
// ---------------------------------------------------------------------------

func TestParserRun_ProcessesSentFile(t *testing.T) {
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

	// Create a file to parse
	relPath := "parsed.go"
	fullPath := filepath.Join(wsDir, relPath)
	if err := os.WriteFile(fullPath, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	p := NewParser()
	var wg sync.WaitGroup
	conf.Get().Server.IndexWorkers = 1
	p.Start(&wg)

	// Drain writer docs channel
	writerDone := make(chan struct{})
	go func() {
		select {
		case <-writer.docs:
		case <-time.After(2 * time.Second):
		}
		close(writerDone)
	}()

	// Add file for parsing
	p.Add(ws, relPath)

	// Wait for writer to receive the document
	<-writerDone

	p.Stop()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Parser did not stop within 2 seconds")
	}
}
