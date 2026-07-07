package invertedstore

import (
	"os"
	"path/filepath"
	"sort"
)

// postingDelta is a keyword's pending head state for one spill window: an ORDERED log of the
// (add|del) operations applied to (keyword, docid) pairs, APPENDED in action order (item H, spec
// §5b). docids are stored RAW in `ops` (one int64 each) and the per-op isAdd flag in a parallel
// bitset `isAdd` (one BIT each, 64 ops per uint64 word) — ~8 B/op + ~0.125 B/op for the flag, far
// below the per-keyword Go map the eager adds/dels sets cost (~48–96 B/entry + two map headers).
//
// Raw int64 docids + a parallel bitset (spec §5b's named full-range fallback) replace the earlier
// `docid<<1 | isAdd` single-int64 packing: that packing stole the low bit, so it could only carry
// docids in [0, 2^62) and the spec's full-int64-range invariant (the §11 owed re-measure, which
// round-trips docids up to math.MaxInt64) was unrepresentable. The raw-docid+bitset form costs the
// same ~8 B/op yet carries the WHOLE int64 range — no out-of-range case to assert away.
//
// The two jobs the old maps did are recovered at CONSUME time by resolveOps: dedup-on-insert is
// redundant (the on-disk encode appendDeltaDocs sort+dedups anyway), and the cross add-vs-del
// "latest action wins" rule is resolved by a STABLE sort keyed on docid only — the LAST op per
// docid is the survivor.
type postingDelta struct {
	docids []int64  // raw docid per op, APPENDED in action order
	isAdd  []uint64 // parallel bitset: bit i is set iff ops[i] is an add (else a tombstone)
}

// nOps returns the number of ops appended to pd.
func (pd *postingDelta) nOps() int { return len(pd.docids) }

// opAt returns the docid and isAdd flag of the i-th op.
func (pd *postingDelta) opAt(i int) (docid int64, isAdd bool) {
	return pd.docids[i], pd.isAdd[i>>6]&(1<<uint(i&63)) != 0
}

// appendOp appends one op (docid, isAdd) in action order, growing the bitset by a word every 64 ops.
func (pd *postingDelta) appendOp(docid int64, isAdd bool) {
	i := len(pd.docids)
	if i>>6 >= len(pd.isAdd) {
		pd.isAdd = append(pd.isAdd, 0)
	}
	if isAdd {
		pd.isAdd[i>>6] |= 1 << uint(i&63)
	}
	pd.docids = append(pd.docids, docid)
}

// headTable is the per-table in-memory head buffer (worker-owned; read under the Store RWMutex).
// It holds the inverted deltas (keyword -> adds/dels), the forward entries (docid -> keyword
// strings, encoded to segment-local term-ids at spill), the set of docids whose forward is a
// tombstone (deleted docs), and a running logical byte estimate that drives spill.
type headTable struct {
	inv        map[string]*postingDelta // keyword -> ordered add/del op log (resolved latest-wins at consume)
	fwd        map[int64][]string       // docid -> keyword strings (-> ordinals at spill)
	delForward map[int64]struct{}       // docids whose forward is a tombstone
	bytes      int64                    // logical byte estimate (matches the spike's accounting)
}

func newHeadTable() *headTable {
	return &headTable{
		inv:        map[string]*postingDelta{},
		fwd:        map[int64][]string{},
		delForward: map[int64]struct{}{},
	}
}

// posting returns keyword's postingDelta, creating an empty one (nil ops slices) on first sight and
// charging a slice-header-sized estimate (item H, spec §5b: the old eager version charged +16 for its
// two map headers; the per-keyword fixed charge is +24). The struct now holds TWO slices (docids +
// isAdd), but both headers are EMPTY/nil at creation — no backing array is allocated until the first
// appendOp — so the fixed charge stays at one slice-header (24 B): the docids array's per-op growth is
// the +8 charged below, and the isAdd bitset's growth (one uint64 word per 64 ops, ~0.125 B/op) is
// folded into that same +8. The per-op cost (h.bytes += 8) is charged in addPosting/tombstonePosting,
// so h.bytes ≈ the actual memory (slightly UNDER-counting by the bitset's amortized word: ~8.125 B/op
// actual vs +8 charged) and CapBytes is honest. Keeping +24 (not +48) matches the spec §5b PRIMARY single-`ops`-slice cadence, so
// the fallback's spill cadence is the one the spec measured.
func (h *headTable) posting(keyword string) *postingDelta {
	pd := h.inv[keyword]
	if pd == nil {
		pd = &postingDelta{}
		h.inv[keyword] = pd
		h.bytes += int64(len(keyword)) + 24 // keyword string + one ops slice header (spec §5b cadence)
	}
	return pd
}

// addPosting records that docid is a member of keyword (item H): an O(1) append of (docid, isAdd=true)
// in ACTION order — no lookup, no per-keyword map, no in-memory dedup (resolveOps recovers latest-wins
// + dedup at consume time). docids are stored RAW alongside a parallel isAdd bitset, so the FULL int64
// range round-trips (spec §5b's named full-range representation); there is no packable-range precondition.
func (h *headTable) addPosting(keyword string, docid int64) {
	h.posting(keyword).appendOp(docid, true)
	h.bytes += 8
}

// tombstonePosting records that docid is removed from keyword (item H). Symmetric to addPosting: an
// O(1) append of (docid, isAdd=false) in action order. Full int64 range, no precondition.
func (h *headTable) tombstonePosting(keyword string, docid int64) {
	h.posting(keyword).appendOp(docid, false)
	h.bytes += 8
}

// setForward records the doc's current full keyword set (clears any pending tombstone for it).
func (h *headTable) setForward(docid int64, words []string) {
	delete(h.delForward, docid)
	h.fwd[docid] = words
	h.bytes += int64(8 + len(words)*4)
}

// deleteForward records that the doc is deleted (forward-tombstone): drop any pending forward
// entry and mark the docid for an explicit tombstone record at spill, so an older non-empty
// forward record in a sealed segment can never win and resurrect the doc.
func (h *headTable) deleteForward(docid int64) {
	delete(h.fwd, docid)
	h.delForward[docid] = struct{}{}
	h.bytes += 12
}

// spill writes the current head for tableId as one immutable L0 segment, appends its segMeta,
// durably rewrites the MANIFEST, publishes the opened segment into s.segs, and resets the head.
// MUST run on the worker (it mutates s.man/s.segs/s.head). Mirrors the spike's spill shape
// (cmd/sortbench/main.go func spill) but uses the production segWriter/encoders, int64 docids,
// the 4-byte tableId keys, and the nKw-prefixed forward value (incl. explicit forward-tombstones).
//
// This is the SYNCHRONOUS spill (spillForTest + the CloseAndWait flush): it encodes the head AND
// installs the segment, both on the worker. The hot build path instead uses the OFF-WORKER detach +
// encode + install (dispatchSpill/encodeSpill/installSpill, F v5).
func (s *Store) spill(tableId int) error {
	s.mu.RLock()
	h := s.head[tableId]
	s.mu.RUnlock()
	if h == nil || (len(h.inv) == 0 && len(h.fwd) == 0 && len(h.delForward) == 0) {
		return nil
	}

	s.mu.RLock()
	segId := s.man.NextSegId
	s.mu.RUnlock()
	path := filepath.Join(s.dir, segFileName(segId))
	res := s.encodeHeadToFile(h, tableId, path)
	seg := res.seg
	seg.id = segId                                  // P5: chunk-LRU keys decompressed dict chunks by (segmentId, chunkIdx)
	seg.minDocid, seg.maxDocid = res.minD, res.maxD // B
	seg.refs.Store(1)                               // P9: the published snapshot holds one ref on this newly sealed segment
	sm := s.spillSegMeta(res, segId, tableId)

	// Persist the new MANIFEST, then publish — but keep the slow fsync OUT of the reader-blocking
	// critical section (P9/T8, design §6: "readers never block on a writer's I/O — the lock is held
	// only for the O(1) pointer swap and ref bookkeeping, never for spill/merge/file work"). All
	// writes run on the single mpsc worker, so there is no concurrent writer of s.man; the lock here
	// guards s.man only against concurrent READERS (tableInfo). So: (a) under the lock, append the
	// segMeta + bump NextSegId + marshal the manifest to bytes (cheap, no I/O); (b) OUTSIDE the lock,
	// do the two fsyncs (writeManifestBytes) — a concurrent Search/GetDocs is not blocked on them; (c)
	// re-take the lock only for the O(1) s.segs append + publishSnapshotLocked + head reset.
	s.mu.Lock()
	s.man.Segments = append(s.man.Segments, sm)
	s.man.NextSegId++
	b, err := marshalManifest(s.man)
	if err != nil {
		s.man.Segments = s.man.Segments[:len(s.man.Segments)-1] // roll back the in-memory manifest
		s.man.NextSegId--
		s.mu.Unlock()
		seg.refs.Store(0) // never published: drop the ref we just took before closing
		seg.close()
		return err
	}
	s.mu.Unlock()

	if err := writeManifestBytes(s.dir, b); err != nil {
		// The fsync failed: roll the in-memory manifest back to the pre-spill set so it stays
		// consistent with the still-old on-disk MANIFEST and with s.segs (which we never touched).
		s.mu.Lock()
		s.man.Segments = s.man.Segments[:len(s.man.Segments)-1]
		s.man.NextSegId--
		s.mu.Unlock()
		seg.refs.Store(0) // never published: drop the ref we just took before closing
		seg.close()
		return err
	}

	s.mu.Lock()
	s.segs = append(s.segs, seg)
	s.publishSnapshotLocked() // P9: republish the live set (this spill's new segment) for readers
	s.head[tableId] = newHeadTable()
	s.mu.Unlock()

	// Background merger (design §6, P8/P9): a new L0 segment may push a level to >= Fanout, or push the
	// bottom level's dead fraction over the covering threshold. When AutoMerge is on, raise a
	// NON-BLOCKING trigger on the background merge goroutine (concurrency.go). spill runs ON the worker,
	// so it MUST NOT send a task to its own queue (s.q.AddFunc would block-send and self-deadlock once
	// the queue fills — the worker is the only consumer). triggerMerge just flips a flag/channel; the
	// merge goroutine drives the actual passes back onto the worker via RunFunc.
	s.triggerMerge(false)
	return nil
}

// spillResult is the product of encoding a head into a segment file (the read-only, install-free part
// of a spill, shared by the synchronous spill + the off-worker encodeSpill): the opened segment and
// the segMeta fields derived at encode time (the forward docid span + the posting count).
type spillResult struct {
	seg        *segment
	minD, maxD int64
	postings   int64
}

// spillSegMeta builds the L0 segMeta for an encoded head, given the seg id (assigned at install for
// the off-worker path) and the table it belongs to. The codecs come from s.opts (the values
// encodeHeadToFile actually used), so the meta matches the bytes on disk.
func (s *Store) spillSegMeta(r spillResult, id uint64, tableId int) segMeta {
	tid := uint32(tableId)
	return segMeta{
		Id: id, Level: 0,
		DataCodec: s.opts.DataCodecL0, DictCodec: s.opts.DictCodec,
		MinTable: tid, MaxTable: tid,
		Size:     fileSize(r.seg.path),
		Postings: r.postings,
		MinDocid: r.minD, MaxDocid: r.maxD,
	}
}

// encodeHeadToFile encodes head (READ-ONLY) for tableId into the segment file at path and returns the
// opened segment + its derived segMeta fields. It does NOT touch s.man/s.segs/s.head, so it is safe to
// run OFF the worker over a DETACHED head (F v5): the only shared state it reads is s.dir/s.opts (both
// immutable after Open). The synchronous spill and the off-worker encodeSpill share it byte-for-byte.
func (s *Store) encodeHeadToFile(h *headTable, tableId int, path string) spillResult {
	// 1. The term dict is the union of keywords with adds and keywords with tombstones; both
	//    are [I] records. Sort once: that single sort yields the sorted inverted order AND each
	//    keyword's ordinal (its term-id) for the term-id forward value.
	terms := make([]string, 0, len(h.inv))
	for kw := range h.inv {
		terms = append(terms, kw)
	}
	sort.Strings(terms)
	kw2ord := make(map[string]uint32, len(terms))
	for i, t := range terms {
		kw2ord[t] = uint32(i)
	}

	// 2. New L0 segment writer: snappy data blocks, the dict codec, term-id mode on.
	w := newSegWriter(path,
		newCodec(s.opts.DataCodecL0), newCodec(s.opts.DictCodec),
		s.opts.BlockTarget, s.opts.Chunk, s.opts.InlineThreshold, true, s.opts.DictChunkBytes)

	// 3. Inverted records in sorted term order: [I] tableId keyword -> invertedValue(adds,dels).
	tid := uint32(tableId)
	var postings int64 // count add+del entries for segMeta.Postings (the deadFraction `written` term)
	for _, t := range terms {
		pd := h.inv[t]
		adds, dels := resolveOps(pd)
		postings += int64(len(adds) + len(dels))
		w.addEntry(invertedKey(tid, t), encodeInvertedValue(adds, dels))
	}

	// 4. Forward records ascending by docid (== ascending forward key, and [I] < [F], so no
	//    second full sort). A live doc -> term-id forward value; a deleted doc -> forward
	//    tombstone. Both key spaces are merged into one ascending docid stream.
	type fwdRec struct {
		docid   int64
		deleted bool
		words   []string
	}
	recs := make([]fwdRec, 0, len(h.fwd)+len(h.delForward))
	for d, words := range h.fwd {
		recs = append(recs, fwdRec{docid: d, words: words})
	}
	for d := range h.delForward {
		recs = append(recs, fwdRec{docid: d, deleted: true})
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].docid < recs[j].docid })
	// B: the forward-read skip range covers every EMITTED forward record (live + tombstone). recs is
	// sorted ascending by docid, so the span is its ends; an empty recs keeps the always-skip range.
	minD, maxD := emptyDocidRange()
	if len(recs) > 0 {
		minD, maxD = recs[0].docid, recs[len(recs)-1].docid
	}
	for _, r := range recs {
		if r.deleted {
			w.addEntry(forwardKey(tid, r.docid), forwardTombstone())
			continue
		}
		ords := make([]uint32, 0, len(r.words))
		for _, word := range r.words {
			ords = append(ords, kw2ord[word])
		}
		w.addEntry(forwardKey(tid, r.docid), encodeForward(ords))
	}

	// 5. Seal: finish() fsyncs the file and returns the opened segment.
	seg := w.finish(path)
	return spillResult{seg: seg, minD: minD, maxD: maxD, postings: postings}
}

// detachHeadLocked detaches tableId's current head for an OFF-WORKER encode (F v5). The CALLER must
// hold s.mu.Lock AND must have already verified (in the SAME lock section) that the head is over-cap
// and !spillInFlight, so two applies can never both detach (one-in-flight stays race-free). It swaps
// the live head for a fresh one, pushes the old head onto s.spilling (readers resolve it as a tier
// between the live head and the segments — B1), reserves a temp-file counter (NOT a seg id; the id is
// assigned at install), and sets spillInFlight. It returns the entry to dispatch (encode + install) on
// a background goroutine OUTSIDE the lock — detachHeadLocked itself does NO I/O.
func (s *Store) detachHeadLocked(tableId int) *spillEntry {
	h := s.head[tableId]
	s.head[tableId] = newHeadTable()
	minD, maxD := headForwardRange(h)
	s.spillTempCtr++
	e := &spillEntry{tableId: tableId, head: h, tempN: s.spillTempCtr, minDocid: minD, maxDocid: maxD}
	s.spilling = append(s.spilling, e)
	s.spillInFlight = true
	return e
}

// dispatchSpill spawns the background goroutine that encodes the detached head OFF the worker and then
// installs it ON the worker (F v5). The encode is read-only over the detached head (M2); the install
// is a worker RunFunc (single-mutator preserved). The install is RETRIED on a transient write failure
// up to MaxInstallRetries, then GIVEN UP: a persistently-failing disk would otherwise wedge the
// producer at the gate forever, so a give-up drops the detached head (treated as crash-volatile),
// clears spillInFlight/blockProducer + broadcasts, and re-dispatches any over-cap head — exactly the
// install's own liveness path. spillWG tracks the goroutine so CloseAndWait can drain it OFF the worker.
func (s *Store) dispatchSpill(e *spillEntry) {
	s.spillWG.Add(1)
	go func() {
		defer s.spillWG.Done()
		res := s.encodeSpill(e)
		for attempt := 0; ; attempt++ {
			var err error
			s.q.RunFunc(func() error { err = s.installSpill(e, res); return nil })
			if err == nil {
				return
			}
			if attempt+1 >= s.opts.MaxInstallRetries {
				// Give up: the disk is persistently failing. Drop the detached head (crash-volatile —
				// indexer replay recovers it), remove its temp file, clear the in-flight + gate state, and
				// re-dispatch any over-cap head so the producer makes progress. Runs on the worker.
				s.q.RunFunc(func() error { return s.giveUpSpill(e, res) })
				return
			}
		}
	}()
}

// encodeSpill encodes the detached head into its temp file (seg-tmp-<n>.dat) OFF the worker — strictly
// READ-ONLY over the detached head (M2), touching no shared mutable state. encodeSpillBlock (test-only)
// parks here so a test can hold the head in s.spilling across a re-post (the B1 gate).
func (s *Store) encodeSpill(e *spillEntry) spillResult {
	if encodeSpillBlock != nil {
		encodeSpillBlock()
	}
	tempPath := filepath.Join(s.dir, segTempFileName(e.tempN))
	return s.encodeHeadToFile(e.head, e.tableId, tempPath)
}

// renameSegmentFile renames seg's backing file from -> to, RESEATING seg's open fd across the
// rename. It closes seg.f before os.Rename and reopens it at the resulting path after — because a
// rename of a file whose handle is still open is refused on Windows: os.Open (which openSegment uses
// to open the segment) requests share mode FILE_SHARE_READ|FILE_SHARE_WRITE with NO FILE_SHARE_DELETE,
// and os.Rename == MoveFileEx(from,to,MOVEFILE_REPLACE_EXISTING) must open the source with DELETE
// access, so the missing share flag makes the rename fail with ERROR_SHARING_VIOLATION. POSIX renames
// an open fd fine (the fd follows the inode), so closing+reopening is behaviorally a no-op there — an
// extra close/open on a segment NO reader can yet reach (it is not published into s.segs until after a
// successful install), so there is no race and POSIX durability/correctness is unchanged. A rename does
// not change the file's BYTES, so seg's already-parsed footer / block index / codecs stay valid; only
// the fd (and seg.path) are reseated. On a rename error the fd is reopened at `from` (the file did not
// move) so the caller's bounded retry / rollback still sees a live segment; seg.path is set to the
// resulting name in both cases.
func renameSegmentFile(seg *segment, from, to string) error {
	seg.close()
	if err := os.Rename(from, to); err != nil {
		seg.f, _ = os.Open(from) // rename failed: the file is still at `from`; reseat the fd there
		seg.path = from
		return err
	}
	seg.f, _ = os.Open(to)
	seg.path = to
	return nil
}

// installSpill installs the off-worker-encoded segment ON the worker (F v5). MUST run on the worker
// (it mutates s.man/s.segs/s.spilling). The seg id is assigned HERE (install order ⇒ correct
// newest-wins), the temp file is renamed atomically to seg-<id>.dat (renameSegmentFile reseats the
// open fd across the rename so it is Windows-safe), the segMeta is appended + the MANIFEST durably
// rewritten (persist-then-publish), the snapshot is republished, and the entry is removed from
// s.spilling — PUBLISH BEFORE REMOVE, so a reader never sees
// the doc in NEITHER tier. Finally spillInFlight is cleared, blockProducer cleared + broadcast, and
// EVERY table is re-checked for an over-cap head (one-in-flight is store-wide, so a different table's
// head that filled while this spill was in flight is found + re-dispatched here — LOAD-BEARING for
// liveness). On a write failure it rolls back and returns the error for the dispatch's bounded retry;
// the entry stays in s.spilling (data preserved) and spillInFlight stays set across the retry.
func (s *Store) installSpill(e *spillEntry, res spillResult) error {
	seg := res.seg
	tempPath := filepath.Join(s.dir, segTempFileName(e.tempN)) // == seg.path on entry (encode wrote it)

	s.mu.Lock()
	id := s.man.NextSegId
	finalPath := filepath.Join(s.dir, segFileName(id))
	if err := renameSegmentFile(seg, tempPath, finalPath); err != nil {
		s.mu.Unlock()
		return err // transient (e.g. disk full); dispatch retries. The temp file + entry are preserved.
	}
	seg.id = id                                     // P5: the chunk-LRU keys decompressed dict chunks by (segmentId, chunkIdx)
	seg.minDocid, seg.maxDocid = res.minD, res.maxD // B
	seg.refs.Store(1)                               // P9: the published snapshot holds one ref on this new segment
	sm := s.spillSegMeta(res, id, e.tableId)
	s.man.Segments = append(s.man.Segments, sm)
	s.man.NextSegId++
	b, err := marshalManifest(s.man)
	if err != nil {
		s.man.Segments = s.man.Segments[:len(s.man.Segments)-1] // roll back the in-memory manifest
		s.man.NextSegId--
		seg.refs.Store(0)
		renameSegmentFile(seg, finalPath, tempPath) // restore the temp file for the retry (final name is unreferenced)
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()

	if err := writeManifestBytes(s.dir, b); err != nil {
		s.mu.Lock()
		s.man.Segments = s.man.Segments[:len(s.man.Segments)-1] // roll back to the pre-install set
		s.man.NextSegId--
		seg.refs.Store(0)
		renameSegmentFile(seg, finalPath, tempPath) // restore the temp file for the retry (MANIFEST never recorded it)
		s.mu.Unlock()
		return err
	}

	s.mu.Lock()
	s.segs = append(s.segs, seg)
	sortSegmentsById(s.segs)  // the new id is the highest, so this is O(n) tail-insert — keep oldest->newest
	s.publishSnapshotLocked() // PUBLISH the new segment BEFORE removing the spilling entry (the doc is in
	s.removeSpillingLocked(e) // both tiers for an instant, never in neither — the forbidden direction)
	s.spillInFlight = false
	s.clearBlockProducerLocked()
	next := s.findOverCapHeadLocked() // re-check ALL tables (one-in-flight is store-wide)
	s.mu.Unlock()

	if next != nil {
		s.dispatchSpill(next)
	}
	s.triggerMerge(false)
	return nil
}

// giveUpSpill abandons a detached head after the install retries are exhausted (a persistently-failing
// disk). MUST run on the worker. The head's data is dropped (crash-volatile — indexer replay recovers
// it), its temp file removed, the in-flight + gate state cleared + broadcast, and any over-cap head
// re-dispatched so the producer is never wedged. Mirrors a successful install's liveness tail.
func (s *Store) giveUpSpill(e *spillEntry, res spillResult) error {
	res.seg.refs.Store(0)
	res.seg.close()
	os.Remove(res.seg.path) // the temp file (rename never succeeded durably)

	s.mu.Lock()
	s.removeSpillingLocked(e)
	s.spillInFlight = false
	s.clearBlockProducerLocked()
	next := s.findOverCapHeadLocked()
	s.mu.Unlock()

	if next != nil {
		s.dispatchSpill(next)
	}
	return nil
}

// removeSpillingLocked removes entry e from s.spilling (caller holds s.mu.Lock). Order within the slice
// is preserved (newest last) so the read tiers stay correctly ordered for the OTHER in-flight entries —
// there is at most one in v5, but the removal is general.
func (s *Store) removeSpillingLocked(e *spillEntry) {
	for i, x := range s.spilling {
		if x == e {
			s.spilling = append(s.spilling[:i], s.spilling[i+1:]...)
			return
		}
	}
}

// clearBlockProducerLocked clears the producer gate and wakes every parked producer (caller holds
// s.mu.Lock; spillCond.L == &s.mu). Broadcast (not Signal): Broadcast wakes all, and each re-checks the
// for-loop condition — the install relieved exactly one head's worth, so a still-over-cap producer
// re-parks, but a producer whose head is now under cap proceeds. A no-op if the gate was not set.
func (s *Store) clearBlockProducerLocked() {
	if s.blockProducer {
		s.blockProducer = false
	}
	s.spillCond.Broadcast() // safe to broadcast unconditionally; harmless if no producer is parked
}

// findOverCapHeadLocked scans ALL tables for a head at/over CapBytes and, if found, detaches it for the
// next off-worker spill, returning the entry to dispatch (or nil). Caller holds s.mu.Lock and has just
// cleared spillInFlight, so this re-detach respects one-in-flight. LOAD-BEARING for liveness: a head
// that went over-cap while a spill was in flight (possibly on a DIFFERENT table) is detached here, so
// the producer parked at the gate is released and the build converges.
func (s *Store) findOverCapHeadLocked() *spillEntry {
	if s.spillInFlight {
		return nil // already re-detached (defensive; install/give-up clear it before calling)
	}
	for tid, h := range s.head {
		if h != nil && h.bytes >= int64(s.opts.CapBytes) {
			return s.detachHeadLocked(tid)
		}
	}
	return nil
}

// resolveOps reduces a keyword's ordered op log (raw docid in pd.docids + isAdd in the parallel
// pd.isAdd bitset, APPENDED in action order) to the per-docid survivors: a docid is in adds iff its
// LAST op is an add, else in dels (item H, spec §5b). It recovers EXACTLY the eager adds/dels maps'
// result.
//
// It is pure and NON-MUTATING: it copies the ops into a FRESH scratch slice and sorts the scratch
// (never pd's slices — the detached-head encode and concurrent Search both share the head's pd), so
// two concurrent callers can never alias. The CALLER must hold the appropriate lock (the Store RLock
// for a live head; ownership for a detached head) across this call so the copy is consistent with the
// worker's appends. resolveOps allocates a fresh scratch per call (no shared/pooled scratch).
//
// The sort is sort.SliceStable keyed on docid ONLY — NOT a value that folds in isAdd: a key that lets
// isAdd break ties makes an add always sort last, so `add->del` mis-resolves to add. Stable +
// docid-keyed keeps equal docids in insertion order, so the last op for a docid is its true latest
// action.
func resolveOps(pd *postingDelta) (adds, dels []int64) {
	n := pd.nOps()
	if n == 0 {
		return nil, nil
	}
	// scratch packs (docid, isAdd) per op into a struct so the stable docid-keyed sort carries the
	// flag along; raw int64 docids (full range) need a side flag rather than the old low-bit steal.
	type scratchOp struct {
		docid int64
		isAdd bool
	}
	scratch := make([]scratchOp, n)
	for i := 0; i < n; i++ {
		d, a := pd.opAt(i)
		scratch[i] = scratchOp{docid: d, isAdd: a}
	}
	sort.SliceStable(scratch, func(i, j int) bool { return scratch[i].docid < scratch[j].docid })
	// Equal docids are adjacent (and in insertion order); the LAST op of each run decides add vs del.
	for i := 0; i < n; {
		j := i + 1
		for j < n && scratch[j].docid == scratch[i].docid {
			j++
		}
		last := scratch[j-1]
		if last.isAdd {
			adds = append(adds, last.docid)
		} else {
			dels = append(dels, last.docid)
		}
		i = j
	}
	return adds, dels
}

// fileSize returns the on-disk size of path (0 on error — only used for the segMeta size field).
func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}
