package invertedindex

import (
	"github.com/codetrek/haystack/packages/core/kv"
)

const MaxBatchSize = 512

var newBatch = func(db kv.Store) kv.Batch {
	return db.NewBatch(MaxBatchSize)
}
