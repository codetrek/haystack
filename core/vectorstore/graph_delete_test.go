package vectorstore

import (
	"math/rand"
	"path/filepath"
	"testing"
)

// --- Graph delete / upsert / batch / txn coverage (appendix #23) ---------------
//
// The vectorstore build path is insert-only (buildSegmentGraph commits a batch of
// puts and never deletes from the graph; deletes are tombstones on the sealed
// segment). The migrated HNSW delete/upsert/transaction machinery — insert,
// delete, deleteOneLocked, deleteNodeLocked, removeId, runInTxnLocked's abort
// branch, and the stores' DeleteNode/ClearEntryPoint/HighestLiveNodeExcluding/
// GetNodeLevel/txnAbort — is therefore unexercised by the seal pipeline. These
// tests drive it directly over the reference memGraphStore (which owns its
// vectors) and the shipped segGraphStore, validating the copied-and-slimmed graph.

func buildMemIndex(t *testing.T, n, dim int) (*hnswIndex, *memGraphStore, map[int64][]float32) {
	t.Helper()
	rng := rand.New(rand.NewSource(1))
	vecs := make(map[int64][]float32, n)
	gs := newMemGraphStore(Cosine)
	idx := newHNSWIndex(gs, withGraphM(16), withGraphEfConstruction(100),
		withGraphRand(rand.New(rand.NewSource(2))))
	b := idx.newBatch()
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		vecs[int64(i)] = v
		b.put(int64(i), v)
	}
	requireNoError(t, b.commit())
	return idx, gs, vecs
}

func TestHNSW_InsertDeleteUpsert(t *testing.T) {
	idx, gs, vecs := buildMemIndex(t, 120, 16)

	// Single-op insert (wraps one insertOneLocked in a transaction).
	requireNoError(t, idx.insert(500, vecs[3]))
	if id, ok, _ := gs.GetNodeId(500); !ok {
		t.Fatalf("insert(500) did not register a node (got %d)", id)
	}

	// Upsert: re-insert an existing docId → insertOneLocked deletes the old node
	// first (deleteNodeLocked path) then re-inserts.
	requireNoError(t, idx.insert(500, vecs[7]))

	// Delete an interior doc → deleteOneLocked → deleteNodeLocked → removeId on
	// each neighbor list; the doc must vanish from results.
	requireNoError(t, idx.delete(10))
	if _, ok, _ := gs.GetNodeId(10); ok {
		t.Fatal("delete(10) left the docId registered")
	}

	// Deleting an unknown docId is a no-op (deleteOneLocked found=false branch).
	requireNoError(t, idx.delete(999999))

	q := vecs[42]
	got, err := idx.search(q, 5)
	requireNoError(t, err)
	for _, r := range got {
		if r.DocID == 10 {
			t.Fatal("deleted docId 10 leaked into search results")
		}
	}
}

func TestHNSW_DeleteEntryPoint(t *testing.T) {
	idx, gs, _ := buildMemIndex(t, 60, 8)
	ep, _, err := gs.GetEntryPoint()
	requireNoError(t, err)
	doc, ok, err := gs.GetDocId(ep)
	requireNoError(t, err)
	if !ok {
		t.Fatal("entry point has no docId")
	}
	// Deleting the entry-point node forces the store to pick a new highest live
	// node (HighestLiveNodeExcluding) or ClearEntryPoint when none remain.
	requireNoError(t, idx.delete(doc))
	newEp, _, err := gs.GetEntryPoint()
	if err == nil && newEp == ep {
		t.Fatal("entry point unchanged after deleting it")
	}
	// Search still works against the re-pointed graph.
	if _, err := idx.search(make([]float32, 8), 3); err != nil {
		t.Fatalf("search after entry-point delete: %v", err)
	}
}

func TestHNSW_DeleteEveryNodeClearsEntry(t *testing.T) {
	idx, gs, vecs := buildMemIndex(t, 20, 4)
	for doc := range vecs {
		requireNoError(t, idx.delete(doc))
	}
	// With every node deleted, the entry point must be cleared (ClearEntryPoint).
	if _, _, err := gs.GetEntryPoint(); err == nil {
		t.Fatal("entry point not cleared after deleting all nodes")
	}
	got, err := idx.search(vecs[0], 3)
	requireNoError(t, err)
	if got != nil {
		t.Fatalf("emptied index search = %v, want nil", got)
	}
}

func TestGraphBatch_DelDiscardCount(t *testing.T) {
	idx, _, vecs := buildMemIndex(t, 10, 4)
	b := idx.newBatch()
	b.put(1000, vecs[1])
	b.del(2) // delete an existing doc
	b.del(2) // coalesces with the prior op (last-op-wins, still one entry)
	if b.count() != 2 {
		t.Fatalf("batch count = %d, want 2 (put 1000 + del 2 coalesced)", b.count())
	}
	b.discard() // drop the buffer without touching the graph
	if b.count() != 0 {
		t.Fatalf("count after discard = %d, want 0", b.count())
	}
	// Reusable after discard: a commit applies a fresh put + del.
	b.put(2000, vecs[3])
	b.del(5)
	requireNoError(t, b.commit())
}

// faultGraphStore wraps memGraphStore and fails PutNode to drive the
// runInTxnLocked abort branch (txnAbort returning the cause).
type faultGraphStore struct {
	*memGraphStore
	failPut bool
}

func (f *faultGraphStore) PutNode(id uint64, level int, vector []float32, docId int64) error {
	if f.failPut {
		return errInjected
	}
	return f.memGraphStore.PutNode(id, level, vector, docId)
}

func TestHNSW_TxnAbortOnInsertError(t *testing.T) {
	fs := &faultGraphStore{memGraphStore: newMemGraphStore(Cosine), failPut: true}
	idx := newHNSWIndex(fs, withGraphM(16))
	err := idx.insert(1, []float32{1, 0, 0, 0})
	if err == nil {
		t.Fatal("insert with failing PutNode should error (txn abort path)")
	}
	// Batch commit must surface the same abort.
	b := idx.newBatch()
	b.put(2, []float32{0, 1, 0, 0})
	if err := b.commit(); err == nil {
		t.Fatal("batch commit with failing PutNode should error")
	}
}

func TestHNSW_SearchAndInsertValidation(t *testing.T) {
	idx, _, _ := buildMemIndex(t, 8, 4)
	if _, err := idx.search([]float32{1, 0, 0, 0}, 0); err == nil {
		t.Fatal("search with k<=0 should error")
	}
	if _, err := idx.search(nil, 5); err == nil {
		t.Fatal("search with empty query should error")
	}
	if _, err := idx.search([]float32{1, 0}, 5); err == nil {
		t.Fatal("search with wrong-dim query should error")
	}
	if err := idx.insert(1, nil); err == nil {
		t.Fatal("insert with empty vector should error")
	}
	if err := idx.insert(1, []float32{1, 0, 0, 0, 0}); err == nil {
		t.Fatal("insert with wrong-dim vector should error")
	}
}

// TestSegGraphStore_DeletePaths drives the shipped segGraphStore through the
// graph delete machinery (DeleteNode/ClearEntryPoint/HighestLiveNodeExcluding/
// GetNodeLevel/txnAbort), which the insert-only build path never exercises.
func TestSegGraphStore_DeletePaths(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	dim := 8
	n := 80
	rows := make([]struct {
		doc int64
		v   []float32
		pl  []byte
	}, 0, n)
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		rows = append(rows, struct {
			doc int64
			v   []float32
			pl  []byte
		}{int64(3000 + i), v, nil})
	}
	head := buildHeadSeg(Cosine, rows)
	segDir := filepath.Join(t.TempDir(), "seg-1-0")
	requireNoError(t, writeSealedSegment(segDir, head))
	ss, err := openSealedSegment(segDir, Cosine)
	requireNoError(t, err)
	defer ss.close()

	gs := newSegGraphStore(ss)
	idx := newHNSWIndex(gs, withGraphM(16), withGraphEfConstruction(100),
		withGraphRand(rand.New(rand.NewSource(14))))
	b := idx.newBatch()
	ss.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		gs.bindSlot(docID, slot)
		b.put(docID, stored)
	})
	requireNoError(t, b.commit())

	// Delete the entry point so the seg store must re-point via HighestLiveNode.
	ep, _, err := gs.GetEntryPoint()
	requireNoError(t, err)
	epDoc, ok, err := gs.GetDocId(ep)
	requireNoError(t, err)
	if !ok {
		t.Fatal("seg entry point has no docId")
	}
	requireNoError(t, idx.delete(epDoc))
	if id, ok, _ := gs.GetNodeId(epDoc); ok {
		t.Fatalf("deleted seg docId still registered as node %d", id)
	}
	// Search still resolves against the re-pointed seg graph.
	q := make([]float32, dim)
	for d := range q {
		q[d] = rng.Float32()
	}
	got, err := idx.search(q, 5)
	requireNoError(t, err)
	for _, r := range got {
		if r.DocID == epDoc {
			t.Fatal("deleted seg docId leaked into results")
		}
	}
	// GetNodeLevel on a deleted node now errors (slot set to -1).
	if _, err := gs.GetNodeLevel(ep); err == nil {
		t.Fatal("GetNodeLevel on deleted node should error")
	}
}
