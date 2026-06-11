package workspace

import (
	"encoding/json"
	"log"
	"sync"

	"github.com/codetrek/haystack/internal/core/workspace/internal"
	"github.com/codetrek/haystack/internal/utils"
	"github.com/codetrek/haystack/searchcore/kv"
)

var (
	workspaces     map[int]*Workspace
	workspacePaths map[string]*Workspace

	mutex sync.RWMutex
)

func Init(database kv.Store) error {
	mutex.Lock()
	defer mutex.Unlock()

	internal.Init(database)

	workspaces = make(map[int]*Workspace)
	workspacePaths = make(map[string]*Workspace)
	allWorkspaces, err := internal.ScanAll()
	if err != nil {
		return err
	}

	for id, data := range allWorkspaces {
		space := Workspace{
			Id:               id,
			UseGlobalFilters: true,
		}

		if err := json.Unmarshal([]byte(data), &space); err == nil {
			space.Path = utils.NormalizePath(space.Path)
			workspaces[space.Id] = &space
			workspacePaths[space.Path] = workspaces[space.Id]
			log.Printf("[Workspace] Found workspace: %v, path: %v", space.Id, space.Path)
		} else {
			log.Printf("[Workspace] Error unmarshalling workspace: %v", err)
			// TODO: Delete the malformed workspace
		}
	}

	return nil
}
