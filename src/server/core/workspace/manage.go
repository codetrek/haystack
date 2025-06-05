package workspace

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/server/core/documents"
	"github.com/ai-microsoft/haystack/server/core/symbols"
	"github.com/ai-microsoft/haystack/server/core/workspace/internal"
	"github.com/ai-microsoft/haystack/shared/types"
	"github.com/ai-microsoft/haystack/utils"
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
		indexing := workspace.GetIndexingStatus()

		totalFiles := workspace.TotalFiles
		if totalFiles == 0 && indexing != nil {
			totalFiles = indexing.TotalFiles
		}

		result = append(result, types.Workspace{
			Id:                workspace.Id,
			Path:              workspace.Path,
			CreatedAt:         workspace.CreatedAt,
			TotalFiles:        totalFiles,
			UseGlobalFilters:  workspace.UseGlobalFilters,
			Filters:           workspace.Filters,
			EnableSymbolParse: workspace.EnableSymbolParse,
			LastAccessed:      workspace.LastAccessed,
			LastFullSync:      workspace.LastFullSync,
			Indexing:          workspace.indexingStatus != nil,
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

	workspace := workspacePaths[workspacePath]
	if workspace != nil {
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

	var id int
	// Try 10 times to generate a unique workspace id
	for range 10 {
		id, err = internal.GetNextId()
		if err != nil {
			return nil, err
		}

		if _, ok := workspaces[id]; !ok {
			break
		}
	}

	if _, ok := workspaces[id]; ok {
		return nil, fmt.Errorf("failed to generate unique workspace id")
	}

	workspace = &Workspace{
		Id:                id,
		Path:              workspacePath,
		UseGlobalFilters:  true,
		EnableSymbolParse: conf.Get().Symbols.DefaultEnableSymbolParse,
		CreatedAt:         time.Now(),
		LastAccessed:      time.Now(),
	}

	if err := workspace.Save(); err != nil {
		return nil, err
	}

	workspaces[workspace.Id] = workspace
	workspacePaths[workspace.Path] = workspace

	log.Printf("[Workspace] New workspace created: %v, path: %v", id, workspacePath)

	documents.Create(workspace.Id, workspacePath)
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

	workspace.SetDeleted()
	delete(workspaces, workspaceId)
	delete(workspacePaths, workspace.Path)

	internal.Delete(workspaceId)
	documents.Delete(workspaceId)
	symbols.Delete(workspace.Id)

	return nil
}
