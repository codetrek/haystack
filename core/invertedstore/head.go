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
func (s *Store) spill(tableId int) error {
	s.mu.RLock()
	h := s.head[tableId]
	s.mu.RUnlock()
	if h == nil || (len(h.inv) == 0 && len(h.fwd) == 0 && len(h.delForward) == 0) {
		return nil
	}

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
	s.mu.RLock()
	segId := s.man.NextSegId
	s.mu.RUnlock()
	path := filepath.Join(s.dir, segFileName(segId))
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

	// 5. Seal: finish() fsyncs the file and returns the opened segment. Record its segMeta,
	//    bump NextSegId, durably rewrite the MANIFEST, publish into s.segs, reset the head.
	seg := w.finish(path)
	seg.id = segId    // P5: chunk-LRU keys decompressed dict chunks by (segmentId, chunkIdx)
	seg.refs.Store(1) // P9: the published snapshot holds one ref on this newly sealed segment
	size := fileSize(path)
	sm := segMeta{
		Id:        segId,
		Level:     0,
		DataCodec: s.opts.DataCodecL0,
		DictCodec: s.opts.DictCodec,
		MinTable:  tid,
		MaxTable:  tid,
		Size:      size,
		Postings:  postings,
	}

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
