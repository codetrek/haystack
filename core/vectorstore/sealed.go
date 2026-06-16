package vectorstore

import (
	"encoding/binary"
	"fmt"
	"math"
	"path/filepath"
	"sync"
)

// sealedSegment is an immutable, mmap-backed records segment. Vectors, slot→docId,
// and payload are read-only; only the tombstone bitmap (tomb.dat) is mutable —
// Delete/Update set bits and msync them. The per-segment HNSW uses a dense
// live-only build nodeId (NOT the slot); the segment owns its vectors and resolves
// a graph hit back to a row by docId (§3 "向量只存一份").
type sealedSegment struct {
	dir    string
	metric Metric
	dim    int
	n      int // row count (incl. tombstoned)

	// vectors.dat mapping: each row is (dim float32 + 1 float32 norm).
	vecMap  []byte
	rowF32  int // dim+1 (floats per row)
	vecBase int // byte offset where row data starts (segPageSize)

	slotDocs []int64 // decoded from slotdoc.dat (small; copy out, not mmap-aliased)

	// tombMu guards the mutable tombstone state (the tomb.dat words + the derived
	// docToSlot index). The background graph builder reads the tomb bitmap via
	// eachLive OFF the store lock while a concurrent Delete writes it via
	// tombstoneSlot under the store lock; without this guard that is a data race on
	// the mmap word and the docToSlot map (appendix #16/#18 — the -race gate flags
	// it). Read paths take RLock, tombstoneSlot takes Lock.
	tombMu sync.RWMutex

	// docToSlot is the derived, resident per-segment docId→slot index over LIVE
	// slots (architecture §4.6: "段内 docId↔slot ... 派生、常驻内存、可重建"). It is
	// built once at open from slotDocs (tombstoned slots excluded) and pruned by
	// tombstoneSlot, so slotOfDoc/Get/Delete and the Search tombstone post-filter
	// are O(1), not an O(n) scan (appendix #6/#17/#20/#24).
	docToSlot map[int64]int

	// tomb.dat mapping (RW): header at offset 0, words start at segPageSize.
	tombMap   []byte
	tombWords int

	// payload.dat mapping (read-only): lengths then bytes.
	plMap     []byte
	plLens    []uint32
	plOffsets []int // byte offset of each payload within the data region
	plBase    int   // byte offset where payload bytes start
}

func (s *sealedSegment) count() int { return s.n }

func (s *sealedSegment) slotDoc(slot int) int64 { return s.slotDocs[slot] }

// tombGet reports whether slot is tombstoned, reading the mmap'd bitmap words
// under the tomb read lock (a concurrent tombstoneSlot mutates the same word).
func (s *sealedSegment) tombGet(slot int) bool {
	s.tombMu.RLock()
	defer s.tombMu.RUnlock()
	return s.tombGetLocked(slot)
}

// tombGetLocked is the lock-free body of tombGet; callers must hold tombMu (R or W).
func (s *sealedSegment) tombGetLocked(slot int) bool {
	w := slot >> 6
	if w >= s.tombWords {
		return false
	}
	off := segPageSize + w*8
	word := binary.LittleEndian.Uint64(s.tombMap[off : off+8])
	return word&(1<<uint(slot&63)) != 0
}

// tombstoneSlot sets slot's tombstone bit and msyncs tomb.dat so the delete is
// durable. The bitmap is pre-sized at seal to cover every slot, so no growth is
// needed (segments are immutable in row count). The derived docToSlot index is
// pruned so the slot is no longer reported live. Held under the write lock so a
// concurrent builder/search read sees a consistent word + map.
func (s *sealedSegment) tombstoneSlot(slot int) error {
	if slot < 0 || slot >= s.n {
		return fmt.Errorf("vectorstore: tombstone slot %d out of range [0,%d)", slot, s.n)
	}
	s.tombMu.Lock()
	defer s.tombMu.Unlock()
	w := slot >> 6
	off := segPageSize + w*8
	word := binary.LittleEndian.Uint64(s.tombMap[off : off+8])
	word |= 1 << uint(slot&63)
	binary.LittleEndian.PutUint64(s.tombMap[off:off+8], word)
	if err := mmapSync(s.tombMap); err != nil {
		return err
	}
	doc := s.slotDocs[slot]
	if cur, ok := s.docToSlot[doc]; ok && cur == slot {
		delete(s.docToSlot, doc)
	}
	return nil
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
// the mmap). Callers must not retain or mutate it. Used by the HNSW graph leg.
func (s *sealedSegment) getVectorRef(slot int) []float32 {
	start := s.vecBase + slot*s.rowF32*4
	out := make([]float32, s.dim)
	for i := 0; i < s.dim; i++ {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(s.vecMap[start+i*4:]))
	}
	return out
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
	if s.tombMap != nil {
		_ = mmapFree(s.tombMap)
		s.tombMap = nil
	}
	if s.plMap != nil {
		_ = mmapFree(s.plMap)
		s.plMap = nil
	}
}

// segFilePath joins a sealed-segment dir with a component filename.
func segFilePath(dir, name string) string { return filepath.Join(dir, name) }
