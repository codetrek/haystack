package vectorstore

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"

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

// defaultIndexName is the reserved name of the vector index that preserves the
// Phases 1-5 single-index behavior. It always carries the store's primary
// (records) metric and cannot be re-Created or Dropped (§4.7).
const defaultIndexName = "default"

// defaultMaxSegSize is the head row-count seal trigger. The architecture's
// adaptive ~10M/dim target (§4.9) is measure-don't-assert; this fixed default is
// a safe placeholder the operator can override via the maxSegSize field. Tunable
// later when the adaptive sizing lands.
const defaultMaxSegSize = 50000

// defaultAttrSearchT is the per-segment |S_seg| threshold under which a filtered
// search uses the exact brute-S leg (iterate the matching slots) instead of the
// graph∩S leg (HNSW filter-during-traversal). Like defaultMaxSegSize it is a
// measure-don't-assert placeholder (§6 "阈值 T 按段内 |S_seg| 判") — TBD by
// measurement, not a load-bearing magic number; tests pin Store.attrSearchT
// per-case to force a specific branch.
const defaultAttrSearchT = 512

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

	sealed   []*sealedSegment // live sealed segments, by attach order
	sealedID []segID          // parallel: sealed[i] has segId sealedID[i]
	docToSeg map[int64]segID  // global docId → owning segId (headSegID for head)
	// indexes maps a named vector index → its (cfg, metric, per-segment graphs).
	// The reserved "default" index carries the store's primary metric and preserves
	// Phases 1-5 behavior byte-identically. Each vindex.graphs is keyed by segID; an
	// absent key means that (index, segment) is pending → served by the brute leg
	// (§4.7). This replaces the Phase-2 single Store.graphs/gcfg.
	indexes map[string]*vindex
	mcfg    mergeConfig // space-reclamation policy tunables (Phase 4)
	nextSeg segID       // next sealed segId to assign

	maxSegSize int // head row-count auto-seal trigger (defaultMaxSegSize unless overridden)

	// attrDecls is the declared attr-index set (property → kind), mirroring the
	// manifest's AttrDecls. It is the source of truth at runtime for which payload
	// fields are indexed; every seal/merge builds the per-segment attr.dat for this
	// set, and recover() loads it from the manifest before opening segments.
	attrDecls map[string]AttrKind
	// attrSearchT is the per-segment |S_seg| brute-S/graph∩S threshold
	// (defaultAttrSearchT unless overridden). Tunable; tests pin it per-case.
	attrSearchT int
	// graphSDispatches counts how many times the filtered indexed leg took the
	// graph∩S branch (|S_seg| > attrSearchT). Test-only observability so the branch
	// dispatch can be pinned per-case (appendix #25) — brute-S is a correct superset
	// either way, so a pure oracle check cannot prove the graph∩S path ran. Atomic
	// so concurrent Search calls (each under s.mu.RLock) increment it race-free.
	graphSDispatches atomic.Uint64
	// headBruteS counts how many times the filtered HEAD leg used the head attr
	// index (s.seg.attr) to compute the matching member set S_head and bruted ONLY
	// that subset (architecture §6 head "brute-S"), rather than the full
	// every-live-slot evalPayload scan. Test-only observability: brute-S is a
	// correct superset of the full-scan answer, so a pure oracle check cannot prove
	// the headAttr leg actually ran. Atomic for the same reason as graphSDispatches.
	headBruteS atomic.Uint64

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

// testHookRecoverBeforeReopenWrite (nil in production) fires inside recover()'s
// Phase-A loop, immediately BEFORE each unlocked `vx.graphs[sid] = newBuiltIndex`
// reopen-write. It is a package-level var (not a Store field) because recover()
// runs inside Open(), before any caller holds the *Store — a regression test sets
// it to a delay so the slow reopen-write would, under the buggy single-loop
// recover, overlap a same-index Phase-B builder's locked vx.graphs write and trip
// the race detector / concurrent-map-write panic. With the two-phase recover, no
// builder goroutine exists yet, so even a widened reopen-write cannot race.
var testHookRecoverBeforeReopenWrite func()

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
		indexes: map[string]*vindex{
			// The "default" index carries the store's primary metric and reproduces
			// Phases 1-5 exactly (empty graphs map → every segment pending until built).
			defaultIndexName: {cfg: graphConfig{}.withDefaults(), metric: opts.Metric, graphs: make(map[segID]*builtIndex)},
		},
		mcfg:    mergeConfig{}.withDefaults(),
		nextSeg: 1,

		maxSegSize:  defaultMaxSegSize,
		attrDecls:   make(map[string]AttrKind),
		attrSearchT: defaultAttrSearchT,
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
	// Load the declared attr-index set BEFORE opening segments so each sealed
	// segment's attr index is loaded/rebuilt for the right field set (§6/§7).
	for _, d := range m.AttrDecls {
		s.attrDecls[d.Property] = d.Kind
	}
	// Reconstruct the named indexes from the manifest BEFORE opening segments. A v4
	// manifest always carries at least the "default" index; synthesize it if somehow
	// absent (an empty Indexes block, e.g. a hand-built crash-window manifest) so the
	// default path is always present. Each vindex starts with an empty graphs map;
	// the reopen/resume pass below fills indexed graphs and spawns pending builds.
	if len(m.Indexes) == 0 {
		s.indexes = map[string]*vindex{
			defaultIndexName: {cfg: graphConfig{}.withDefaults(), metric: s.metric, graphs: make(map[segID]*builtIndex)},
		}
	} else {
		s.indexes = make(map[string]*vindex, len(m.Indexes))
		for _, ic := range m.Indexes {
			s.indexes[ic.Name] = newVindex(VectorIndexConfig{
				Type: ic.Type, Metric: ic.Metric, M: ic.M, EfConstruction: ic.EfConstruction, EfSearch: ic.EfSearch,
			})
		}
	}
	// Lookup of every (index, segment) build state from the v4 IndexSegs block,
	// keyed by "<index>\x00<segId>" (a NUL-joined string, robust to arbitrary index
	// names). An absent key defaults to segPending → resumed below.
	indexedState := make(map[string]segState, len(m.IndexSegs))
	for _, is := range m.IndexSegs {
		indexedState[is.Index+"\x00"+itoaSeg(int64(is.SegID))] = is.State
	}
	for _, e := range m.Segments {
		segDir := filepath.Join(s.dir, segDirName(e.SegID, e.Gen))
		ss, oerr := openSealedSegment(segDir, s.metric)
		if oerr != nil {
			return oerr
		}
		// Load (or rebuild-from-payload) the per-segment attr index for the declared
		// set. A missing/corrupt attr.dat is rebuilt by openAttrFile (derived floor).
		if len(s.attrDecls) > 0 {
			ss.attr, _ = openAttrFile(segDir, ss, s.attrDecls)
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
	}
	// Sweep any seg-* directory (and a stranded manifest.tmp) the committed
	// manifest does not reference — a crash mid-seal leaves half-written files
	// the manifest swap never committed to (appendix #3). Also sweep stray
	// graph-<name>.dat files for indexes no longer in the manifest (a crash after a
	// Drop's manifest commit but before its unlink hit disk leaves an orphan in a
	// LIVE seg dir; sweeping it here prevents a later re-Create from opening a stale
	// graph — appendix #21).
	if err := s.sweepOrphansLocked(m); err != nil {
		return err
	}
	// Head WAL replay last (against the now-populated docToSeg), exactly once
	// (appendix #15: replay must not run twice — that would double-apply every
	// head record). Then, per index, reopen its indexed graphs from disk and resume
	// every pending (index, segment) build (crash mid-build). The resume runs AFTER
	// replay so the build reads a consistent tombstone view (a replayed head Delete
	// may tombstone a sealed slot).
	if err := s.replay(); err != nil {
		return err
	}
	// Two phases, and the ORDER is load-bearing for data-race freedom. Phase A
	// reopens every indexed graph into vx.graphs; these writes run only here in the
	// single recover goroutine with no lock held, so they must not overlap any other
	// writer of that same Go map. Phase B then spawns buildAndPublish for the
	// still-pending (index, segment) pairs, and each builder ALSO writes vx.graphs —
	// but under s.mu (store.go's buildAndPublish install). Were the spawn interleaved
	// with the reopen (a single loop), an indexed segment's unlocked Phase-A write to
	// vx.graphs could race a same-index pending segment's builder write under s.mu:
	// no happens-before edge joins an unlocked write to a locked one, so -race fires
	// and concurrent map writes are a fatal runtime panic. Completing ALL Phase-A
	// reopen-writes before creating ANY builder goroutine removes the overlap: once a
	// builder exists, recover() performs no further unlocked vx.graphs write.
	type pendingBuild struct {
		name   string
		sid    segID
		segDir string
		ss     *sealedSegment
	}
	var pending []pendingBuild
	for name, vx := range s.indexes {
		for i, sid := range s.sealedID {
			segDir := filepath.Join(s.dir, segDirName(sid, 0))
			if indexedState[name+"\x00"+itoaSeg(int64(sid))] == segIndexed {
				g, gerr := openGraphFile(segDir, name, s.sealed[i])
				if gerr == nil {
					// Test-only seam (nil in production): widen the Phase-A reopen-write
					// window so a regression test can prove this unlocked write never
					// overlaps a Phase-B builder's locked vx.graphs write (the Task-8 race).
					if testHookRecoverBeforeReopenWrite != nil {
						testHookRecoverBeforeReopenWrite()
					}
					vx.graphs[sid] = newBuiltIndexFor(g, s.sealed[i], s.metric, vx.metric, vx.cfg)
					continue
				}
				// A torn/missing graph file falls through to a rebuild from records.
			}
			if vx.graphs[sid] == nil {
				pending = append(pending, pendingBuild{name, sid, segDir, s.sealed[i]})
			}
		}
	}
	// Phase B: every Phase-A reopen-write is now complete, so spawning builders here
	// cannot race them. Each buildAndPublish writes vx.graphs under s.mu.
	for _, pb := range pending {
		s.buildBeginLocked()
		go s.buildAndPublish(pb.name, pb.sid, pb.segDir, pb.ss)
	}
	return nil
}

// sweepOrphansLocked removes any seg-* directory on disk not referenced by the
// loaded manifest, plus a stranded manifest.tmp. A crash mid-seal leaves a
// half-written segment the manifest never committed to; the manifest swap is the
// commit point, so anything not in it is an orphan (§4.8, appendix #3). The
// stranded manifest.tmp from a crashed write is harmless to readManifest (which
// reads "manifest", not the tmp) but is cleaned here so it cannot accumulate or
// be mistaken for a committed manifest later. It also removes any stray
// graph-<name>.dat in a LIVE seg dir for an index NOT in the manifest: a crash
// after a Drop's manifest commit but before its unlink reached disk leaves such an
// orphan, and a later re-Create of that name must not open the stale graph
// (appendix #21). Caller holds s.mu.
func (s *Store) sweepOrphansLocked(m *manifest) error {
	referenced := make(map[string]bool, len(m.Segments))
	for _, e := range m.Segments {
		referenced[segDirName(e.SegID, e.Gen)] = true
	}
	// The set of index graph files the manifest still references, by file name.
	liveGraphFiles := make(map[string]bool, len(m.Indexes))
	for _, ic := range m.Indexes {
		liveGraphFiles[graphFileName(ic.Name)] = true
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
		if !referenced[name] {
			if err := os.RemoveAll(filepath.Join(s.dir, name)); err != nil {
				return err
			}
			continue
		}
		// A live seg dir: sweep stray graph-<name>.dat for indexes the manifest no
		// longer carries (orphaned by a Drop whose unlink did not survive the crash).
		if err := s.sweepStrayGraphsLocked(filepath.Join(s.dir, name), liveGraphFiles); err != nil {
			return err
		}
	}
	return nil
}

// sweepStrayGraphsLocked removes any graph-*.dat in segDir whose index name is not
// in liveGraphFiles (the manifest's referenced index set). Caller holds s.mu.
func (s *Store) sweepStrayGraphsLocked(segDir string, liveGraphFiles map[string]bool) error {
	entries, err := os.ReadDir(segDir)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		fn := ent.Name()
		if ent.IsDir() || len(fn) < len("graph-")+len(".dat") || fn[:len("graph-")] != "graph-" || fn[len(fn)-len(".dat"):] != ".dat" {
			continue
		}
		if liveGraphFiles[fn] {
			continue
		}
		if err := fsRemove(filepath.Join(segDir, fn)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// Metric returns the store's distance metric.
func (s *Store) Metric() Metric { return s.metric }

// ListVectorIndexes returns a read-only snapshot of every named index, sorted by
// name, with each index's config + per-segment build progress (architecture §7).
func (s *Store) ListVectorIndexes() []VectorIndexInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.indexes))
	for n := range s.indexes {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]VectorIndexInfo, 0, len(names))
	for _, n := range names {
		vx := s.indexes[n]
		indexed := 0
		for _, id := range s.sealedID {
			if vx.graphs[id] != nil {
				indexed++
			}
		}
		out = append(out, VectorIndexInfo{
			Name: n, Type: "hnsw", Metric: vx.metric,
			M: vx.cfg.M, EfConstruction: vx.cfg.EfConstruction, EfSearch: vx.cfg.EfSearch,
			Segments: len(s.sealedID), Indexed: indexed,
		})
	}
	return out
}

// IndexLag returns the pending build progress of a named index (architecture §7).
// An unknown index reports Exists=false with zero counts. The vector count sums
// the LIVE rows of each pending segment, the unit a WaitForIndex drain converges.
func (s *Store) IndexLag(name string) IndexLagInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	vx, ok := s.indexes[name]
	if !ok {
		return IndexLagInfo{}
	}
	li := IndexLagInfo{Exists: true}
	for i, sid := range s.sealedID {
		if vx.graphs[sid] == nil {
			li.PendingSegments++
			li.PendingVectors += s.sealed[i].count() - s.sealed[i].tombCount()
		}
	}
	return li
}

// attrDeclsSnapshotLocked returns a private copy of the declared attr set so an
// off-lock consumer (the merge write phase) reads a stable view while a concurrent
// CreateAttrIndex/DropAttrIndex (under s.mu) may mutate the live map. Caller holds
// s.mu.
func (s *Store) attrDeclsSnapshotLocked() map[string]AttrKind {
	if len(s.attrDecls) == 0 {
		return nil
	}
	cp := make(map[string]AttrKind, len(s.attrDecls))
	for k, v := range s.attrDecls {
		cp[k] = v
	}
	return cp
}

// CreateAttrIndex declares property as an indexed attr of kind, scans every sealed
// segment's payloads to build its per-segment bitmap (writing attr.dat), rebuilds
// the head's in-memory attr index over the new declared set, persists the
// declaration in the manifest (v3), and makes every future seal/merge build the
// index for this property. Idempotent on an already-declared property of the SAME
// kind; a kind change is an error.
func (s *Store) CreateAttrIndex(property string, kind AttrKind) error {
	if kind != Keyword && kind != Numeric {
		return fmt.Errorf("vectorstore: invalid attr kind %d", kind)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if k, ok := s.attrDecls[property]; ok {
		if k != kind {
			return fmt.Errorf("vectorstore: attr index %q already declared as kind %d", property, k)
		}
		return nil
	}
	s.attrDecls[property] = kind
	// Rebuild the head's in-memory index over the new declared set (over LIVE slots).
	s.seg.attr = newHeadAttr(s.attrDecls)
	for slot, pl := range s.seg.payloads {
		if !s.seg.tomb.get(slot) {
			s.seg.attr.index(slot, pl)
		}
	}
	// Scan + persist every sealed segment's attr.dat for the full declared set.
	for i, ss := range s.sealed {
		segDir := filepath.Join(s.dir, segDirName(s.sealedID[i], 0))
		ai := buildSegAttr(s.attrDecls, ss.count(), func(slot int) Payload {
			p, _ := ss.payloadDecoded(slot)
			return p
		})
		if err := writeAttrFile(segDir, ai, ss.count()); err != nil {
			return err
		}
		ss.attr = ai
	}
	return s.writeManifestLocked()
}

// DropAttrIndex removes property from the declared set, the manifest, the head
// index, and rewrites each segment's attr.dat without it. Records/payload/vectors/
// graph are untouched. Idempotent on an unknown property.
func (s *Store) DropAttrIndex(property string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.attrDecls[property]; !ok {
		return nil
	}
	delete(s.attrDecls, property)
	s.seg.attr = newHeadAttr(s.attrDecls)
	for slot, pl := range s.seg.payloads {
		if !s.seg.tomb.get(slot) {
			s.seg.attr.index(slot, pl)
		}
	}
	for i, ss := range s.sealed {
		segDir := filepath.Join(s.dir, segDirName(s.sealedID[i], 0))
		ai := buildSegAttr(s.attrDecls, ss.count(), func(slot int) Payload {
			p, _ := ss.payloadDecoded(slot)
			return p
		})
		if err := writeAttrFile(segDir, ai, ss.count()); err != nil {
			return err
		}
		ss.attr = ai
	}
	return s.writeManifestLocked()
}

// CreateVectorIndex declares a new named vector index over the SAME records as the
// existing indexes (segment boundaries are shared, §4.7). The index is born
// PENDING for every existing sealed segment (empty graphs map) → immediately
// queryable via the brute fallback in searchLocked → the background builder fills
// its per-segment graphs (pending→indexed). It mirrors CreateAttrIndex: validate,
// install in the s.indexes map, persist to the manifest (so a crash mid-build
// resumes via recover, gotcha 8), THEN spawn the builds. Idempotent on the same
// config; an existing name with a different config (or the reserved "default") is
// an error. v1 supports Type "hnsw" only.
func (s *Store) CreateVectorIndex(name string, cfg VectorIndexConfig) error {
	if cfg.Type != "hnsw" {
		return fmt.Errorf("vectorstore: unsupported index type %q (v1 supports \"hnsw\")", cfg.Type)
	}
	if name == "" {
		return errors.New("vectorstore: index name must be non-empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == defaultIndexName {
		return fmt.Errorf("vectorstore: index %q is reserved", name)
	}
	if existing, ok := s.indexes[name]; ok {
		want := graphConfigFromCfg(cfg)
		if existing.metric != cfg.Metric || existing.cfg != want {
			return fmt.Errorf("vectorstore: index %q already exists with a different config", name)
		}
		return nil // idempotent on identical config
	}
	if s.closing {
		return errors.New("vectorstore: store is closing")
	}
	s.indexes[name] = newVindex(cfg)
	// Persist the new index (pending for all segments) BEFORE spawning builds, so a
	// crash mid-build is resumed by recover() (same crash-safety as a pending seal).
	if err := s.writeManifestLocked(); err != nil {
		delete(s.indexes, name)
		return err
	}
	// Spawn one background build per existing sealed segment (every (index,seg) is
	// pending). buildBeginLocked under s.mu before the goroutine, so WaitForIndex
	// counts it (gotcha 4). Gated by s.closing above.
	for i, sid := range s.sealedID {
		segDir := filepath.Join(s.dir, segDirName(sid, 0))
		s.buildBeginLocked()
		go s.buildAndPublish(name, sid, segDir, s.sealed[i])
	}
	return nil
}

// DropVectorIndex removes a named index: its in-memory vindex, its graph-<name>.dat
// file in every sealed segment dir, and its manifest entries. Records, payload, and
// all OTHER indexes are untouched (architecture §4.7). It takes buildMu+s.mu so the
// removal never races a buildAndPublish writing the graphs map (buildAndPublish
// re-checks the index still exists after taking s.mu — gotcha 5). In-flight builds
// for this index harmlessly no-op on that re-check. The reserved "default" index
// cannot be dropped; an unknown name is a no-op.
func (s *Store) DropVectorIndex(name string) error {
	if name == defaultIndexName {
		return fmt.Errorf("vectorstore: cannot drop the reserved %q index", name)
	}
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.indexes[name]; !ok {
		return nil // unknown → no-op (idempotent)
	}
	delete(s.indexes, name)
	if err := s.dropGraphFilesLocked(name); err != nil {
		return err
	}
	return s.writeManifestLocked()
}

// dropGraphFilesLocked removes graph-<name>.dat from every sealed segment dir, then
// fsyncs each dir so the unlink is durable BEFORE the manifest commit that drops
// the index (appendix #21: the manifest must never be ahead of a non-durable
// unlink, else a crash leaves an orphan graph-<name>.dat with no manifest entry). A
// missing file is fine (the (index,seg) was still pending). Caller holds s.mu (and
// buildMu, so no builder is mid-install for this index).
func (s *Store) dropGraphFilesLocked(name string) error {
	for _, sid := range s.sealedID {
		segDir := filepath.Join(s.dir, segDirName(sid, 0))
		p := segFilePath(segDir, graphFileName(name))
		removed := true
		if err := fsRemove(p); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			removed = false // already absent (pending) → nothing to make durable
		}
		if removed {
			if err := fsyncDir(segDir); err != nil {
				return err
			}
		}
	}
	return nil
}

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
			if err := s.applyPut(r); err != nil {
				return err
			}
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
// slot, then append the new one. The WAL record carries the serialized payload
// blob; applyPut decodes it to the typed Payload the head stores. Shared by Put
// and replay.
func (s *Store) applyPut(r putRecord) error {
	if r.OldSlot >= 0 {
		s.seg.tombstone(int(r.OldSlot))
	}
	pl, err := decodePayload(r.Payload)
	if err != nil {
		return err
	}
	s.seg.append(r.DocID, r.Stored, r.Norm, pl)
	return nil
}

// Put inserts or replaces the vector and payload for id. It is crash-atomic: a
// single WAL record (the string id, its docId, the old slot to tombstone if any,
// and the new stored vector + norm + payload) is fsync'd before the in-memory
// state is mutated, so a crash either loses the whole Put or applies it whole on
// replay. The string→docId mapping is recovered from the same WAL record, so Put
// is fully durable on return without depending on idtable's lazy commit.
func (s *Store) Put(id string, v []float32, payload Payload) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateVector(v, s.seg.dim, s.metric); err != nil {
		return err
	}
	plBytes, err := encodePayload(payload)
	if err != nil {
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
	rec := putRecord{ID: id, DocID: docID, OldSlot: oldSlot, Stored: stored, Norm: norm, Payload: plBytes}
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
	if err := s.applyPut(rec); err != nil {
		return err
	}

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
func (s *Store) Get(id string) (v []float32, payload Payload, found bool, err error) {
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
		return out, pl.clone(), true, nil
	}
	ss := s.sealedByID(segId)
	if ss == nil {
		return nil, nil, false, nil
	}
	slot, found2 := ss.slotOfDoc(docID)
	if !found2 {
		return nil, nil, false, nil
	}
	stored, norm, plBytes, live := ss.read(slot)
	if !live {
		return nil, nil, false, nil
	}
	pl, derr := decodePayload(plBytes)
	if derr != nil {
		return nil, nil, false, derr
	}
	out := append([]float32(nil), s.metric.restore(stored, norm)...)
	return out, pl, true, nil
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

// Search returns the k nearest live records to q in the named index, under THAT
// index's metric, merging every leg into one shared top-k heap: the head (brute),
// each pending sealed segment (brute over its live slots), and each indexed sealed
// segment (its HNSW, post-filtered by that segment's tombstone bitmap — the
// immutable graph can return tombstoned nodes, the single most important
// correctness gotcha). All legs emit exact same-metric distances into one heap; no
// cross-leg dedup is needed because a docId is live in exactly one segment. Results
// are in docId space, ascending by distance. An empty store returns (nil, nil).
//
// The "default" index reproduces the Phases 1-5 behavior exactly. An unknown index
// name is an error. (Per-index metric — when vx.metric differs from the store's
// primary metric — is wired in Tasks 11-12; in this phase only "default" exists, so
// every leg runs under the primary metric.)
//
// filter is an optional metadata Predicate (Eq/In/Range/And; nil = unfiltered).
// A nil filter is exactly the pre-Phase-5 behavior (the indexed leg uses the
// HNSW). When non-nil, each leg restricts to the filter-MATCHING LIVE set: the
// head/pending legs apply a brute payload eval, and each indexed segment evaluates
// the filter to its segment-local slot bitmap S_seg, ANDs it with the live set
// (so deletes never leak — §6 "member AND live"), then ADAPTIVELY dispatches on
// |S_seg| vs the per-segment threshold attrSearchT: |S_seg| ≤ T brute-scores only
// those slots (exact), while |S_seg| > T runs the HNSW with a member predicate
// (filter-during-traversal — graph∩S). Both feed the same shared top-k heap.
func (s *Store) Search(index string, q []float32, k int, filter Predicate) ([]SearchResult, error) {
	if k <= 0 {
		return nil, errors.New("vectorstore: k must be positive")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	vx, ok := s.indexes[index]
	if !ok {
		return nil, fmt.Errorf("vectorstore: unknown index %q", index)
	}
	return s.searchLocked(vx, q, k, filter)
}

// searchLocked is the per-index Search core. The caller has already validated k>0,
// resolved the *vindex, and holds s.mu.RLock(). Every distance is computed under
// vx.metric (== s.metric for the "default" index, so the default path is identical
// to Phases 1-5).
func (s *Store) searchLocked(vx *vindex, q []float32, k int, filter Predicate) ([]SearchResult, error) {
	if err := validateVector(q, s.searchDimLocked(), vx.metric); err != nil {
		return nil, err
	}
	if err := validatePredicate(filter, s.attrDecls); err != nil {
		return nil, err
	}
	pq, _ := vx.metric.prepare(q)
	tk := newTopK(k)

	// Per-index-metric reconstruction for the BRUTE legs (head + pending/brute-S
	// sealed). The records store vectors in the PRIMARY metric's form; an index whose
	// metric differs must compute distance over the raw vector re-prepared under ITS
	// metric (§3.4 reconstruct-raw). dvec does that reconstruction once per row when
	// reindex is set; for the default/same-metric index it returns stored VERBATIM, so
	// the default path is byte-identical to Phases 1-5 (appendix #8/#9/#13/#15/#17 —
	// ALL four brute distance sites must route through this, not just the eachLive
	// legs). The indexed GRAPH legs reconstruct via the reindexNodeStore wrapper on
	// bi.idx (newBuiltIndexFor), so they need no change here.
	reindex := vx.metric != s.metric
	dvec := func(stored []float32, norm float32) []float32 {
		if !reindex {
			return stored
		}
		return reindexVector(stored, norm, s.metric, vx.metric)
	}

	// Head leg — ALWAYS brute-S (no graph). When a filter is present AND the head
	// has a declared attr index, use it to compute the matching member set S_head
	// and brute ONLY that subset (architecture §6 head "brute-S = brute over the
	// matching subset S"), instead of scanning every live slot. evalSeg uses the
	// maintained head index for declared leaves and an evalPayload residual scan for
	// non-declared ones, so the result set is identical to the full-scan floor.
	if filter != nil && s.seg.attr != nil {
		n := len(s.seg.slotDoc)
		shead, ok := s.seg.attr.evalSeg(filter, n, func(slot int) Payload { return s.seg.payloads[slot] })
		if ok && shead != nil {
			s.headBruteS.Add(1)
			shead.iterate(func(slot int) {
				if s.seg.tomb.get(slot) { // member ∧ live (§6): deleted docs never leak
					return
				}
				tk.offer(SearchResult{DocID: s.seg.slotDoc[slot], Distance: vx.metric.distance(dvec(s.seg.vectors[slot], s.seg.norms[slot]), pq)})
			})
		} else {
			// Predicate unanswerable by the index view (never happens for the closed
			// Eq/In/Range/And set, which validatePredicate already gated) → fall back
			// to the full brute eval for safety, sharing the no-index path's scan.
			s.headBruteEvalLocked(vx, filter, pq, tk, dvec)
		}
	} else {
		// Unfiltered, or no declared head index: brute every live slot.
		s.headBruteEvalLocked(vx, filter, pq, tk, dvec)
	}

	// Sealed legs.
	for i, ss := range s.sealed {
		bi := vx.graphs[s.sealedID[i]]
		if bi == nil {
			// Pending: ALWAYS brute over the segment's live slots; filter by brute eval.
			ss.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
				if filter != nil {
					pl, _ := ss.payloadDecoded(slot)
					if !filter.evalPayload(pl) {
						return
					}
				}
				tk.offer(SearchResult{DocID: docID, Distance: vx.metric.distance(dvec(stored, norm), pq)})
			})
			continue
		}
		// Indexed sealed segment.
		if filter == nil {
			// Unfiltered fast path — exactly today's behavior (regression-guarded).
			if err := s.searchIndexedUnfiltered(ss, bi, q, k, tk); err != nil {
				return nil, err
			}
			continue
		}
		// Filtered indexed leg: eval filter → S_seg (segment-local slot bitmap),
		// AND with live (§6: deletes never leak), then ADAPTIVELY dispatch on
		// |S_seg| vs the per-segment threshold T (§6 "阈值 T 按段内 |S_seg| 判"):
		//   - |S_seg| ≤ T → brute-S: exact distance over only the matching slots.
		//   - |S_seg| > T → graph∩S: HNSW filter-during-traversal, a member predicate
		//     (nodeId→slot→S_seg) admitting only matching nodes to the result heap
		//     while still traversing through non-members for connectivity.
		// Both legs feed the same shared top-k heap.
		sseg := s.evalSegLocked(ss, filter)
		s.andLive(ss, sseg) // member ∧ live
		card := sseg.count()
		if card == 0 {
			continue
		}
		if card <= s.attrSearchT {
			// Brute-S over ONLY the S_seg slots (exact).
			sseg.iterate(func(slot int) {
				tk.offer(SearchResult{DocID: ss.slotDoc(slot), Distance: vx.metric.distance(dvec(ss.getVectorRef(slot), ss.norm(slot)), pq)})
			})
			continue
		}
		// graph∩S: member keyed by nodeId via the segment's nodeSlot table.
		s.graphSDispatches.Add(1)
		gs := bi.store
		member := func(nodeId uint64) bool {
			if nodeId >= uint64(len(gs.nodeSlot)) {
				return false
			}
			slot := gs.nodeSlot[nodeId]
			return slot >= 0 && sseg.get(slot)
		}
		// Over-fetch must account for FILTER SELECTIVITY, not just tombstones: a
		// selective filter (small |S_seg| vs the segment's live rows) needs a much
		// wider beam (ef = max(efSearch, fetchK)) to traverse enough graph to reach k
		// spread members. Inflate the effective k by the inverse selectivity
		// (liveCount/card), so ef ≈ k/selectivity, bounded by the live count, and add
		// the tombstone slack. (measure-don't-assert: the plan's k+tomb+1 under-returns
		// for a graph-distant selective filter; tuned up until Task 10's recall ≥ 0.8.)
		liveN := ss.count() - ss.tombCount()
		fetchK := k + ss.tombCount() + 1
		if card > 0 {
			if scaled := (k*liveN)/card + 1; scaled > fetchK {
				fetchK = scaled
			}
		}
		if fetchK > liveN {
			fetchK = liveN
		}
		hits, err := bi.idx.searchFiltered(q, fetchK, member)
		if err != nil {
			return nil, err
		}
		for _, h := range hits {
			// sseg is already ANDed live, but re-resolve once for the rare
			// concurrent-delete window (cheap insurance, mirrors the unfiltered leg).
			if _, live := ss.slotOfDoc(h.DocID); !live {
				continue
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

// headBruteEvalLocked is the head leg's full-scan fallback: brute every LIVE head
// slot and admit those matching filter via evalPayload. It is the correctness
// floor used when no head attr index is available, and the safety net if the
// index view ever reports a predicate it cannot answer (never, for the closed
// Eq/In/Range/And set). Distance is under the INDEX metric (vx), reconstructing raw
// via dvec for a non-primary index (appendix #8/#13) — for the default index dvec is
// the identity and this is byte-identical to Phases 1-5. Caller holds s.mu (R).
func (s *Store) headBruteEvalLocked(vx *vindex, filter Predicate, pq []float32, tk *topK, dvec func([]float32, float32) []float32) {
	s.seg.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		if filter != nil && !filter.evalPayload(s.seg.payloads[slot]) {
			return
		}
		tk.offer(SearchResult{DocID: docID, Distance: vx.metric.distance(dvec(stored, norm), pq)})
	})
}

// searchIndexedUnfiltered is the unfiltered indexed-segment leg (the pre-Phase-5
// body, extracted verbatim): run the per-segment HNSW, over-fetching by the
// segment's tombstone count so k LIVE hits survive the tombstone post-filter, then
// drop any hit no longer live in the segment (the immutable graph still contains
// post-seal-tombstoned nodes). Caller holds s.mu (R).
func (s *Store) searchIndexedUnfiltered(ss *sealedSegment, bi *builtIndex, q []float32, k int, tk *topK) error {
	fetchK := k + ss.tombCount()
	hits, err := bi.idx.search(q, fetchK)
	if err != nil {
		return err
	}
	for _, h := range hits {
		if _, live := ss.slotOfDoc(h.DocID); !live {
			continue // tombstoned after seal → exclude
		}
		tk.offer(h)
	}
	return nil
}

// evalSegLocked evaluates filter against one sealed segment, producing S_seg (a
// segment-local slot bitmap of matching rows). It uses the segment's resident
// attr index (ss.attr) when present; if a segment was sealed before any
// declaration (ss.attr == nil) it builds the index on the fly from payloads, so
// the filtered path is always correct even on an un-indexed segment (architecture
// §6: non-declared/un-indexed fields fall back to a payload scan). Caller holds
// s.mu (R).
func (s *Store) evalSegLocked(ss *sealedSegment, filter Predicate) *bitmap {
	payloadAt := func(slot int) Payload {
		p, _ := ss.payloadDecoded(slot)
		return p
	}
	ai := ss.attr
	if ai == nil {
		ai = buildSegAttr(s.attrDecls, ss.count(), payloadAt)
	}
	bm, ok := ai.evalSeg(filter, ss.count(), payloadAt)
	if !ok || bm == nil {
		// The closed predicate set always yields ok=true; this guard keeps the
		// filtered leg panic-free if an unusable predicate ever reaches here.
		return &bitmap{}
	}
	return bm
}

// andLive clears the tombstoned slots from S_seg in place (member ∧ live, §6), so
// a deleted-but-matching doc never enters the result heap. It snapshots the
// segment's mmap'd tomb words under the segment's tomb RLock (mirroring eachLive's
// lock discipline, appendix #16/#18) and applies them via bitmap.andNotWords.
func (s *Store) andLive(ss *sealedSegment, sseg *bitmap) {
	ss.tombMu.RLock()
	tomb := make([]uint64, ss.tombWords)
	for w := 0; w < ss.tombWords; w++ {
		off := segPageSize + w*8
		tomb[w] = binary.LittleEndian.Uint64(ss.tombMap[off : off+8])
	}
	ss.tombMu.RUnlock()
	sseg.andNotWords(tomb)
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

	// (1) Dump head → sealed records-segment files (fsync, fast, durable). The
	// declared attr set is built into attr.dat in the same write.
	if err := writeSealedSegment(segDir, s.seg, s.attrDecls); err != nil {
		return err
	}
	ss, err := openSealedSegment(segDir, s.metric)
	if err != nil {
		return err
	}
	// Load the per-segment attr index (from the just-written attr.dat, or rebuilt
	// from payload if no declarations / a torn file).
	if len(s.attrDecls) > 0 {
		ss.attr, _ = openAttrFile(segDir, ss, s.attrDecls)
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
	// Build a graph for EVERY named index over the new segment (§4.7 "对 head 建 N
	// 张图"). Each (index, segment) is born pending; its background build flips it to
	// indexed. buildBeginLocked under s.mu before each spawn so WaitForIndex counts
	// all N (gotcha 4). The global nInflightBuilds counter already aggregates them.
	for name := range s.indexes {
		s.buildBeginLocked()
		go s.buildAndPublish(name, id, segDir, ss)
	}

	// (5) Opportunistic background reclamation: a fresh seal may push a size tier to
	// fanout (growth) or expose a deflated segment (delete-driven). maybeMergeLocked
	// launches any qualifying merges off the write path; it only picks already-INDEXED
	// inputs, so it never touches the segment we just sealed above (still pending) and
	// never blocks Put/Seal. Healthy stores no-op. (Phase 4, architecture §4.9.)
	s.maybeMergeLocked()
	return nil
}

// buildAndPublish builds the HNSW for a pending (index, segment) off the write
// path, then installs the graph into that named index and flips the manifest to
// indexed. name selects which index's vindex.graphs receives the built graph and
// which cfg drives the build. The build failure path drops the error: the segment
// stays pending → still brute-searched, still correct; recovery (or a future
// RebuildVectorIndex) retries. buildMu serializes the install+manifest rewrite
// across concurrent builders so two flips never race a single manifest file.
func (s *Store) buildAndPublish(name string, id segID, segDir string, ss *sealedSegment) {
	defer s.buildDone()
	// Snapshot the index's cfg under s.mu: the s.indexes map is now mutated
	// concurrently by CreateVectorIndex/DropVectorIndex (Phase 6), so this first
	// lookup — which precedes the off-lock build — must be synchronized. A missing
	// name means the index was dropped before this build started (Task 6).
	s.mu.RLock()
	vx, ok := s.indexes[name]
	var cfg graphConfig
	var idxMetric Metric
	if ok {
		cfg = vx.cfg
		idxMetric = vx.metric
	}
	s.mu.RUnlock()
	if !ok {
		return
	}
	// Build under the INDEX metric. When it equals the primary (records) metric — the
	// "default" index and any same-metric index — this is the plain build, byte-
	// identical to Phases 1-5. When it differs, buildSegmentGraphReindex reconstructs
	// raw per node (primary.restore → index.prepare) so the graph topology is built in
	// the index's space (§3.4). s.metric is immutable, so it needs no lock snapshot.
	var gs *segGraphStore
	var err error
	if idxMetric == s.metric {
		gs, err = buildSegmentGraph(segDir, name, ss, cfg)
	} else {
		gs, err = buildSegmentGraphReindex(segDir, name, ss, s.metric, idxMetric, cfg)
	}
	if err != nil {
		return // stays pending; brute leg keeps results correct
	}
	// newBuiltIndexFor re-wraps the reopened graph so search-time distances also
	// reconstruct raw for a non-primary index (symmetric with the build).
	bi := newBuiltIndexFor(gs, ss, s.metric, idxMetric, cfg)
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-check the index still exists after taking s.mu: a concurrent
	// DropVectorIndex (Task 6) may have removed it while this build ran off-lock
	// (gotcha 5). If gone, discard the freshly-built graph AND delete the file we
	// just wrote: Drop's dropGraphFilesLocked ran while this build had not yet
	// written graph-<name>.dat (it found nothing to remove), so without this cleanup
	// the file is an orphan on disk with no map/manifest entry (appendix #21). We
	// hold buildMu+s.mu, so no concurrent re-Create/build can be mid-flight here.
	vx, ok = s.indexes[name]
	if !ok {
		p := segFilePath(segDir, graphFileName(name))
		if rerr := fsRemove(p); rerr == nil {
			_ = fsyncDir(segDir)
		}
		return
	}
	vx.graphs[id] = bi
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

// isIndexedForTest reports whether sealed segment id has its graph installed in
// the default index.
func (s *Store) isIndexedForTest(id segID) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.indexes[defaultIndexName].graphs[id] != nil
}

// writeManifestLocked rewrites the manifest from the current segment set. Caller
// holds s.mu. The manifest Version is bumped exactly once per rewrite
// (appendix #19 — the draft double-incremented with a dead assignment).
func (s *Store) writeManifestLocked() error {
	s.manifestVersion++
	m := &manifest{Version: s.manifestVersion, Head: headSegID, Metric: s.metric}
	// Persist the declared attr-index set (sorted for a deterministic manifest).
	props := make([]string, 0, len(s.attrDecls))
	for p := range s.attrDecls {
		props = append(props, p)
	}
	sort.Strings(props)
	for _, p := range props {
		m.AttrDecls = append(m.AttrDecls, attrDecl{Property: p, Kind: s.attrDecls[p]})
	}
	for i, ss := range s.sealed {
		m.Segments = append(m.Segments, segmentEntry{
			SegID:     s.sealedID[i],
			Gen:       0,
			VecCount:  uint64(ss.count()),
			TombCount: uint64(ss.tombCount()),
		})
	}
	// Index configs + per-(index,segment) states (v4), sorted by name for a
	// deterministic manifest. Each index emits its config once and one IndexSegs
	// entry per sealed segment: segIndexed when its graph is built, else segPending
	// (the brute-served state recover() resumes from).
	inames := make([]string, 0, len(s.indexes))
	for n := range s.indexes {
		inames = append(inames, n)
	}
	sort.Strings(inames)
	for _, n := range inames {
		vx := s.indexes[n]
		m.Indexes = append(m.Indexes, indexConfigEntry{
			Name: n, Type: "hnsw", Metric: vx.metric,
			M: vx.cfg.M, EfConstruction: vx.cfg.EfConstruction, EfSearch: vx.cfg.EfSearch,
		})
		for _, sid := range s.sealedID {
			st := segPending
			if vx.graphs[sid] != nil {
				st = segIndexed
			}
			m.IndexSegs = append(m.IndexSegs, indexSegEntry{Index: n, SegID: sid, Gen: 0, State: st})
		}
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
