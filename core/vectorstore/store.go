package vectorstore

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// headSegID is the reserved segId for the in-memory head in the global
// docId→segId map. Sealed segments use ids >= 1 (the manifest version space).
const headSegID = segID(0)

// defaultMaxSegSize is the head row-count seal trigger. The architecture's
// adaptive ~10M/dim target (§4.9) is measure-don't-assert; this fixed default is
// a safe placeholder the operator can override via the maxSegSize field. Tunable
// later when the adaptive sizing lands.
const defaultMaxSegSize = 50000

// Store is the segmented records layer (Phase 2): one in-memory brute head plus
// an ordered set of immutable on-disk sealed segments, each with a tombstone
// bitmap and (when indexed) a per-segment HNSW. Writes go to the head; Delete is
// routed to the owning segment via the global docId→segId map. A manifest +
// head WAL provide crash recovery. All public methods are serialized by mu.
type Store struct {
	mu      sync.RWMutex
	metric  Metric
	dir     string
	kv      kv.Store
	alloc   *idtable.Allocator
	seg     *segment // the head
	wal     *WAL
	idToDoc map[string]int64 // derived from WAL replay; lets reads avoid allocating

	sealed   []*sealedSegment      // live sealed segments, by attach order
	sealedID []segID               // parallel: sealed[i] has segId sealedID[i]
	docToSeg map[int64]segID       // global docId → owning segId (headSegID for head)
	graphs   map[segID]*builtIndex // segId → built index (absent until indexed)
	gcfg     graphConfig           // the single index's HNSW config
	mcfg     mergeConfig           // space-reclamation policy tunables (Phase 4)
	nextSeg  segID                 // next sealed segId to assign

	maxSegSize int // head row-count auto-seal trigger (defaultMaxSegSize unless overridden)

	buildMu sync.Mutex // serializes builder graph install + manifest rewrite
	// quiescence accounting (replaces two sync.WaitGroups). nInflightBuilds and
	// nInflightMerges count in-flight background builds/merges; every increment,
	// decrement, AND wait happens under s.mu, so the WaitGroup add-after-wait hazard
	// is structurally impossible — a waiter holding s.mu cannot observe a "zero"
	// counter concurrently with an Add (the Add also needs s.mu). quiesced is
	// Broadcast on every decrement so waiters re-check. This is what lets a build
	// completion safely re-trigger a merge (Phase-4 finding #1): the build→merge
	// edge would race a plain merges.Wait() if these were WaitGroups (a builds-
	// tracked goroutine calling merges.Add after merges.Wait already returned at a
	// zero counter), but under s.mu serialization it cannot.
	quiesced        *sync.Cond
	nInflightBuilds int
	nInflightMerges int
	closing         bool // set under s.mu in Close; gates new merge/build launches so a
	//                       launch never races teardown (appendix #1)

	manifestVersion uint64 // monotonic manifest version (bumped per rewrite)

	// Test-only crash seams in mergeAndPublish (nil in production). They let a test
	// force the two durability-critical crash windows on the REAL merge path
	// (appendix #4/#5/#7), instead of fabricating a stray dir that only re-tests the
	// Phase-2 orphan sweep. Each is passed the live mergePlan and, returning true,
	// makes the merge return as if the process died at that exact point:
	//   - testHookAfterWrite: AFTER every output bucket is written+fsynced+reopened,
	//     BEFORE the manifest swap → outputs on disk, unreferenced (crash-before-swap).
	//   - testHookAfterSwap: AFTER writeManifestLocked committed, BEFORE os.RemoveAll
	//     of the old input dirs → inputs leftover, unreferenced (crash-after-swap),
	//     and any reconcile-tombstone from step 2a already msync'd to tomb.dat
	//     (the highest-risk reconciliation durability window, appendix #7).
	testHookAfterWrite func(p *mergePlan) bool
	testHookAfterSwap  func(p *mergePlan) bool
	// testHookInMergeWindow (nil in production) is invoked in mergeAndPublish AFTER
	// the off-lock output write+reopen and BEFORE the swap takes buildMu/s.mu — the
	// exact window in which a concurrent Delete/Put can mutate an input doc's
	// liveness. Unlike the two crash seams it does NOT abort: it lets a test block
	// here on a real concurrent goroutine, deterministically forcing the reconcile
	// path (step 2a) to run against mutations that landed mid-merge. It holds NO
	// lock, so the concurrent mutation can take s.mu freely.
	testHookInMergeWindow func(p *mergePlan)
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
		metric:   opts.Metric,
		dir:      opts.Dir,
		kv:       opts.KV,
		alloc:    alloc,
		seg:      newSegment(opts.Metric),
		wal:      w,
		idToDoc:  make(map[string]int64),
		docToSeg: make(map[int64]segID),
		graphs:   make(map[segID]*builtIndex),
		gcfg:     graphConfig{}.withDefaults(),
		mcfg:     mergeConfig{}.withDefaults(),
		nextSeg:  1,

		maxSegSize: defaultMaxSegSize,
	}
	s.quiesced = sync.NewCond(&s.mu)
	if err := s.recover(); err != nil {
		w.Close()
		alloc.Close()
		return nil, err
	}
	return s, nil
}

// recover rebuilds the full segmented state. A missing manifest means a fresh or
// Phase-1 store: just replay the head WAL. Otherwise: load the manifest, mmap
// every sealed segment, rebuild the global docId→segId from each segment's
// slotDoc over LIVE slots (the persisted tombstone bitmap is authoritative),
// reopen each indexed segment's graph, then replay the head WAL last so a head
// put that supersedes a sealed old slot resolves against the now-populated
// docToSeg.
//
// Crash-safety (appendix #8/#19): the manifest is authoritative for every
// segment it lists. If a crash happened after the manifest swap but BEFORE the
// head WAL was truncated, the old WAL still carries the just-sealed pre-seal
// records. Re-homing those into the head would tombstone the (immutable) sealed
// slot and double-store the doc. So replay SKIPS any record whose docId is still
// live in a sealed segment loaded from the manifest — it is already durable
// there. A genuine post-seal Update already tombstoned the sealed slot at Put
// time, so that doc is NOT live in the segment and is correctly re-homed.
func (s *Store) recover() error {
	m, err := readManifest(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Fresh / Phase-1 store, OR a crash during the very FIRST seal (segment
			// dir written, manifest not yet committed). Sweep orphans before the
			// head-only WAL replay: with no committed manifest, every seg-* dir is
			// unreferenced and must be reclaimed, else a first-seal crash leaks a
			// half-written segment dir forever (appendix #3).
			if serr := s.sweepOrphansLocked(&manifest{}); serr != nil {
				return serr
			}
			return s.replay()
		}
		return err
	}
	s.manifestVersion = m.Version
	// The on-disk vector form is metric-dependent (cosine stores unit+|v|;
	// dot/euclid store raw), so reopening sealed segments under a different metric
	// would silently mis-read every vector. Options.Metric is operator-settable —
	// reject a mismatch with a clear error rather than wrong-restore.
	if s.metric != m.Metric {
		return fmt.Errorf("vectorstore: metric mismatch: store was sealed under %s but opened with %s", m.Metric, s.metric)
	}
	for _, e := range m.Segments {
		segDir := filepath.Join(s.dir, segDirName(e.SegID, e.Gen))
		ss, oerr := openSealedSegment(segDir, s.metric)
		if oerr != nil {
			return oerr
		}
		s.sealed = append(s.sealed, ss)
		s.sealedID = append(s.sealedID, e.SegID)
		if e.SegID >= s.nextSeg {
			s.nextSeg = e.SegID + 1
		}
		for slot := 0; slot < ss.count(); slot++ {
			if !ss.tombGet(slot) {
				s.docToSeg[ss.slotDoc(slot)] = e.SegID
			}
		}
		if e.State == segIndexed {
			g, gerr := openGraphFile(segDir, ss)
			if gerr != nil {
				return gerr
			}
			s.graphs[e.SegID] = newBuiltIndex(g, s.gcfg)
		}
	}
	// Sweep any seg-* directory (and a stranded manifest.tmp) the committed
	// manifest does not reference — a crash mid-seal leaves half-written files
	// the manifest swap never committed to (appendix #3).
	if err := s.sweepOrphansLocked(m); err != nil {
		return err
	}
	// Head WAL replay last (against the now-populated docToSeg), exactly once
	// (appendix #15: replay must not run twice — that would double-apply every
	// head record). Then resume builds for any segment left pending (crash
	// mid-build); the resume runs AFTER replay so the build reads a consistent
	// tombstone view (a replayed head Delete may tombstone a sealed slot).
	if err := s.replay(); err != nil {
		return err
	}
	for i, sid := range s.sealedID {
		if s.graphs[sid] == nil {
			segDir := filepath.Join(s.dir, segDirName(sid, 0))
			s.buildBeginLocked()
			go s.buildAndPublish(sid, segDir, s.sealed[i])
		}
	}
	return nil
}

// sweepOrphansLocked removes any seg-* directory on disk not referenced by the
// loaded manifest, plus a stranded manifest.tmp. A crash mid-seal leaves a
// half-written segment the manifest never committed to; the manifest swap is the
// commit point, so anything not in it is an orphan (§4.8, appendix #3). The
// stranded manifest.tmp from a crashed write is harmless to readManifest (which
// reads "manifest", not the tmp) but is cleaned here so it cannot accumulate or
// be mistaken for a committed manifest later. Caller holds s.mu.
func (s *Store) sweepOrphansLocked(m *manifest) error {
	referenced := make(map[string]bool, len(m.Segments))
	for _, e := range m.Segments {
		referenced[segDirName(e.SegID, e.Gen)] = true
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		name := ent.Name()
		if name == "manifest.tmp" && !ent.IsDir() {
			if err := fsRemove(filepath.Join(s.dir, name)); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if !ent.IsDir() || len(name) < 4 || name[:4] != "seg-" {
			continue
		}
		if referenced[name] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// Metric returns the store's distance metric.
func (s *Store) Metric() Metric { return s.metric }

// --- quiescence accounting (cond-based; all under s.mu) ----------------------
//
// These replace two sync.WaitGroups. Keeping the counts under s.mu (the same lock
// the cond uses) makes Add-after-Wait impossible: a waiter blocked in
// quiesced.Wait() has released s.mu, but it re-checks the counter under s.mu on
// every wakeup, and an Add holds s.mu, so the waiter can never "miss" an Add and
// return early. This is the property that lets a build completion re-trigger a
// merge (finding #1) without the WaitGroup add-after-wait race.

// buildBeginLocked counts one background build as in-flight. Caller holds s.mu.
func (s *Store) buildBeginLocked() { s.nInflightBuilds++ }

// buildDone marks one background build complete and wakes any waiter.
func (s *Store) buildDone() {
	s.mu.Lock()
	s.nInflightBuilds--
	s.quiesced.Broadcast()
	s.mu.Unlock()
}

// mergeBeginLocked counts n background merges as in-flight. Caller holds s.mu.
func (s *Store) mergeBeginLocked(n int) { s.nInflightMerges += n }

// mergeDone marks one background merge complete and wakes any waiter.
func (s *Store) mergeDone() {
	s.mu.Lock()
	s.nInflightMerges--
	s.quiesced.Broadcast()
	s.mu.Unlock()
}

// waitMergesLocked blocks until no merge is in flight. Caller holds s.mu.
func (s *Store) waitMergesLocked() {
	for s.nInflightMerges > 0 {
		s.quiesced.Wait()
	}
}

// waitQuiescentLocked blocks until neither a merge NOR a build is in flight.
// Because a merge can spawn builds and a build can re-trigger a merge (finding #1),
// it loops until BOTH counters are simultaneously zero under s.mu — the only
// moment no further background work can self-spawn (every spawn increments a
// counter under s.mu before its parent's Done decrements). Caller holds s.mu.
func (s *Store) waitQuiescentLocked() {
	for s.nInflightMerges > 0 || s.nInflightBuilds > 0 {
		s.quiesced.Wait()
	}
}

// Close waits for any in-flight background merges and builds, then flushes and
// releases the WAL, sealed-segment mmaps, and idtable. It sets s.closing under
// s.mu so every launch site (sealLocked/mergeNow/Compact/maybeMergeLocked) refuses
// to start new work, then drains to quiescence (no merge AND no build in flight)
// under the same s.mu the quiescence cond uses — so a launch can never race the
// drain. Because both the swap path (mergeAndPublish) and the build path
// (buildAndPublish) acquire s.mu transiently to do their accounting Done +
// Broadcast, Close must NOT hold s.mu across the drain except as the cond requires
// (quiesced.Wait releases it while parked). Closing the allocator commits any
// pending id→docId mappings re-driven during replay, making the recovered state
// durable.
func (s *Store) Close() error {
	s.mu.Lock()
	s.closing = true
	s.waitQuiescentLocked()
	defer s.mu.Unlock()
	for _, ss := range s.sealed {
		ss.close()
	}
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

// lookupDocID resolves a string id to its docId WITHOUT allocating, for the read
// path (Get). It consults the in-memory idToDoc cache first; on a miss it reads
// the idtable's durable key→id entry directly from the KV (the same key encoding
// the allocator uses: {idtableKeyTypeKey}+id, value = 8-byte big-endian docId).
// This is required so a sealed doc — whose Put record was truncated from the head
// WAL at seal time and whose string id is therefore absent from idToDoc after
// recovery — is still resolvable on Get. A truly-unknown id (never Put) yields
// found=false and, unlike GetId, allocates nothing.
func (s *Store) lookupDocID(id string) (int64, bool, error) {
	if d, ok := s.idToDoc[id]; ok {
		return d, true, nil
	}
	key := make([]byte, 1+len(id))
	key[0] = idtableKeyTypeKey
	copy(key[1:], id)
	v, err := s.kv.Get(key)
	if err != nil {
		return 0, false, err
	}
	if len(v) != 8 {
		return 0, false, nil // missing (nil) or malformed → unknown id
	}
	return int64(binary.BigEndian.Uint64(v)), true, nil
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
			if r.badVersion {
				return fmt.Errorf("vectorstore: incompatible WAL record format (pre-Phase-5 WAL not supported)")
			}
			// Re-establish id→docId in the allocator and the derived map.
			if _, err := s.docIDForAlloc(r.ID); err != nil {
				return err
			}
			s.idToDoc[r.ID] = r.DocID
			// Crash-window guard (appendix #8/#19): if this docId is still LIVE in a
			// sealed segment loaded from the manifest, this is a pre-seal record the
			// seal already folded into that segment; the WAL just wasn't truncated
			// before the crash. Skip it — re-homing would tombstone the immutable
			// sealed slot and double-store the doc.
			if prev, ok := s.docToSeg[r.DocID]; ok && prev != headSegID {
				if ss := s.sealedByID(prev); ss != nil {
					if _, live := ss.slotOfDoc(r.DocID); live {
						return nil
					}
				}
			}
			s.docToSeg[r.DocID] = headSegID
			s.applyPut(r)
		case recDelete:
			d := decodeDelete(payload)
			if _, err := s.docIDForAlloc(d.ID); err != nil {
				return err
			}
			s.idToDoc[d.ID] = d.DocID
			if segId, ok := s.docToSeg[d.DocID]; ok {
				if segId == headSegID {
					s.seg.tombstone(int(d.Slot))
				} else if ss := s.sealedByID(segId); ss != nil {
					if slot, found := ss.slotOfDoc(d.DocID); found {
						_ = ss.tombstoneSlot(slot)
					}
				}
				delete(s.docToSeg, d.DocID)
			}
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
	// Cross-segment update (appendix #7): if this docId currently lives in a
	// SEALED segment, tombstone that sealed slot so it does not stay live in both
	// the sealed graph and the head. The new copy lands in the head below; the WAL
	// record (already fsynced) drives the same tombstone on replay.
	if prev, ok := s.docToSeg[docID]; ok && prev != headSegID {
		if ss := s.sealedByID(prev); ss != nil {
			if slot, found := ss.slotOfDoc(docID); found {
				if err := ss.tombstoneSlot(slot); err != nil {
					return err
				}
			}
		}
	}
	s.idToDoc[id] = docID
	s.docToSeg[docID] = headSegID
	s.applyPut(rec)

	// Auto-seal when the head fills (§4.2): freeze it into a sealed segment and
	// start a fresh head, so callers never have to call Seal() explicitly. Put
	// already holds s.mu, which sealLocked requires; sealLocked publishes the
	// segment pending and spawns the background builder, so Put latency stays O(1)
	// — the (slow) HNSW build happens off the write path.
	if len(s.seg.slotDoc) >= s.maxSegSize {
		if err := s.sealLocked(); err != nil {
			return err
		}
	}
	return nil
}

// Get returns the original (restored) vector and payload for id from its owning
// segment (head or sealed). Reads never allocate a docId: an unknown id (never
// Put) returns found=false. The returned vector and payload are fresh copies the
// caller may mutate freely.
func (s *Store) Get(id string) (v []float32, payload []byte, found bool, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	docID, ok, err := s.lookupDocID(id)
	if err != nil {
		return nil, nil, false, err
	}
	if !ok {
		return nil, nil, false, nil
	}
	segId, ok := s.docToSeg[docID]
	if !ok {
		return nil, nil, false, nil
	}
	if segId == headSegID {
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
		return out, append([]byte(nil), pl...), true, nil
	}
	ss := s.sealedByID(segId)
	if ss == nil {
		return nil, nil, false, nil
	}
	slot, found2 := ss.slotOfDoc(docID)
	if !found2 {
		return nil, nil, false, nil
	}
	stored, norm, pl, live := ss.read(slot)
	if !live {
		return nil, nil, false, nil
	}
	out := append([]float32(nil), s.metric.restore(stored, norm)...)
	return out, append([]byte(nil), pl...), true, nil
}

// Delete tombstones id's current slot in its owning segment (head or sealed).
// Deleting an unknown or already-deleted id is a no-op. The id↔docId mapping is
// left in place; a later Put reuses the same docId (in the head). For a sealed
// segment the tombstone is persisted in that segment's mmap'd bitmap (durable on
// return); for the head it is a WAL-protected in-memory tombstone.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	docID, ok, err := s.lookupDocID(id)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	segId, ok := s.docToSeg[docID]
	if !ok {
		return nil
	}
	if segId == headSegID {
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
		delete(s.docToSeg, docID)
		return nil
	}
	// Sealed segment: tombstone is persisted in the segment's mmap'd bitmap.
	ss := s.sealedByID(segId)
	if ss == nil {
		return nil
	}
	slot, found := ss.slotOfDoc(docID)
	if !found {
		return nil
	}
	if err := ss.tombstoneSlot(slot); err != nil {
		return err
	}
	delete(s.docToSeg, docID)
	return nil
}

// Search returns the k nearest live records to q under the store's metric,
// merging every leg into one shared top-k heap: the head (brute), each pending
// sealed segment (brute over its live slots), and each indexed sealed segment
// (its HNSW, post-filtered by that segment's tombstone bitmap — the immutable
// graph can return tombstoned nodes, the single most important correctness
// gotcha). All legs emit exact same-metric distances into one heap; no cross-leg
// dedup is needed because a docId is live in exactly one segment. Results are in
// docId space, ascending by distance. An empty store returns (nil, nil).
func (s *Store) Search(q []float32, k int) ([]SearchResult, error) {
	if k <= 0 {
		return nil, errors.New("vectorstore: k must be positive")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := validateVector(q, s.searchDimLocked(), s.metric); err != nil {
		return nil, err
	}
	pq, _ := s.metric.prepare(q)
	tk := newTopK(k)

	// Head leg (brute).
	s.seg.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		tk.offer(SearchResult{DocID: docID, Distance: s.metric.distance(stored, pq)})
	})

	// Sealed legs.
	for i, ss := range s.sealed {
		bi := s.graphs[s.sealedID[i]]
		if bi == nil {
			// Pending: brute over the segment's live slots.
			ss.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
				tk.offer(SearchResult{DocID: docID, Distance: s.metric.distance(stored, pq)})
			})
			continue
		}
		// Indexed: reuse the per-segment hnswIndex built once at install/open
		// (appendix #26), then drop any hit whose docId is no longer live in the
		// segment — the immutable graph still contains post-seal-tombstoned nodes.
		// Over-fetch by the segment's tombstone count so k LIVE hits survive the
		// post-filter: fetching exactly k and then dropping tombstones makes a
		// heavily-deleted segment under-return (recall ≈ 1-delFrac). Inflating k by
		// the live-tombstone count restores the k live results the merge heap needs
		// (the graph caps its own ef at the available node count, so an over-large
		// fetchK is harmless).
		fetchK := k + ss.tombCount()
		hits, err := bi.idx.search(q, fetchK)
		if err != nil {
			return nil, err
		}
		for _, h := range hits {
			if _, live := ss.slotOfDoc(h.DocID); !live {
				continue // tombstoned after seal → exclude
			}
			tk.offer(h)
		}
	}

	out := tk.sorted()
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// searchDimLocked returns the dimension to validate the query against: the head's
// learned dim if non-zero, else the first sealed segment's dim, else 0 (empty).
// Caller holds s.mu (R or W).
func (s *Store) searchDimLocked() int {
	if s.seg.dim != 0 {
		return s.seg.dim
	}
	if len(s.sealed) > 0 {
		return s.sealed[0].dim
	}
	return 0
}

// sealedByID returns the live sealed segment with segId, or nil. Caller holds s.mu.
func (s *Store) sealedByID(id segID) *sealedSegment {
	for i, sid := range s.sealedID {
		if sid == id {
			return s.sealed[i]
		}
	}
	return nil
}

// attachSealedForTest installs a sealed segment under segId and empties the head,
// mirroring what the seal pipeline does, so tests can exercise multi-segment
// routing before the full Seal path exists. Live sealed docs are (re)homed to the
// new segment. Test-only (no manifest write).
func (s *Store) attachSealedForTest(ss *sealedSegment, id segID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for slot := 0; slot < ss.count(); slot++ {
		if !ss.tombGet(slot) {
			s.docToSeg[ss.slotDoc(slot)] = id
		}
	}
	s.sealed = append(s.sealed, ss)
	s.sealedID = append(s.sealedID, id)
	s.seg = newSegment(s.metric)
	if id >= s.nextSeg {
		s.nextSeg = id + 1
	}
}

// Seal freezes the current head into a new immutable sealed records-segment on
// disk, atomically updates the manifest (head→new sealed seg, state pending,
// fresh empty head), truncates the head WAL, then spawns a BACKGROUND build of
// the segment's HNSW. The records-segment is durable before the manifest swap and
// the WAL truncate, so a crash never loses a durably-acked write. Seal returns as
// soon as the segment is published pending — the store is immediately writable
// (fresh head) and searchable (pending segment served by its brute leg). An empty
// head is a no-op.
func (s *Store) Seal() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sealLocked()
}

func (s *Store) sealLocked() error {
	if len(s.seg.slotDoc) == 0 {
		return nil // nothing to seal
	}
	id := s.nextSeg
	segDir := filepath.Join(s.dir, segDirName(id, 0))

	// (1) Dump head → sealed records-segment files (fsync, fast, durable).
	if err := writeSealedSegment(segDir, s.seg); err != nil {
		return err
	}
	ss, err := openSealedSegment(segDir, s.metric)
	if err != nil {
		return err
	}

	// (2) Publish as PENDING + fresh head + atomic manifest swap.
	s.sealed = append(s.sealed, ss)
	s.sealedID = append(s.sealedID, id)
	s.nextSeg++
	for slot := 0; slot < ss.count(); slot++ {
		if !ss.tombGet(slot) {
			s.docToSeg[ss.slotDoc(slot)] = id
		}
	}
	s.seg = newSegment(s.metric)
	// Make the idtable's string→docId mappings + nextId counter durable BEFORE the
	// manifest swap and the WAL reset below. The head WAL is the only other record
	// of these mappings; once wal.Reset truncates it, the just-sealed docs'
	// identities exist solely in the idtable. The allocator commits lazily (5s tick
	// / Close), so without this synchronous commit a crash between Seal and the next
	// tick loses every sealed doc's mapping → phantom docs (searchable but Get/Delete
	// not-found) + docId collisions on re-Put. Order is load-bearing: idtable durable
	// → manifest durable → WAL truncated.
	if err := s.alloc.Commit(); err != nil {
		return err
	}
	if err := s.writeManifestLocked(); err != nil {
		return err
	}

	// (3) Truncate the old head WAL — the writes it carried are now in the durable
	// sealed segment + manifest.
	if err := s.wal.Reset(); err != nil {
		return err
	}

	// (4) Build the graph in the BACKGROUND, off the write lock. The sealed segment
	// is immutable except its tombstone bitmap, which the builder reads under the
	// segment's tomb lock (eachLive), so the build needs no lock on the store.
	//
	// Gate on s.closing (appendix #1): Close sets s.closing under s.mu, THEN waits
	// builds/merges (without s.mu). A Put/Seal racing Close must not spawn a build
	// here after Close's builds.Wait() has passed — that is a WaitGroup add-after-
	// wait AND would let Close's segment-close() free an mmap this builder is mid-
	// read (SIGSEGV). Since both this path and Close's closing=true write are under
	// s.mu, skipping the spawn when closing closes the window. The segment is durable
	// + pending in the manifest, so recover() resumes its build on the next Open.
	if s.closing {
		return nil
	}
	s.buildBeginLocked()
	go s.buildAndPublish(id, segDir, ss)

	// (5) Opportunistic background reclamation: a fresh seal may push a size tier to
	// fanout (growth) or expose a deflated segment (delete-driven). maybeMergeLocked
	// launches any qualifying merges off the write path; it only picks already-INDEXED
	// inputs, so it never touches the segment we just sealed above (still pending) and
	// never blocks Put/Seal. Healthy stores no-op. (Phase 4, architecture §4.9.)
	s.maybeMergeLocked()
	return nil
}

// buildAndPublish builds the HNSW for a pending sealed segment off the write
// path, then installs the index and flips the manifest to indexed. The build
// failure path drops the error: the segment stays pending → still brute-searched,
// still correct; recovery (or a future RebuildVectorIndex) retries. buildMu
// serializes the install+manifest rewrite across concurrent builders so two
// flips never race a single manifest file.
func (s *Store) buildAndPublish(id segID, segDir string, ss *sealedSegment) {
	defer s.buildDone()
	gs, err := buildSegmentGraph(segDir, ss, s.gcfg)
	if err != nil {
		return // stays pending; brute leg keeps results correct
	}
	bi := newBuiltIndex(gs, s.gcfg)
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.graphs[id] = bi
	_ = s.writeManifestLocked()

	// Re-evaluate the merge policy on the pending→indexed flip (Phase-4 finding #1).
	// The on-seal trigger (sealLocked → maybeMergeLocked) cannot roll up the K-th of
	// K full maxSegSize segments: when its OWN seal fires the trigger it is still
	// pending, so planReclamationLocked sees only K-1 INDEXED tier peers and the
	// anti-thrash guard skips the under-fanout tier — and no later seal/write is
	// guaranteed to follow. This segment just became indexed, so the tier may now
	// have reached fanout; re-running the policy here is the growth driver's missing
	// edge. maybeMergeLocked only launches off-lock mergeAndPublish goroutines (it
	// never calls sealLocked/buildAndPublish synchronously) and only picks already-
	// INDEXED inputs, so it is non-reentrant-safe under the buildMu+s.mu we already
	// hold and never blocks the write path. Its mergeBeginLocked increment happens
	// under s.mu BEFORE this function's deferred buildDone decrement, so a concurrent
	// waitQuiescentLocked never observes a false "quiescent" between the build
	// finishing and the merge it spawned being counted. Gated by s.closing inside
	// maybeMergeLocked so no launch races Close's drain (appendix #1).
	s.maybeMergeLocked()
}

// WaitForIndex blocks until every pending sealed-segment build has finished AND
// every merge they may have spawned/re-triggered has settled — i.e. the store is
// quiescent (no build and no merge in flight). It drains both counters under s.mu
// in a single wait: a merge spawns its outputs' builds and a completed build can
// re-trigger a merge (finding #1), so the only safe stopping point is both
// counters simultaneously zero. Because all begin/done/wait happen under s.mu, a
// caller can never miss a spawn that races the wait (the WaitGroup add-after-wait
// hazard, appendix #1).
func (s *Store) WaitForIndex() error {
	s.mu.Lock()
	s.waitQuiescentLocked()
	s.mu.Unlock()
	return nil
}

// isIndexedForTest reports whether sealed segment id has its graph installed.
func (s *Store) isIndexedForTest(id segID) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.graphs[id] != nil
}

// writeManifestLocked rewrites the manifest from the current segment set. Caller
// holds s.mu. The manifest Version is bumped exactly once per rewrite
// (appendix #19 — the draft double-incremented with a dead assignment).
func (s *Store) writeManifestLocked() error {
	s.manifestVersion++
	m := &manifest{Version: s.manifestVersion, Head: headSegID, Metric: s.metric}
	for i, ss := range s.sealed {
		st := segPending
		if s.graphs[s.sealedID[i]] != nil {
			st = segIndexed
		}
		m.Segments = append(m.Segments, segmentEntry{
			SegID:     s.sealedID[i],
			Gen:       0,
			VecCount:  uint64(ss.count()),
			TombCount: uint64(ss.tombCount()),
			State:     st,
		})
	}
	return writeManifest(s.dir, m)
}

// segDirName derives the on-disk directory name for a sealed segment (§4.8: paths
// are derived, not stored).
func segDirName(id segID, gen uint32) string {
	return "seg-" + itoaSeg(int64(id)) + "-" + itoaSeg(int64(gen))
}

func itoaSeg(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	p := len(b)
	for v > 0 {
		p--
		b[p] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
