package documents

import (
	"github.com/codetrek/haystack/searchcore/kv"
)

const MaxBatchSize = 512

var NewBatch = func(db kv.Store) kv.Batch {
	return db.NewBatch(MaxBatchSize)
}
