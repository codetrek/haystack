package invertedindex

import (
	"time"
)

type TableInfo struct {
	Id        int       `json:"id"`
	CreatedAt time.Time `json:"created_time"`

	Description string `json:"description"`
}

func CreateTable(description string) (int, error) {
	tableId, err := db.GetIncrementalId(encodeNextTableIdKey())
	if err != nil {
		return -1, err
	}

	// Create a new table in the database
	// This is a placeholder function and should be implemented
	info := TableInfo{
		Id:          tableId, // This should be replaced with the actual ID
		CreatedAt:   time.Now(),
		Description: description,
	}

	return tableId, db.Put(encodeTableKey(tableId), encodeTableValue(info))
}

// DeleteTable deletes a table from the database
// and all of its inverted index data
// This function is not thread-safe and should be called in database mpsc queue
func DeleteTable(tableId int) error {
	clearPendingWrites(tableId)

	batch := db.NewBatch(0)
	batch.DeletePrefix(encodeInvertedSearchKey(tableId, ""))
	batch.Delete(encodeTableKey(tableId))
	return batch.Commit()
}
