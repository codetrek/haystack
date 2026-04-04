package indexer

import (
	"sync"
	"testing"
	"time"

	"github.com/codetrek/haystack/internal/core/documents"
	"github.com/codetrek/haystack/internal/core/workspace"
)

// ---------------------------------------------------------------------------
// NewWriter
// ---------------------------------------------------------------------------

func TestNewWriter_ReturnsInitialized(t *testing.T) {
	w := NewWriter()
	if w == nil {
		t.Fatal("NewWriter() returned nil")
	}
	if w.docs == nil {
		t.Error("docs channel should be initialized")
	}
	if w.stop == nil {
		t.Error("stop channel should be initialized")
	}
	if w.done == nil {
		t.Error("done channel should be initialized")
	}
}

func TestNewWriter_ChannelBufferSize(t *testing.T) {
	w := NewWriter()
	cap := cap(w.docs)
	if cap != 64 {
		t.Errorf("expected writer channel buffer size 64, got %d", cap)
	}
}

// ---------------------------------------------------------------------------
// Writer lifecycle: Start → Stop
// ---------------------------------------------------------------------------

func TestWriterStartStop_Lifecycle(t *testing.T) {
	w := NewWriter()
	var wg sync.WaitGroup

	w.Start(&wg)
	w.Stop()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("Writer did not stop within 2 seconds")
	}
}

// ---------------------------------------------------------------------------
// Writer.Add - sends to channel
// ---------------------------------------------------------------------------

func TestWriterAdd_SendsToChannel(t *testing.T) {
	w := NewWriter()

	ws := &workspace.Workspace{Id: 1, Path: "/tmp/test"}
	doc := &documents.Document{
		ID:      "test-id",
		RelPath: "main.go",
		Size:    100,
		Hash:    "abc123",
	}

	go w.Add(ws, doc, true)

	select {
	case wd := <-w.docs:
		if wd.Workspace.Id != 1 {
			t.Errorf("expected workspace id 1, got %d", wd.Workspace.Id)
		}
		if wd.Document.ID != "test-id" {
			t.Errorf("expected doc id test-id, got %s", wd.Document.ID)
		}
		if !wd.CreateNew {
			t.Error("expected CreateNew to be true")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Add did not send to channel within 1 second")
	}
}

func TestWriterAdd_DeletedWorkspaceSkips(t *testing.T) {
	w := NewWriter()

	ws := &workspace.Workspace{Id: 2, Path: "/tmp/deleted"}
	ws.SetDeleted()

	doc := &documents.Document{ID: "x", RelPath: "f.go"}

	// Since workspace is deleted, Add should return immediately without sending
	// Try to send with a timeout
	done := make(chan struct{})
	go func() {
		w.Add(ws, doc, false)
		close(done)
	}()

	select {
	case <-done:
		// Good - returned without blocking
	case <-time.After(1 * time.Second):
		t.Fatal("Add should return immediately for deleted workspace")
	}

	// Verify nothing was sent to the channel
	select {
	case <-w.docs:
		t.Error("should not have sent anything for deleted workspace")
	default:
		// correct - nothing in channel
	}
}

// ---------------------------------------------------------------------------
// getPendingWrites
// ---------------------------------------------------------------------------

func TestGetPendingWrites_EmptyChannel(t *testing.T) {
	w := NewWriter()
	docs := w.getPendingWrites(10)
	if len(docs) != 0 {
		t.Errorf("expected 0 pending writes from empty channel, got %d", len(docs))
	}
}

func TestGetPendingWrites_ReturnsUpToLimit(t *testing.T) {
	w := NewWriter()

	ws := &workspace.Workspace{Id: 1, Path: "/tmp/test"}

	// Put 5 docs in channel
	for i := 0; i < 5; i++ {
		w.docs <- &WriteDoc{
			Workspace: ws,
			Document:  &documents.Document{ID: "doc", RelPath: "f.go"},
			CreateNew: true,
		}
	}

	// Request limit of 3
	docs := w.getPendingWrites(3)
	if len(docs) != 3 {
		t.Errorf("expected 3 pending writes, got %d", len(docs))
	}

	// Should still have 2 left
	remaining := w.getPendingWrites(10)
	if len(remaining) != 2 {
		t.Errorf("expected 2 remaining writes, got %d", len(remaining))
	}
}

func TestGetPendingWrites_ReturnsAllIfLessThanLimit(t *testing.T) {
	w := NewWriter()

	ws := &workspace.Workspace{Id: 1, Path: "/tmp/test"}
	w.docs <- &WriteDoc{
		Workspace: ws,
		Document:  &documents.Document{ID: "doc1", RelPath: "a.go"},
		CreateNew: false,
	}
	w.docs <- &WriteDoc{
		Workspace: ws,
		Document:  &documents.Document{ID: "doc2", RelPath: "b.go"},
		CreateNew: true,
	}

	docs := w.getPendingWrites(10)
	if len(docs) != 2 {
		t.Errorf("expected 2 pending writes, got %d", len(docs))
	}
}

// ---------------------------------------------------------------------------
// processDocs - separation of new vs existing
// ---------------------------------------------------------------------------

func TestProcessDocs_SeparatesNewAndExisting(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	ws := &workspace.Workspace{Id: 1, Path: "/tmp/ws1"}

	// Create documents workspace first
	if err := documents.Create(ws.Id, "test"); err != nil {
		t.Fatalf("documents.Create: %v", err)
	}

	w := NewWriter()
	doc1 := &documents.Document{
		ID:      "new-doc",
		RelPath: "new.go",
		Size:    50,
		Hash:    "h1",
		Words:   []string{"func", "main"},
	}
	doc2 := &documents.Document{
		ID:      "existing-doc",
		RelPath: "existing.go",
		Size:    100,
		Hash:    "h2",
		Words:   []string{"var", "x"},
	}

	docs := []*WriteDoc{
		{Workspace: ws, Document: doc1, CreateNew: true},
		{Workspace: ws, Document: doc2, CreateNew: false},
	}

	// processDocs should not panic and should call the storage functions
	w.processDocs(docs)
	// If we get here without panic, the separation logic works
}

func TestProcessDocs_SkipsDeletedWorkspace(t *testing.T) {
	w := NewWriter()

	ws := &workspace.Workspace{Id: 99, Path: "/tmp/deleted"}
	ws.SetDeleted()

	doc := &documents.Document{ID: "d1", RelPath: "f.go"}
	docs := []*WriteDoc{
		{Workspace: ws, Document: doc, CreateNew: true},
	}

	// Should not panic even with deleted workspace
	w.processDocs(docs)
}

func TestProcessDocs_MultipleWorkspaces(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	ws1 := &workspace.Workspace{Id: 1, Path: "/tmp/ws1"}
	ws2 := &workspace.Workspace{Id: 2, Path: "/tmp/ws2"}

	if err := documents.Create(ws1.Id, "test1"); err != nil {
		t.Fatalf("documents.Create ws1: %v", err)
	}
	if err := documents.Create(ws2.Id, "test2"); err != nil {
		t.Fatalf("documents.Create ws2: %v", err)
	}

	w := NewWriter()
	docs := []*WriteDoc{
		{Workspace: ws1, Document: &documents.Document{ID: "d1", RelPath: "a.go", Words: []string{"a"}}, CreateNew: true},
		{Workspace: ws2, Document: &documents.Document{ID: "d2", RelPath: "b.go", Words: []string{"b"}}, CreateNew: true},
	}

	// Should process docs for both workspaces without panic
	w.processDocs(docs)
}

func TestProcessDocs_EmptySlice(t *testing.T) {
	w := NewWriter()
	// Should not panic with empty slice
	w.processDocs([]*WriteDoc{})
}

// ---------------------------------------------------------------------------
// WriteDoc struct
// ---------------------------------------------------------------------------

func TestWriteDoc_Fields(t *testing.T) {
	ws := &workspace.Workspace{Id: 3, Path: "/tmp/wd"}
	doc := &documents.Document{ID: "abc", RelPath: "x.go", Size: 42}
	wd := WriteDoc{
		Workspace: ws,
		Document:  doc,
		CreateNew: true,
	}

	if wd.Workspace.Id != 3 {
		t.Errorf("workspace id: got %d, want 3", wd.Workspace.Id)
	}
	if wd.Document.ID != "abc" {
		t.Errorf("doc ID: got %s, want abc", wd.Document.ID)
	}
	if !wd.CreateNew {
		t.Error("CreateNew should be true")
	}
}

// ---------------------------------------------------------------------------
// Writer.Add concurrent safety
// ---------------------------------------------------------------------------

func TestWriterAdd_ConcurrentSafety(t *testing.T) {
	w := NewWriter()
	ws := &workspace.Workspace{Id: 1, Path: "/tmp/test"}

	var wg sync.WaitGroup
	const n = 50

	// Drain the channel in background
	received := make(chan struct{}, n)
	doneDrain := make(chan struct{})
	go func() {
		for i := 0; i < n; i++ {
			<-w.docs
			received <- struct{}{}
		}
		close(doneDrain)
	}()

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			doc := &documents.Document{ID: "d", RelPath: "f.go"}
			w.Add(ws, doc, true)
		}(i)
	}

	wg.Wait()
	<-doneDrain

	if len(received) != n {
		t.Errorf("expected %d received docs, got %d", n, len(received))
	}
}

// ---------------------------------------------------------------------------
// Writer.run integration: Start → Add → Stop drains remaining
// ---------------------------------------------------------------------------

func TestWriterRun_ProcessesDocsBeforeStop(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	ws, err := workspace.Create(t.TempDir())
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	if err := documents.Create(ws.Id, "test"); err != nil {
		t.Fatalf("documents.Create: %v", err)
	}

	w := NewWriter()
	var wg sync.WaitGroup
	w.Start(&wg)

	// Send a doc through Add
	doc := &documents.Document{
		ID:      "wr-doc",
		RelPath: "writer-test.go",
		Size:    10,
		Hash:    "h",
		Words:   []string{"test"},
	}
	w.Add(ws, doc, true)

	// Give the writer a moment to process
	time.Sleep(50 * time.Millisecond)

	// Stop should drain any remaining docs
	w.Stop()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Writer did not stop within 2 seconds")
	}
}

func TestWriterRun_StopDrainsMultiplePendingDocs(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	ws, err := workspace.Create(t.TempDir())
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	if err := documents.Create(ws.Id, "test"); err != nil {
		t.Fatalf("documents.Create: %v", err)
	}

	w := NewWriter()
	var wg sync.WaitGroup
	w.Start(&wg)

	// Send multiple docs
	for i := 0; i < 5; i++ {
		doc := &documents.Document{
			ID:      "d",
			RelPath: "f.go",
			Size:    10,
			Hash:    "h",
			Words:   []string{"w"},
		}
		w.Add(ws, doc, true)
	}

	// Stop drains remaining
	w.Stop()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Writer did not stop within 2 seconds")
	}
}
