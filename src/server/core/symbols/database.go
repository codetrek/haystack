package symbols

import (
	"fmt"
	"time"

	"github.com/ai-microsoft/haystack/server/core/invertedindex"
)

type SymbolUniversalTable struct {
	WorkspaceId int        `json:"workspace_id"`
	InvertedId  int        `json:"inverted_id"`
	Desc        string     `json:"desc"`
	CreateAt    *time.Time `json:"create_at"`
}

func Create(workspaceId int, desc string) error {

	tableMetaKeys := [][]byte{
		EncodeSymbolTableKey(workspaceId),
		EncodeSymbolWordsTableKey(workspaceId),
	}

	for _, key := range tableMetaKeys {
		inverted, err := invertedindex.CreateTable(fmt.Sprintf("workspace:%d,desc:%s", workspaceId, desc))
		if err != nil {
			return fmt.Errorf("failed to create inverted index table: %w", err)
		}

		s := SymbolUniversalTable{
			WorkspaceId: workspaceId,
			InvertedId:  inverted,
			Desc:        desc,
		}

		err = db.Put(key, EncodeSymbolTableValue(s))
		if err != nil {
			return fmt.Errorf("failed to create symbol table meta, key: %s, error: %w", key, err)
		}
	}
	return nil
}

// Delete deletes a symbols and all of its documents and keywords
func Delete(workspaceId int) error {
	return mpsc.RunFunc(func() error {
		tableMetaKeys := [][]byte{
			EncodeSymbolTableKey(workspaceId),
			EncodeSymbolWordsTableKey(workspaceId),
		}

		for _, key := range tableMetaKeys {
			ft, err := getTable(key)
			if err != nil {
				return err
			}

			invertedindex.DeleteTable(ft.InvertedId)

			batch := db.NewBatch(0)
			batch.DeletePrefix(EncodeDocFunctionsKey(workspaceId, ""))

			err = batch.Commit()
			if err != nil {
				return fmt.Errorf("failed to delete symbol table, key: %s, error: %w", key, err)
			}
		}
		return nil
	})
}

func getTable(key []byte) (*SymbolUniversalTable, error) {
	meta, err := db.Get(key)
	if err != nil {
		return nil, fmt.Errorf("failed to get symbol table meta, key: %s, error: %w", key, err)
	}

	ft, err := DecodeSymbolTableValue(meta)
	if err != nil {
		return nil, fmt.Errorf("failed to decode symbol table meta, key: %s, error: %w", key, err)
	}

	return &ft, nil
}

func GetSymbolTable(workspaceid int) (*SymbolUniversalTable, error) {
	return getTable(EncodeSymbolTableKey(workspaceid))
}

func GetSymbolWordsTable(workspaceid int) (*SymbolUniversalTable, error) {
	return getTable(EncodeSymbolWordsTableKey(workspaceid))
}
