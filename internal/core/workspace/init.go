package workspace

import (
	"log"
	"sync"

	"github.com/codetrek/haystack/internal/utils"
	"github.com/codetrek/haystack/core/collection"
)

var (
	workspaces     map[int]*Workspace
	workspacePaths map[string]*Workspace

	mutex sync.RWMutex

	// catalog is the backing Catalog injected by Init.
	catalog *collection.Catalog
)

// Init wires the workspace package to an already-constructed Catalog.
// It builds the in-memory *Workspace maps from cat.List(); runtime indexing
// state starts fresh for every record.
func Init(cat *collection.Catalog) error {
	mutex.Lock()
	defer mutex.Unlock()

	catalog = cat

	workspaces = make(map[int]*Workspace)
	workspacePaths = make(map[string]*Workspace)

	for _, r := range cat.List() {
		useGlobal, filters := decodeExtra(r.Extra)

		ws := &Workspace{
			Id:               r.ID,
			Path:             utils.NormalizePath(r.Name),
			Desc:             r.Desc,
			UseGlobalFilters: useGlobal,
			Filters:          filters,
			CreatedAt:        r.CreatedAt,
			LastAccessed:     r.LastAccessed,
			LastFullSync:     r.LastFullSync,
		}

		workspaces[ws.Id] = ws
		workspacePaths[ws.Path] = ws
		log.Printf("[Workspace] Found workspace: %v, path: %v", ws.Id, ws.Path)
	}

	return nil
}
