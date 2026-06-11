package invertedindex

import (
	"time"
)

type TableInfo struct {
	Id        int       `json:"id"`
	CreatedAt time.Time `json:"created_time"`

	Description string `json:"description"`
}

func (idx *Index) CreateTable(description string) (int, error) {
	tableId, err := idx.db.GetIncrementalId(encodeNextTableIdKey())
	if err != nil {
		return -1, err
	}

	// Create a new table in the database
	info := TableInfo{
		Id:          tableId,
		CreatedAt:   time.Now(),
		Description: description,
	}

	return tableId, idx.db.Put(encodeTableKey(tableId), encodeTableValue(info))
}

// DeleteTable deletes a table from the database and all of its inverted index data.
// This function is not thread-safe and should be called in database mpsc queue.
func (idx *Index) DeleteTable(tableId int) error {
	idx.clearPendingWrites(tableId)

	batch := idx.db.NewBatch(0)
	batch.DeletePrefix(encodeInvertedSearchKey(tableId, ""))
	batch.Delete(encodeTableKey(tableId))
	return batch.Commit()
}
