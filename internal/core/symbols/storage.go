package symbols

import (
	"github.com/codetrek/haystack/internal/utils/queue"
	"github.com/codetrek/haystack/searchcore/kv"
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
