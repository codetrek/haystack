package workspace

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/server/core/workspace/internal"
	"github.com/ai-microsoft/haystack/shared/types"
)

type IndexingStatus struct {
	StartedAt         *time.Time
	TotalFiles        int
	IndexedFiles      int
	SymbolParsedFiles int
}

type Workspace struct {
	Id                 int            `json:"id"`
	Path               string         `json:"path"`
	UseGlobalFilters   bool           `json:"use_global_filters"`
	Filters            *types.Filters `json:"filters,omitempty" optional:"true"`
	TotalFiles         int            `json:"total_files"`
	EnableSymbolParse  bool           `json:"enable_symbol_parse"`
	EnablePromptSearch bool           `json:"enable_prompt_search"`

	CreatedAt    time.Time `json:"created_time"`
	LastAccessed time.Time `json:"last_accessed_time"`
	LastFullSync time.Time `json:"last_full_sync_time"`

	deleted        bool            `json:"-"`
	indexingStatus *IndexingStatus `json:"-"`
	mutex          sync.Mutex      `json:"-"`
}

func (w *Workspace) AddTotalFiles(n int) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	w.TotalFiles += n
}

func (w *Workspace) AddIndexingFiles(n int) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.indexingStatus != nil {
		w.indexingStatus.IndexedFiles += n
	}
}

func (w *Workspace) AddSymbolParsedFiles(n int) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.indexingStatus != nil {
		w.indexingStatus.SymbolParsedFiles += n
	}
}

func (w *Workspace) AddIndexingTotalFiles(n int) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.indexingStatus != nil {
		w.indexingStatus.TotalFiles += n
	}
}

func (w *Workspace) GetTotalFiles() int {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	totalFiles := w.TotalFiles
	if totalFiles == 0 && w.indexingStatus != nil {
		totalFiles = w.indexingStatus.TotalFiles
	}

	return totalFiles
}

func (w *Workspace) GetIndexingStatus() *IndexingStatus {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	return w.indexingStatus
}

func (w *Workspace) StartIndexing() error {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.indexingStatus != nil {
		return fmt.Errorf("workspace is indexing")
	}

	now := time.Now()
	w.indexingStatus = &IndexingStatus{
		StartedAt:         &now,
		TotalFiles:        0,
		IndexedFiles:      0,
		SymbolParsedFiles: 0,
	}

	return nil
}

func (w *Workspace) Save() error {
	json, err := w.Serialize()
	if err != nil {
		return err
	}

	internal.Save(w.Id, string(json))
	return nil
}

func (w *Workspace) UpdateLastFullSync() {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.indexingStatus != nil {
		w.TotalFiles = w.indexingStatus.TotalFiles
		w.indexingStatus = nil
	}

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

func (w *Workspace) Serialize() ([]byte, error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.deleted {
		return nil, fmt.Errorf("workspace is deleted")
	}

	return json.Marshal(w)
}
