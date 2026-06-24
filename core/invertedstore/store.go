package invertedstore

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/codetrek/haystack/core/queue"
)

// Options configure a Store. Zero values are filled with the design §4 defaults by
// (*Options).withDefaults, so callers can pass Options{} for a sane production setup.
type Options struct {
	CapBytes        int  // head byte cap (the memory knob); default 16 MiB
	Fanout          int  // tiered-merge fanout; default 4
	DataCodecL0     byte // L0 spill data-block codec; default snappy
	DataCodecMerged byte // background-merged data-block codec; default zstd (bounded)
	DictCodec       byte // term-dict region codec; default zstd
	DictChunkBytes  int  // term-dict chunk size; default 4096
	ChunkCacheBytes int  // Store-level dict-chunk LRU budget; default 32 MiB
	InlineThreshold int  // value <= this is inline, else external; default 1 KiB

	// blockTarget/chunk are the segment block geometry; kept here (not in design §4) so the
	// spill path can size blocks/external chunks. Defaults match the SSTable conventions used
	// by the spike (32 KiB blocks, 64 KiB external chunks).
	BlockTarget int // raw bytes per data block before compression; default 32 KiB
	Chunk       int // raw bytes per external-value chunk; default 64 KiB
}

func (o Options) withDefaults() Options {
	if o.CapBytes <= 0 {
		o.CapBytes = 16 << 20
	}
	if o.Fanout <= 0 {
		o.Fanout = 4
	}
	if o.DataCodecL0 == 0 {
		o.DataCodecL0 = codecSnappy
	}
	if o.DataCodecMerged == 0 {
		o.DataCodecMerged = codecZstd
	}
	if o.DictCodec == 0 {
		o.DictCodec = codecZstd
	}
	if o.DictChunkBytes <= 0 {
		o.DictChunkBytes = 4096
	}
	if o.ChunkCacheBytes <= 0 {
		o.ChunkCacheBytes = 32 << 20
	}
	if o.InlineThreshold <= 0 {
		o.InlineThreshold = 1 << 10
	}
	if o.BlockTarget <= 0 {
		o.BlockTarget = 32 << 10
	}
	if o.Chunk <= 0 {
		o.Chunk = 64 << 10
	}
	return o
}

// Store is the pebble-free, segment-based inverted index. It owns a byte-capped in-memory
// head (per table), an atomically-replaced MANIFEST, and the set of immutable sealed segments.
//
// Concurrency (design §6): all writes (table ops, applies, spills) run on the single mpsc
// worker, so the head and segment set have one mutator. A RWMutex guards reader access to the
// in-memory head and the published segment slice; full lock-free snapshotting (atomic.Pointer)
// is a later task (design T8). For P4 the mutex is sufficient.
type Store struct {
	dir  string
	q    queue.Queue
	opts Options

	mu   sync.RWMutex
	man  *manifest
	head map[int]*headTable // tableId -> in-memory head (P4c)
	segs []*segment         // live sealed segments, oldest->newest (open file handles)
}

// segFileName is the on-disk name for a sealed segment with the given seal-sequence id.
// Matches design §5's layout (seg-000123.dat).
func segFileName(id uint64) string { return fmt.Sprintf("seg-%06d.dat", id) }

// Open reads (or bootstraps) the MANIFEST under path and opens each live segment file. A
// missing MANIFEST yields a fresh empty store. The queue must already be started.
func Open(path string, q queue.Queue, opts Options) (*Store, error) {
	man, err := readManifest(path)
	if err != nil {
		return nil, err
	}
	s := &Store{
		dir:  path,
		q:    q,
		opts: opts.withDefaults(),
		man:  man,
		head: map[int]*headTable{},
	}
	for _, sm := range man.Segments {
		seg := openSegment(filepath.Join(path, segFileName(sm.Id)))
		s.segs = append(s.segs, seg)
	}
	return s, nil
}

// CloseAndWait flushes any non-empty head (spilling it to a sealed segment so no buffered write
// is lost across a clean close), then closes every open segment. It runs the flush on the worker
// so it is serialized with in-flight applies.
func (s *Store) CloseAndWait() {
	s.q.RunFunc(func() error {
		s.mu.Lock()
		tables := make([]int, 0, len(s.head))
		for id, h := range s.head {
			if h != nil && (len(h.inv) > 0 || len(h.fwd) > 0 || len(h.delForward) > 0) {
				tables = append(tables, id)
			}
		}
		s.mu.Unlock()
		for _, id := range tables {
			if err := s.spill(id); err != nil {
				return err
			}
		}
		return nil
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, seg := range s.segs {
		seg.close()
	}
	s.segs = nil
}

// CreateTable allocates the next table id, records it in the catalog, and durably rewrites the
// MANIFEST. It returns a value, so it runs synchronously on the worker (design §6: don't call
// from within a worker task).
func (s *Store) CreateTable(description string) (int, error) {
	var id int
	err := s.q.RunFunc(func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		id = s.man.NextTableId
		s.man.NextTableId++
		s.man.Tables[id] = tableInfo{Id: id, CreatedAt: time.Now(), Description: description}
		return writeManifest(s.dir, s.man)
	})
	return id, err
}

// DeleteTable drops the table's catalog entry and durably rewrites the MANIFEST. The table's
// [I]/[F] segment bytes are NOT reclaimed here — Search/GetDocs return empty for an absent
// tableId immediately, and the dead keys are reclaimed by a covering merge (P8). So this is just
// the catalog drop for P4.
func (s *Store) DeleteTable(tableId int) error {
	return s.q.RunFunc(func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.man.Tables, tableId)
		delete(s.head, tableId)
		return writeManifest(s.dir, s.man)
	})
}

// tableInfo returns the catalog entry for tableId, if present. A small read helper used by the
// tests and (later) Search to reject an absent/deleted table.
func (s *Store) tableInfo(tableId int) (tableInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ti, ok := s.man.Tables[tableId]
	return ti, ok
}
