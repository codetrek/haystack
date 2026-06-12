package workspace

import (
	"fmt"
	"sync"
	"time"

	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/internal/shared/types"
	"github.com/codetrek/haystack/searchcore/collection"
	"github.com/codetrek/haystack/searchcore/documents"
)

type IndexingState int

const (
	IndexingIdle IndexingState = iota
	IndexingScanning
	IndexingDone
	IndexingFailed
)

// docStoreInst is injected via SetDocStore to avoid having workspace call
// package-level documents functions. Set once during server initialisation.
var docStoreInst *documents.Store

// SetDocStore injects the documents.Store instance used for file counting.
func SetDocStore(st *documents.Store) {
	docStoreInst = st
}

type IndexingProgress struct {
	StartedAt         *time.Time
	IndexedFiles      int
	SymbolParsedFiles int
}

type Workspace struct {
	Id               int            `json:"id"`
	Path             string         `json:"path"`
	Desc             string         `json:"desc,omitempty"`
	UseGlobalFilters bool           `json:"use_global_filters"`
	Filters          *types.Filters `json:"filters,omitempty" optional:"true"`

	CreatedAt    time.Time `json:"created_time"`
	LastAccessed time.Time `json:"last_accessed_time"`
	LastFullSync time.Time `json:"last_full_sync_time"`

	deleted          bool              `json:"-"`
	indexingState    IndexingState     `json:"-"`
	indexingProgress *IndexingProgress `json:"-"`
	mutex            sync.Mutex        `json:"-"`
}

func (w *Workspace) AddIndexingFiles(n int) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.indexingProgress != nil {
		w.indexingProgress.IndexedFiles += n
	}
}

func (w *Workspace) AddSymbolParsedFiles(n int) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.indexingProgress != nil {
		w.indexingProgress.SymbolParsedFiles += n
	}
}

func (w *Workspace) GetTotalFiles() int {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if docStoreInst != nil {
		return docStoreInst.CountByCollection(w.Id)
	}
	return 0
}

func (w *Workspace) GetIndexingProgress() *IndexingProgress {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	return w.indexingProgress
}

func (w *Workspace) StartIndexing() error {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.indexingState == IndexingScanning {
		return fmt.Errorf("workspace is indexing")
	}

	now := time.Now()
	w.indexingState = IndexingScanning
	w.indexingProgress = &IndexingProgress{
		StartedAt:         &now,
		IndexedFiles:      0,
		SymbolParsedFiles: 0,
	}

	return nil
}

// Save persists the workspace metadata to the Catalog.
func (w *Workspace) Save() error {
	w.mutex.Lock()
	if w.deleted {
		w.mutex.Unlock()
		return fmt.Errorf("workspace is deleted")
	}
	// Snapshot fields under the lock.
	rec := collection.Record{
		ID:           w.Id,
		Name:         w.Path,
		Desc:         w.Desc,
		CreatedAt:    w.CreatedAt,
		LastAccessed: w.LastAccessed,
		LastFullSync: w.LastFullSync,
		Extra:        encodeExtra(w.UseGlobalFilters, w.Filters),
	}
	w.mutex.Unlock()

	if catalog == nil {
		return fmt.Errorf("workspace catalog not initialised")
	}

	return catalog.Save(&rec)
}

func (w *Workspace) GetLastFullSync() time.Time {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	return w.LastFullSync
}

func (w *Workspace) UpdateLastFullSync() {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.indexingProgress != nil {
		w.indexingProgress = nil
	}

	w.indexingState = IndexingDone
	w.LastFullSync = time.Now()
}

func (w *Workspace) GetFilters() types.Filters {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	if w.Filters == nil || w.UseGlobalFilters {
		return conf.Get().Server.Filters
	}

	t := *w.Filters
	if !t.Exclude.UseGitIgnore && len(t.Exclude.Customized) == 0 {
		t.Exclude.Customized = conf.Get().Server.Filters.Exclude.Customized
	}

	if len(t.Include) == 0 {
		t.Include = conf.Get().Server.Filters.Include
	}

	return t
}

func (w *Workspace) SetIndexingFailed() {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	w.indexingState = IndexingFailed
	w.indexingProgress = nil
}

func (w *Workspace) ResetIndexingState() {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	w.indexingState = IndexingIdle
	w.indexingProgress = nil
}

func (w *Workspace) GetIndexingState() IndexingState {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	return w.indexingState
}

func (w *Workspace) SetDeleted() {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	w.deleted = true
}

func (w *Workspace) IsDeleted() bool {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	return w.deleted
}
