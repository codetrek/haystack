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

// newInvertedIndex is the constructor seam; tests override it to capture the
// Options the production wiring passes. Its type is invertedindex.New's:
// func(kv.Store, queue.Queue, invertedindex.Options) (*invertedindex.Index, error)
// — note the 2nd param is the queue.Queue INTERFACE (a test override must use
// queue.Queue, not *queue.Mpsc; see server_maxpending_test.go).
var newInvertedIndex = invertedindex.New

// Function variables for Init calls, enabling test overrides.
var (
	invertedindexInit = func(db kv.Store, mpsc *queue.Mpsc) (*invertedindex.Index, error) {
		// Cap the pending-write buffer at the measured-good bound so build-phase
		// peak RSS is bounded and predictable (~0.66 GiB vs an unbounded, noisy
		// ~1.3 GiB at scale) — a deliberate memory-over-build-speed default for the
		// deployment, at a measured ~+11% build cost. See RecommendedMaxPendingPostings.
		return newInvertedIndex(db, mpsc, invertedindex.Options{
			MaxPendingPostings: invertedindex.RecommendedMaxPendingPostings,
		})
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

	// idtable is a thin docid allocator OVER the shared `data` pebble store,
	// namespaced by its default 28/29 key prefixes — it coexists with
	// documents/invertedindex in the one store (no separate file).
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
