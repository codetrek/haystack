package workspace

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/codetrek/haystack/internal/shared/types"
)

// wsOnDisk is the legacy JSON shape emitted by Serialize. It has no production
// callers; Serialize/wsOnDisk live here so the existing serialization tests can
// continue to exercise the legacy snapshot shape without bloating the
// production package.
type wsOnDisk struct {
	Id               int            `json:"id"`
	Path             string         `json:"path"`
	UseGlobalFilters bool           `json:"use_global_filters"`
	Filters          *types.Filters `json:"filters,omitempty"`
	CreatedAt        time.Time      `json:"created_time"`
	LastAccessed     time.Time      `json:"last_accessed_time"`
	LastFullSync     time.Time      `json:"last_full_sync_time"`
}

// Serialize returns the legacy on-disk JSON snapshot for a workspace. Test-only.
func (w *Workspace) Serialize() ([]byte, error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.deleted {
		return nil, fmt.Errorf("workspace is deleted")
	}

	return json.Marshal(wsOnDisk{
		Id:               w.Id,
		Path:             w.Path,
		UseGlobalFilters: w.UseGlobalFilters,
		Filters:          w.Filters,
		CreatedAt:        w.CreatedAt,
		LastAccessed:     w.LastAccessed,
		LastFullSync:     w.LastFullSync,
	})
}
