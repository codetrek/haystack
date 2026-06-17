package vectorstore

import (
	"encoding/binary"
	"math"
	"path/filepath"
	"sync"
	"unsafe"
)

// sealedSegment is an immutable, mmap-backed records segment. Vectors, slot→docId,
// and payload are read-only; only the tombstone bitmap is mutable — Delete/Update
// set bits and commit them to the bbolt tomb bucket (incr 3; the per-segment
// tomb.dat msync is gone). The per-segment HNSW uses a dense live-only build nodeId
// (NOT the slot); the segment owns its vectors and resolves a graph hit back to a
// row by docId (§3 "向量只存一份").
type sealedSegment struct {
	dir    string
	id     segID // this segment's id (the tomb-bucket key prefix; set at open)
	metric Metric
	dim    int
	n      int // row count (incl. tombstoned)

	// vectors.dat mapping: each row is (dim float32 + 1 float32 norm).
	vecMap  []byte
	rowF32  int // dim+1 (floats per row)
	vecBase int // byte offset where row data starts (segPageSize)

	slotDocs []int64 // decoded from slotdoc.dat (small; copy out, not mmap-aliased)

	// tombMu guards the mutable tombstone state (the tomb words + the derived
	// docToSlot index). The background graph builder reads the tomb bitmap via
	// eachLive OFF the store lock while a concurrent Delete writes it via
	// markTombLocked under the store lock; without this guard that is a data race on
	// the tomb word and the docToSlot map (appendix #16/#18 — the -race gate flags
	// it). Read paths take RLock, markTombLocked takes Lock.
	tombMu sync.RWMutex

	// docToSlot is the derived, resident per-segment docId→slot index over LIVE
	// slots (architecture §4.6: "段内 docId↔slot ... 派生、常驻内存、可重建"). It is
	// built once at open from slotDocs (tombstoned slots excluded) and pruned by
	// markTombLocked, so slotOfDoc/Get/Delete and the Search tombstone post-filter
	// are O(1), not an O(n) scan (appendix #6/#17/#20/#24).
	docToSlot map[int64]int

	// tomb is the in-memory tombstone bitmap (one bit per slot, word w covers slots
	// [w*64, w*64+64)). Since incr 3 it is a heap slice rebuilt at open from the
	// bbolt tomb bucket (listTombSlots), NOT a tomb.dat mmap: the durable form lives
	// in the control store (one bbolt txn per delete) so the data plane carries no
	// mutable mmap'd file. It is sized to cover every slot at open and never grows
	// (segments are immutable in row count). andLive reads these words directly.
	tomb []uint64

	// payload.dat mapping (read-only): lengths then bytes.
	plMap     []byte
	plLens    []uint32
	plOffsets []int // byte offset of each payload within the data region
	plBase    int   // byte offset where payload bytes start

	// attr is the per-segment derived attr index (segAttrIndex). It is loaded from
	// attr.dat (or rebuilt from payload) by the store under s.mu when the declared
	// attr set is known (CreateAttrIndex / recover / seal / merge); nil until then,
	// in which case the filtered-search leg builds it on the fly. The store owns its
	// lifecycle (the declared set lives in the manifest, not the segment), so the
	// segment merely carries the pointer — it is never mutated through the segment.
	attr *segAttrIndex
}

func (s *sealedSegment) count() int { return s.n }

func (s *sealedSegment) slotDoc(slot int) int64 { return s.slotDocs[slot] }

// tombGet reports whether slot is tombstoned, reading the bitmap words under the
// tomb read lock (a concurrent markTombLocked mutates the same word).
func (s *sealedSegment) tombGet(slot int) bool {
	s.tombMu.RLock()
	defer s.tombMu.RUnlock()
	return s.tombGetLocked(slot)
}

// tombGetLocked is the lock-free body of tombGet; callers must hold tombMu (R or W).
func (s *sealedSegment) tombGetLocked(slot int) bool {
	w := slot >> 6
	if w < 0 || w >= len(s.tomb) {
		return false
	}
	return s.tomb[w]&(1<<uint(slot&63)) != 0
}

// markTombLocked sets slot's in-memory tombstone bit and prunes the derived
// docToSlot index so the slot is no longer reported live. It does NOT persist:
// since incr 3 the durable tombstone is one bbolt tomb-bucket Put the Store commits
// (it owns the control store and this segment's id) — typically in the SAME txn as
// the docseg delete, so a sealed Delete is one atomic commit. Held under the write
// lock so a concurrent builder/search read sees a consistent word + map. ok is true
// when this call newly tombstoned a live slot (false on a re-tombstone / out-of-
// range), letting the caller skip a redundant commit.
func (s *sealedSegment) markTombLocked(slot int) (ok bool) {
	if slot < 0 || slot >= s.n {
		return false
	}
	s.tombMu.Lock()
	defer s.tombMu.Unlock()
	w := slot >> 6
	if s.tomb[w]&(1<<uint(slot&63)) != 0 {
		return false // already tombstoned
	}
	s.tomb[w] |= 1 << uint(slot&63)
	doc := s.slotDocs[slot]
	if cur, ok := s.docToSlot[doc]; ok && cur == slot {
		delete(s.docToSlot, doc)
	}
	return true
}

// slotOfDoc returns the live slot for docID in O(1) via the derived index. A
// tombstoned (or absent) docId returns found=false — this is the liveness check
// the indexed-search tombstone post-filter relies on.
func (s *sealedSegment) slotOfDoc(docID int64) (int, bool) {
	s.tombMu.RLock()
	defer s.tombMu.RUnlock()
	slot, ok := s.docToSlot[docID]
	return slot, ok
}

// tombCount returns the number of tombstoned slots (live count = n − tombCount).
func (s *sealedSegment) tombCount() int {
	s.tombMu.RLock()
	defer s.tombMu.RUnlock()
	return s.n - len(s.docToSlot)
}

// getVectorRef returns the stored-form vector for slot without copying (aliases
// the mmap directly). Valid only while the segment is open: the merge swap that
// close()s the mmap takes s.mu exclusively, and every reader (Search) holds
// s.mu.RLock() across its whole traversal, so no caller aliases a freed mmap.
// Callers must not retain past that window or mutate it. The dim floats begin at
// vecBase+slot*rowF32*4, which is 4-aligned (vecBase is page-aligned, rowF32*4 is a
// multiple of 4), so the little-endian on-disk layout maps directly to []float32 on
// little-endian targets (amd64/arm64) — mirrors vectorindex MmapStore (#66).
func (s *sealedSegment) getVectorRef(slot int) []float32 {
	start := s.vecBase + slot*s.rowF32*4
	return unsafe.Slice((*float32)(unsafe.Pointer(&s.vecMap[start])), s.dim)
}

func (s *sealedSegment) norm(slot int) float32 {
	start := s.vecBase + slot*s.rowF32*4 + s.dim*4
	return math.Float32frombits(binary.LittleEndian.Uint32(s.vecMap[start:]))
}

// read returns slot's stored vector, norm, payload, and liveness.
func (s *sealedSegment) read(slot int) (stored []float32, norm float32, payload []byte, live bool) {
	if slot < 0 || slot >= s.n || s.tombGet(slot) {
		return nil, 0, nil, false
	}
	return s.getVectorRef(slot), s.norm(slot), s.payload(slot), true
}

func (s *sealedSegment) payload(slot int) []byte {
	n := int(s.plLens[slot])
	if n == 0 {
		return nil
	}
	off := s.plBase + s.plOffsets[slot]
	out := make([]byte, n)
	copy(out, s.plMap[off:off+n])
	return out
}

// payloadDecoded returns the slot's payload deserialized to a Payload. A sealed
// segment is immutable and its payload blobs were written by encodePayload, so a
// decode error means on-disk corruption (surfaced to the caller, not swallowed).
func (s *sealedSegment) payloadDecoded(slot int) (Payload, error) {
	return decodePayload(s.payload(slot))
}

// eachLive visits non-tombstoned slots ascending, mirroring segment.eachLive so
// the brute leg and graph builder share one iterator contract. It holds the tomb
// read lock for the whole pass so the live set is a consistent snapshot even when
// a concurrent Delete tombstones a slot mid-iteration (appendix #16/#18). fn must
// not call back into this segment's tomb methods (would re-enter the RLock).
func (s *sealedSegment) eachLive(fn func(slot int, docID int64, stored []float32, norm float32)) {
	s.tombMu.RLock()
	defer s.tombMu.RUnlock()
	for slot := 0; slot < s.n; slot++ {
		if s.tombGetLocked(slot) {
			continue
		}
		fn(slot, s.slotDocs[slot], s.getVectorRef(slot), s.norm(slot))
	}
}

func (s *sealedSegment) close() {
	if s.vecMap != nil {
		_ = mmapFree(s.vecMap)
		s.vecMap = nil
	}
	s.tomb = nil
	if s.plMap != nil {
		_ = mmapFree(s.plMap)
		s.plMap = nil
	}
}

// segFilePath joins a sealed-segment dir with a component filename.
func segFilePath(dir, name string) string { return filepath.Join(dir, name) }
