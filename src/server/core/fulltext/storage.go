package fulltext

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/server/core/pebble"
)

const StorageVersion = "1.0"
const Shards = 8

var (
	db             pebble.DB
	closeOnce      *sync.Once
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

func Init() error {
	var ctxCloseDB context.Context
	ctxCloseDB, closeStorage = context.WithCancel(context.Background())
	closeOnce = &sync.Once{}

	homePath := conf.Get().Global.DataPath
	storagePath := filepath.Join(homePath, "data")

	log.Printf("Init storage path: %s", storagePath)

	dbPath := filepath.Join(storagePath, StorageVersion)
	versionPath := filepath.Join(storagePath, "version")

	os.MkdirAll(storagePath, 0755)
	os.WriteFile(versionPath, []byte(StorageVersion), 0644)

	var err error
	db, err = pebble.OpenDB(dbPath)
	if err != nil {
		return err
	}

	writeQueue = make(chan WriteTask)

	go func() {
		for {
			task, ok := <-writeQueue
			if !ok {
				log.Println("Database write queue closed")
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
	closeOnce.Do(func() {
		closeStorage()
		keywordsMerger.Shutdown()
		keywordsMerger.Wait()

		closeWriteQueue := &closeWriteQueue{
			done: make(chan struct{}),
		}
		writeQueue <- closeWriteQueue
		closeWriteQueue.Wait()

		log.Println("Closing storage...")
		defer log.Println("Storage closed")

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		for {
			select {
			case <-ctx.Done():
				log.Println("Storage close timeout, force quiting...")
				return
			case <-time.After(1 * time.Second):
			}

			db.Close()
			if db.IsClosed() {
				break
			}
			log.Println("Waiting for storage to be closed...")
		}
	})
}
