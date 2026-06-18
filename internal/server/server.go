package server

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"

	"github.com/codetrek/haystack/core/collection"
	"github.com/codetrek/haystack/core/documents"
	"github.com/codetrek/haystack/core/idtable"
	"github.com/codetrek/haystack/core/invertedindex"
	"github.com/codetrek/haystack/core/kv"
	"github.com/codetrek/haystack/core/queue"
	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/internal/core/storage"
	"github.com/codetrek/haystack/internal/core/symbols"
	"github.com/codetrek/haystack/internal/core/workspace"
	"github.com/codetrek/haystack/internal/server/httpapi"
	"github.com/codetrek/haystack/internal/server/indexer"
	"github.com/codetrek/haystack/internal/server/searcher"
	"github.com/codetrek/haystack/internal/shared/running"
)

// Function variables for Init calls, enabling test overrides.
var (
	invertedindexInit = func(db kv.Store, mpsc *queue.Mpsc) (*invertedindex.Index, error) {
		// Zero-value Options selects production defaults inside New.
		return invertedindex.New(db, mpsc, invertedindex.Options{})
	}
	documentsNew = func(db kv.Store, mpsc *queue.Mpsc, idx *invertedindex.Index) (*documents.Store, error) {
		return documents.New(db, mpsc, idx, documents.Options{})
	}
	// workspaceInit receives the fully-constructed Catalog so the workspace
	// package no longer needs its own kv.Store reference.
	workspaceInit = func(cat *collection.Catalog) error { return workspace.Init(cat) }
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
	defer closeLog() // release the log file handle when the server stops

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
	// Close on EVERY return path (incl. the early-return startup-error paths below),
	// not just the happy path: a failed startup otherwise leaks the open pebble
	// handle, and on Windows an open handle blocks the data dir from being removed
	// (e.g. t.TempDir cleanup in the run() error-path tests). Runs after the stores
	// that use db are torn down (deferred LIFO, after the manual teardown below).
	defer db.Close()

	indexdb, err := storage.Open(filepath.Join(conf.Get().Global.DataPath, "index"), conf.Get().Server.CacheSize)
	if err != nil {
		running.Shutdown()
		return fmt.Errorf("error initializing index storage: %w", err)
	}
	defer indexdb.Close()

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

	st, err := documentsNew(db, mpsc, idx)
	if err != nil {
		running.Shutdown()
		return fmt.Errorf("error initializing documents store: %w", err)
	}

	// Wire the documents store into dependent packages.
	indexer.SetDocStore(st)
	workspace.SetDocStore(st)

	// Migrate any legacy workspace records BEFORE constructing the Catalog so
	// that collection.New sees only the new-format JSON. A migration failure may
	// leave the store partially migrated, so abort startup rather than run
	// collection.New against inconsistent data.
	if err := workspace.MigrateLegacyRecords(db, collection.Options{}); err != nil {
		running.Shutdown()
		return fmt.Errorf("error migrating legacy workspace records: %w", err)
	}

	cat, err := collection.New(db, st, collection.Options{})
	if err != nil {
		running.Shutdown()
		return fmt.Errorf("error initializing collection catalog: %w", err)
	}

	if err := workspaceInit(cat); err != nil {
		running.Shutdown()
		return fmt.Errorf("error initializing workspace: %w", err)
	}

	if err := symbolsInit(db, mpsc, idx); err != nil {
		running.Shutdown()
		return fmt.Errorf("error initializing symbols: %w", err)
	}

	indexer.Run(wg)
	searcher.Run(wg, idx, st)

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
	st.CloseAndWait()
	idx.CloseAndWait()
	symbols.CloseAndWait()
	mpsc.Stop()

	idAlloc.Close()

	// db and indexdb are closed by the deferred Close() calls registered right after
	// each storage.Open above (they also cover the early-return error paths).
	log.Println("[Server] Haystack server stopped")
	return nil
}
