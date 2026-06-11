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

type Workspace struct {
	WorkspaceId int        `json:"workspace_id"`
	InvertedId  int        `json:"inverted_id"`
	Desc        string     `json:"desc"`
	CreateAt    *time.Time `json:"create_at"`
}

// Options holds tunables for Store. Zero value selects production defaults.
type Options struct{}

// Store is the instance-based documents store.
type Store struct {
	db   kv.Store
	q    queue.Queue
	idx  *invertedindex.Index
	opts Options

	workspacesMu      sync.Mutex
	workspaces        map[int]*Workspace
	deletedWorkspaces map[int]struct{}

	docCountMu sync.RWMutex
	docCount   map[int]int // workspaceId -> document count
}

// New creates a new Store. idx may be nil in tests that do not exercise
// index-linked paths.
func New(store kv.Store, q queue.Queue, idx *invertedindex.Index, opts Options) (*Store, error) {
	s := &Store{
		db:                store,
		q:                 q,
		idx:               idx,
		opts:              opts,
		workspaces:        make(map[int]*Workspace),
		deletedWorkspaces: make(map[int]struct{}),
		docCount:          make(map[int]int),
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

	err = s.db.Put(EncodeMetaKey(workspaceId), EncodeFTMetaValue(ft))
	if err != nil {
		return err
	}

	// Initialize the in-memory document count by scanning the DB once
	prefix := EncodeDocumentMetaKey(workspaceId, "")
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

// Delete deletes a workspace and all of its documents and keywords
func (s *Store) Delete(workspaceId int) error {
	return s.q.RunFunc(func() error {
		ft, err := s.GetWorkspace(workspaceId)
		if err != nil {
			return fmt.Errorf("failed to get workspace: %w", err)
		}
		s.markWorkspaceDeleted(workspaceId)

		s.indexDeleteTable(ft.InvertedId)

		batch := s.db.NewBatch(0)
		batch.DeletePrefix(EncodeDocumentMetaKey(workspaceId, ""))
		batch.DeletePrefix(EncodeDocumentWordsKey(workspaceId, ""))

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

// GetWorkspace retrieves the workspace information for a given workspace ID
func (s *Store) GetWorkspace(workspaceid int) (*Workspace, error) {
	s.workspacesMu.Lock()
	defer s.workspacesMu.Unlock()
	if f, ok := s.workspaces[workspaceid]; ok {
		return f, nil
	}

	meta, err := s.db.Get(EncodeMetaKey(workspaceid))
	if err != nil {
		return nil, fmt.Errorf("failed to get workspace meta, workspace: %d, error: %w", workspaceid, err)
	}

	f, err := DecodeFTMetaValue(meta)
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
