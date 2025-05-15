package workspace

import (
	"encoding/json"
	"log"
	"sync"

	"github.com/ai-microsoft/haystack/server/core/pebble"
	"github.com/ai-microsoft/haystack/server/core/workspace/internal"
	"github.com/ai-microsoft/haystack/utils"
)

var (
	workspaces     map[int]*Workspace
	workspacePaths map[string]*Workspace

	mutex sync.RWMutex
)

func Init(database pebble.DB) error {
	mutex.Lock()
	defer mutex.Unlock()

	internal.Init(database)

	workspaces = make(map[int]*Workspace)
	workspacePaths = make(map[string]*Workspace)
	allWorkspaces, err := internal.GetAllWorkspaces()
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
