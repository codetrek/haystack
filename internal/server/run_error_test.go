package server

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/codetrek/haystack/internal/conf"
	"github.com/codetrek/haystack/searchcore/invertedindex"
	"github.com/codetrek/haystack/searchcore/kv"
	"github.com/codetrek/haystack/searchcore/queue"
)

var errFake = errors.New("fake init error")

// noopInitII is a no-op invertedindexInit replacement: returns a nil Index with no error.
func noopInitII(_ kv.Store, _ *queue.Mpsc) (*invertedindex.Index, error) { return nil, nil }

// noopInitDoc is a no-op documentsInit / symbolsInit replacement.
func noopInitDoc(_ kv.Store, _ *queue.Mpsc, _ *invertedindex.Index) error { return nil }

// noopInitDBOnly is a no-op Init replacement for kv.Store only.
func noopInitDBOnly(_ kv.Store) error { return nil }

// saveAndMockInits saves the four Init function variables, replaces them all
// with no-ops, and returns a restore function.
func saveAndMockInits() func() {
	origII := invertedindexInit
	origDoc := documentsInit
	origWS := workspaceInit
	origSym := symbolsInit

	invertedindexInit = noopInitII
	documentsInit = noopInitDoc
	workspaceInit = noopInitDBOnly
	symbolsInit = noopInitDoc

	return func() {
		invertedindexInit = origII
		documentsInit = origDoc
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

	invertedindexInit = func(_ kv.Store, _ *queue.Mpsc) (*invertedindex.Index, error) {
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

	documentsInit = func(_ kv.Store, _ *queue.Mpsc, _ *invertedindex.Index) error {
		return errFake
	}

	setupRunEnv(t)

	err := run()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error initializing storage")
}

func TestRun_WorkspaceInitError(t *testing.T) {
	restore := saveAndMockInits()
	defer restore()

	workspaceInit = func(_ kv.Store) error {
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

	symbolsInit = func(_ kv.Store, _ *queue.Mpsc, _ *invertedindex.Index) error {
		return errFake
	}

	setupRunEnv(t)

	err := run()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error initializing symbols")
}
