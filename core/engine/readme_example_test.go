package engine_test

// TestReadmeExample mirrors the Usage snippet in core/README.md end-to-end:
// open a temp pebblekv store, build the stack, create a collection, save a
// document, compile and run a query. Keeping this test in sync with the README
// ensures the example compiles and the described API actually works.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/codetrek/haystack/core/collection"
	"github.com/codetrek/haystack/core/documents"
	"github.com/codetrek/haystack/core/engine"
	"github.com/codetrek/haystack/core/idtable"
	"github.com/codetrek/haystack/core/invertedindex"
	"github.com/codetrek/haystack/core/kv/pebblekv"
	"github.com/codetrek/haystack/core/queue"
)

func TestReadmeExample(t *testing.T) {
	// 1. Open a store.
	tmpDir, err := os.MkdirTemp("", "core-readme-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := pebblekv.Open(filepath.Join(tmpDir, "data"), 16<<20)
	if err != nil {
		t.Fatalf("pebblekv.Open: %v", err)
	}

	// 2. A shared async write queue.
	q := queue.NewMpsc("readme-writes")
	q.Start()

	// Allocate a stable 8-byte document id from idtable (IDs must be exactly
	// 8 bytes so the inverted-index codec can decode them correctly).
	alloc, err := idtable.New(store, idtable.Options{})
	if err != nil {
		t.Fatalf("idtable.New: %v", err)
	}
	docID, err := alloc.GetId([]byte("main.go")) // path → stable 8-byte id
	if err != nil {
		t.Fatalf("alloc.GetId: %v", err)
	}
	alloc.Close()

	// 3. Compose the stack (one shared inverted index instance).
	idx, err := invertedindex.New(store, q, invertedindex.Options{})
	if err != nil {
		t.Fatalf("invertedindex.New: %v", err)
	}

	docs, err := documents.New(store, q, idx, documents.Options{})
	if err != nil {
		t.Fatalf("documents.New: %v", err)
	}

	cat, err := collection.New(store, docs, collection.Options{})
	if err != nil {
		t.Fatalf("collection.New: %v", err)
	}

	// 4. Create a collection and add documents (Save auto-indexes).
	col, err := cat.Create("my-project")
	if err != nil {
		t.Fatalf("cat.Create: %v", err)
	}

	if err := col.Save([]*documents.Document{
		{ID: docID, RelPath: "main.go", Words: []string{"hello", "world", "main"}},
	}); err != nil {
		t.Fatalf("col.Save: %v", err)
	}

	// Flush pending index writes to disk before querying.
	// Save enqueues work that accumulates in an in-memory write buffer; the
	// index flushes to disk periodically (or on CloseAndWait). We call
	// CloseAndWait here to guarantee the flush has completed; the kv.Store
	// remains open and Search still reads from it correctly.
	idx.CloseAndWait()

	// 5. Query its content.
	eng := engine.New(idx, docs, col.ID(), engine.Options{
		MaxWildcardLength:  24,
		MaxKeywordDistance: 32,
	})
	if err := eng.Compile("hello world", false /* caseSensitive */); err != nil {
		t.Fatalf("eng.Compile: %v", err)
	}
	result, err := eng.CollectDocuments() // candidate doc ids from the inverted index
	if err != nil {
		t.Fatalf("eng.CollectDocuments: %v", err)
	}

	if _, found := result.DocIds[docID]; !found {
		t.Errorf("expected docID %q in results; got %v", docID, result.DocIds)
	}

	// Shutdown order: docs → q → store. (idx already shut down above.)
	docs.CloseAndWait()
	q.Stop()
	store.Close()
}
