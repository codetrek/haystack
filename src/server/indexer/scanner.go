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
	log.Println("Scanner stopped")
}

// run is the main scanning loop that processes workspaces from the queue.
func (s *Scanner) run(wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		workspace := s.tryPopJob()
		if workspace == nil {
			select {
			case <-s.stop:
				close(s.done)
				return
			case <-time.After(500 * time.Millisecond):
				continue
			}
		}

		s.setCurrent(workspace)
		if err := s.processWorkspace(workspace); err != nil {
			log.Printf("Error scanning workspace %s: %v", workspace.Path, err)
		} else {
			workspace.UpdateLastFullSync()
			workspace.Save()
		}
		s.setCurrent(nil)
	}
}

// processWorkspace processes a single workspace by scanning its files and applying filters.
func (s *Scanner) processWorkspace(w *workspace.Workspace) error {
	log.Printf("Start processing workspace %s", w.Path)
	start := time.Now()
	fileCount := 0
	interrupted := false
	defer func() {
		log.Printf("Finished processing workspace %s, cost %s, %d files, interrupted: %t", w.Path, time.Since(start), fileCount, interrupted)
	}()

	baseDir := w.Path
	filters := w.GetFilters()

	var exclude fsutils.ListFileFilter
	if filters.Exclude.UseGitIgnore {
		exclude = &GitIgnoreFilter{
			ignore: gitutils.NewGitIgnore(baseDir, true),
		}
	} else {
		exclude = utils.NewSimpleFilterExclude(filters.Exclude.Customized, baseDir)
	}

	include := utils.NewSimpleFilter(filters.Include, baseDir)
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
			log.Printf("Scanning %s, %d files found, elapsed %s", w.Path, fileCount, time.Since(startTime))
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
func (s *Scanner) tryPopJob() *workspace.Workspace {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.queue.Len() > 0 {
		job := s.queue.Remove(s.queue.Front())
		return job.(*workspace.Workspace)
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
func (s *Scanner) Add(w *workspace.Workspace) error {
	if w == nil {
		return fmt.Errorf("cannot add nil workspace to scanner queue")
	}

	if err := w.StartIndexing(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue.PushBack(w)
	return nil
}
