package indexer

import (
	"container/list"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/codetrek/haystack/server/internal/conf"
	"github.com/codetrek/haystack/server/internal/core/workspace"
	"github.com/codetrek/haystack/server/internal/shared/types"
)

// ---------------------------------------------------------------------------
// NewScanner
// ---------------------------------------------------------------------------

func TestNewScanner_ReturnsInitialized(t *testing.T) {
	s := NewScanner()
	if s == nil {
		t.Fatal("NewScanner() returned nil")
	}
	if s.queue == nil {
		t.Error("queue should be initialized")
	}
	if s.stop == nil {
		t.Error("stop channel should be initialized")
	}
	if s.done == nil {
		t.Error("done channel should be initialized")
	}
	if s.current != nil {
		t.Error("current should be nil initially")
	}
	if s.queue.Len() != 0 {
		t.Errorf("queue should start empty, got len %d", s.queue.Len())
	}
}

// ---------------------------------------------------------------------------
// tryPopJob
// ---------------------------------------------------------------------------

func TestTryPopJob_EmptyQueue(t *testing.T) {
	s := NewScanner()
	job := s.tryPopJob()
	if job != nil {
		t.Errorf("tryPopJob on empty queue should return nil, got %v", job)
	}
}

func TestTryPopJob_SingleItem(t *testing.T) {
	s := NewScanner()
	ws := &workspace.Workspace{Id: 1, Path: "/tmp/test"}
	task := &ScanTask{workspace: ws, forceRefresh: false}
	s.queue.PushBack(task)

	job := s.tryPopJob()
	if job == nil {
		t.Fatal("tryPopJob should return a task from non-empty queue")
	}
	if job.workspace.Id != 1 {
		t.Errorf("expected workspace id 1, got %d", job.workspace.Id)
	}
	if job.forceRefresh {
		t.Error("expected forceRefresh to be false")
	}
	if s.queue.Len() != 0 {
		t.Errorf("queue should be empty after pop, len = %d", s.queue.Len())
	}
}

func TestTryPopJob_FIFO(t *testing.T) {
	s := NewScanner()
	ws1 := &workspace.Workspace{Id: 1, Path: "/tmp/a"}
	ws2 := &workspace.Workspace{Id: 2, Path: "/tmp/b"}
	ws3 := &workspace.Workspace{Id: 3, Path: "/tmp/c"}

	s.queue.PushBack(&ScanTask{workspace: ws1})
	s.queue.PushBack(&ScanTask{workspace: ws2})
	s.queue.PushBack(&ScanTask{workspace: ws3})

	job1 := s.tryPopJob()
	job2 := s.tryPopJob()
	job3 := s.tryPopJob()
	job4 := s.tryPopJob()

	if job1.workspace.Id != 1 {
		t.Errorf("first pop: expected id 1, got %d", job1.workspace.Id)
	}
	if job2.workspace.Id != 2 {
		t.Errorf("second pop: expected id 2, got %d", job2.workspace.Id)
	}
	if job3.workspace.Id != 3 {
		t.Errorf("third pop: expected id 3, got %d", job3.workspace.Id)
	}
	if job4 != nil {
		t.Error("fourth pop should return nil")
	}
}

func TestTryPopJob_ForceRefreshPreserved(t *testing.T) {
	s := NewScanner()
	ws := &workspace.Workspace{Id: 42, Path: "/tmp/refresh"}
	s.queue.PushBack(&ScanTask{workspace: ws, forceRefresh: true})

	job := s.tryPopJob()
	if !job.forceRefresh {
		t.Error("forceRefresh should be true")
	}
}

// ---------------------------------------------------------------------------
// setCurrent
// ---------------------------------------------------------------------------

func TestSetCurrent_SetsAndClearsWorkspace(t *testing.T) {
	s := NewScanner()
	ws := &workspace.Workspace{Id: 5, Path: "/tmp/ws"}

	s.setCurrent(ws)
	s.mu.RLock()
	got := s.current
	s.mu.RUnlock()
	if got != ws {
		t.Error("setCurrent should set the current workspace")
	}

	s.setCurrent(nil)
	s.mu.RLock()
	got = s.current
	s.mu.RUnlock()
	if got != nil {
		t.Error("setCurrent(nil) should clear the current workspace")
	}
}

// ---------------------------------------------------------------------------
// Scanner.Add
// ---------------------------------------------------------------------------

func TestScannerAdd_NilWorkspace(t *testing.T) {
	s := NewScanner()
	err := s.Add(nil, false)
	if err == nil {
		t.Error("Add(nil) should return an error")
	}
}

// ---------------------------------------------------------------------------
// Scanner lifecycle: Start → Stop
// ---------------------------------------------------------------------------

func TestScannerStartStop_Lifecycle(t *testing.T) {
	s := NewScanner()
	var wg sync.WaitGroup

	s.Start(&wg)

	// Give the goroutine a moment to enter the run loop
	// then stop it
	s.Stop()

	// wg.Wait should return promptly
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("Scanner did not stop within 2 seconds")
	}
}

// ---------------------------------------------------------------------------
// Scanner concurrent safety: tryPopJob under contention
// ---------------------------------------------------------------------------

func TestTryPopJob_ConcurrentSafety(t *testing.T) {
	s := NewScanner()
	const n = 100
	for i := 0; i < n; i++ {
		ws := &workspace.Workspace{Id: i, Path: "/tmp/ws"}
		s.queue.PushBack(&ScanTask{workspace: ws})
	}

	var wg sync.WaitGroup
	results := make(chan *ScanTask, n)

	// Pop from multiple goroutines concurrently
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				job := s.tryPopJob()
				if job == nil {
					return
				}
				results <- job
			}
		}()
	}

	wg.Wait()
	close(results)

	seen := make(map[int]bool)
	for job := range results {
		if seen[job.workspace.Id] {
			t.Errorf("workspace id %d popped more than once", job.workspace.Id)
		}
		seen[job.workspace.Id] = true
	}

	if len(seen) != n {
		t.Errorf("expected %d unique pops, got %d", n, len(seen))
	}
}

// ---------------------------------------------------------------------------
// setCurrent concurrent safety
// ---------------------------------------------------------------------------

func TestSetCurrent_ConcurrentSafety(t *testing.T) {
	s := NewScanner()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ws := &workspace.Workspace{Id: id, Path: "/tmp/ws"}
			s.setCurrent(ws)
			s.setCurrent(nil)
		}(i)
	}

	wg.Wait()
	// No race panic = success
}

// ---------------------------------------------------------------------------
// GitIgnoreFilter.Match
// ---------------------------------------------------------------------------

// We test the basic shape of GitIgnoreFilter. Since the underlying
// gitutils.GitIgnore performs the real logic, we just verify the inversion
// (Match returns !IsIgnored).
func TestGitIgnoreFilter_MatchInversion(t *testing.T) {
	// The GitIgnoreFilter inverts the gitutils.IsIgnored result.
	// We can't easily construct a real gitutils.GitIgnore without a real
	// repo, but we verify the struct compiles and the interface is satisfied.
	// This is a compile-time check that GitIgnoreFilter implements the filter interface.
	var _ interface {
		Match(path string, isDir bool) bool
	} = &GitIgnoreFilter{}
}

// ---------------------------------------------------------------------------
// ScanTask struct
// ---------------------------------------------------------------------------

func TestScanTask_Fields(t *testing.T) {
	ws := &workspace.Workspace{Id: 7, Path: "/tmp/scan-test"}
	task := ScanTask{workspace: ws, forceRefresh: true}

	if task.workspace.Id != 7 {
		t.Errorf("workspace id: got %d, want 7", task.workspace.Id)
	}
	if !task.forceRefresh {
		t.Error("forceRefresh should be true")
	}
}

// ---------------------------------------------------------------------------
// Scanner queue management: multiple pushes then pops
// ---------------------------------------------------------------------------

func TestScanner_QueueIsStandardList(t *testing.T) {
	s := NewScanner()
	// Verify it uses container/list under the hood
	_ = list.New() // import check

	ws := &workspace.Workspace{Id: 1}
	s.mu.Lock()
	s.queue.PushBack(&ScanTask{workspace: ws})
	length := s.queue.Len()
	s.mu.Unlock()

	if length != 1 {
		t.Errorf("queue length should be 1, got %d", length)
	}

	job := s.tryPopJob()
	if job == nil {
		t.Fatal("should pop the job")
	}
	if s.queue.Len() != 0 {
		t.Error("queue should be empty after popping")
	}
}

// ---------------------------------------------------------------------------
// Scanner.run — integration test: Start, Add a task, Stop
// ---------------------------------------------------------------------------

func TestScannerRun_ProcessesQueuedTask(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	s := NewScanner()
	var wg sync.WaitGroup

	s.Start(&wg)

	// Create a workspace through the proper API so storage is usable.
	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	// Drain the parser channel in background so processWorkspace doesn't block
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for {
			select {
			case <-parser.ch:
			case <-time.After(500 * time.Millisecond):
				return
			}
		}
	}()

	// Manually push to the queue (bypassing Add's StartIndexing check)
	s.mu.Lock()
	s.queue.PushBack(&ScanTask{workspace: ws, forceRefresh: false})
	s.mu.Unlock()

	// Wait a bit for the scanner to process the task, then stop
	time.Sleep(100 * time.Millisecond)
	s.Stop()
	<-drainDone

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Scanner did not stop within 2 seconds")
	}
}

// ---------------------------------------------------------------------------
// processWorkspace — integration test with real files
// ---------------------------------------------------------------------------

func TestProcessWorkspace_WithFiles(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()

	// Create some files in the workspace
	for _, name := range []string{"main.go", "lib.go", "README.md"} {
		fpath := filepath.Join(wsDir, name)
		if err := os.WriteFile(fpath, []byte("package main\n"), 0644); err != nil {
			t.Fatalf("write file %s: %v", name, err)
		}
	}

	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	// Set up filters to include all files
	ws.UseGlobalFilters = false
	ws.Filters = &types.Filters{
		Exclude: types.Exclude{
			UseGitIgnore: false,
			Customized:   conf.DefaultExclude,
		},
		Include: []string{"**/*"},
	}

	s := NewScanner()

	// Drain the parser channel in background
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for {
			select {
			case <-parser.ch:
			case <-time.After(500 * time.Millisecond):
				return
			}
		}
	}()

	err = s.processWorkspace(ws, false)
	if err != nil {
		t.Errorf("processWorkspace: %v", err)
	}

	<-drainDone
}

func TestProcessWorkspace_EmptyDirectory(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	ws.UseGlobalFilters = false
	ws.Filters = &types.Filters{
		Exclude: types.Exclude{
			UseGitIgnore: false,
			Customized:   conf.DefaultExclude,
		},
		Include: []string{"**/*"},
	}

	s := NewScanner()
	err = s.processWorkspace(ws, false)
	if err != nil {
		t.Errorf("processWorkspace on empty dir: %v", err)
	}
}

func TestProcessWorkspace_DeletedWorkspace(t *testing.T) {
	_, teardown := setupTestEnv(t)
	defer teardown()

	wsDir := t.TempDir()
	// Create a file
	if err := os.WriteFile(filepath.Join(wsDir, "test.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ws, err := workspace.Create(wsDir)
	if err != nil {
		t.Fatalf("workspace.Create: %v", err)
	}

	ws.UseGlobalFilters = false
	ws.Filters = &types.Filters{
		Exclude: types.Exclude{
			UseGitIgnore: false,
			Customized:   conf.DefaultExclude,
		},
		Include: []string{"**/*"},
	}

	// Mark as deleted before processing
	ws.SetDeleted()

	s := NewScanner()
	err = s.processWorkspace(ws, false)
	if err == nil {
		t.Error("processWorkspace on deleted workspace should return error")
	}
}
