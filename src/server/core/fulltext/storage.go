package fulltext

import (
	"github.com/ai-microsoft/haystack/server/core/pebble"
	"github.com/ai-microsoft/haystack/utils/queue"
)

const Shards = 8

var (
	db   pebble.DB
	mpsc *queue.Mpsc
	ft   map[int]*Fulltext
)

func Init(database pebble.DB, q *queue.Mpsc) error {
	db = database
	mpsc = q
	ft = make(map[int]*Fulltext)
	return nil
}

func CloseAndWait() {
	mpsc.RunTask(&queue.NopeTask{})

	db = nil
	mpsc = nil
	ft = nil
}
