package invertedindex

import (
	"context"
	"time"

	"github.com/ai-microsoft/haystack/server/core/pebble"
	"github.com/ai-microsoft/haystack/utils/queue"
)

const Shards = 8

var (
	db             pebble.DB
	mpscQueue      *queue.Mpsc
	closeStorage   context.CancelFunc
	keywordsMerger *KeywordsMerger
)

func Init(database pebble.DB, mpsc *queue.Mpsc) error {
	var ctxCloseDB context.Context
	ctxCloseDB, closeStorage = context.WithCancel(context.Background())
	db = database
	mpscQueue = mpsc

	go func() {
		timer := time.NewTicker(1 * time.Second)
		defer timer.Stop()

		for {
			select {
			case <-ctxCloseDB.Done():
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
	closeStorage()
	keywordsMerger.Shutdown()
	keywordsMerger.Wait()

	// Wait for all tasks to finish
	mpscQueue.RunTask(&flushPendingWritesTask{
		closing: true,
	})
}
