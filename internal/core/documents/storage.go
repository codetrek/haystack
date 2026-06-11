package documents

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/codetrek/haystack/internal/core/invertedindex"
	"github.com/codetrek/haystack/searchcore/kv"
	"github.com/codetrek/haystack/searchcore/queue"
)

var (
	db      kv.Store
	mpsc    *queue.Mpsc
	idxInst *invertedindex.Index

	mutex             sync.Mutex
	Workspaces        map[int]*Workspace
	deletedWorkspaces map[int]struct{}

	docCountMu sync.RWMutex
	docCount   map[int]int // workspaceId -> document count
)

type Workspace struct {
	WorkspaceId int        `json:"workspace_id"`
	InvertedId  int        `json:"inverted_id"`
	Desc        string     `json:"desc"`
	CreateAt    *time.Time `json:"create_at"`
}

func isWorkspaceDeleted(workspaceId int) bool {
	mutex.Lock()
	defer mutex.Unlock()

	_, ok := deletedWorkspaces[workspaceId]
	return ok
}

func markWorkspaceDeleted(workspaceId int) {
	mutex.Lock()
	defer mutex.Unlock()

	deletedWorkspaces[workspaceId] = struct{}{}
}

func Init(database kv.Store, q *queue.Mpsc, idx *invertedindex.Index) error {
	db = database
	mpsc = q
	idxInst = idx
	Workspaces = make(map[int]*Workspace)
	deletedWorkspaces = make(map[int]struct{})
	docCount = make(map[int]int)

	log.Println("[Documents] Initialized")
	return nil
}

func CloseAndWait() {
	mpsc.RunTask(&queue.NopeTask{})

	db = nil
	mpsc = nil
	idxInst = nil
	Workspaces = nil
	deletedWorkspaces = nil

	docCountMu.Lock()
	docCount = nil
	docCountMu.Unlock()

	log.Println("[Documents] Closed")
}

func Create(workspaceId int, desc string) error {
	inverted, err := idxInst.CreateTable(fmt.Sprintf("workspace:%d,desc:%s", workspaceId, desc))
	if err != nil {
		return fmt.Errorf("failed to create inverted index table: %w", err)
	}

	ft := Workspace{
		WorkspaceId: workspaceId,
		InvertedId:  inverted,
		Desc:        desc,
	}

	// Create a new collection in the database
	// This is a placeholder function and should be implemented
	err = db.Put(EncodeMetaKey(workspaceId), EncodeFTMetaValue(ft))
	if err != nil {
		return err
	}

	// Initialize the in-memory document count by scanning the DB once
	prefix := EncodeDocumentMetaKey(workspaceId, "")
	count := 0
	db.Scan(prefix, func(key, value []byte) bool {
		count++
		return true
	})

	docCountMu.Lock()
	docCount[workspaceId] = count
	docCountMu.Unlock()

	return nil
}

// Delete deletes a workspace and all of its documents and keywords
func Delete(workspaceId int) error {
	return mpsc.RunFunc(func() error {
		ft, err := GetWorkspace(workspaceId)
		if err != nil {
			return fmt.Errorf("failed to get workspace: %w", err)
		}
		markWorkspaceDeleted(workspaceId)

		idxInst.DeleteTable(ft.InvertedId)

		batch := db.NewBatch(0)
		batch.DeletePrefix(EncodeDocumentMetaKey(workspaceId, ""))
		batch.DeletePrefix(EncodeDocumentWordsKey(workspaceId, ""))

		err = batch.Commit()
		if err != nil {
			return err
		}

		// Clean up in-memory document count for this workspace
		docCountMu.Lock()
		delete(docCount, workspaceId)
		docCountMu.Unlock()

		return nil
	})
}

// CountByWorkspace returns the number of documents for a given workspace ID
// using an in-memory counter maintained by document mutations. O(1).
func CountByWorkspace(workspaceId int) int {
	docCountMu.RLock()
	defer docCountMu.RUnlock()
	return docCount[workspaceId]
}

// GetWorkspace retrieves the workspace information for a given workspace ID
func GetWorkspace(workspaceid int) (*Workspace, error) {
	mutex.Lock()
	defer mutex.Unlock()
	if f, ok := Workspaces[workspaceid]; ok {
		return f, nil
	}

	meta, err := db.Get(EncodeMetaKey(workspaceid))
	if err != nil {
		return nil, fmt.Errorf("failed to get workspace meta, workspace: %d, error: %w", workspaceid, err)
	}

	f, err := DecodeFTMetaValue(meta)
	if err != nil {
		return nil, fmt.Errorf("failed to decode workspace meta, workspace: %d, error: %w", workspaceid, err)
	}

	Workspaces[workspaceid] = f

	return f, nil
}
