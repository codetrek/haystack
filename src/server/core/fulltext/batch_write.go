package fulltext

import (
	"github.com/ai-microsoft/haystack/server/core/pebble"
)

const MaxBatchSize = 512

var NewBatch = func(db pebble.DB) pebble.Batch {
	return db.NewBatch(MaxBatchSize)
}
