package symbols

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/codetrek/haystack/core/idtable"
	"github.com/stretchr/testify/assert"
)

// docIDString encodes i as the canonical 8-byte big-endian docid string the
// inverted index keys postings by (matches idtable.EncodeId / GetId). Using the
// real encoding means idtable.DecodeId(df.ID) yields a deterministic int64 we can
// look up in the index after the async apply lands.
func docIDString(i int) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(i))
	return string(b[:])
}

// flushQueue waits for every previously-enqueued worker task (including the async
// index applies AddFunctions/DeleteDocument enqueue OUTSIDE the worker) to run: a
// no-op RunFunc returns only after all earlier tasks on the single worker complete.
func flushQueue(t *testing.T) {
	t.Helper()
	if err := mpsc.RunFunc(func() error { return nil }); err != nil {
		t.Fatalf("flush queue: %v", err)
	}
}

// waitForDocPosting polls GetDocs(tableId, key) until docid is present, or the
// deadline elapses. The live backend is the pebble-backed invertedindex, whose
// GetDocs reads only FLUSHED rows: draining the worker (flushQueue) applies the
// async Update to the in-memory pending buffer, but the periodic flush ticker
// (set to 20ms via setupTestEnv's fast-flush options) must still move it to
// pebble before the posting becomes visible. Returns true once seen.
func waitForDocPosting(t *testing.T, tableId int, key string, docid int64) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := idxInst.GetDocs(tableId, key).DocIds[docid]; ok {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// waitForDocRetracted polls GetDocs(tableId, key) until docid is ABSENT, or the
// deadline elapses. Used to confirm a forward-map retraction (Update with the key
// dropped / empty keyword set) has flushed through to pebble. Returns true once
// the posting is gone.
func waitForDocRetracted(t *testing.T, tableId int, key string, docid int64) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := idxInst.GetDocs(tableId, key).DocIds[docid]; !ok {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// TestAddFunctions_NoDeadlockWithSharedQueueIndexer is the symbols counterpart to
// core/documents/save_no_deadlock_test.go. It guards the symbols↔inverted-index write
// seam through the REAL shared-queue wiring (setupTestEnv builds the pebble-backed
// invertedindex + NewIndexerAdapter on the same env.Mpsc that drives the symbols
// package — the same construction the production server performs).
//
// The hazard: AddFunctions runs its kv writes inside mpsc.RunFunc (occupying the
// single worker). Each doc previously called idxInst.Update TWICE (symbol +
// symbol-words tables) from inside that task; the adapter's Update enqueues onto the
// SAME shared queue (q.AddFunc = a blocking channel send). With a batch larger than
// the 100-deep channel buffer, the worker would block sending to a queue only it can
// drain → permanent deadlock. A 200-doc batch issues ~400 such sends, far past the
// buffer. The fix hoists the index notifications OUTSIDE the worker task; this test
// makes the regression observable (the watchdog turns a hang into a failure) and
// confirms every doc's postings actually land.
func TestAddFunctions_NoDeadlockWithSharedQueueIndexer(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	// 200 docs >> the 100-deep channel buffer — the pre-fix deadlock fires only once
	// the buffer is overrun mid-task. Each doc carries a UNIQUE function name so we can
	// assert its posting lands afterward.
	const n = 200
	docs := make([]DocFunction, 0, n)
	names := make([]string, n)
	for i := 0; i < n; i++ {
		name := "fnDeadlockProbe" + docIDString(i) // unique per doc
		names[i] = name
		docs = append(docs, DocFunction{
			ID:        docIDString(i + 1),
			RelPath:   "f.go",
			Functions: []Function{{Name: name, Line: i + 1}},
		})
	}

	done := make(chan error, 1)
	go func() { done <- AddFunctions(1, docs) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("AddFunctions returned error: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("symbols.AddFunctions deadlocked: the index notification was enqueued from inside the worker task and overran the channel buffer")
	}

	// Flush the queue so the async index applies have run, then confirm every doc's
	// function name posting landed in the symbol table.
	flushQueue(t)

	st, err := GetSymbolTable(1)
	if !assert.NoError(t, err) {
		return
	}
	for i := 0; i < n; i++ {
		wantDocid := idtable.DecodeId(docIDString(i + 1))
		if !waitForDocPosting(t, st.InvertedId, names[i], wantDocid) {
			res := env.idx.GetDocs(st.InvertedId, names[i])
			t.Fatalf("doc %d function %q not found in symbol index (got %d docids)", i+1, names[i], len(res.DocIds))
		}
	}
}

// TestAddFunctions_RetractsDroppedFunction proves the forward-map retraction the old
// words/symbol tables could NOT do: re-AddFunctions the SAME doc id with a different
// function name and the OLD name's posting must be GONE while the new one is present.
// The inverted index owns the forward map keyed by (InvertedId, docid) and diffs the
// CURRENT keyword set against the stored one, so passing only the new names retracts
// the dropped ones. This verifies the §4/§8 contract on the symbols keyspace and
// covers the words table too (the tokenized words of the dropped name vanish).
func TestAddFunctions_RetractsDroppedFunction(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	docID := docIDString(99)
	docid := idtable.DecodeId(docID)

	// First index: function "oldFunction".
	if err := AddFunctions(1, []DocFunction{{
		ID:        docID,
		RelPath:   "main.go",
		Functions: []Function{{Name: "oldFunction", Line: 1}},
	}}); !assert.NoError(t, err) {
		return
	}
	flushQueue(t)

	st, err := GetSymbolTable(1)
	if !assert.NoError(t, err) {
		return
	}
	swt, err := GetSymbolWordsTable(1)
	if !assert.NoError(t, err) {
		return
	}

	// The old name must be present in the symbol table, and its tokenized word
	// "oldfunction" (TokenizeForIndex lower-cases and keeps the whole identifier as a
	// token) must be present in the words table after the first index.
	if !waitForDocPosting(t, st.InvertedId, "oldFunction", docid) {
		t.Fatal("oldFunction posting missing after first AddFunctions")
	}
	if !waitForDocPosting(t, swt.InvertedId, "oldfunction", docid) {
		t.Fatal("word 'oldfunction' posting missing in words table after first AddFunctions")
	}

	// Re-index the SAME doc id with a DIFFERENT function name. The store diffs against
	// its forward map and must retract the dropped name/word.
	if err := AddFunctions(1, []DocFunction{{
		ID:        docID,
		RelPath:   "main.go",
		Functions: []Function{{Name: "newFunction", Line: 1}},
	}}); !assert.NoError(t, err) {
		return
	}
	flushQueue(t)

	// New name present (symbol table) and its word "newfunction" present (words table).
	if !waitForDocPosting(t, st.InvertedId, "newFunction", docid) {
		t.Fatal("newFunction posting missing after re-AddFunctions")
	}
	if !waitForDocPosting(t, swt.InvertedId, "newfunction", docid) {
		t.Fatal("word 'newfunction' posting missing in words table after re-AddFunctions")
	}

	// Old name retracted (the forward-map diff dropped it).
	if !waitForDocRetracted(t, st.InvertedId, "oldFunction", docid) {
		t.Fatal("oldFunction posting NOT retracted after re-AddFunctions: forward-map diff failed")
	}
	if !waitForDocRetracted(t, swt.InvertedId, "oldfunction", docid) {
		t.Fatal("word 'oldfunction' posting NOT retracted in words table after re-AddFunctions")
	}
}

// TestDeleteDocument_NoDeadlockAndRetracts guards the single-doc delete path: its
// index removal (Update with empty keywords) must also be hoisted OUT of the worker
// task. We index a doc, then delete it, and assert (1) it completes without hanging
// and (2) the symbol posting is retracted. Benign at n=1 today (one send) but the
// same latent contract violation as AddFunctions, so the hoist is asserted here too.
func TestDeleteDocument_NoDeadlockAndRetracts(t *testing.T) {
	env := setupTestEnv(t)
	defer env.teardown()

	mustCreateWorkspace(t, 1)

	docID := docIDString(7)
	docid := idtable.DecodeId(docID)

	if err := AddFunctions(1, []DocFunction{{
		ID:        docID,
		RelPath:   "main.go",
		Functions: []Function{{Name: "toBeDeleted", Line: 1}},
	}}); !assert.NoError(t, err) {
		return
	}
	flushQueue(t)

	st, err := GetSymbolTable(1)
	if !assert.NoError(t, err) {
		return
	}
	if !waitForDocPosting(t, st.InvertedId, "toBeDeleted", docid) {
		t.Fatal("toBeDeleted posting missing after AddFunctions")
	}

	done := make(chan error, 1)
	go func() { done <- DeleteDocument(1, docID) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("DeleteDocument returned error: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("symbols.DeleteDocument deadlocked: the index removal was enqueued from inside the worker task")
	}
	flushQueue(t)

	// The symbol posting must be retracted (empty keyword set ⇒ delete via forward map).
	if !waitForDocRetracted(t, st.InvertedId, "toBeDeleted", docid) {
		t.Fatal("toBeDeleted posting NOT retracted after DeleteDocument")
	}
}
