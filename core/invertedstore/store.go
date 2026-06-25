package invertedstore

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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

	// AutoMerge enables the background tiered merger (P8): after each spill the worker enqueues a
	// maybeMerge task (tiered fanout + covering-merge trigger). It defaults OFF so a test that asserts
	// an exact segment count is not surprised by a merge collapsing segments; production wiring (and
	// the P8 merge tests) turn it on. A merge is still always available synchronously via the merge
	// entry points (mergeForTest/coveringMergeForTest) regardless of this flag.
	AutoMerge bool

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
// Concurrency (design §6, P9/T8): all writes (table ops, applies, spills, merges) run on the single
// mpsc worker, so the head and segment set have one mutator. The live segment set is PUBLISHED via
// an atomic.Pointer[segSnapshot] (concurrency.go) that the worker swaps on every seal/merge/table
// change; Search/GetDocs/forwardKeywords load it once and hold a refcounted view, so a merged-away
// segment's file is unlinked only after the last in-flight reader of it finishes (deferred deletion,
// no use-after-free). The RWMutex guards the head maps + the MANIFEST + the worker's seg slice and
// serializes the brief acquire/swap handoff; readers never block on a writer's I/O.
type Store struct {
	dir  string
	q    queue.Queue
	opts Options

	mu   sync.RWMutex
	man  *manifest
	head map[int]*headTable // tableId -> in-memory head (P4c)
	segs []*segment         // worker-owned live sealed segment slice, oldest->newest (the swap source)

	// liveByTable[tableId] = distinct live (keyword,docid) pairs in that table = Σ over the table's
	// live docs of their distinct keyword count. The `live` term of the covering-merge trigger
	// (deadFraction). NOT persisted: recomputed on Open from the segments' forward records
	// (recomputeLive), maintained incrementally in applyBatch (under s.mu.Lock, so the RLock read in
	// deadFraction is race-free), and dropped per-table by DeleteTable. A plain arithmetic counter
	// (missing key reads 0; no CreateTable seeding). Mutated only on the worker, like head/segs.
	liveByTable map[int]int64

	// snap is the atomically-published live segment set readers load (concurrency.go, P9/T8). The
	// worker rebuilds + Store()s it from s.segs on every spill/merge/table change; a reader Load()s it
	// once per call and refcounts its segments for the scan. Always non-nil (Open seeds emptySnapshot).
	snap atomic.Pointer[segSnapshot]

	// Background merge scheduler (concurrency.go, P9/T8). The merger runs on its OWN goroutine, not by
	// re-enqueuing onto the mpsc worker (which self-deadlocks when the queue fills). A spill/DeleteTable
	// raises a non-blocking trigger on mergeSignal; mergeLoop drives the merge passes back onto the
	// worker via RunFunc. forceCovering makes the next pass a covering merge (DeleteTable). The
	// req/ack sequence counters let waitMergeIdle (test) wait for quiescence. Only set when AutoMerge on.
	mergeSignal   chan struct{}
	mergeStop     chan struct{}
	mergeDone     chan struct{}
	forceCovering atomic.Bool
	mergeReqSeq   atomic.Int64 // bumped by every triggerMerge
	mergeAckSeq   atomic.Int64 // set to the reqSeq a completed pass observed

	// dictCache is the Store-level LRU of decompressed term-dict chunks, keyed by
	// (segmentId, chunkIdx) (design §6/§8). It is read only on the forward (Update) path —
	// Search never touches it — and is purged of a segment's chunks when a merge retires it.
	dictCache *chunkLRU

	// onForwardRead, if non-nil, is invoked by forwardKeywords whenever it performs a REAL
	// forward read (a head-forward hit or a segment-I/O scan) — i.e. not on a cold-build miss.
	// Test-only observability hook (P7) for the "cold build takes no forward read" assertion;
	// it is set/read only on the worker so it needs no extra locking.
	onForwardRead func()

	// onForwardProbe, if non-nil, fires once per segment forwardKeywords actually PROBES (decompresses
	// a block via lookupForward) — i.e. NOT for a range-skipped segment. Test-only (B): asserts a
	// cold-build read skips every sealed segment. Set/read only on the worker.
	onForwardProbe func()
}

// noteForwardRead fires the forward-read observability hook if one is installed (P7).
func (s *Store) noteForwardRead() {
	if s.onForwardRead != nil {
		s.onForwardRead()
	}
}

func (s *Store) noteForwardProbe() {
	if s.onForwardProbe != nil {
		s.onForwardProbe()
	}
}

// segFileName is the on-disk name for a sealed segment with the given seal-sequence id.
// Matches design §5's layout (seg-000123.dat).
func segFileName(id uint64) string { return fmt.Sprintf("seg-%06d.dat", id) }

// Open reads (or bootstraps) the MANIFEST under path and opens each live segment file. A
// missing MANIFEST yields a fresh empty store. The queue must already be started.
//
// Open creates path (and any missing parents) before reading the MANIFEST, matching the
// idtable/vectorstore Open ergonomics: the production wiring is
// Open(filepath.Join(storagePath, StorageVersion, "invertedstore"), ...) — a versioned
// subdir that does NOT exist on first boot — and readManifest/writeManifest would otherwise
// fail with "no such file or directory" on the MANIFEST(.tmp) path.
func Open(path string, q queue.Queue, opts Options) (*Store, error) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return nil, fmt.Errorf("invertedstore: create dir %q: %w", path, err)
	}
	man, err := readManifest(path)
	if err != nil {
		return nil, err
	}
	s := &Store{
		dir:         path,
		q:           q,
		opts:        opts.withDefaults(),
		man:         man,
		head:        map[int]*headTable{},
		liveByTable: map[int]int64{},
	}
	s.dictCache = newChunkLRU(int64(s.opts.ChunkCacheBytes))
	for _, sm := range man.Segments {
		seg := openSegment(filepath.Join(path, segFileName(sm.Id)))
		seg.id = sm.Id                                        // P5: the chunk-LRU keys decompressed dict chunks by (segmentId, chunkIdx)
		seg.minDocid, seg.maxDocid = sm.MinDocid, sm.MaxDocid // B
		seg.refs.Store(1)                                     // P9: the published snapshot holds one ref per live segment
		s.segs = append(s.segs, seg)
	}
	if man.FormatVersion < 3 {
		// Pre-B manifests have no docid range (unmarshals to [0,0], which would mis-skip every docid
		// != 0). Recompute each segment's range from its forward records, then persist at v3 so the
		// stale range can never reach forwardKeywords.
		if err := s.upgradeSegmentRanges(); err != nil {
			return nil, err
		}
	}
	s.snap.Store(emptySnapshot)
	s.publishSnapshotLocked() // seed the atomic pointer with the opened set (no concurrent readers yet)
	s.recomputeLive()         // rebuild the live counter from the opened segments (catalog-gated, §4.2.1)
	s.startMergeLoop()        // P9: background merger on its own goroutine (no-op unless AutoMerge)
	s.reclaimOrphanTables()   // §6: synchronously reclaim a dead table left orphaned by a DeleteTable-window crash
	return s, nil
}

// CloseAndWait flushes any non-empty head (spilling it to a sealed segment so no buffered write
// is lost across a clean close), stops the background merge goroutine, then closes every open
// segment. It runs the flush on the worker so it is serialized with in-flight applies. The merge
// goroutine is stopped AFTER the spill (so its final drain can collapse the just-spilled segments)
// but BEFORE the fds are closed (so a merge in flight never reads a closed fd).
//
// Close honors the P9/T8 refcount path so it is SAFE against a Search/GetDocs in flight: it publishes
// emptySnapshot (no later reader can acquire these segments) and then retireKeepFile()s each segment.
// A reader that acquired a refcounted snapshot just before Close still holds its refs, so each
// segment's fd is closed only once that last reader releases — no use-after-free reading a closed fd.
// retireKeepFile (unlike the merge path's retire) does NOT unlink the files: they are still live in
// the on-disk MANIFEST and must survive for the next Open. Callers should still quiesce writers (Close
// is terminal), but a racing reader is handled correctly rather than crashing.
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
	s.stopMergeLoop() // P9: drain + stop the background merger before we close any segment fd
	s.mu.Lock()
	segs := s.segs
	s.segs = nil
	s.snap.Store(emptySnapshot) // P9: drop the published set FIRST so no late reader acquires a ref
	s.mu.Unlock()
	// Drop the published ref on each segment via the refcount path: the fd is closed (file kept) only
	// once the last in-flight reader that still holds a ref has released it (deferred, no closed-fd read).
	for _, seg := range segs {
		seg.retireKeepFile()
	}
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
// [I]/[F] segment bytes are NOT reclaimed synchronously — Search/GetDocs return empty for an absent
// tableId immediately (segments are immutable, no DeletePrefix). Instead, when AutoMerge is on,
// DeleteTable SCHEDULES a covering merge (design §6/§8, P8): a covering merge drops keys for
// tableIds no longer in the catalog, so the dead table's bytes are reclaimed even if its segments
// sit at the bottom level with no further writes. The schedule is enqueued (q.AddFunc) so the
// merge runs off the synchronous DeleteTable return.
func (s *Store) DeleteTable(tableId int) error {
	err := s.q.RunFunc(func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.man.Tables, tableId)
		delete(s.head, tableId)
		delete(s.liveByTable, tableId) // drop the table's live-pair partition in O(1) (spec §4.2.3)
		return writeManifest(s.dir, s.man)
	})
	if err != nil {
		return err
	}
	// P9: schedule a covering merge on the background merge goroutine (non-blocking trigger), not via
	// s.q.AddFunc — a covering merge dropping the dead table's keys is what reclaims its bytes even if
	// its segments sit at the bottom level with no further writes (design §6/§8).
	s.triggerMerge(true)
	return nil
}

// tableInfo returns the catalog entry for tableId, if present. A small read helper used by the
// tests and (later) Search to reject an absent/deleted table.
func (s *Store) tableInfo(tableId int) (tableInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ti, ok := s.man.Tables[tableId]
	return ti, ok
}
