package server

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/codetrek/haystack/conf"
	"github.com/codetrek/haystack/server/core/idtable"
	"github.com/codetrek/haystack/server/core/pebble"
	"github.com/codetrek/haystack/utils/queue"
)

var errFake = errors.New("fake init error")

// noopInitDB is a no-op Init replacement for pebble.DB + *queue.Mpsc.
func noopInitDB(_ pebble.DB, _ *queue.Mpsc) error { return nil }

// noopInitDBOnly is a no-op Init replacement for pebble.DB only.
func noopInitDBOnly(_ pebble.DB) error { return nil }

// saveAndMockInits saves the four Init function variables, replaces them all
// with no-ops, and returns a restore function.
func saveAndMockInits() func() {
	origII := invertedindexInit
	origDoc := documentsInit
	origWS := workspaceInit
	origSym := symbolsInit

	invertedindexInit = noopInitDB
	documentsInit = noopInitDB
	workspaceInit = noopInitDBOnly
	symbolsInit = noopInitDB

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
	idtable.Close() // ensure clean state so run() can init idtable
}

func TestRun_InvertedIndexInitError(t *testing.T) {
	restore := saveAndMockInits()
	defer restore()

	invertedindexInit = func(_ pebble.DB, _ *queue.Mpsc) error {
		return errFake
	}

	setupRunEnv(t)

	err := run()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error initializing inverted index")
	idtable.Close()
}

func TestRun_DocumentsInitError(t *testing.T) {
	restore := saveAndMockInits()
	defer restore()

	documentsInit = func(_ pebble.DB, _ *queue.Mpsc) error {
		return errFake
	}

	setupRunEnv(t)

	err := run()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error initializing storage")
	idtable.Close()
}

func TestRun_WorkspaceInitError(t *testing.T) {
	restore := saveAndMockInits()
	defer restore()

	workspaceInit = func(_ pebble.DB) error {
		return errFake
	}

	setupRunEnv(t)

	err := run()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error initializing workspace")
	idtable.Close()
}

func TestRun_SymbolsInitError(t *testing.T) {
	restore := saveAndMockInits()
	defer restore()

	symbolsInit = func(_ pebble.DB, _ *queue.Mpsc) error {
		return errFake
	}

	setupRunEnv(t)

	err := run()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error initializing symbols")
	idtable.Close()
}
