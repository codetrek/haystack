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
	segs := s.acquireSnapshotLocked()
	s.mu.RUnlock()
	defer s.releaseSnapshot(segs)

	// 2. Head is newest: yield its live forwards first.
	for _, d := range headLive {
		if !fn(d) {
			return
		}
	}

	// 3. Segments newest -> oldest. Scan the table's whole [F] keyspace; the first segment (newest)
	//    to mention a docid decides it — a forward-tombstone marks it dead (decided, never yielded),
	//    a live forward yields it (also decided so an older segment cannot re-yield a duplicate).
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
			_, del := decodeForward(value)
			if del {
				return // forward-tombstone: dead, decided, not yielded
			}
			if !fn(docid) {
				stop = true
			}
		})
		if stop {
			return
		}
	}
}

// forwardKeyPrefix is the [F] tableId key prefix (no docid) — the lower bound for scanning a table's
// entire forward keyspace. Shares the layout of forwardKey: keyType(1) + tableId(4 BE).
func forwardKeyPrefix(tableId uint32) []byte {
	b := make([]byte, 5)
	b[0] = ktForward
	binary.BigEndian.PutUint32(b[1:5], tableId)
	return b
}
