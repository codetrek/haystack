package server

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"

	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/internal/core/documents"
	"github.com/codetrek/haystack/internal/core/invertedindex"
	"github.com/codetrek/haystack/internal/core/storage"
	"github.com/codetrek/haystack/internal/core/symbols"
	"github.com/codetrek/haystack/internal/core/workspace"
	"github.com/codetrek/haystack/internal/server/httpapi"
	"github.com/codetrek/haystack/internal/server/indexer"
	"github.com/codetrek/haystack/internal/server/searcher"
	"github.com/codetrek/haystack/internal/shared/running"
	"github.com/codetrek/haystack/searchcore/idtable"
	"github.com/codetrek/haystack/searchcore/kv"
	"github.com/codetrek/haystack/searchcore/queue"
)

// Function variables for Init calls, enabling test overrides.
var (
	invertedindexInit = func(db kv.Store, mpsc *queue.Mpsc) (*invertedindex.Index, error) {
		// Zero-value Options selects production defaults inside New.
		return invertedindex.New(db, mpsc, invertedindex.Options{})
	}
	documentsInit = func(db kv.Store, mpsc *queue.Mpsc, idx *invertedindex.Index) error {
		return documents.Init(db, mpsc, idx)
	}
	workspaceInit = func(db kv.Store) error { return workspace.Init(db) }
	symbolsInit   = func(db kv.Store, mpsc *queue.Mpsc, idx *invertedindex.Index) error {
		return symbols.Init(db, mpsc, idx)
	}
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

	idAlloc, err := idtable.New(db, idtable.Options{})
	if err != nil {
		running.Shutdown()
		return fmt.Errorf("error initializing id table: %w", err)
	}
	indexer.SetIdAllocator(idAlloc)

	idx, err := invertedindexInit(indexdb, mpsc)
	if err != nil {
		running.Shutdown()
		return fmt.Errorf("error initializing inverted index: %w", err)
	}

	if err := documentsInit(db, mpsc, idx); err != nil {
		running.Shutdown()
		return fmt.Errorf("error initializing storage: %w", err)
	}

	if err := workspaceInit(db); err != nil {
		running.Shutdown()
		return fmt.Errorf("error initializing workspace: %w", err)
	}

	// Wire up the documents count function for workspace to derive TotalFiles.
	workspace.CountByWorkspaceFunc = documents.CountByWorkspace

	if err := symbolsInit(db, mpsc, idx); err != nil {
		running.Shutdown()
		return fmt.Errorf("error initializing symbols: %w", err)
	}

	indexer.Run(wg)
	searcher.Run(wg, idx)

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
	idx.CloseAndWait()
	symbols.CloseAndWait()
	mpsc.Stop()

	idAlloc.Close()

	// DB could be closed safely now!
	log.Println("[Server] Closing storage...")
	db.Close()
	indexdb.Close()
	log.Println("[Server] Storage closed")

	log.Println("[Server] Haystack server stopped")
	return nil
}
