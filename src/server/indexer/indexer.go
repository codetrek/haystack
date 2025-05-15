package indexer

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/ai-microsoft/haystack/server/core/fulltext"
	"github.com/ai-microsoft/haystack/server/core/workspace"
	"github.com/ai-microsoft/haystack/shared/running"
	"github.com/ai-microsoft/haystack/shared/types"
)

var (
	scanner = NewScanner()
	parser  = NewParser()
	writer  = NewWriter()
)

// Run starts the indexer components in separate goroutines.
func Run(wg *sync.WaitGroup) {
	log.Println("[Indexer] Starting...")

	scanner.Start(wg)
	parser.Start(wg)
	writer.Start(wg)
	log.Println("[Indexer] Started.")

	go func() {
		<-running.GetShutdown().Done()
		log.Println("[Indexer] Stopping...")
		scanner.Stop()
		parser.Stop()
		writer.Stop()
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

	Sync(w)
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

	if workspace.LastFullSync.IsZero() {
		return Sync(workspace)
	} else {
		log.Printf("[Indexer] Workspace %s is up to date, skipping", workspacePath)
	}
	return nil
}

func Sync(workspace *workspace.Workspace) error {
	return scanner.Add(workspace)
}

func AddOrSyncFile(workspace *workspace.Workspace, relPath string) error {
	fullPath := filepath.Join(workspace.Path, relPath)
	docid := GetDocumentId(fullPath)
	doc, err := fulltext.GetDocument(workspace.Id, docid, false)
	if err != nil {
		return err
	}

	if doc == nil {
		stat, err := os.Stat(fullPath)
		if err != nil || stat.IsDir() {
			return err
		}

		// Add new file to the parser queue
		parser.Add(workspace, relPath)
	} else {
		stat, err := os.Stat(fullPath)
		if err != nil || stat.IsDir() {
			// Remove the file from the index
			RemoveFile(workspace, relPath)
		} else {
			// Sync existing file to the parser queue
			parser.Add(workspace, relPath)
		}
	}

	return nil
}

func RemoveFile(workspace *workspace.Workspace, relPath string) error {
	fullPath := filepath.Join(workspace.Path, relPath)

	docid := GetDocumentId(fullPath)
	if err := fulltext.DeleteDocument(workspace.Id, docid); err != nil {
		return err
	}

	workspace.AddTotalFiles(-1)
	workspace.Save()
	return nil
}

func RefreshFilesIfNeeded(workspaceId int, docs map[string]*fulltext.Document) []string {
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

func RefreshFileIfNeeded(workspace *workspace.Workspace, doc *fulltext.Document) (removed bool, err error) {
	fullPath := filepath.Join(workspace.Path, doc.RelPath)

	stat, err := os.Stat(fullPath)
	// If the file becomes a directory or there is an error, remove it
	if err != nil || stat.IsDir() {
		RemoveFile(workspace, doc.RelPath)
		return true, nil
	}

	// If the file has been modified, add it to the parser queue
	if stat.ModTime().UnixNano() != doc.ModifiedTime {
		parser.Add(workspace, doc.RelPath)
	}

	return false, nil
}
