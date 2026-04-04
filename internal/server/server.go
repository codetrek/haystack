package server

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"

	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/internal/core/documents"
	"github.com/codetrek/haystack/internal/core/idtable"
	"github.com/codetrek/haystack/internal/core/invertedindex"
	"github.com/codetrek/haystack/internal/core/pebble"
	"github.com/codetrek/haystack/internal/core/storage"
	"github.com/codetrek/haystack/internal/core/symbols"
	"github.com/codetrek/haystack/internal/core/workspace"
	"github.com/codetrek/haystack/internal/server/httpapi"
	"github.com/codetrek/haystack/internal/server/indexer"
	"github.com/codetrek/haystack/internal/server/searcher"
	"github.com/codetrek/haystack/internal/shared/running"
	"github.com/codetrek/haystack/internal/utils/queue"
)

// Function variables for Init calls, enabling test overrides.
var (
	invertedindexInit = func(db pebble.DB, mpsc *queue.Mpsc) error { return invertedindex.Init(db, mpsc) }
	documentsInit     = func(db pebble.DB, mpsc *queue.Mpsc) error { return documents.Init(db, mpsc) }
	workspaceInit     = func(db pebble.DB) error { return workspace.Init(db) }
	symbolsInit       = func(db pebble.DB, mpsc *queue.Mpsc) error { return symbols.Init(db, mpsc) }
)

func Run() {
	cleanup, err := running.CheckAndLockServer()
	if err != nil {
		log.Println("[Server] Error locking and running as server:", err)
		return
	}
	defer cleanup()

	initLog()

	log.Println(strings.Repeat("=", 64))
	log.Println("[Server] Starting haystack server...")

	if err := run(); err != nil {
		log.Println("[Server] ", err)
	}
}

func run() error {
	wg := &sync.WaitGroup{}
	running.InitShutdown(wg)

	db, err := storage.Open(filepath.Join(conf.Get().Global.DataPath, "data"), conf.Get().Server.CacheSize)
	if err != nil {
		running.Shutdown()
		return fmt.Errorf("error initializing data storage: %w", err)
	}

	indexdb, err := storage.Open(filepath.Join(conf.Get().Global.DataPath, "index"), conf.Get().Server.CacheSize)
	if err != nil {
		running.Shutdown()
		return fmt.Errorf("error initializing index storage: %w", err)
	}

	mpsc := queue.NewMpsc("DBQueue")
	mpsc.Start()

	if err := idtable.Init(db); err != nil {
		running.Shutdown()
		return fmt.Errorf("error initializing id table: %w", err)
	}

	if err := invertedindexInit(indexdb, mpsc); err != nil {
		running.Shutdown()
		return fmt.Errorf("error initializing inverted index: %w", err)
	}

	if err := documentsInit(db, mpsc); err != nil {
		running.Shutdown()
		return fmt.Errorf("error initializing storage: %w", err)
	}

	if err := workspaceInit(db); err != nil {
		running.Shutdown()
		return fmt.Errorf("error initializing workspace: %w", err)
	}

	if err := symbolsInit(db, mpsc); err != nil {
		running.Shutdown()
		return fmt.Errorf("error initializing symbols: %w", err)
	}

	indexer.Run(wg)
	searcher.Run(wg)

	if conf.Get().ForTest.Path != "" {
		indexer.SyncIfNeeded(conf.Get().ForTest.Path)
	}

	tcpAddr := ""
	if conf.Get().Global.Port > 0 {
		tcpAddr = fmt.Sprintf("127.0.0.1:%d", conf.Get().Global.Port)
	}
	httpapi.StartServer(
		wg,
		tcpAddr,
		conf.Get().Global.SocketPath,
	)

	wg.Wait()
	documents.CloseAndWait()
	invertedindex.CloseAndWait()
	symbols.CloseAndWait()
	mpsc.Stop()

	idtable.Close()

	// DB could be closed safely now!
	log.Println("[Server] Closing storage...")
	db.Close()
	indexdb.Close()
	log.Println("[Server] Storage closed")

	log.Println("[Server] Haystack server stopped")
	return nil
}
