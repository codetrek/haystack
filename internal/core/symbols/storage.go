package symbols

import (
	"github.com/codetrek/haystack/internal/core/pebble"
	"github.com/codetrek/haystack/internal/utils/queue"
)

const Shards = 8

var (
	db   pebble.DB
	mpsc *queue.Mpsc
)

func Init(database pebble.DB, q *queue.Mpsc) error {
	db = database
	mpsc = q
	return nil
}

func CloseAndWait() {
	mpsc.RunTask(&queue.NopeTask{})

	db = nil
	mpsc = nil
}
