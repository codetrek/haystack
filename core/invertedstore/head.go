package invertedstore

import (
	"os"
	"path/filepath"
	"sort"
)

// postingDelta is a keyword's pending head state for one spill window: the set of docids added
// to the keyword and the set tombstoned (removed) from it. Keeping these as sets enforces the
// "latest action per (keyword,docid)" rule and dedups docids in memory (design §6) — a later
// add cancels a pending delete and vice-versa, so a spilled value never holds both for a docid.
// Both sets are allocated LAZILY (nil until the first add/tombstone of that kind): a cold build
// has no deletes, so the del-set stays nil and the per-add cross-delete is skipped. A nil set is
// semantically an empty set (setToSlice handles nil), so spill output is unchanged.
type postingDelta struct {
	adds map[int64]struct{}
	dels map[int64]struct{}
}

// headTable is the per-table in-memory head buffer (worker-owned; read under the Store RWMutex).
// It holds the inverted deltas (keyword -> adds/dels), the forward entries (docid -> keyword
// strings, encoded to segment-local term-ids at spill), the set of docids whose forward is a
// tombstone (deleted docs), and a running logical byte estimate that drives spill.
type headTable struct {
	inv        map[string]*postingDelta // keyword -> latest adds/dels (per (kw,docid))
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

// posting returns keyword's postingDelta, creating an empty one (both sets nil/lazy) on first sight
// and charging the same logical byte estimate the eager version did (so spill cadence is unchanged).
func (h *headTable) posting(keyword string) *postingDelta {
	pd := h.inv[keyword]
	if pd == nil {
		pd = &postingDelta{}
		h.inv[keyword] = pd
		h.bytes += int64(len(keyword)) + 16
	}
	return pd
}

// addPosting records that docid is a member of keyword (latest action wins, in-memory dedup). The
// del-set is allocated lazily (nil on a cold build), so the cross-delete is skipped when dels==nil.
func (h *headTable) addPosting(keyword string, docid int64) {
	pd := h.posting(keyword)
	if pd.dels != nil {
		delete(pd.dels, docid) // latest action wins: a re-add cancels a pending tombstone
	}
	if pd.adds == nil {
		pd.adds = make(map[int64]struct{})
	}
	if _, ok := pd.adds[docid]; !ok {
		pd.adds[docid] = struct{}{}
		h.bytes += 4
	}
}

// tombstonePosting records that docid is removed from keyword (latest action wins). Symmetric to
// addPosting: the add-set is consulted only if allocated.
func (h *headTable) tombstonePosting(keyword string, docid int64) {
	pd := h.posting(keyword)
	if pd.adds != nil {
		delete(pd.adds, docid) // latest action wins: a delete cancels a pending add
	}
	if pd.dels == nil {
		pd.dels = make(map[int64]struct{})
	}
	if _, ok := pd.dels[docid]; !ok {
		pd.dels[docid] = struct{}{}
		h.bytes += 4
	}
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
		adds := setToSlice(pd.adds)
		dels := setToSlice(pd.dels)
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

// installSpill installs the off-worker-encoded segment ON the worker (F v5). MUST run on the worker
// (it mutates s.man/s.segs/s.spilling). The seg id is assigned HERE (install order ⇒ correct
// newest-wins), the temp file is renamed atomically to seg-<id>.dat (the open fd survives the rename),
// the segMeta is appended + the MANIFEST durably rewritten (persist-then-publish), the snapshot is
// republished, and the entry is removed from s.spilling — PUBLISH BEFORE REMOVE, so a reader never sees
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
	if err := os.Rename(tempPath, finalPath); err != nil {
		s.mu.Unlock()
		return err // transient (e.g. disk full); dispatch retries. The temp file + entry are preserved.
	}
	seg.id = id                                     // P5: the chunk-LRU keys decompressed dict chunks by (segmentId, chunkIdx)
	seg.path = finalPath                            // teardown/retire must unlink the renamed file, not the temp name
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
		os.Rename(finalPath, tempPath) // restore the temp file for the retry (final name is unreferenced)
		seg.path = tempPath
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()

	if err := writeManifestBytes(s.dir, b); err != nil {
		s.mu.Lock()
		s.man.Segments = s.man.Segments[:len(s.man.Segments)-1] // roll back to the pre-install set
		s.man.NextSegId--
		seg.refs.Store(0)
		os.Rename(finalPath, tempPath) // restore the temp file for the retry (MANIFEST never recorded it)
		seg.path = tempPath
		s.mu.Unlock()
		return err
	}

	s.mu.Lock()
	s.segs = append(s.segs, seg)
	sortSegmentsById(s.segs) // the new id is the highest, so this is O(n) tail-insert — keep oldest->newest
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

// setToSlice flattens a docid set to a slice (encodeDocs sorts+dedups, so order is irrelevant).
func setToSlice(m map[int64]struct{}) []int64 {
	out := make([]int64, 0, len(m))
	for d := range m {
		out = append(out, d)
	}
	return out
}

// fileSize returns the on-disk size of path (0 on error — only used for the segMeta size field).
func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}
