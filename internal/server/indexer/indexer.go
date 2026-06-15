package indexer

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/codetrek/haystack/internal/core/symbols"
	"github.com/codetrek/haystack/internal/core/workspace"
	"github.com/codetrek/haystack/internal/shared/running"
	"github.com/codetrek/haystack/internal/shared/types"
	"github.com/codetrek/haystack/core/documents"
)

var (
	mu           sync.Mutex
	scanner      = NewScanner()
	parser       = NewParser()
	writer       = NewWriter()
	symbolParser = NewSymbolParser()

	// stInst is the documents.Store instance injected via SetDocStore.
	stInst *documents.Store
)

// SetDocStore injects the documents.Store instance used by indexer operations.
func SetDocStore(st *documents.Store) {
	mu.Lock()
	defer mu.Unlock()
	stInst = st
}

// snapshotComponents returns a snapshot of the package-level components under the lock.
func snapshotComponents() (*Scanner, *Parser, *Writer, *SymbolParser) {
	mu.Lock()
	defer mu.Unlock()
	return scanner, parser, writer, symbolParser
}

// Run starts the indexer components in separate goroutines.
func Run(wg *sync.WaitGroup) {
	log.Println("[Indexer] Starting...")

	// Capture local copies under the lock so the goroutine below is not
	// affected by a concurrent ResetForTest() call in another test.
	sc, pa, wr, sp := snapshotComponents()

	sc.Start(wg)
	pa.Start(wg)
	wr.Start(wg)
	sp.Start(wg)
	log.Println("[Indexer] Started.")

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-running.GetShutdown().Done()
		log.Println("[Indexer] Stopping...")
		sc.Stop()
		pa.Stop()
		wr.Stop()
		sp.Stop()
		log.Println("[Indexer] Stopped.")
	}()
}

func CreateWorkspace(workspacePath string, useGlobalFilter bool, filters *types.Filters) (*workspace.Workspace, error) {
	w, err := workspace.Create(workspacePath)
	if err != nil {
		return nil, err
	}

	w.UseGlobalFilters = useGlobalFilter
	w.Filters = filters
	w.Save()

	Sync(w, false)
	return w, nil
}

// SyncIfNeeded checks if a workspace needs to be synced and adds it to the scanner queue if necessary.
// A workspace needs to be synced if:
// 1. It has never been successfully synced (LastFullSync is zero)
func SyncIfNeeded(workspacePath string) error {
	workspace, _ := workspace.GetByPath(workspacePath)
	if workspace == nil {
		return fmt.Errorf("workspace not found")
	}

	if workspace.GetLastFullSync().IsZero() {
		return Sync(workspace, false)
	} else {
		log.Printf("[Indexer] Workspace %s is up to date, skipping", workspacePath)
	}
	return nil
}

func Sync(workspace *workspace.Workspace, forceRefresh bool) error {
	mu.Lock()
	sc := scanner
	mu.Unlock()
	return sc.Add(workspace, forceRefresh)
}

func AddOrSyncFile(workspace *workspace.Workspace, relPath string) error {
	mu.Lock()
	pa := parser
	st := stInst
	mu.Unlock()

	fullPath := filepath.Join(workspace.Path, relPath)
	docid, err := GetDocumentId(relPath)
	if err != nil {
		return err
	}

	doc, err := st.GetDocument(workspace.Id, docid, false)
	if err != nil {
		return err
	}

	if doc == nil {
		// log.Printf("[Indexer] Adding new file `%s` to workspace `%s`", relPath, workspace.Path)
		stat, err := os.Stat(fullPath)
		if err != nil || stat.IsDir() {
			return err
		}

		// Add new file to the parser queue
		pa.Add(workspace, relPath)
	} else {
		// log.Printf("[Indexer] Syncing existing file `%s` in workspace `%s`", relPath, workspace.Path)
		stat, err := os.Stat(fullPath)
		if err != nil || stat.IsDir() {
			// Remove the file from the index
			RemoveFile(workspace, relPath)
		} else {
			// Sync existing file to the parser queue
			pa.Add(workspace, relPath)
		}
	}

	return nil
}

func RemoveFile(workspace *workspace.Workspace, relPath string) error {
	mu.Lock()
	st := stInst
	mu.Unlock()

	docid, err := GetDocumentId(relPath)
	if err != nil {
		return err
	}

	if err := st.DeleteDocument(workspace.Id, docid); err != nil {
		return err
	}

	if err := symbols.DeleteDocument(workspace.Id, docid); err != nil {
		return err
	}

	workspace.Save()
	return nil
}

func RefreshFilesIfNeeded(workspaceId int, docs map[string]*documents.Document) []string {
	workspace, err := workspace.Get(workspaceId)
	if err != nil {
		return []string{}
	}

	removedDocs := []string{}
	for _, doc := range docs {
		removed, err := RefreshFileIfNeeded(workspace, doc)
		if err != nil {
			continue
		}

		if removed {
			removedDocs = append(removedDocs, doc.ID)
		}
	}

	// Return the list of removed documents
	return removedDocs
}

func RefreshFileIfNeeded(workspace *workspace.Workspace, doc *documents.Document) (removed bool, err error) {
	mu.Lock()
	pa := parser
	mu.Unlock()

	fullPath := filepath.Join(workspace.Path, doc.RelPath)

	stat, err := os.Stat(fullPath)
	// If the file becomes a directory or there is an error, remove it
	if err != nil || stat.IsDir() {
		RemoveFile(workspace, doc.RelPath)
		return true, nil
	}

	// If the file has been modified, add it to the parser queue
	if stat.ModTime().UnixNano() != doc.ModifiedTime {
		pa.Add(workspace, doc.RelPath)
	}

	return false, nil
}
