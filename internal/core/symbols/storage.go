package symbols

import (
	"github.com/codetrek/haystack/searchcore/invertedindex"
	"github.com/codetrek/haystack/searchcore/kv"
	"github.com/codetrek/haystack/searchcore/queue"
)

const Shards = 8

var (
	db      kv.Store
	mpsc    *queue.Mpsc
	idxInst *invertedindex.Index
)

func Init(database kv.Store, q *queue.Mpsc, idx *invertedindex.Index) error {
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
