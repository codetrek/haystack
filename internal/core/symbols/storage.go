package symbols

import (
	"github.com/codetrek/haystack/packages/core/invertedindex"
	"github.com/codetrek/haystack/packages/core/kv"
	"github.com/codetrek/haystack/packages/core/queue"
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
