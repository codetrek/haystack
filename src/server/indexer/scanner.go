package indexer

import (
	"container/list"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/ai-microsoft/haystack/server/core/workspace"
	"github.com/ai-microsoft/haystack/shared/running"
	"github.com/ai-microsoft/haystack/utils"
	fsutils "github.com/ai-microsoft/haystack/utils/fs"
	gitutils "github.com/ai-microsoft/haystack/utils/git"
)

type GitIgnoreFilter struct {
	ignore *gitutils.GitIgnore
}

func (f *GitIgnoreFilter) Match(path string, isDir bool) bool {
	return !f.ignore.IsIgnored(path, isDir)
}

// Scanner represents a file system scanner that processes workspaces in a queue.
// It is responsible for scanning files in workspaces and applying appropriate filters.
type Scanner struct {
	current *workspace.Workspace
	queue   *list.List
	mu      sync.RWMutex
	stop    chan struct{}
	done    chan struct{}
}

// ScannTask represents a workspace to be scanned.
// forceRefresh true means the file will be re-index even if it not changed.
type ScanTask struct {
	workspace    *workspace.Workspace
	forceRefresh bool
}

// NewScanner creates a new Scanner instance.
func NewScanner() *Scanner {
	return &Scanner{
		queue: list.New(),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
}

// Start begins the scanning process in a goroutine.
// It will continue running until the application is shutting down.
func (s *Scanner) Start(wg *sync.WaitGroup) {
	wg.Add(1)
	go s.run(wg)
}

func (s *Scanner) Stop() {
	close(s.stop)
	<-s.done
	log.Println("[Indexer] Scanner stopped")
}

// run is the main scanning loop that processes workspaces from the queue.
func (s *Scanner) run(wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		scanTask := s.tryPopJob()

		if scanTask == nil {
			select {
			case <-s.stop:
				close(s.done)
				return
			case <-time.After(100 * time.Millisecond):
				continue
			}
		}
		workspace := scanTask.workspace
		s.setCurrent(workspace)
		if err := s.processWorkspace(workspace, scanTask.forceRefresh); err != nil {
			log.Printf("[Indexer] Error scanning workspace %s: %v", workspace.Path, err)
		} else {
			workspace.UpdateLastFullSync()
			workspace.Save()
		}
		s.setCurrent(nil)
	}
}

func ShouldIndexFile(w *workspace.Workspace, relPath string) bool {
	if w == nil {
		return false
	}

	if IsNotIndexiable(relPath) {
		return false
	}

	baseDir := w.Path
	filters := w.GetFilters()

	var exclude fsutils.ListFileFilter
	if filters.Exclude.UseGitIgnore {
		exclude = &GitIgnoreFilter{
			ignore: gitutils.NewGitIgnore(baseDir, true),
		}
	} else {
		exclude = utils.NewSimpleFilter(filters.Exclude.Customized)
	}

	if exclude.Match(relPath, false) {
		return false
	}

	include := utils.NewSimpleFilter(filters.Include)
	return include.Match(relPath, false)
}

// processWorkspace processes a single workspace by scanning its files and applying filters.
func (s *Scanner) processWorkspace(w *workspace.Workspace, forceRefresh bool) error {
	log.Printf("[Indexer] Scanner start processing workspace %s", w.Path)
	start := time.Now()
	fileCount := 0
	interrupted := false
	defer func() {
		log.Printf("[Indexer] Scanner finished processing workspace %s, cost %s, %d files, interrupted: %t",
			w.Path, time.Since(start), fileCount, interrupted)
	}()

	baseDir := w.Path
	filters := w.GetFilters()

	var exclude fsutils.ListFileFilter
	if filters.Exclude.UseGitIgnore {
		exclude = &GitIgnoreFilter{
			ignore: gitutils.NewGitIgnore(baseDir, true),
		}
	} else {
		exclude = utils.NewSimpleFilterExclude(filters.Exclude.Customized)
	}

	include := utils.NewSimpleFilter(filters.Include)
	startTime := time.Now()
	lastTime := time.Now()
	err := fsutils.ListFiles(baseDir, fsutils.ListFileOptions{Filter: exclude}, func(fileInfo fsutils.FileInfo) bool {
		if w.IsDeleted() {
			return false
		}

		if IsNotIndexiable(fileInfo.Path) {
			return true
		}

		if include.Match(fileInfo.Path, false) {
			parser.Add(w, fileInfo.Path)
			fileCount++

			w.AddIndexingTotalFiles(1)
		}

		if time.Since(lastTime) > 1000*time.Millisecond {
			log.Printf("[Indexer] Scanning %s, %d files found, elapsed %s", w.Path, fileCount, time.Since(startTime))
			lastTime = time.Now()
		}

		interrupted = running.IsShuttingDown()
		return !interrupted
	})

	if err != nil {
		return err
	}

	if interrupted {
		return fmt.Errorf("interrupted")
	}

	if w.IsDeleted() {
		return fmt.Errorf("workspace is deleted")
	}

	return nil
}

// tryPopJob attempts to remove and return the next workspace from the queue.
// Returns nil if the queue is empty.
func (s *Scanner) tryPopJob() *ScanTask {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.queue.Len() > 0 {
		job := s.queue.Remove(s.queue.Front())
		return job.(*ScanTask)
	}
	return nil
}

// setCurrent updates the current workspace being processed.
func (s *Scanner) setCurrent(w *workspace.Workspace) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = w
}

// Add adds a workspace to the scanning queue.
func (s *Scanner) Add(w *workspace.Workspace, forceRefresh bool) error {
	if w == nil {
		return fmt.Errorf("cannot add nil workspace to scanner queue")
	}

	if err := w.StartIndexing(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.queue.PushBack(&ScanTask{
		workspace:    w,
		forceRefresh: forceRefresh,
	})
	return nil
}
