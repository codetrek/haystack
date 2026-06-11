// Package documents provides per-collection document storage that composes an
// inverted index, persisting document metadata, keywords, and path information
// in a kv.Store with async writes serialized through a shared queue.
package documents

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/codetrek/haystack/searchcore/invertedindex"
	"github.com/codetrek/haystack/searchcore/kv"
	"github.com/codetrek/haystack/searchcore/queue"
)

// Workspace holds per-collection metadata persisted in the key-value store.
// It is returned by GetWorkspace and exposes the workspace's identifiers and
// description as JSON-tagged fields.
type Workspace struct {
	WorkspaceId int        `json:"workspace_id"`
	InvertedId  int        `json:"inverted_id"`
	Desc        string     `json:"desc"`
	CreateAt    *time.Time `json:"create_at"`
}

// Options holds tunables for Store. Zero values select production defaults.
//
// The KeyType* fields select the single on-disk key-type prefix byte for each
// kind of key. Byte 0 (NUL) is reserved and cannot be selected by a consumer:
// a zero field means "use the default" (NUL is a poor key prefix, and
// reserving it lets the zero value double as the default sentinel). To use a
// custom prefix, pick any non-zero byte.
//
// Changing any KeyType* field after data has been written is a breaking
// on-disk change.
type Options struct {
	// KeyTypeDocWorkspace is the on-disk prefix byte for workspace metadata keys.
	// Zero selects DefaultKeyTypeDocWorkspace (10).
	KeyTypeDocWorkspace byte

	// KeyTypeDocWords is the on-disk prefix byte for document words keys.
	// Zero selects DefaultKeyTypeDocWords (11).
	KeyTypeDocWords byte

	// KeyTypeDocMeta is the on-disk prefix byte for document metadata keys.
	// Zero selects DefaultKeyTypeDocMeta (12).
	KeyTypeDocMeta byte

	// KeyTypeDocPath is the on-disk prefix byte for document path keys.
	// Zero selects DefaultKeyTypeDocPath (13).
	KeyTypeDocPath byte
}

// Store is the instance-based document store. It persists document metadata,
// keywords, and path information in a kv.Store, and optionally maintains a
// linked invertedindex.Index for full-text search.
type Store struct {
	db  kv.Store
	q   queue.Queue
	idx *invertedindex.Index

	// resolved on-disk key-type bytes (set in New from opts with defaults applied)
	keyTypeDocWorkspace byte
	keyTypeDocWords     byte
	keyTypeDocMeta      byte
	keyTypeDocPath      byte

	workspacesMu      sync.Mutex
	workspaces        map[int]*Workspace
	deletedWorkspaces map[int]struct{}

	docCountMu sync.RWMutex
	docCount   map[int]int // workspaceId -> document count
}

// New creates a new Store backed by the given kv.Store and queue.Queue.
// idx may be nil in tests or configurations that do not exercise index-linked
// paths; when non-nil it is notified of all document mutations so it stays in
// sync with the kv.Store.
func New(store kv.Store, q queue.Queue, idx *invertedindex.Index, opts Options) (*Store, error) {
	// Apply key-type defaults (zero means "use default").
	if opts.KeyTypeDocWorkspace == 0 {
		opts.KeyTypeDocWorkspace = DefaultKeyTypeDocWorkspace
	}
	if opts.KeyTypeDocWords == 0 {
		opts.KeyTypeDocWords = DefaultKeyTypeDocWords
	}
	if opts.KeyTypeDocMeta == 0 {
		opts.KeyTypeDocMeta = DefaultKeyTypeDocMeta
	}
	if opts.KeyTypeDocPath == 0 {
		opts.KeyTypeDocPath = DefaultKeyTypeDocPath
	}

	s := &Store{
		db:                  store,
		q:                   q,
		idx:                 idx,
		keyTypeDocWorkspace: opts.KeyTypeDocWorkspace,
		keyTypeDocWords:     opts.KeyTypeDocWords,
		keyTypeDocMeta:      opts.KeyTypeDocMeta,
		keyTypeDocPath:      opts.KeyTypeDocPath,
		workspaces:          make(map[int]*Workspace),
		deletedWorkspaces:   make(map[int]struct{}),
		docCount:            make(map[int]int),
	}
	log.Println("[Documents] Initialized")
	return s, nil
}

func (s *Store) isWorkspaceDeleted(workspaceId int) bool {
	s.workspacesMu.Lock()
	defer s.workspacesMu.Unlock()

	_, ok := s.deletedWorkspaces[workspaceId]
	return ok
}

func (s *Store) markWorkspaceDeleted(workspaceId int) {
	s.workspacesMu.Lock()
	defer s.workspacesMu.Unlock()

	s.deletedWorkspaces[workspaceId] = struct{}{}
}

// CloseAndWait flushes any pending work through the queue and releases
// in-memory workspace state. The caller must not use the Store after this
// returns.
func (s *Store) CloseAndWait() {
	s.q.RunTask(&queue.NopeTask{})

	s.workspacesMu.Lock()
	s.workspaces = nil
	s.deletedWorkspaces = nil
	s.workspacesMu.Unlock()

	s.docCountMu.Lock()
	s.docCount = nil
	s.docCountMu.Unlock()

	log.Println("[Documents] Closed")
}

// Create initialises a new workspace in the store, allocating an inverted-index
// table for it and seeding the in-memory document counter.
func (s *Store) Create(workspaceId int, desc string) error {
	inverted, err := s.indexCreateTable(fmt.Sprintf("workspace:%d,desc:%s", workspaceId, desc))
	if err != nil {
		return fmt.Errorf("failed to create inverted index table: %w", err)
	}

	ft := Workspace{
		WorkspaceId: workspaceId,
		InvertedId:  inverted,
		Desc:        desc,
	}

	err = s.db.Put(s.encodeMetaKey(workspaceId), encodeFTMetaValue(ft))
	if err != nil {
		return err
	}

	// Initialize the in-memory document count by scanning the DB once
	prefix := s.encodeDocumentMetaKey(workspaceId, "")
	count := 0
	s.db.Scan(prefix, func(key, value []byte) bool {
		count++
		return true
	})

	s.docCountMu.Lock()
	s.docCount[workspaceId] = count
	s.docCountMu.Unlock()

	return nil
}

// Delete deletes a workspace and all of its documents and keywords.
func (s *Store) Delete(workspaceId int) error {
	return s.q.RunFunc(func() error {
		ft, err := s.GetWorkspace(workspaceId)
		if err != nil {
			return fmt.Errorf("failed to get workspace: %w", err)
		}
		s.markWorkspaceDeleted(workspaceId)

		s.indexDeleteTable(ft.InvertedId)

		batch := s.db.NewBatch(0)
		batch.DeletePrefix(s.encodeDocumentMetaKey(workspaceId, ""))
		batch.DeletePrefix(s.encodeDocumentWordsKey(workspaceId, ""))

		err = batch.Commit()
		if err != nil {
			return err
		}

		// Clean up in-memory document count for this workspace
		s.docCountMu.Lock()
		delete(s.docCount, workspaceId)
		s.docCountMu.Unlock()

		return nil
	})
}

// CountByWorkspace returns the number of documents for a given workspace ID
// using an in-memory counter maintained by document mutations. O(1).
func (s *Store) CountByWorkspace(workspaceId int) int {
	s.docCountMu.RLock()
	defer s.docCountMu.RUnlock()
	return s.docCount[workspaceId]
}

// GetWorkspace retrieves the workspace information for a given workspace ID.
// Results are cached in memory after the first lookup.
func (s *Store) GetWorkspace(workspaceid int) (*Workspace, error) {
	s.workspacesMu.Lock()
	defer s.workspacesMu.Unlock()
	if f, ok := s.workspaces[workspaceid]; ok {
		return f, nil
	}

	meta, err := s.db.Get(s.encodeMetaKey(workspaceid))
	if err != nil {
		return nil, fmt.Errorf("failed to get workspace meta, workspace: %d, error: %w", workspaceid, err)
	}

	f, err := decodeFTMetaValue(meta)
	if err != nil {
		return nil, fmt.Errorf("failed to decode workspace meta, workspace: %d, error: %w", workspaceid, err)
	}

	s.workspaces[workspaceid] = f

	return f, nil
}

// indexCreateTable is the seam that isolates the inverted-index notification
// for workspace creation. A future second index type is an additive change here.
func (s *Store) indexCreateTable(name string) (int, error) {
	if s.idx == nil {
		return 0, nil
	}
	return s.idx.CreateTable(name)
}

// indexDeleteTable is the seam for workspace deletion notification.
func (s *Store) indexDeleteTable(tableId int) {
	if s.idx == nil {
		return
	}
	s.idx.DeleteTable(tableId)
}

// indexDocument is the seam for per-document index update (add/update words).
func (s *Store) indexDocument(tableId int, docId string, newWords, oldWords []string) {
	if s.idx == nil {
		return
	}
	s.idx.Update(tableId, docId, newWords, oldWords)
}
