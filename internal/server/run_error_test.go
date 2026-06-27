package server

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/codetrek/haystack/core/collection"
	"github.com/codetrek/haystack/core/documents"
	"github.com/codetrek/haystack/core/invertedindex"
	"github.com/codetrek/haystack/core/kv"
	"github.com/codetrek/haystack/core/queue"
	"github.com/codetrek/haystack/internal/conf"
)

var errFake = errors.New("fake init error")

// noopInitII is a no-op invertedindexInit replacement: returns a nil Indexer with no error.
func noopInitII(_ kv.Store, _ *queue.Mpsc) (invertedindex.Indexer, error) { return nil, nil }

// noopDocNew is a no-op documentsNew replacement.
func noopDocNew(_ kv.Store, _ *queue.Mpsc, _ invertedindex.Indexer) (*documents.Store, error) {
	return nil, nil
}

// noopInitCat is a no-op Init replacement for *collection.Catalog.
func noopInitCat(_ *collection.Catalog) error { return nil }

// saveAndMockInits saves the four Init function variables, replaces them all
// with no-ops, and returns a restore function.
func saveAndMockInits() func() {
	origII := invertedindexInit
	origDoc := documentsNew
	origWS := workspaceInit
	origSym := symbolsInit

	invertedindexInit = noopInitII
	documentsNew = noopDocNew
	workspaceInit = noopInitCat
	symbolsInit = func(_ kv.Store, _ *queue.Mpsc, _ invertedindex.Indexer) error { return nil }

	return func() {
		invertedindexInit = origII
		documentsNew = origDoc
		workspaceInit = origWS
		symbolsInit = origSym
	}
}

// setupRunEnv configures a minimal environment for calling run().
func setupRunEnv(t *testing.T) {
	t.Helper()
	tempDir := t.TempDir()
	conf.Get().Global.DataPath = tempDir
	conf.Get().Server.CacheSize = 8 * 1024 * 1024
}

func TestRun_InvertedIndexInitError(t *testing.T) {
	restore := saveAndMockInits()
	defer restore()

	invertedindexInit = func(_ kv.Store, _ *queue.Mpsc) (invertedindex.Indexer, error) {
		return nil, errFake
	}

	setupRunEnv(t)

	err := run()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error initializing inverted index")
}

func TestRun_DocumentsInitError(t *testing.T) {
	restore := saveAndMockInits()
	defer restore()

	documentsNew = func(_ kv.Store, _ *queue.Mpsc, _ invertedindex.Indexer) (*documents.Store, error) {
		return nil, errFake
	}

	setupRunEnv(t)

	err := run()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error initializing documents store")
}

func TestRun_WorkspaceInitError(t *testing.T) {
	restore := saveAndMockInits()
	defer restore()

	workspaceInit = func(_ *collection.Catalog) error {
		return errFake
	}

	setupRunEnv(t)

	err := run()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error initializing workspace")
}

func TestRun_SymbolsInitError(t *testing.T) {
	restore := saveAndMockInits()
	defer restore()

	symbolsInit = func(_ kv.Store, _ *queue.Mpsc, _ invertedindex.Indexer) error {
		return errFake
	}

	setupRunEnv(t)

	err := run()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error initializing symbols")
}
