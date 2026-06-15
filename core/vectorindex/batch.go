package vectorindex

// batchOpKind distinguishes buffered Put from Delete.
type batchOpKind int

const (
	opPut batchOpKind = iota
	opDelete
)

type batchOp struct {
	kind   batchOpKind
	docId  int64
	vector []float32 // populated for opPut (already copied); nil for opDelete
}

// Batch is the unit of mutation. Created via HNSWIndex.NewBatch and owned by a
// single goroutine. Put/Delete buffer operations in memory (coalesced per
// docId, last-op-wins); Commit applies them to the graph atomically inside one
// store transaction; Discard drops the buffer without touching the graph.
// It is not safe to call Put, Delete, Commit, or Discard concurrently from
// multiple goroutines.
type Batch struct {
	idx  *HNSWIndex
	ops  []batchOp
	seen map[int64]int // docId -> index in ops (coalescing)
}

// NewBatch returns an empty Batch bound to this index.
func (h *HNSWIndex) NewBatch() *Batch {
	return &Batch{idx: h, seen: make(map[int64]int)}
}

// Put buffers an insert/replace of docId with vector. The vector is copied, so
// the caller may reuse its slice. A later Put/Delete of the same docId in this
// batch supersedes this one.
func (b *Batch) Put(docId int64, vector []float32) {
	cp := make([]float32, len(vector))
	copy(cp, vector)
	b.set(batchOp{kind: opPut, docId: docId, vector: cp})
}

// Delete buffers a removal of docId. Coalesces with any prior op for docId.
func (b *Batch) Delete(docId int64) {
	b.set(batchOp{kind: opDelete, docId: docId})
}

// set inserts or overwrites the op for op.docId, preserving first-seen order.
func (b *Batch) set(op batchOp) {
	if i, ok := b.seen[op.docId]; ok {
		b.ops[i] = op
		return
	}
	b.seen[op.docId] = len(b.ops)
	b.ops = append(b.ops, op)
}

// Len returns the number of pending (coalesced) operations.
func (b *Batch) Len() int { return len(b.ops) }

// Discard drops all buffered operations. The graph is untouched (nothing was
// applied), so no rollback is needed. The batch is reusable afterward.
func (b *Batch) Discard() {
	b.ops = b.ops[:0]
	b.seen = make(map[int64]int)
}

// Commit applies all buffered operations to the graph atomically inside one
// store transaction, then empties the batch (reusable). An empty batch is a
// no-op (no transaction opened). On error the store is faulted (for persistent
// stores) and the batch is emptied; the error is returned.
func (b *Batch) Commit() error {
	if len(b.ops) == 0 {
		return nil
	}
	h := b.idx
	h.mu.Lock()
	defer h.mu.Unlock()

	err := h.runInTxnLocked(func() error {
		for _, op := range b.ops {
			switch op.kind {
			case opPut:
				if e := h.insertOneLocked(op.docId, op.vector); e != nil {
					return e
				}
			case opDelete:
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
