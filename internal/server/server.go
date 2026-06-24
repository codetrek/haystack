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
	"github.com/codetrek/haystack/core/invertedstore"
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
	invertedindexInit = func(path string, mpsc *queue.Mpsc) (*invertedstore.Store, error) {
		// AutoMerge ON in production so the live segment count stays bounded (design §6/§12
		// P8); the rest of Options{} fills in the §3/§7 production config via withDefaults.
		return invertedstore.Open(path, mpsc, invertedstore.Options{AutoMerge: true})
	}
	documentsNew = func(db kv.Store, mpsc *queue.Mpsc, idx invertedindex.Indexer) (*documents.Store, error) {
		return documents.New(db, mpsc, idx, documents.Options{})
	}
	// workspaceInit receives the fully-constructed Catalog so the workspace
	// package no longer needs its own kv.Store reference.
	workspaceInit = func(cat *collection.Catalog) error { return workspace.Init(cat) }
	symbolsInit   = func(db kv.Store, mpsc *queue.Mpsc, idx invertedindex.Indexer) error {
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

	mpsc := queue.NewMpsc("DBQueue")
	mpsc.Start()

	// idtable is a standalone bbolt-backed component (separate from the `data`
	// pebble store); the legacy 28/29-prefix KV idtable predates it and is no
	// longer migrated.
	idtablePath := filepath.Join(conf.Get().Global.DataPath, "idtable.db")
	idAlloc, err := idtable.Open(idtablePath, idtable.Options{})
	if err != nil {
		running.Shutdown()
		return fmt.Errorf("error initializing id table: %w", err)
	}
	indexer.SetIdAllocator(idAlloc)

	// The pebble inverted-index store is gone (replaced by the segment-based
	// invertedstore), so storage.Open no longer runs over the `index` root to
	// reclaim its stale version dirs. Run the cleanup explicitly so the dead
	// pebble index version dirs (incl. the just-superseded "1.5") under the index
	// root are removed before the invertedstore opens its own versioned subdir.
	indexRoot := filepath.Join(conf.Get().Global.DataPath, "index")
	storage.Cleanup(indexRoot)
	idx, err := invertedindexInit(filepath.Join(indexRoot, storage.StorageVersion, "invertedstore"), mpsc)
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

	// db is closed by the deferred Close() registered right after storage.Open
	// above (it also covers the early-return error paths). The index is the
	// self-managed invertedstore (no pebble handle), closed by idx.CloseAndWait above.
	log.Println("[Server] Haystack server stopped")
	return nil
}
