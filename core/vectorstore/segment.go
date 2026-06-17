package vectorstore

// segment is the in-memory head: a slot-addressed records block. Each slot holds
// a stored-form vector + norm + payload; slotDoc[slot] is the source of truth for
// slot→docId, and docToSlot is the derived reverse index over LIVE slots only
// (the per-segment level of the two-level id model, §4.6). tomb marks deleted
// slots. In Phase 1 there is exactly one segment (the head).
type segment struct {
	metric    Metric
	dim       int           // learned from the first append (0 until then)
	vectors   [][]float32   // slot → stored-form vector
	norms     []float32     // slot → norm (meaningful only for cosine)
	payloads  []Payload     // slot → decoded payload (head holds the typed form)
	slotDoc   []int64       // slot → docId (source of truth)
	tomb      bitmap        // slot → deleted?
	docToSlot map[int64]int // docId → live slot (derived)
	attr      *headAttr     // declared-field in-memory index, maintained on append (nil until declared)
}

func newSegment(m Metric) *segment {
	return &segment{metric: m, docToSlot: make(map[int64]int)}
}

// append stores a new slot and indexes it as the live slot for docID. The vector
// must already be in stored form (caller runs metric.prepare). The slice and
// payload are copied so the caller may reuse its buffers.
func (s *segment) append(docID int64, stored []float32, norm float32, payload Payload) int {
	if s.dim == 0 && len(stored) > 0 {
		s.dim = len(stored)
	}
	vcp := make([]float32, len(stored))
	copy(vcp, stored)
	slot := len(s.vectors)
	s.vectors = append(s.vectors, vcp)
	s.norms = append(s.norms, norm)
	s.payloads = append(s.payloads, payload.clone())
	s.slotDoc = append(s.slotDoc, docID)
	s.docToSlot[docID] = slot
	if s.attr != nil {
		s.attr.index(slot, payload) // maintain the head attr index on Put (declared fields)
	}
	return slot
}

// tombstone marks slot deleted and drops it from the derived index (only if that
// docId still points at this slot — guards against an overwritten mapping).
func (s *segment) tombstone(slot int) {
	if slot < 0 || slot >= len(s.slotDoc) {
		return
	}
	s.tomb.set(slot)
	doc := s.slotDoc[slot]
	if cur, ok := s.docToSlot[doc]; ok && cur == slot {
		delete(s.docToSlot, doc)
	}
}

// slotOfDoc returns the live slot for docID.
func (s *segment) slotOfDoc(docID int64) (int, bool) {
	slot, ok := s.docToSlot[docID]
	return slot, ok
}

// read returns the slot's stored vector, norm, payload, and liveness.
func (s *segment) read(slot int) (stored []float32, norm float32, payload Payload, live bool) {
	if slot < 0 || slot >= len(s.vectors) {
		return nil, 0, nil, false
	}
	if s.tomb.get(slot) {
		return nil, 0, nil, false
	}
	return s.vectors[slot], s.norms[slot], s.payloads[slot], true
}

// eachLive visits every non-tombstoned slot in ascending order. The stored slice
// is the internal buffer; callers must not retain it past the callback.
func (s *segment) eachLive(fn func(slot int, docID int64, stored []float32, norm float32)) {
	for slot := range s.vectors {
		if s.tomb.get(slot) {
			continue
		}
		fn(slot, s.slotDoc[slot], s.vectors[slot], s.norms[slot])
	}
}
