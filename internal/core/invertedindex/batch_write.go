package invertedindex

import (
	"github.com/codetrek/haystack/internal/core/pebble"
)

const MaxBatchSize = 512

var NewBatch = func(db pebble.DB) pebble.Batch {
	return db.NewBatch(MaxBatchSize)
}
