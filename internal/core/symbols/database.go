package symbols

import (
	"fmt"
	"time"

	"github.com/codetrek/haystack/internal/conf"
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
		inverted, err := idxInst.CreateTable(fmt.Sprintf("workspace:%d,desc:%s", workspaceId, desc))
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

// Delete deletes a symbols and all of its documents and keywords.
//
// idxInst.DeleteTable runs OUTSIDE the mpsc.RunFunc task, exactly like
// documents.Store.Delete hoists indexDeleteTable: the live IndexerAdapter.DeleteTable
// does its own q.RunFunc on the SHARED worker, so calling it from inside symbols' own
// mpsc.RunFunc would nest RunFunc-in-RunFunc and deadlock the single worker. The
// meta lookup (getTable) and the db doc-functions cleanup stay serialized on the
// queue; only the index table-drop is hoisted out.
func Delete(workspaceId int) error {
	if !conf.Get().Symbols.EnableFeature {
		return nil
	}

	tableMetaKeys := [][]byte{
		EncodeSymbolTableKey(workspaceId),
		EncodeSymbolWordsTableKey(workspaceId),
	}

	for _, key := range tableMetaKeys {
		ft, err := getTable(key)
		if err != nil {
			return err
		}

		idxInst.DeleteTable(ft.InvertedId)
	}

	return mpsc.RunFunc(func() error {
		batch := db.NewBatch(0)
		batch.DeletePrefix(EncodeDocFunctionsKey(workspaceId, ""))

		if err := batch.Commit(); err != nil {
			return fmt.Errorf("failed to delete symbol doc-functions, workspace: %d, error: %w", workspaceId, err)
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
