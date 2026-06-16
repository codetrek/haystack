package vectorstore

import (
	"encoding/binary"
	"errors"
	"sync"

	"github.com/codetrek/haystack/core/idtable"
	"github.com/codetrek/haystack/core/kv"
)

// Distinct idtable key-prefix bytes for the vectorstore allocator, so it never
// collides with idtable's default doc-id allocator (28/29) when sharing a KV.
const (
	idtableKeyTypeNextId = byte(40)
	idtableKeyTypeKey    = byte(41)
)

// Options configures a Store. KV backs the string→docId idtable; Dir holds the
// records WAL (records.wal).
type Options struct {
	Dir    string
	KV     kv.Store
	Metric Metric
}

// Store is the Phase-1 records layer: one in-memory head segment fronted by an
// idtable (string id → docId) and protected by a WAL. The WAL is the single
// crash-safe source of truth for both the records and the id↔docId mapping. All
// public methods are serialized by mu (single-writer; readers take RLock and
// copy out of the segment).
type Store struct {
	mu      sync.RWMutex
	metric  Metric
	dir     string
	alloc   *idtable.Allocator
	seg     *segment
	wal     *WAL
	idToDoc map[string]int64 // derived from WAL replay; lets reads avoid allocating
}

// Open creates or recovers a Store at opts.Dir, replaying the WAL to rebuild the
// head segment, the id↔docId map, and the allocator state.
func Open(opts Options) (*Store, error) {
	if opts.KV == nil {
		return nil, errors.New("vectorstore: Options.KV is required")
	}
	if opts.Dir == "" {
		return nil, errors.New("vectorstore: Options.Dir is required")
	}
	alloc, err := idtable.New(opts.KV, idtable.Options{
		KeyTypeNextId: idtableKeyTypeNextId,
		KeyTypeKey:    idtableKeyTypeKey,
	})
	if err != nil {
		return nil, err
	}
	w, err := OpenWAL(opts.Dir)
	if err != nil {
		alloc.Close()
		return nil, err
	}
	s := &Store{
		metric:  opts.Metric,
		dir:     opts.Dir,
		alloc:   alloc,
		seg:     newSegment(opts.Metric),
		wal:     w,
		idToDoc: make(map[string]int64),
	}
	if err := s.replay(); err != nil {
		w.Close()
		alloc.Close()
		return nil, err
	}
	return s, nil
}

// Metric returns the store's distance metric.
func (s *Store) Metric() Metric { return s.metric }

// Close flushes and releases the WAL and idtable. Closing the allocator commits
// any pending id→docId mappings re-driven during replay, making the recovered
// state durable.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	werr := s.wal.Close()
	s.alloc.Close()
	return werr
}

// docIDForAlloc maps a string id to its stable int64 docId via the idtable,
// ALLOCATING on first sight. Use only on the write path (Put). The idtable
// returns an 8-byte big-endian id.
func (s *Store) docIDForAlloc(id string) (int64, error) {
	v, err := s.alloc.GetId([]byte(id))
	if err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64([]byte(v))), nil
}

// replay rebuilds in-memory state from the records WAL. Records are applied in
// LSN order; recPut re-drives the allocator for its string id (reconstructing
// the same monotonic docId the original run assigned — see store.go decision #9),
// tombstones the recorded old slot if any, then appends the new slot. recDelete
// tombstones the recorded slot. The idToDoc map is rebuilt as a side effect so
// reads never need to allocate. (Filled here; on a fresh Open the log is empty.)
func (s *Store) replay() error {
	return s.wal.Replay(func(typ recType, payload []byte) error {
		switch typ {
		case recPut:
			r := decodePut(payload)
			// Re-establish id→docId in the allocator and the derived map.
			if _, err := s.docIDForAlloc(r.ID); err != nil {
				return err
			}
			s.idToDoc[r.ID] = r.DocID
			s.applyPut(r)
		case recDelete:
			d := decodeDelete(payload)
			if _, err := s.docIDForAlloc(d.ID); err != nil {
				return err
			}
			s.idToDoc[d.ID] = d.DocID
			s.seg.tombstone(int(d.Slot))
		}
		return nil
	})
}

// applyPut mutates the segment for a (durably logged) put: tombstone the prior
// slot, then append the new one. Shared by Put and replay.
func (s *Store) applyPut(r putRecord) {
	if r.OldSlot >= 0 {
		s.seg.tombstone(int(r.OldSlot))
	}
	s.seg.append(r.DocID, r.Stored, r.Norm, r.Payload)
}

// Put inserts or replaces the vector and payload for id. It is crash-atomic: a
// single WAL record (the string id, its docId, the old slot to tombstone if any,
// and the new stored vector + norm + payload) is fsync'd before the in-memory
// state is mutated, so a crash either loses the whole Put or applies it whole on
// replay. The string→docId mapping is recovered from the same WAL record, so Put
// is fully durable on return without depending on idtable's lazy commit.
func (s *Store) Put(id string, v []float32, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateVector(v, s.seg.dim, s.metric); err != nil {
		return err
	}
	docID, err := s.docIDForAlloc(id)
	if err != nil {
		return err
	}
	stored, norm := s.metric.prepare(v)

	oldSlot := int64(-1)
	if slot, ok := s.seg.slotOfDoc(docID); ok {
		oldSlot = int64(slot)
	}
	rec := putRecord{ID: id, DocID: docID, OldSlot: oldSlot, Stored: stored, Norm: norm, Payload: payload}
	if _, err := s.wal.Append(recPut, encodePut(rec)); err != nil {
		return err
	}
	if err := s.wal.Sync(); err != nil {
		return err
	}
	s.idToDoc[id] = docID
	s.applyPut(rec)
	return nil
}

// Get returns the original (restored) vector and payload for id. Reads never
// allocate a docId: an unknown id (never Put) returns found=false. The returned
// vector and payload are fresh copies the caller may mutate freely.
func (s *Store) Get(id string) (v []float32, payload []byte, found bool, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	docID, ok := s.idToDoc[id]
	if !ok {
		return nil, nil, false, nil
	}
	slot, ok := s.seg.slotOfDoc(docID)
	if !ok {
		return nil, nil, false, nil
	}
	stored, norm, pl, live := s.seg.read(slot)
	if !live {
		return nil, nil, false, nil
	}
	// restore is the identity for non-cosine metrics, so it may alias the
	// segment's internal buffer. Always hand the caller a private copy.
	out := append([]float32(nil), s.metric.restore(stored, norm)...)
	plcp := append([]byte(nil), pl...)
	return out, plcp, true, nil
}

// Delete tombstones id's current slot. Deleting an unknown or already-deleted id
// is a pure no-op (no WAL write, no idtable allocation). The id↔docId mapping is
// intentionally left in place; a later Put of the same id reuses the same docId.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	docID, ok := s.idToDoc[id]
	if !ok {
		return nil
	}
	slot, ok := s.seg.slotOfDoc(docID)
	if !ok {
		return nil
	}
	if _, err := s.wal.Append(recDelete, encodeDelete(id, docID, int64(slot))); err != nil {
		return err
	}
	if err := s.wal.Sync(); err != nil {
		return err
	}
	s.seg.tombstone(slot)
	return nil
}

// Search returns the k nearest live records to q under the store's metric,
// brute-scanning the single head segment. An empty store returns (nil, nil).
// Results are in docId space (see SearchResult / decision #4).
func (s *Store) Search(q []float32, k int) ([]SearchResult, error) {
	if k <= 0 {
		return nil, errors.New("vectorstore: k must be positive")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := validateVector(q, s.seg.dim, s.metric); err != nil {
		return nil, err
	}
	pq, _ := s.metric.prepare(q)
	tk := newTopK(k)
	s.seg.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		tk.offer(SearchResult{DocID: docID, Distance: s.metric.distance(stored, pq)})
	})
	out := tk.sorted()
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
