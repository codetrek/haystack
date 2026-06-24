package symbols

import (
	"github.com/codetrek/haystack/core/invertedindex"
	"github.com/codetrek/haystack/core/kv"
	"github.com/codetrek/haystack/core/queue"
)

const Shards = 8

var (
	db      kv.Store
	mpsc    *queue.Mpsc
	idxInst invertedindex.Indexer
)

func Init(database kv.Store, q *queue.Mpsc, idx invertedindex.Indexer) error {
	db = database
	mpsc = q
	idxInst = idx
	return nil
}

func CloseAndWait() {
	mpsc.RunTask(&queue.NopeTask{})

	db = nil
	mpsc = nil
	idxInst = nil
}
