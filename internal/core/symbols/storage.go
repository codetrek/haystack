package symbols

import (
	"github.com/codetrek/haystack/searchcore/kv"
	"github.com/codetrek/haystack/searchcore/queue"
)

const Shards = 8

var (
	db   kv.Store
	mpsc *queue.Mpsc
)

func Init(database kv.Store, q *queue.Mpsc) error {
	db = database
	mpsc = q
	return nil
}

func CloseAndWait() {
	mpsc.RunTask(&queue.NopeTask{})

	db = nil
	mpsc = nil
}
