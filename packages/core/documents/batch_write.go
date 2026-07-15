package documents

import (
	"github.com/codetrek/haystack/core/kv"
)

// MaxBatchSize is the default maximum number of operations in a write batch.
const MaxBatchSize = 512

var newBatch = func(db kv.Store) kv.Batch {
	return db.NewBatch(MaxBatchSize)
}
