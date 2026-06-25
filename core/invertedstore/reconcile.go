package invertedstore

// reconcile.go — §9 durability hook: the forward-docid enumeration the indexer drives the
// deletion-reconciliation pass over on Open.
//
// Recovery is INDEXER-driven (design §9): the store keeps no recovery watermark, only
// crash-consistency (sealed segments durable, head volatile). On Open the indexer re-Updates every
// source doc newer than its OWN durable cursor and reconciles deletions — a docid that is LIVE in
// the store's forward map but ABSENT from source must be re-Updated with empty keywords (= delete).
// To drive that deletion pass the indexer needs to enumerate the store's currently-live forward
// docids; ForwardDocids is that enumeration hook (the one PUBLIC API the §9 contract was missing).

import (
	"encoding/binary"
)

// ForwardDocids invokes fn for every docid that is currently LIVE (present, not tombstoned) in the
// forward map of tableId, resolving newest-wins exactly like forwardKeywords: the head's pending
// forward is newest, then sealed segments newest -> oldest, and the FIRST source to mention a docid
// — a live forward OR a forward-tombstone — decides it. A docid the head/newer segment deleted is
// never yielded even if an older segment still holds a live forward for it. fn returning false stops
// the enumeration early.
//
// It is the §9 deletion-reconciliation hook: the indexer calls it on Open, and for each yielded
// docid that is ABSENT from its source view re-Updates with empty keywords (= delete). Re-Update is
// idempotent in result (§9), so the indexer may over-yield/over-replay without corrupting the index.
//
// Concurrency: like Search/GetDocs it snapshots the head (under the RLock) and acquires a refcounted
// segment snapshot, so it is safe to call concurrently with writes — though §9 calls it on Open
// before serving, when there are no concurrent writers. It does NOT resolve term-ids to strings (the
// deletion pass needs only the docid), so it never touches the dict cache.
func (s *Store) ForwardDocids(tableId int, fn func(docid int64) bool) {
	if _, ok := s.tableInfo(tableId); !ok {
		return
	}

	// decided[docid] = true means the newest source for this docid has already been seen, so every
	// older source is ignored for it (newest-wins). We do NOT pre-populate it from a flat set: the
	// head is processed first, then segments newest->oldest, marking each docid decided on first sight.
	decided := map[int64]struct{}{}

	// 1. Snapshot the head's forward state under the RLock (the worker mutates h.fwd/h.delForward
	//    under s.mu.Lock()), then acquire the refcounted segment snapshot in the SAME window so the
	//    head copy and the segment set are one consistent point (mirrors Search/forwardKeywords).
	s.mu.RLock()
	h := s.head[tableId]
	var headLive []int64
	if h != nil {
		for d := range h.delForward {
			decided[d] = struct{}{} // a pending delete is newest: this docid is dead, decided
		}
		for d := range h.fwd {
			// A pending delete for the same docid (recorded above) wins — they cannot both exist in
			// one head (setForward/deleteForward are mutually exclusive), but guard defensively.
			if _, dead := decided[d]; dead {
				continue
			}
			decided[d] = struct{}{}
			headLive = append(headLive, d)
		}
	}
	// Spilling tier (item F, B1): heads DETACHED for off-worker encode, between the live head and the
	// sealed segments, newest -> oldest. A tombstone in a newer-or-equal tier decides the docid dead; a
	// live forward not yet decided is yielded (after the head's headLive, before the segment resolver).
	// Collected inside this RLock window, before RUnlock.
	var spillingLive []int64 // spilling-tier live fwd docids, newest -> oldest; yielded after headLive
	for i := len(s.spilling) - 1; i >= 0; i-- {
		e := s.spilling[i]
		if e.tableId != tableId {
			continue
		}
		for d := range e.head.delForward {
			decided[d] = struct{}{} // a tombstone in a newer-or-equal tier decides the docid dead
		}
		for d := range e.head.fwd {
			if _, dead := decided[d]; dead {
				continue
			}
			decided[d] = struct{}{}
			spillingLive = append(spillingLive, d)
		}
	}
	segs := s.acquireSnapshotLocked()
	s.mu.RUnlock()
	defer s.releaseSnapshot(segs)

	// 2. Head is newest: yield its live forwards first.
	for _, d := range headLive {
		if !fn(d) {
			return
		}
	}

	// 2b. Spilling tier next, newest -> oldest (already collected in that order).
	for _, d := range spillingLive {
		if !fn(d) {
			return
		}
	}

	// 3. Segments newest -> oldest: the shared resolver yields each not-yet-decided docid's newest
	//    forward; ForwardDocids only needs the docid, so it ignores the ords and yields live ones.
	s.forEachLiveSegmentForward(tableId, decided, segs, func(docid int64, _ []uint32, deleted bool) bool {
		if deleted {
			return true // forward-tombstone: dead, decided, not yielded — keep scanning
		}
		return fn(docid)
	})
}

// forEachLiveSegmentForward is the segment half of ForwardDocids's newest-wins forward resolution,
// factored out so the Open live-recompute (recomputeLive) can reuse the EXACT same resolution
// (tombstone handling, newest-wins, per-table) and surface each live docid's ORDS — which
// ForwardDocids discards. It scans segs (a caller-owned slice — the refcounted snapshot for
// ForwardDocids, or s.segs directly on Open when there are no concurrent readers) newest -> oldest:
// the first segment to mention a docid decides it (a forward-tombstone marks it dead via deleted=true,
// a live forward yields its ords). `decided` carries any newer-source decisions (the head's, for
// ForwardDocids; empty for the head-less Open recompute). visit returning false stops early. It does
// NOT take s.mu — the caller owns the consistency of `segs` (snapshot ref or single-threaded Open).
func (s *Store) forEachLiveSegmentForward(tableId int, decided map[int64]struct{}, segs []*segment, visit func(docid int64, ords []uint32, deleted bool) (keepGoing bool)) {
	tid := uint32(tableId)
	lo := forwardKeyPrefix(tid)
	hi := prefixUpper(lo)
	for i := len(segs) - 1; i >= 0; i-- {
		stop := false
		segs[i].scanPrefix(lo, hi, func(key, value []byte) {
			if stop {
				return
			}
			docid := int64(binary.BigEndian.Uint64(key[5:13])) // keyType(1)+tableId(4 BE) then docid(8 BE)
			if _, seen := decided[docid]; seen {
				return // an equal-or-newer source already decided this docid
			}
			decided[docid] = struct{}{}
			ords, del := decodeForward(value)
			if !visit(docid, ords, del) {
				stop = true
			}
		})
		if stop {
			return
		}
	}
}

// distinctOrds returns the count of distinct ords as a slice (the input is sorted by encodeForward,
// so a single skip-equal-previous pass dedups). The forward stores RAW ords (encodeForward does not
// dedup, head.setForward keeps caller duplicates), so a doc indexed with duplicate keywords yields
// duplicate ords; distinctOrds collapses them to match the inverted index, which dedups via addPosting.
func distinctOrds(ords []uint32) []uint32 {
	if len(ords) <= 1 {
		return ords
	}
	out := ords[:1]
	for _, o := range ords[1:] {
		if o != out[len(out)-1] {
			out = append(out, o)
		}
	}
	return out
}

// forwardKeyPrefix is the [F] tableId key prefix (no docid) — the lower bound for scanning a table's
// entire forward keyspace. Shares the layout of forwardKey: keyType(1) + tableId(4 BE).
func forwardKeyPrefix(tableId uint32) []byte {
	b := make([]byte, 5)
	b[0] = ktForward
	binary.BigEndian.PutUint32(b[1:5], tableId)
	return b
}

// upgradeSegmentRanges recomputes every live segment's [minDocid,maxDocid] from its forward records
// (live AND tombstone) and rewrites the MANIFEST at FormatVersion 3. One-time legacy migration for
// the forward-skip range (B): a pre-3 manifest lacks the fields, so a stale [0,0] would mis-skip.
// Open-only (no snapshot refcount, no concurrent writers).
func (s *Store) upgradeSegmentRanges() error {
	for i := range s.segs {
		seg := s.segs[i]
		minD, maxD := emptyDocidRange()
		lo := []byte{ktForward}
		hi := prefixUpper(lo)
		seg.scanPrefix(lo, hi, func(key, _ []byte) {
			d := int64(binary.BigEndian.Uint64(key[5:13]))
			if d < minD {
				minD = d
			}
			if d > maxD {
				maxD = d
			}
		})
		seg.minDocid, seg.maxDocid = minD, maxD
		for j := range s.man.Segments {
			if s.man.Segments[j].Id == seg.id {
				s.man.Segments[j].MinDocid, s.man.Segments[j].MaxDocid = minD, maxD
			}
		}
	}
	s.man.FormatVersion = 3
	return writeManifest(s.dir, s.man)
}

// recomputeLive rebuilds s.liveByTable from the segments' forward records, catalog-gated. It is the
// authoritative anchor for the live counter (spec §4.2.1): called on Open, it is consistent with
// `written` (Σ segMeta.Postings) by construction, so a crash that dropped unspilled head writes
// drops them from `live` too — no persisted scalar to go stale, no double-count on indexer replay.
//
// It iterates ONLY catalog tables (s.man.Tables), so a table dropped by DeleteTable whose segments
// are not yet covering-merged away is NOT resurrected into `live`. The head is empty on Open, so it
// runs segments-only (decided starts empty) over s.segs directly — safe lock-free here because Open
// has no concurrent readers/writers yet (it runs after publishSnapshotLocked, before startMergeLoop).
// MUST NOT be called once the store is serving (it reads s.segs without the snapshot refcount).
func (s *Store) recomputeLive() {
	s.liveByTable = make(map[int]int64, len(s.man.Tables))
	for tid := range s.man.Tables {
		s.forEachLiveSegmentForward(tid, map[int64]struct{}{}, s.segs,
			func(_ int64, ords []uint32, deleted bool) bool {
				if !deleted {
					s.liveByTable[tid] += int64(len(distinctOrds(ords)))
				}
				return true
			})
	}
}
