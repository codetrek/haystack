package prompts

import (
	"log"

	"github.com/ai-microsoft/haystack/server/core/pebble"
	"github.com/ai-microsoft/haystack/utils/queue"
)

const MaxBatchSize = 512

var (
	db   pebble.DB
	mpsc *queue.Mpsc
)

func Init(database pebble.DB, q *queue.Mpsc) error {
	db = database
	mpsc = q

	log.Println("[Prompts] Initialized")
	return nil
}

func CloseAndWait() {
	mpsc.RunTask(&queue.NopeTask{})

	db = nil
	mpsc = nil
}

var NewBatch = func(db pebble.DB) pebble.Batch {
	return db.NewBatch(MaxBatchSize)
}