package invertedstore

// merge.go — P8 (design §6 merger + §8 remap; task T6).
//
// The background tiered merger: the long-term correctness + bounded-K story. A level with >=
// Fanout segments is streaming k-way merged into ONE next-level segment; a COVERING merge fully
// compacts the bottom level + everything above to reclaim dangling tombstones, fully-tombstoned
// keys and dead-tableId keys. Every merge:
//
//   - is a STREAMING k-way merge (one block per source resident at a time — bounded memory);
//   - RECONCILES each (keyword, docid) NEWEST-WINS across the inputs (merged oldest -> newest, the
//     latest add-or-tombstone wins) — it does NOT blindly concatenate adds+dels, so add->del->add
//     on one (keyword,docid) collapses to the survivor (PRESENT). This is the fix the spike never
//     had (its edit workload added globally-unique words, so concat never aliased);
//   - in a TIERED merge CANNOT drop a keyword key (the term-id remap append-index IS the source
//     ordinal, §8): a fully-tombstoned keyword persists as a small del-only record, and a
//     forward-tombstone (nKw=0) is carried through verbatim;
//   - in a COVERING merge (bottom + everything above) DOES reclaim: it drops fully-tombstoned keys,
//     drops dangling tombstones (keeps adds-only, since nothing older survives to be suppressed),
//     drops forward-tombstones, and drops keys for tableIds no longer in the catalog — the remap
//     then carries a sentinel for each dropped key so a surviving forward never dereferences one;
//   - REMAPS forward ordinals srcOrd -> outputOrd via per-source int arrays (built incrementally as
//     the merged term dict is emitted) and rebuilds the term-dict region (segWriter.writeTermDict),
//     so merge memory is Sum(source term counts) ints — NOT a string map;
//   - swaps the MANIFEST crash-safely (new segment fully written + fsync'd before the swap), then
//     deletes the input files and purges their chunk-LRU entries (pre-T8: inputs are dropped
//     immediately on swap; refcount-deferred deletion is T8).
//
// Port of cmd/sortbench/main.go mergeSegments + maybeMerge, in production shape: []byte keys with a
// 4-byte tableId, int64 docids/ordinals, per-(kw,docid) reconciliation (the spike concatenated),
// the covering-merge reclamation path (new — not in the spike), and the worker-owned segment set.

import (
	"encoding/binary"
	"os"
	"path/filepath"
)

// ordSentinel marks a source ordinal whose keyword was DROPPED by a covering merge (so it has no
// output ordinal). A surviving forward should never reference a dropped keyword — a covering merge
// only drops fully-tombstoned (no live add) or dead-table keywords, and a live forward only claims
// keywords it has a live add for. If a forward DOES reference a dropped ordinal (a pre-existing
// inverted/forward inconsistency in the input — a corrupt or legacy segment), the merge SELF-HEALS
// by dropping that stale term from the forward rather than crashing: the keyword has no live posting
// so the forward must not claim it. The merge MUST NOT panic here — it runs on the mpsc worker
// goroutine, which has no recover, so a panic would brick the whole process (not just one request),
// turning a recoverable, contained data-quality event into a hard crash. See design §6/§8.
const ordSentinel = ^uint32(0)

// mergeRemapObserver, when non-nil, is invoked by mergeSegments with the realized per-source remap
// arrays just before the output is finished. Test-only observability (P8) for the "merge memory =
// Sum source term counts, int arrays not a string map" assertion; nil in production.
//
// It (and mergeDroppedForwardTermObserver below) are package globals on the merge hot path: a test
// installs one, runs a merge synchronously on the worker, then clears it (defer). This is safe ONLY
// because no merge test runs in parallel — a test that installs either hook MUST NOT call t.Parallel
// (the merge tests don't). The production nil-check is one predictable branch.
var mergeRemapObserver func(remap [][]uint32)

// mergeDroppedForwardTermObserver, when non-nil, is invoked by mergeSegments once for each forward
// term it had to drop because the term's keyword was reclaimed by a covering merge (a self-heal of a
// pre-existing inverted/forward inconsistency in the input). Test-only observability (P8); nil in
// production. A non-zero count signals a contained data-quality event, never a crash. Same parallel-
// safety constraint as mergeRemapObserver (no t.Parallel in a test that installs it).
var mergeDroppedForwardTermObserver func()

// mergeCursor streams one source segment's (key,value) records in sorted order, decoding external
// values on the fly. Only ONE decompressed block is resident per cursor, so a k-way merge over K
// sources holds K blocks — bounded memory regardless of segment size. (Port of the spike
// mergeCursor; keys are now full []byte = keyType(1) tableId(4 BE) keyword|docid.)
type mergeCursor struct {
	s    *segment
	bi   int
	blk  []byte
	p    int
	key  []byte
	val  []byte
	done bool
}

func newMergeCursor(s *segment) *mergeCursor {
	c := &mergeCursor{s: s}
	if len(s.idx) > 0 {
		c.blk = s.blockBytes(0)
	}
	c.advance()
	return c
}

// advance reads the next record into c.key/c.val, decoding an external value if needed, crossing
// block boundaries, and setting c.done at end-of-segment.
func (c *mergeCursor) advance() {
	for {
		if c.p < len(c.blk) {
			kl, n := binary.Uvarint(c.blk[c.p:])
			c.p += n
			c.key = c.blk[c.p : c.p+int(kl)]
			c.p += int(kl)
			flag := c.blk[c.p]
			c.p++
			if flag == 0 {
				vl, n2 := binary.Uvarint(c.blk[c.p:])
				c.p += n2
				c.val = c.blk[c.p : c.p+int(vl)]
				c.p += int(vl)
			} else {
				off, n2 := binary.Uvarint(c.blk[c.p:])
				c.p += n2
				cl, n3 := binary.Uvarint(c.blk[c.p:])
				c.p += n3
				c.val = c.s.readExternal(int64(off), int(cl))
			}
			return
		}
		c.bi++
		if c.bi >= len(c.s.idx) {
			c.done = true
			return
		}
		c.blk = c.s.blockBytes(c.bi)
		c.p = 0
	}
}

// keyType / keyTableId pull the type byte and 4-byte BE tableId out of a full key.
func keyType(k []byte) byte      { return k[0] }
func keyTableId(k []byte) uint32 { return binary.BigEndian.Uint32(k[1:5]) }

// mergeResult is the product of one mergeSegments call: the opened output segment + its segMeta.
type mergeResult struct {
	seg *segment
	sm  segMeta
}

// mergeSegments streams a k-way merge of segs (ordered OLDEST -> NEWEST) into one output segment at
// the given level, with per-(keyword,docid) newest-wins reconciliation + term-id ord->ord remap +
// term-dict rebuild. covering=true runs the reclaiming bottom compaction (drop fully-tombstoned
// keys, dangling tombstones, forward-tombstones, and dead-tableId keys — liveTables gates that
// last). It returns the opened output segment + its segMeta; it does NOT touch the MANIFEST or
// delete inputs (the caller's installMerge does that crash-safely).
//
// The merge is bounded-memory: one block per source cursor, the remap arrays are ints (Sum source
// term counts), and the term-dict region is rebuilt by re-reading the just-written [I] blocks.
func (s *Store) mergeSegments(segs []*segment, outId uint64, level int, dataCodec byte, covering bool, liveTables map[int]bool) mergeResult {
	curs := make([]*mergeCursor, len(segs))
	for i, seg := range segs {
		curs[i] = newMergeCursor(seg)
	}
	path := filepath.Join(s.dir, segFileName(outId))
	w := newSegWriter(path,
		newCodec(dataCodec), newCodec(s.opts.DictCodec),
		s.opts.BlockTarget, s.opts.Chunk, s.opts.InlineThreshold, true, s.opts.DictChunkBytes)

	// term-id remap: as the merged term dict ([I] keys, sorted) is emitted, each key is assigned
	// its output ordinal and, for EVERY source that contained the key (in srcOrd order), one entry
	// is appended to remap[src]. So remap[src][srcOrd] = outputOrd (or ordSentinel for a key dropped
	// by a covering merge). Because [I] sorts before [F], every remap entry is built before any
	// forward record is emitted. tableRange tracks the output's covered tableIds for prune.
	remap := make([][]uint32, len(segs))
	outOrd := uint32(0)
	minTable := uint32(0)
	maxTable := uint32(0)
	haveTable := false
	noteTable := func(t uint32) {
		if !haveTable || t < minTable {
			minTable = t
		}
		if !haveTable || t > maxTable {
			maxTable = t
		}
		haveTable = true
	}

	for {
		// Find the minimum key across all live cursors (byte-wise). first guards "no min yet".
		var min []byte
		first := true
		for _, cu := range curs {
			if cu.done {
				continue
			}
			if first || compareKeys(cu.key, min) < 0 {
				min, first = cu.key, false
			}
		}
		if first {
			break // all cursors drained
		}
		// hit = source indexes whose current key == min, in cursor order (== OLDEST -> NEWEST).
		var hit []int
		for i, cu := range curs {
			if !cu.done && equalKeys(cu.key, min) {
				hit = append(hit, i)
			}
		}

		tid := keyTableId(min)
		tableLive := liveTables == nil || liveTables[int(tid)]

		if keyType(min) == ktForward {
			// FORWARD: newest source wins (last in hit). Remap its ordinals; a tombstone (nKw=0)
			// decodes to zero ords -> remaps nothing -> re-emits nKw=0 verbatim.
			src := hit[len(hit)-1]
			if covering && (!tableLive) {
				// dead-tableId forward: drop.
			} else {
				val := curs[src].val
				ords, deleted := decodeForward(val)
				if deleted {
					// covering merge drops forward-tombstones (nothing older survives to suppress);
					// a tiered merge carries them through verbatim.
					if !covering {
						w.addEntry(min, forwardTombstone())
						noteTable(tid)
					}
				} else {
					out := make([]uint32, 0, len(ords))
					dropped := false
					for _, o := range ords {
						mo := remap[src][o]
						if mo == ordSentinel {
							// The forward references a keyword the covering merge reclaimed (fully
							// tombstoned / dead-table). This is a pre-existing inverted/forward
							// inconsistency in the input — the public write path cannot produce it, only a
							// corrupt or legacy segment. Self-heal: drop this stale term from the forward (the
							// keyword has no live posting, so the doc must not claim it) and keep going. NEVER
							// panic — this runs on the un-recovered mpsc worker, where a panic bricks the
							// process; a contained, valid output segment is the correct background-merge outcome.
							dropped = true
							if mergeDroppedForwardTermObserver != nil {
								mergeDroppedForwardTermObserver()
							}
							continue
						}
						out = append(out, mo)
					}
					// If EVERY term was reclaimed (covering merge), the doc has no live keyword left. Don't
					// emit encodeForward(nil) — that is the nKw=0 tombstone encoding, and a covering merge
					// drops forward-tombstones anyway. Drop the forward record entirely (a clean miss), the
					// same outcome a covering merge gives any doc with nothing live to point at.
					if dropped && len(out) == 0 {
						// drop the forward entirely
					} else {
						w.addEntry(min, encodeForward(out))
						noteTable(tid)
					}
				}
			}
		} else {
			// INVERTED: reconcile (keyword,docid) NEWEST-WINS across hit (oldest->newest). action
			// maps docid -> latest action (true=add, false=del); insertion-ordered isn't needed, the
			// encoders sort. We walk hit in cursor order, which IS oldest->newest, so a later source's
			// add or del overwrites an earlier one for the same docid.
			adds := map[int64]struct{}{}
			dels := map[int64]struct{}{}
			for _, i := range hit {
				ab, db := splitInvertedValue(curs[i].val)
				// A spilled/merged value never holds both an add and a del for the same docid, but
				// process adds THEN dels within one source so a del overwrites an add for the same docid
				// (the source's own single latest action lands); across sources, the LATER source wins.
				decodeDocs(ab, func(d int64) { delete(dels, d); adds[d] = struct{}{} })
				decodeDocs(db, func(d int64) { delete(adds, d); dels[d] = struct{}{} })
			}
			keep := true
			var addList, delList []int64
			if covering {
				// Bottom compaction: nothing older survives, so a del can suppress nothing — drop ALL
				// dels (dangling tombstones reclaimed). Keep adds-only. A key with no surviving add is
				// fully tombstoned -> drop the key entirely. A dead-tableId key -> drop.
				for d := range adds {
					addList = append(addList, d)
				}
				if !tableLive || len(addList) == 0 {
					keep = false
				}
			} else {
				// Tiered merge: keep both adds and dels (a del must still suppress an older add in a
				// segment NOT part of this merge); NEVER drop a key (the remap append-index == srcOrd).
				for d := range adds {
					addList = append(addList, d)
				}
				for d := range dels {
					delList = append(delList, d)
				}
			}

			if keep {
				w.addEntry(min, encodeInvertedValue(addList, delList))
				noteTable(tid)
				for _, i := range hit {
					remap[i] = append(remap[i], outOrd) // append index == this key's srcOrd in seg i
				}
				outOrd++
			} else {
				// Key dropped by the covering merge: every source that had it gets a sentinel at this
				// srcOrd so the per-source remap stays index-aligned (srcOrd -> sentinel/no-output).
				for _, i := range hit {
					remap[i] = append(remap[i], ordSentinel)
				}
			}
		}

		for _, i := range hit {
			curs[i].advance()
		}
	}

	seg := w.finish(path)
	seg.id = outId
	if mergeRemapObserver != nil {
		mergeRemapObserver(remap)
	}
	size := fileSize(path)
	sm := segMeta{
		Id:        outId,
		Level:     level,
		DataCodec: dataCodec,
		DictCodec: s.opts.DictCodec,
		MinTable:  minTable,
		MaxTable:  maxTable,
		Size:      size,
	}
	return mergeResult{seg: seg, sm: sm}
}

// compareKeys / equalKeys order/compare full []byte keys lexicographically (the on-disk sort
// order). Kept tiny and inlinable; bytes.Compare would do but this avoids the import churn and is
// the merge inner loop.
func compareKeys(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}

func equalKeys(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// installMerge crash-safely swaps the MANIFEST to replace inputs (by id) with the merged output,
// publishes the new segment snapshot for readers, then DEFERS each input's deletion until no reader
// references it (P9/T8 refcount). MUST run on the worker (it mutates s.man/s.segs under the lock). A
// crash between writing the new segment and the MANIFEST swap leaves the inputs live + the new
// segment an unreferenced orphan (GC'd on next Open, design §9).
func (s *Store) installMerge(inputIds map[uint64]bool, res mergeResult) error {
	res.seg.refs.Store(1) // P9: the published snapshot will hold one ref on the merged output

	// Persist the new MANIFEST, then publish — keeping the slow fsync OUT of the reader-blocking
	// critical section (P9/T8, design §6: "readers never block on a writer's I/O — the lock is held
	// only for the O(1) pointer swap and ref bookkeeping, never for spill/merge/file work"). All
	// writes run on the single mpsc worker, so the lock here guards s.man only against concurrent
	// READERS (tableInfo). So: (a) under the lock, compute the new segMeta set, swap s.man.Segments,
	// and marshal the manifest to bytes (cheap, no I/O); (b) OUTSIDE the lock, do the two fsyncs
	// (writeManifestBytes) so a concurrent Search/GetDocs is not blocked on them; (c) re-take the lock
	// only for the O(1) s.segs swap + publishSnapshotLocked + retire of the inputs.
	//
	// Persist-then-publish keeps s.man/s.segs and the on-disk MANIFEST consistent on every failure
	// path: do NOT mutate s.segs (the live handle set) until writeManifestBytes succeeds. If the write
	// fails after we swapped s.man, we roll s.man back to the pre-merge set under the lock; s.segs was
	// never touched, so the in-memory state, the still-old on-disk MANIFEST, and the open handles all
	// agree (the just-written orphan output is removed). A crash between writing the new segment and
	// the MANIFEST swap leaves the inputs live + the new segment an unreferenced orphan (GC'd on next
	// Open, design §9).
	s.mu.Lock()
	newSegs := make([]segMeta, 0, len(s.man.Segments))
	for _, sm := range s.man.Segments {
		if !inputIds[sm.Id] {
			newSegs = append(newSegs, sm)
		}
	}
	newSegs = append(newSegs, res.sm)
	prevSegs := s.man.Segments
	s.man.Segments = newSegs
	b, err := marshalManifest(s.man)
	if err != nil {
		s.man.Segments = prevSegs // roll the in-memory manifest back to the pre-merge set
		s.mu.Unlock()
		res.seg.refs.Store(0) // never published: drop the ref before removing the orphan output
		res.seg.close()
		os.Remove(res.seg.path)
		return err
	}
	s.mu.Unlock()

	if err := writeManifestBytes(s.dir, b); err != nil {
		s.mu.Lock()
		s.man.Segments = prevSegs // roll the in-memory manifest back to the pre-merge set
		s.mu.Unlock()
		res.seg.refs.Store(0) // never published: drop the ref before removing the orphan output
		res.seg.close()
		os.Remove(res.seg.path) // the output is unreferenced; remove only after the rollback
		return err
	}

	// Publish the new live segment slice (oldest->newest by id, preserving search's newest-wins
	// scan order). Collect the retired input handles to retire AFTER publishing the new snapshot.
	s.mu.Lock()
	var retired []*segment
	live := make([]*segment, 0, len(s.segs))
	for _, seg := range s.segs {
		if inputIds[seg.id] {
			retired = append(retired, seg)
			continue
		}
		live = append(live, seg)
	}
	live = append(live, res.seg)
	sortSegmentsById(live)
	s.segs = live
	// Publish the new snapshot BEFORE retiring the inputs, all under the write lock: a reader either
	// already loaded the OLD snapshot and took its refs on the inputs under its RLock (so retire's
	// decref won't hit zero — that reader's release will, later), or it loads the NEW snapshot here
	// and never references the inputs. Either way no in-flight reader is left scanning a torn-down fd.
	s.publishSnapshotLocked()
	for _, seg := range retired {
		s.dictCache.purge(seg.id) // chunk-LRU: drop the gone segment's cached dict chunks on swap
		seg.retire()              // deferred deletion: close + unlink only once refcount hits zero
	}
	s.mu.Unlock()
	return nil
}

// sortSegmentsById orders segments ascending by seal-sequence id (== oldest->newest), which Search
// relies on (it scans newest->oldest by walking the slice in reverse). A merged segment gets a
// FRESH (higher) id than all its inputs, so it correctly sorts as the newest of the set it replaces.
func sortSegmentsById(segs []*segment) {
	for i := 1; i < len(segs); i++ {
		for j := i; j > 0 && segs[j-1].id > segs[j].id; j-- {
			segs[j-1], segs[j] = segs[j], segs[j-1]
		}
	}
}

// nextSegId allocates and reserves the next seal-sequence id under the lock (mirrors spill's bump).
func (s *Store) nextSegId() uint64 {
	s.mu.Lock()
	id := s.man.NextSegId
	s.man.NextSegId++
	s.mu.Unlock()
	return id
}

// maybeMerge applies the tiered policy repeatedly: while some level L has >= Fanout segments, merge
// ALL of that level's segments (oldest->newest) into one level-(L+1) segment. After the tiered loop
// it checks the covering-merge trigger (bottom-level dead fraction >= the threshold) and fires one
// if needed. MUST run on the worker. This is the production maybeMerge (the spike merges
// synchronously inside spill; here it is a worker task enqueued after each spill).
func (s *Store) maybeMerge() error {
	for {
		merged, err := s.mergeOneLevel()
		if err != nil {
			return err
		}
		if !merged {
			break
		}
	}
	return s.maybeCoveringMerge()
}

// mergeOneLevel finds the LOWEST level with >= Fanout live segments and merges all of them into one
// next-level segment. Returns merged=false when no level qualifies. Segments are merged
// oldest->newest (ascending id) so newest-wins reconciliation is correct.
func (s *Store) mergeOneLevel() (bool, error) {
	s.mu.RLock()
	byLevel := map[int][]segMeta{}
	maxL := 0
	for _, sm := range s.man.Segments {
		byLevel[sm.Level] = append(byLevel[sm.Level], sm)
		if sm.Level > maxL {
			maxL = sm.Level
		}
	}
	level := -1
	for l := 0; l <= maxL; l++ {
		if len(byLevel[l]) >= s.opts.Fanout {
			level = l
			break
		}
	}
	s.mu.RUnlock()
	if level < 0 {
		return false, nil
	}

	metas := byLevel[level]
	sortSegMetasById(metas)
	inputIds := map[uint64]bool{}
	for _, m := range metas {
		inputIds[m.Id] = true
	}
	segs := s.segsByIds(inputIds) // oldest->newest
	outId := s.nextSegId()
	res := s.mergeSegments(segs, outId, level+1, s.opts.DataCodecMerged, false, nil)
	return true, s.installMerge(inputIds, res)
}

// maybeCoveringMerge fires a full bottom-up covering merge when the bottom level's dead fraction
// (tombstoned + superseded postings / total postings) crosses the threshold (design §6 default
// ~25%). It compacts the bottom level together with EVERYTHING above it (all live segments), so it
// can reclaim dangling tombstones, fully-tombstoned keys, forward-tombstones and dead-tableId keys.
// Returns nil (no-op) when the index is small or clean. MUST run on the worker.
func (s *Store) maybeCoveringMerge() error {
	s.mu.RLock()
	nseg := len(s.man.Segments)
	s.mu.RUnlock()
	if nseg < 2 {
		return nil // nothing to reclaim across (a single segment is already compact)
	}
	frac := s.bottomDeadFraction()
	if frac < coveringDeadThreshold {
		return nil
	}
	return s.coveringMerge()
}

// coveringDeadThreshold is the bottom-level dead-fraction trigger for a covering merge (design §6).
const coveringDeadThreshold = 0.25

// coveringMerge compacts ALL live segments (the bottom level + everything above) into one segment
// at the max level + 0 (it stays the bottom), reclaiming everything a covering merge can. It is
// also what DeleteTable schedules so a dropped table's bytes go even if its segments sit at the
// bottom with no further writes. MUST run on the worker.
func (s *Store) coveringMerge() error {
	s.mu.RLock()
	if len(s.man.Segments) == 0 {
		s.mu.RUnlock()
		return nil
	}
	metas := append([]segMeta(nil), s.man.Segments...)
	level := 0
	for _, sm := range metas {
		if sm.Level > level {
			level = sm.Level
		}
	}
	liveTables := map[int]bool{}
	for id := range s.man.Tables {
		liveTables[id] = true
	}
	s.mu.RUnlock()

	sortSegMetasById(metas)
	inputIds := map[uint64]bool{}
	for _, m := range metas {
		inputIds[m.Id] = true
	}
	segs := s.segsByIds(inputIds)
	outId := s.nextSegId()
	res := s.mergeSegments(segs, outId, level, s.opts.DataCodecMerged, true, liveTables)
	return s.installMerge(inputIds, res)
}

// bottomDeadFraction estimates the bottom (max) level's dead fraction = (tombstoned + superseded
// postings) / total postings across that level's segments. It is a STREAMING k-way pass over the
// already-sorted bottom segments (one decompressed block per cursor + a per-keyword running map),
// so its resident memory is O(K cursors + the docids of ONE keyword), NEVER a global map over every
// posting in the level — bounded regardless of how large the bottom level grows (design §3). For
// each (tableId,keyword,docid) it counts a docid as DEAD if its newest action across the bottom
// level is a tombstone OR if an older appearance is superseded by a newer add/del (a duplicate).
// Forward records are skipped (the fraction is about inverted-posting reclamation). The full [I]
// key carries the 4-byte tableId, so postings of distinct tables are counted independently (two
// tables sharing a keyword+docid never collide).
func (s *Store) bottomDeadFraction() float64 {
	s.mu.RLock()
	maxL := 0
	for _, sm := range s.man.Segments {
		if sm.Level > maxL {
			maxL = sm.Level
		}
	}
	inputIds := map[uint64]bool{}
	for _, sm := range s.man.Segments {
		if sm.Level == maxL {
			inputIds[sm.Id] = true
		}
	}
	s.mu.RUnlock()
	segs := s.segsByIds(inputIds) // oldest->newest
	if len(segs) == 0 {
		return 0
	}

	curs := make([]*mergeCursor, len(segs))
	for i, seg := range segs {
		curs[i] = newMergeCursor(seg)
	}

	// latest[docid] = newest action for this ONE [I] key (true=add,false=del); count[docid] = how
	// many appearances. Reused (cleared) per keyword key, so resident size is bounded by a single
	// keyword's distinct docids — never the whole level. total/dead accumulate across all keys.
	latest := map[int64]bool{}
	count := map[int64]int{}
	var total, dead int64

	flushKey := func() {
		for d, c := range count {
			// The surviving posting is one live add (if the newest action for the pair is an add);
			// every other appearance — a superseded add, a dangling tombstone, or a tombstone over an
			// add — is dead. If the newest action is a del, ALL appearances of the pair are dead.
			survivors := int64(0)
			if latest[d] {
				survivors = 1
			}
			dead += int64(c) - survivors
		}
		// Clear in place (cheap; Go reuses the backing buckets) for the next keyword key.
		for d := range count {
			delete(count, d)
			delete(latest, d)
		}
	}

	for {
		// Minimum key across live cursors (== the next [I] key in sort order). [F] keys sort AFTER
		// every [I] key (ktForward > ktInverted), so once we hit a forward we are done with inverted.
		var min []byte
		first := true
		for _, cu := range curs {
			if cu.done {
				continue
			}
			if first || compareKeys(cu.key, min) < 0 {
				min, first = cu.key, false
			}
		}
		if first {
			break // all cursors drained
		}
		if keyType(min) != ktInverted {
			break // reached the forward region; nothing left to count
		}
		// Tally every source whose current key == min (this exact tableId+keyword), oldest->newest.
		for _, cu := range curs {
			if cu.done || !equalKeys(cu.key, min) {
				continue
			}
			ab, db := splitInvertedValue(cu.val)
			decodeDocs(ab, func(d int64) {
				latest[d] = true
				count[d]++
				total++
			})
			decodeDocs(db, func(d int64) {
				latest[d] = false
				count[d]++
				total++
			})
			cu.advance()
		}
		flushKey()
	}
	if total == 0 {
		return 0
	}
	return float64(dead) / float64(total)
}

// segsByIds returns the open segment handles whose ids are in ids, in OLDEST -> NEWEST (ascending
// id) order — the order mergeSegments needs for newest-wins reconciliation. Read under the lock.
func (s *Store) segsByIds(ids map[uint64]bool) []*segment {
	s.mu.RLock()
	out := make([]*segment, 0, len(ids))
	for _, seg := range s.segs {
		if ids[seg.id] {
			out = append(out, seg)
		}
	}
	s.mu.RUnlock()
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].id > out[j].id; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// sortSegMetasById orders segMetas ascending by id (oldest->newest) in place.
func sortSegMetasById(metas []segMeta) {
	for i := 1; i < len(metas); i++ {
		for j := i; j > 0 && metas[j-1].Id > metas[j].Id; j-- {
			metas[j-1], metas[j] = metas[j], metas[j-1]
		}
	}
}
