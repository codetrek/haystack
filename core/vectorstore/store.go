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
