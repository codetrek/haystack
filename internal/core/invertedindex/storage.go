package invertedindex

import (
	"context"
	"time"

	"github.com/codetrek/haystack/searchcore/kv"
	"github.com/codetrek/haystack/searchcore/queue"
)

var (
	db             kv.Store
	mpscQueue      *queue.Mpsc
	cancelFlush    context.CancelFunc
	keywordsMerger *KeywordsMerger
)

func Init(database kv.Store, mpsc *queue.Mpsc) error {
	var flushDB context.Context
	flushDB, cancelFlush = context.WithCancel(context.Background())
	db = database
	mpscQueue = mpsc

	go func() {
		timer := time.NewTicker(FlushTicker)
		defer timer.Stop()

		for {
			select {
			case <-flushDB.Done():
				return
			case <-timer.C:
				mpscQueue.Add(&flushPendingWritesTask{
					closing: false,
				})
			}
		}
	}()

	keywordsMerger = &KeywordsMerger{}
	keywordsMerger.Start()

	return nil
}

func CloseAndWait() {
	cancelFlush()
	keywordsMerger.Shutdown()
	keywordsMerger.Wait()

	// Wait for all tasks to finish
	mpscQueue.RunTask(&flushPendingWritesTask{
		closing: true,
	})
}
