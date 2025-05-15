package fulltext

import (
	"context"
	"log"
	"time"

	"github.com/ai-microsoft/haystack/server/core/pebble"
)

const Shards = 8

var (
	db             pebble.DB
	writeQueue     chan WriteTask
	closeStorage   context.CancelFunc
	keywordsMerger *KeywordsMerger
)

type WriteTask interface {
	Run()
}

type closeWriteQueue struct {
	done chan struct{}
}

func (c *closeWriteQueue) Run() {
	close(writeQueue)

	// flush pending writes
	t := &flushPendingWritesTask{
		closing: true,
	}
	t.Run()

	c.done <- struct{}{}
}

func (c *closeWriteQueue) Wait() {
	<-c.done
}

func Init(database pebble.DB) error {
	var ctxCloseDB context.Context
	ctxCloseDB, closeStorage = context.WithCancel(context.Background())
	db = database

	writeQueue = make(chan WriteTask)

	go func() {
		for {
			task, ok := <-writeQueue
			if !ok {
				log.Println("[Fulltext] Write queue closed")
				break
			}
			task.Run()
		}
	}()

	go func() {
		timer := time.NewTicker(1 * time.Second)
		defer timer.Stop()

		for {
			select {
			case <-ctxCloseDB.Done():
				return
			case <-timer.C:
				writeQueue <- &flushPendingWritesTask{
					closing: false,
				}
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

	closeWriteQueue := &closeWriteQueue{
		done: make(chan struct{}),
	}
	writeQueue <- closeWriteQueue
	closeWriteQueue.Wait()
}
