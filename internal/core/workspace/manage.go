package workspace

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/codetrek/haystack/internal/core/symbols"
	"github.com/codetrek/haystack/internal/shared/types"
	"github.com/codetrek/haystack/internal/utils"
)

func GetAllPaths() []string {
	mutex.RLock()
	defer mutex.RUnlock()

	result := []string{}
	for _, workspace := range workspaces {
		result = append(result, workspace.Path)
	}

	return result
}

func GetAll() []types.Workspace {
	mutex.RLock()
	defer mutex.RUnlock()

	result := []types.Workspace{}
	for _, workspace := range workspaces {
		indexing := workspace.GetIndexingProgress()

		totalFiles := workspace.GetTotalFiles()

		result = append(result, types.Workspace{
			Id:               workspace.Id,
			Path:             workspace.Path,
			CreatedAt:        workspace.CreatedAt,
			TotalFiles:       totalFiles,
			UseGlobalFilters: workspace.UseGlobalFilters,
			Filters:          workspace.Filters,
			LastAccessed:     workspace.LastAccessed,
			LastFullSync:     workspace.GetLastFullSync(),
			Indexing:         indexing != nil,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Id < result[j].Id
	})

	return result
}

func GetByPath(path string) (*Workspace, error) {
	path = utils.NormalizePath(path)

	mutex.RLock()
	defer mutex.RUnlock()
	if workspace, ok := workspacePaths[path]; ok {
		return workspace, nil
	}

	return nil, fmt.Errorf("workspace not found")
}

func Get(workspaceId int) (*Workspace, error) {
	mutex.RLock()
	defer mutex.RUnlock()

	workspace, ok := workspaces[workspaceId]
	if !ok || workspace.deleted {
		return nil, fmt.Errorf("workspace not found")
	}

	return workspace, nil
}

func Create(workspacePath string) (*Workspace, error) {
	workspacePath = utils.NormalizePath(workspacePath)

	mutex.Lock()
	defer mutex.Unlock()

	if workspacePaths[workspacePath] != nil {
		return nil, fmt.Errorf("workspace already exists")
	}

	// Validate the workspace path
	// 1. It must be absolute
	// 2. It must be a directory
	if !filepath.IsAbs(workspacePath) {
		return nil, fmt.Errorf("workspace path must be absolute")
	}

	info, err := os.Stat(workspacePath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat workspace: %v", err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("workspace path must be a directory")
	}

	// catalog.Create allocates the id, persists the Record, and calls
	// docs.Create — no separate docStoreInst.Create call needed.
	col, err := catalog.Create(workspacePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create workspace record: %w", err)
	}

	meta := col.Meta()
	workspace := &Workspace{
		Id:               meta.ID,
		Path:             workspacePath,
		Desc:             meta.Desc,
		UseGlobalFilters: true,
		CreatedAt:        meta.CreatedAt,
		LastAccessed:     meta.LastAccessed,
	}

	// Persist UseGlobalFilters=true into the record's Extra field. The workspace
	// is not yet published into the maps, so no other goroutine can observe it;
	// the per-workspace mutex is unnecessary here.
	rec := meta
	rec.Desc = workspace.Desc
	rec.Extra = encodeExtra(workspace.UseGlobalFilters, workspace.Filters)
	if err := catalog.Save(rec); err != nil {
		log.Printf("[Workspace] Warning: failed to save extra for new workspace %d: %v", workspace.Id, err)
	}

	workspaces[workspace.Id] = workspace
	workspacePaths[workspace.Path] = workspace

	log.Printf("[Workspace] New workspace created: %v, path: %v", workspace.Id, workspacePath)

	symbols.Create(workspace.Id, workspacePath)
	return workspace, nil
}

func Delete(workspaceId int) error {
	mutex.Lock()
	defer mutex.Unlock()

	workspace, ok := workspaces[workspaceId]
	if !ok {
		return fmt.Errorf("workspace not found")
	}

	// Delete the catalog record (and its document data) FIRST. If this fails the
	// in-memory overlay is left untouched so it stays consistent with disk; a
	// later retry can succeed. catalog.Delete removes the record AND calls
	// docs.Delete — no separate docStoreInst.Delete call needed.
	if err := catalog.Delete(workspaceId); err != nil {
		return fmt.Errorf("failed to delete workspace from catalog: %w", err)
	}

	// Catalog delete succeeded — now drop the workspace from the overlay.
	workspace.SetDeleted()
	delete(workspaces, workspaceId)
	delete(workspacePaths, workspace.Path)

	symbols.Delete(workspace.Id)

	return nil
}

func Move(id int, newPath string) (*Workspace, error) {
	newPath = utils.NormalizePath(newPath)

	mutex.Lock()
	defer mutex.Unlock()

	workspace, ok := workspaces[id]
	if !ok || workspace.deleted {
		return nil, fmt.Errorf("workspace id not found")
	}

	if _, ok := workspacePaths[newPath]; ok {
		return nil, fmt.Errorf("workspace for this path already exists")
	}

	// Validate the workspace path
	// 1. It must be absolute
	// 2. It must be a directory
	if !filepath.IsAbs(newPath) {
		return nil, fmt.Errorf("workspace path must be absolute")
	}

	info, err := os.Stat(newPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat workspace: %v", err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("workspace path must be a directory")
	}

	oldPath := workspace.Path
	log.Printf("[Workspace] Moving workspace %v from %v to %v", id, oldPath, newPath)

	// Mutate Path/LastAccessed under the per-workspace mutex: Save() and
	// Serialize() read these fields under the same lock, so writing them without
	// it would be a data race. Save() takes the mutex itself, so release it
	// first.
	workspace.mutex.Lock()
	workspace.Path = newPath
	workspace.LastAccessed = time.Now()
	workspace.mutex.Unlock()

	workspace.Save() //nolint:errcheck — best-effort; catalog.Save guards the rename

	workspacePaths[newPath] = workspace
	delete(workspacePaths, oldPath)

	log.Printf("[Workspace] Workspace %v moved from %v to %v", id, oldPath, newPath)

	return workspace, nil
}
