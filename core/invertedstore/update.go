package invertedstore

// update.go — P7 (design §6 write path, §8 full re-post; task T5).
//
// The write side of the store. All public writes are thread-safe and ASYNCHRONOUS: each enqueues
// an apply task on the mpsc worker via q.AddFunc, so callers never need to be "on the worker" (the
// contract improvement over invertedindex). Update is exactly a single-item Batch; Batch amortizes
// N ops into ONE apply task. The apply runs on the single worker, serialized with spills and table
// ops, so the head and segment set have one mutator and Search reads concurrently via the RWMutex.
//
// Diff model (term-id, §8). term-id CANNOT do a string-style delta — a doc's forward references its
// FULL current keyword set, which must all be [I] keys in the segment that holds the forward — so on
// edit the doc is FULLY RE-POSTED: addPosting for EVERY current keyword, plus a per-keyword
// tombstone for each keyword the doc no longer has. Empty keywords ⇒ DELETE: a forward-tombstone
// (nKw=0) plus a tombstone in ALL the doc's old keywords, so an older non-empty segment can never
// resurrect it. This is a direct port of cmd/sortbench/main.go runUpdates' termid branch, in
// production shape (int64 docids, the head buffer, the forward read of P5).

// updateOp is one queued (tableId, docid, keywords) edit. keywords == nil/empty ⇒ delete the doc.
type updateOp struct {
	tableId  int
	docid    int64
	keywords []string
}

// Batch accumulates updateOps in memory; Commit enqueues ONE apply task that applies them in order
// on the worker (a repeated docid → last op wins). It is the bulk-ingest path; Update is the n=1
// convenience that wraps a single-op Batch.
type Batch struct {
	s   *Store
	ops []updateOp
}

// waitProducerGate blocks the calling PRODUCER goroutine while the worker has engaged the F v5
// producer gate (over-cap with a spill already in flight). It loops `for blockProducer` (NOT an `if`:
// Broadcast wakes all parked producers but each install relieves only one head's worth, so a producer
// must re-check) on spillCond, whose L is s.mu — so the flag is read under the same lock the worker
// sets/clears it under, closing the lost-wakeup window. It MUST be called BEFORE q.AddFunc: a parked
// producer holds ZERO queue slots, which is what keeps the gate deadlock-free (the worker can always
// drain the in-flight install RunFunc and clear the gate). The Wait releases + reacquires s.mu.
func (s *Store) waitProducerGate() {
	s.mu.Lock()
	for s.blockProducer {
		s.spillCond.Wait()
	}
	s.mu.Unlock()
}

// applyGate, when non-nil, is invoked at the START of an enqueued apply closure (on the worker),
// BEFORE applyBatch runs — while the producer's acquired budget is still held. Test-only (E): a test
// installs one that blocks so applies cannot drain, then observes the in-flight postings pile up at
// the budget ceiling (the producer must block once the budget fills). nil in production.
var applyGate func()

// applyFastPathTaken, when non-nil, fires in applyBatch's len(ops)==1 branch (the 1-op fast path).
// Test-only (C.1): a test asserts the fast path is actually taken for a 1-op apply and NOT for a
// multi-op batch — the behavior test alone passes even without the fast path, so this hook is the
// only thing that covers the optimization being taken. nil in production.
var applyFastPathTaken func()

// NewBatch starts an empty Batch bound to this store.
func (s *Store) NewBatch() *Batch { return &Batch{s: s} }

// Update appends a (tableId, docid, keywords) op to the batch. keywords is the doc's CURRENT full
// keyword set; empty ⇒ delete. Returns the batch for chaining.
func (b *Batch) Update(tableId int, docid int64, keywords []string) *Batch {
	// Defensive copy: the caller's slice may be mutated/reused after Update returns, but the op is
	// applied LATER on the worker. nil keywords stays nil (a delete).
	var kw []string
	if len(keywords) > 0 {
		kw = append([]string(nil), keywords...)
	}
	b.ops = append(b.ops, updateOp{tableId: tableId, docid: docid, keywords: kw})
	return b
}

// Commit enqueues the batch as a SINGLE async apply task (q.AddFunc). An empty batch is a no-op.
// Ops are applied in order on the worker; a docid repeated in the batch resolves to its LAST op.
func (b *Batch) Commit() {
	if len(b.ops) == 0 {
		return
	}
	ops := b.ops
	b.ops = nil // a committed batch is spent; don't let a later Commit re-apply
	s := b.s
	var postings int64
	for _, op := range ops {
		postings += int64(len(op.keywords))
	}
	got := s.budget.acquire(postings) // producer backpressure (spec §7 E)
	s.waitProducerGate()              // F v5 producer gate (over-cap + spill-in-flight); before q.AddFunc
	s.q.AddFunc(func() error {
		defer s.budget.release(got)
		if applyGate != nil {
			applyGate()
		}
		return s.applyBatch(ops)
	})
}

// Update is the single-item Batch: it enqueues ONE async apply task for one doc. keywords is the
// doc's CURRENT full keyword set; empty ⇒ delete. Thread-safe; never blocks (design §4/§6).
func (s *Store) Update(tableId int, docid int64, keywords []string) {
	var kw []string
	if len(keywords) > 0 {
		kw = append([]string(nil), keywords...)
	}
	op := updateOp{tableId: tableId, docid: docid, keywords: kw}
	got := s.budget.acquire(int64(len(kw))) // producer backpressure (spec §7 E)
	s.waitProducerGate()                    // F v5 producer gate (over-cap + spill-in-flight); before q.AddFunc
	s.q.AddFunc(func() error {
		defer s.budget.release(got)
		if applyGate != nil {
			applyGate()
		}
		return s.applyBatch([]updateOp{op})
	})
}

// applyBatch applies ops in order on the worker. For each op it diffs the doc's CURRENT keywords
// (the forward read of P5 — head pending first, then segments newest→oldest) against the new set
// and full-re-posts; an empty new set deletes. After every op it spills the table if the head's
// byte estimate crossed CapBytes (reusing P4c spill), so memory stays bounded mid-batch.
//
// In-batch last-wins: a docid touched earlier in THIS batch must diff against the keywords its
// earlier op set (not a stale sealed copy and not a forward read that hasn't observed the earlier
// op's head writes through the dedup yet), so we track the in-batch state per (tableId,docid). This
// also means a cold-build batch — every docid new, never re-touched — takes NO forward read at all.
func (s *Store) applyBatch(ops []updateOp) error {
	// 1-op fast path (C.1): the hot Update path is always 1-op, and a 1-op batch cannot repeat a
	// docid, so the inBatch/seen last-wins bookkeeping is dead weight and `old` always comes from the
	// forward read. Skip the maps entirely and apply the single op directly.
	if len(ops) == 1 {
		if applyFastPathTaken != nil {
			applyFastPathTaken() // test-only (C.1): proves the 1-op fast path is actually taken
		}
		op := ops[0]
		old, _ := s.forwardKeywords(op.tableId, op.docid)
		return s.applyOneOp(op, old)
	}

	// inBatch[(tableId,docid)] is the doc's keyword set as left by its latest op SO FAR in this
	// batch; a nil entry that EXISTS means the last op deleted it. Presence (ok) means "seen this
	// batch", so a later op for the same docid diffs against it instead of re-reading the forward.
	type dk struct {
		t int
		d int64
	}
	inBatch := map[dk]([]string){}
	seen := map[dk]bool{}

	for _, op := range ops {
		key := dk{op.tableId, op.docid}

		// 1. Old keyword set. If this docid was already touched in this batch, its old state is the
		//    last op's result (no forward read). Otherwise read the forward map (P5).
		var old []string
		if seen[key] {
			old = inBatch[key]
		} else {
			words, _ := s.forwardKeywords(op.tableId, op.docid)
			old = words
		}

		if err := s.applyOneOp(op, old); err != nil {
			return err
		}

		seen[key] = true
		if len(op.keywords) == 0 {
			inBatch[key] = nil
		} else {
			inBatch[key] = op.keywords
		}
	}
	return nil
}

// applyOneOp applies a single op against its already-resolved `old` keyword set: it takes s.mu,
// re-posts the doc into the head (DELETE ⇒ tombstone old keywords + forward-tombstone; otherwise
// FULL RE-POST + tombstone removed keywords), updates the liveByTable delta, then spills the table
// outside the lock if the head crossed CapBytes. The in-batch last-wins bookkeeping (inBatch/seen)
// is NOT here — it closes over multi-op loop state and stays in applyBatch's loop. Returns the spill
// error (the spill-on-`over` stays inside).
func (s *Store) applyOneOp(op updateOp, old []string) error {
	s.mu.Lock()
	h := s.head[op.tableId]
	if h == nil {
		h = newHeadTable()
		s.head[op.tableId] = h
	}

	// liveByTable delta (spec §4.2.2): live pairs change by (new distinct − old distinct). `old`
	// may carry caller duplicates (the forward stores raw keywords), so dedup it — len(old) is not
	// the distinct count. Runs under s.mu.Lock so deadFraction's RLock read is race-free.
	oldN := int64(distinctStrings(old))

	if len(op.keywords) == 0 {
		// DELETE: tombstone the docid in ALL its old keywords + write a forward-tombstone, so
		// no older non-empty segment can win and resurrect the doc (design §6).
		for _, w := range old {
			h.tombstonePosting(w, op.docid)
		}
		h.deleteForward(op.docid)
		s.liveByTable[op.tableId] -= oldN
	} else {
		// FULL RE-POST (term-id, §8): add EVERY current keyword (addPosting dedups in the head),
		// then a per-keyword tombstone for each removed keyword (in old, not in new).
		newSet := make(map[string]struct{}, len(op.keywords))
		for _, w := range op.keywords {
			newSet[w] = struct{}{}
		}
		for w := range newSet {
			h.addPosting(w, op.docid)
		}
		for _, w := range old {
			if _, ok := newSet[w]; !ok {
				h.tombstonePosting(w, op.docid)
			}
		}
		h.setForward(op.docid, op.keywords)
		s.liveByTable[op.tableId] += int64(len(newSet)) - oldN
	}
	over := h.bytes >= int64(s.opts.CapBytes)

	// Over-cap handling (F v5): the spill encode runs OFF the worker. If no spill is in flight, detach
	// this head NOW (under the SAME lock — the over-cap read + spillInFlight read + detach are one
	// section, so two applies can never both detach) and dispatch its encode after dropping the lock.
	// If a spill IS in flight, the worker must NOT detach a second one (one-in-flight); instead it sets
	// the producer gate so Update/Commit park BEFORE enqueuing more applies — the live head keeps the
	// data, bounded by that backpressure, until the in-flight install re-dispatches it.
	var toDispatch *spillEntry
	if over {
		if !s.spillInFlight {
			toDispatch = s.detachHeadLocked(op.tableId)
		} else {
			s.blockProducer = true // the producer parks until installSpill clears + broadcasts
		}
	}
	s.mu.Unlock()

	if toDispatch != nil {
		s.dispatchSpill(toDispatch)
	}
	return nil
}

// distinctStrings counts the distinct strings in ss. Converts a doc's (possibly caller-duplicated)
// keyword slice to its distinct count for the liveByTable delta, matching the inverted index which
// dedups via addPosting.
func distinctStrings(ss []string) int {
	if len(ss) <= 1 {
		return len(ss)
	}
	seen := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		seen[s] = struct{}{}
	}
	return len(seen)
}
