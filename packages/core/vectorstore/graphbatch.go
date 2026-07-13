package vectorstore

import "fmt"

// --- graph batch (buffers a segment's HNSW build ops) ---

// graphBatchOpKind distinguishes buffered Put from Delete.
type graphBatchOpKind int

const (
	graphOpPut graphBatchOpKind = iota
	graphOpDelete
)

type graphBatchOp struct {
	kind   graphBatchOpKind
	docId  int64
	vector []float32 // populated for graphOpPut (already copied); nil for delete
}

// graphBatch is the unit of mutation. Created via hnswIndex.newBatch and owned by
// a single goroutine. put/del buffer operations in memory (coalesced per docId,
// last-op-wins); commit applies them to the graph atomically inside one store
// transaction; discard drops the buffer without touching the graph. It is not
// safe to call put, del, commit, or discard concurrently from multiple goroutines.
type graphBatch struct {
	idx  *hnswIndex
	ops  []graphBatchOp
	seen map[int64]int // docId -> index in ops (coalescing)
}

// newBatch returns an empty graphBatch bound to this index.
func (h *hnswIndex) newBatch() *graphBatch {
	return &graphBatch{idx: h, seen: make(map[int64]int)}
}

// put buffers an insert/replace of docId with vector. The vector is copied, so
// the caller may reuse its slice. A later put/del of the same docId in this
// batch supersedes this one.
func (b *graphBatch) put(docId int64, vector []float32) {
	cp := make([]float32, len(vector))
	copy(cp, vector)
	b.set(graphBatchOp{kind: graphOpPut, docId: docId, vector: cp})
}

// del buffers a removal of docId. Coalesces with any prior op for docId.
func (b *graphBatch) del(docId int64) {
	b.set(graphBatchOp{kind: graphOpDelete, docId: docId})
}

// set inserts or overwrites the op for op.docId, preserving first-seen order.
func (b *graphBatch) set(op graphBatchOp) {
	if i, ok := b.seen[op.docId]; ok {
		b.ops[i] = op
		return
	}
	b.seen[op.docId] = len(b.ops)
	b.ops = append(b.ops, op)
}

// count returns the number of pending (coalesced) operations.
func (b *graphBatch) count() int { return len(b.ops) }

// discard drops all buffered operations. The graph is untouched (nothing was
// applied), so no rollback is needed. The batch is reusable afterward.
func (b *graphBatch) discard() {
	b.ops = b.ops[:0]
	b.seen = make(map[int64]int)
}

// commit applies all buffered operations to the graph atomically inside one
// store transaction, then empties the batch (reusable). An empty batch is a
// no-op (no transaction opened). On error the store is faulted (for persistent
// stores) and the batch is emptied; the error is returned.
func (b *graphBatch) commit() error {
	if len(b.ops) == 0 {
		return nil
	}
	h := b.idx
	// Validate every put before opening a transaction, so a malformed vector
	// returns a clean error without faulting the store or applying a partial
	// batch. The batch is left intact for the caller to fix and retry.
	//
	// A fresh store has Dim()==0, so validateVector cannot catch a dimension
	// mismatch on its own; pin the dimension across the batch (from the store if
	// it has one, else the first put) and reject any op that disagrees.
	pinnedDim := h.store.Dim()
	for _, op := range b.ops {
		if op.kind != graphOpPut {
			continue
		}
		if err := h.validateVector(op.vector); err != nil {
			return err
		}
		if pinnedDim == 0 {
			pinnedDim = len(op.vector)
		} else if len(op.vector) != pinnedDim {
			return fmt.Errorf("vectorstore: batch has mixed vector dimensions: got %d, want %d", len(op.vector), pinnedDim)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	err := h.runInTxnLocked(func() error {
		for _, op := range b.ops {
			switch op.kind {
			case graphOpPut:
				if e := h.insertOneLocked(op.docId, op.vector); e != nil {
					return e
				}
			case graphOpDelete:
				if e := h.deleteOneLocked(op.docId); e != nil {
					return e
				}
			}
		}
		return nil
	})

	b.ops = b.ops[:0]
	b.seen = make(map[int64]int)
	return err
}
