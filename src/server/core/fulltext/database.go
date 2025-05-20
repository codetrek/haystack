package fulltext

import (
	"fmt"
	"time"

	"github.com/ai-microsoft/haystack/server/core/invertedindex"
)

type Fulltext struct {
	WorkspaceId int        `json:"workspace_id"`
	InvertedId  int        `json:"inverted_id"`
	Desc        string     `json:"desc"`
	CreateAt    *time.Time `json:"create_at"`
}

func Create(workspaceId int, desc string) error {
	inverted, err := invertedindex.CreateTable(fmt.Sprintf("workspace:%d,desc:%s", workspaceId, desc))
	if err != nil {
		return fmt.Errorf("failed to create inverted index table: %w", err)
	}

	ft := Fulltext{
		WorkspaceId: workspaceId,
		InvertedId:  inverted,
		Desc:        desc,
	}

	// Create a new collection in the database
	// This is a placeholder function and should be implemented
	return db.Put(EncodeFTMetaKey(workspaceId), EncodeFTMetaValue(ft))
}

// Delete deletes a fulltext and all of its documents and keywords
func Delete(workspaceId int) error {
	return mpsc.RunFunc(func() error {
		ft, err := GetFT(workspaceId)
		if err != nil {
			return fmt.Errorf("failed to get fulltext: %w", err)
		}

		invertedindex.DeleteTable(ft.InvertedId)

		batch := db.NewBatch(0)
		batch.DeletePrefix(EncodeDocumentMetaKey(workspaceId, ""))
		batch.DeletePrefix(EncodeDocumentWordsKey(workspaceId, ""))

		return batch.Commit()
	})
}

// GetFT retrieves the fulltext information for a given workspace ID
func GetFT(workspaceid int) (*Fulltext, error) {
	meta, err := db.Get(EncodeFTMetaKey(workspaceid))
	if err != nil {
		return nil, fmt.Errorf("failed to get fulltext meta, workspace: %d, error: %w", workspaceid, err)
	}

	ft, err := DecodeFTMetaValue(meta)
	if err != nil {
		return nil, fmt.Errorf("failed to decode fulltext meta, workspace: %d, error: %w", workspaceid, err)
	}

	return &ft, nil
}
