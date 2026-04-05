package documents

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/codetrek/haystack/internal/core/invertedindex"
	"github.com/codetrek/haystack/internal/core/pebble"
	"github.com/codetrek/haystack/internal/utils/queue"
)

var (
	db   pebble.DB
	mpsc *queue.Mpsc

	mutex             sync.Mutex
	Workspaces        map[int]*Workspace
	deletedWorkspaces map[int]struct{}
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

func Init(database pebble.DB, q *queue.Mpsc) error {
	db = database
	mpsc = q
	Workspaces = make(map[int]*Workspace)
	deletedWorkspaces = make(map[int]struct{})

	log.Println("[Documents] Initialized")
	return nil
}

func CloseAndWait() {
	mpsc.RunTask(&queue.NopeTask{})

	db = nil
	mpsc = nil
	Workspaces = nil
	deletedWorkspaces = nil

	log.Println("[Documents] Closed")
}

func Create(workspaceId int, desc string) error {
	inverted, err := invertedindex.CreateTable(fmt.Sprintf("workspace:%d,desc:%s", workspaceId, desc))
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
	return db.Put(EncodeMetaKey(workspaceId), EncodeFTMetaValue(ft))
}

// Delete deletes a workspace and all of its documents and keywords
func Delete(workspaceId int) error {
	return mpsc.RunFunc(func() error {
		ft, err := GetWorkspace(workspaceId)
		if err != nil {
			return fmt.Errorf("failed to get workspace: %w", err)
		}
		markWorkspaceDeleted(workspaceId)

		invertedindex.DeleteTable(ft.InvertedId)

		batch := db.NewBatch(0)
		batch.DeletePrefix(EncodeDocumentMetaKey(workspaceId, ""))
		batch.DeletePrefix(EncodeDocumentWordsKey(workspaceId, ""))

		return batch.Commit()
	})
}

// CountByWorkspace counts all document meta keys for a given workspace ID
// by scanning the DB with the workspace prefix.
func CountByWorkspace(workspaceId int) int {
	prefix := EncodeDocumentMetaKey(workspaceId, "")
	count := 0
	db.Scan(prefix, func(key, value []byte) bool {
		count++
		return true
	})
	return count
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
