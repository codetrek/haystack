package invertedstore

// spilling.go — item F (B1 fix): the `spilling` read tier. A head DETACHED for off-worker encode is
// published into s.spilling and resolved by every read path as a tier BETWEEN the live head and the
// sealed segments (newest -> oldest by detach order), so a doc whose forward lives only in a detached
// head is never momentarily invisible to forwardKeywords / Search / GetDocs / ForwardDocids.

// spillEntry is one DETACHED head being encoded off-worker (item F). It is published into s.spilling
// at detach (under s.mu.Lock) and removed at install (under s.mu.Lock). Readers resolve it as a tier
// BETWEEN the live head and the sealed segments, newest -> oldest by detach order. The head is
// READ-ONLY once detached (the encode + readers only read it); it is never pooled/reused while listed.
type spillEntry struct {
	tableId            int
	head               *headTable
	outId              uint64 // the segment id reserved at detach (the file the encode writes)
	minDocid, maxDocid int64  // forward-record docid span (the spilling-head analog of B; Task 7C)
}

// headForwardLookup resolves docid's forward decision in ONE head: found=false ⇒ this head does not
// mention the docid (keep looking older). Words are COPIED so the caller may use them after dropping
// the lock (M1 copy-under-RLock). Caller holds s.mu.RLock.
func headForwardLookup(h *headTable, docid int64) (words []string, deleted, found bool) {
	if h == nil {
		return nil, false, false
	}
	if _, del := h.delForward[docid]; del {
		return nil, true, true
	}
	if w, ok := h.fwd[docid]; ok {
		return append([]string(nil), w...), false, true
	}
	return nil, false, false
}

// headForwardRange is the docid span of a head's forward records (live fwd + delForward) — the
// spilling-head analog of segMeta's [MinDocid,MaxDocid] (B). 7A uses the full span; Task 7C wires the
// docid-range skip into forwardKeywords' spilling loop using it. An empty head ⇒ the always-skip range.
func headForwardRange(h *headTable) (min, max int64) {
	min, max = emptyDocidRange()
	note := func(d int64) {
		if d < min {
			min = d
		}
		if d > max {
			max = d
		}
	}
	for d := range h.fwd {
		note(d)
	}
	for d := range h.delForward {
		note(d)
	}
	return
}
