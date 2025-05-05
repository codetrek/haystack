package server

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/server/core/fulltext"
	"github.com/ai-microsoft/haystack/server/core/storage"
	"github.com/ai-microsoft/haystack/server/core/workspace"
	"github.com/ai-microsoft/haystack/server/indexer"
	"github.com/ai-microsoft/haystack/server/searcher"
	"github.com/ai-microsoft/haystack/server/server"
	"github.com/ai-microsoft/haystack/shared/running"
)

func Run() {
	cleanup, err := running.CheckAndLockServer()
	if err != nil {
		log.Fatal("[Server] Error locking and running as server:", err)
		return
	}
	defer cleanup()

	initLog()

	log.Println(strings.Repeat("=", 64))
	log.Println("[Server] Starting haystack server...")

	wg := &sync.WaitGroup{}
	running.InitShutdown(wg)

	db, err := storage.Open(conf.Get().Global.DataPath)
	if err != nil {
		log.Fatal("[Server] Error initializing storage:", err)
		running.Shutdown()
		return
	}

	if err := fulltext.Init(db); err != nil {
		log.Fatal("[Server] Error initializing storage:", err)
		running.Shutdown()
		return
	}

	if err := workspace.Init(); err != nil {
		log.Fatal("[Server] Error initializing workspace:", err)
		running.Shutdown()
		return
	}

	indexer.Run(wg)
	searcher.Run(wg)

	if conf.Get().ForTest.Path != "" {
		indexer.SyncIfNeeded(conf.Get().ForTest.Path)
	}

	server.StartServer(wg, fmt.Sprintf("127.0.0.1:%d", conf.Get().Global.Port))

	wg.Wait()
	fulltext.CloseAndWait()

	// DB could be closed safely now!
	log.Println("[Server] Closing storage...")
	db.Close()
	log.Println("[Server] Storage closed")

	log.Println("[Server] Haystack server stopped")
}
