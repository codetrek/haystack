package vectorstore

import (
	bolt "go.etcd.io/bbolt"
)

// batchOpKind distinguishes a buffered Put from a Delete.
type batchOpKind int

const (
	batchPut batchOpKind = iota
	batchDelete
)

type batchOp struct {
	kind    batchOpKind
	id      string
	v       []float32 // batchPut only (already copied)
	payload Payload   // batchPut only (already cloned)
}

// Batch buffers Put/Delete operations and applies them in ONE control-store
// write-txn on Commit — a single fsync amortized over every record, the
// write-throughput lever (a single Put is one commit/fsync each, the worst case).
// It mirrors vectorindex.Batch. Created via Store.NewBatch and owned by a single
// goroutine: Put, Delete, Commit, and Discard are NOT safe to call concurrently.
// Operations coalesce per id (last-op-wins): a Put then Delete of the same id in
// one batch commits only the Delete.
//
// A Batch should hold no more than the head's capacity (maxSegSize); a much larger
// batch seals into one over-target segment. The head never gets a graph, so a Put
// here is O(1) regardless — the HNSW build happens in the background at seal.
type Batch struct {
	s    *Store
	ops  []batchOp
	seen map[string]int // id → index in ops (coalescing)
}

// NewBatch returns an empty Batch bound to this store.
func (s *Store) NewBatch() *Batch {
	return &Batch{s: s, seen: make(map[string]int)}
}

// Put buffers an insert/replace of id with vector v and payload. v and payload are
// copied, so the caller may reuse them. A later Put/Delete of the same id in this
// batch supersedes this one.
func (b *Batch) Put(id string, v []float32, payload Payload) {
	b.set(batchOp{kind: batchPut, id: id, v: append([]float32(nil), v...), payload: payload.clone()})
}

// Delete buffers a removal of id. Coalesces with any prior op for id.
func (b *Batch) Delete(id string) {
	b.set(batchOp{kind: batchDelete, id: id})
}

// set inserts or overwrites the op for op.id, preserving first-seen order.
func (b *Batch) set(op batchOp) {
	if i, ok := b.seen[op.id]; ok {
		b.ops[i] = op
		return
	}
	b.seen[op.id] = len(b.ops)
	b.ops = append(b.ops, op)
}

// Len returns the number of pending (coalesced) operations.
func (b *Batch) Len() int { return len(b.ops) }

// Discard drops all buffered operations. Nothing was applied, so no rollback is
// needed; the batch is reusable.
func (b *Batch) Discard() {
	b.ops = b.ops[:0]
	b.seen = make(map[string]int)
}

// batchItem is a resolved op, prepared before the txn opens.
type batchItem struct {
	kind    batchOpKind
	id      string
	docID   int64
	stored  []float32 // put
	norm    float32   // put
	plBytes []byte    // put (durable)
	pl      Payload   // put (in-memory)
	// A put overwriting a doc currently in a sealed segment, OR a delete of a
	// sealed doc: tombstone (sealSeg, sealSlot) and drop its docseg routing.
	hasSealed bool
	sealSeg   segID
	sealSlot  int
	// A delete of a doc currently in the head: tombstone headSlot in-memory.
	inHead   bool
	headSlot int
}

// Commit applies every buffered operation in ONE control-store write-txn (durable
// on return), then empties the batch (reusable). An empty batch is a no-op. A
// validation error returns before any apply and leaves the batch intact for retry;
// a commit error rolls the txn back fully and leaves in-memory state unchanged.
func (b *Batch) Commit() error {
	if len(b.ops) == 0 {
		return nil
	}
	s := b.s
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Validate every Put BEFORE opening the txn — a malformed vector returns a
	//    clean error with nothing applied. Pin the dimension across the batch (from
	//    the head, else the first Put) so a mixed-dim batch is rejected here, not
	//    mid-apply in the SIMD kernel.
	pinnedDim := s.seg.dim
	for i := range b.ops {
		if b.ops[i].kind != batchPut {
			continue
		}
		if pinnedDim == 0 {
			pinnedDim = len(b.ops[i].v)
		}
		if err := validateVector(b.ops[i].v, pinnedDim, s.metric); err != nil {
			return err
		}
	}

	// 2. Resolve each op (id→docId alloc/lookup, prepare, payload encode, sealed-slot
	//    routing) BEFORE the txn. idtable allocs are not part of the control txn.
	items := make([]batchItem, 0, len(b.ops))
	for i := range b.ops {
		op := &b.ops[i]
		if op.kind == batchPut {
			docID, err := s.docIDForAlloc(op.id)
			if err != nil {
				return err
			}
			plBytes, err := encodePayload(op.payload)
			if err != nil {
				return err
			}
			pl, err := decodePayload(plBytes)
			if err != nil {
				return err
			}
			stored, norm := s.metric.prepare(op.v)
			it := batchItem{kind: batchPut, id: op.id, docID: docID, stored: stored, norm: norm, plBytes: plBytes, pl: pl}
			if prev, ok := s.docToSeg[docID]; ok && prev != headSegID {
				if ss := s.sealedByID(prev); ss != nil {
					if slot, found := ss.slotOfDoc(docID); found {
						it.hasSealed, it.sealSeg, it.sealSlot = true, prev, slot
					}
				}
			}
			items = append(items, it)
			continue
		}
		// batchDelete — unknown / already-gone ids are no-ops (like single Delete).
		docID, ok, err := s.lookupDocID(op.id)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		segId, ok := s.docToSeg[docID]
		if !ok {
			continue
		}
		it := batchItem{kind: batchDelete, id: op.id, docID: docID}
		if segId == headSegID {
			slot, ok := s.seg.slotOfDoc(docID)
			if !ok {
				continue
			}
			it.inHead, it.headSlot = true, slot
		} else {
			ss := s.sealedByID(segId)
			if ss == nil {
				continue
			}
			slot, found := ss.slotOfDoc(docID)
			if !found {
				continue
			}
			it.hasSealed, it.sealSeg, it.sealSlot = true, segId, slot
		}
		items = append(items, it)
	}

	// 3. One durable commit: all head rows + all sealed tombstones/docseg removals.
	//    Folding the cross-segment tombstone into this txn (the single-Put path uses a
	//    second txn + recovery reconciliation) makes the batch strictly more atomic.
	if err := s.cs.update(func(tx *bolt.Tx) error {
		for i := range items {
			it := &items[i]
			switch {
			case it.kind == batchPut:
				if err := putHeadRecord(tx, headRecord{ID: it.id, DocID: it.docID, Stored: it.stored, Norm: it.norm, Payload: it.plBytes}); err != nil {
					return err
				}
				if it.hasSealed {
					if err := putTomb(tx, it.sealSeg, it.sealSlot); err != nil {
						return err
					}
					if err := deleteDocSeg(tx, it.docID); err != nil {
						return err
					}
				}
			case it.inHead:
				if err := deleteHeadRecord(tx, it.docID); err != nil {
					return err
				}
			case it.hasSealed: // sealed delete
				if err := putTomb(tx, it.sealSeg, it.sealSlot); err != nil {
					return err
				}
				if err := deleteDocSeg(tx, it.docID); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}

	// 4. Post-commit in-memory updates (the durable side is committed).
	for i := range items {
		it := &items[i]
		if it.kind == batchPut {
			if it.hasSealed {
				if ss := s.sealedByID(it.sealSeg); ss != nil {
					ss.markTombLocked(it.sealSlot)
				}
			}
			if oldSlot, ok := s.seg.slotOfDoc(it.docID); ok {
				s.seg.tombstone(oldSlot)
			}
			s.idToDoc[it.id] = it.docID
			s.docToSeg[it.docID] = headSegID
			s.seg.append(it.docID, it.stored, it.norm, it.pl)
		} else {
			if it.inHead {
				s.seg.tombstone(it.headSlot)
			} else if it.hasSealed {
				if ss := s.sealedByID(it.sealSeg); ss != nil {
					ss.markTombLocked(it.sealSlot)
				}
			}
			delete(s.docToSeg, it.docID)
		}
	}

	// 5. Seal once if the head crossed maxSegSize (the slow HNSW build runs in the
	//    background; Commit latency stays off the build path).
	if len(s.seg.slotDoc) >= s.maxSegSize {
		if err := s.sealLocked(); err != nil {
			return err
		}
	}

	b.Discard()
	return nil
}
