package invertedstore

import (
	"strings"
)

// SearchResult is the membership result of a Search/GetDocs: the live docids whose keyword(s)
// matched. WildDocIds is preserved for compatibility with invertedindex's SearchResult (the
// suffix/wildcard path) — the store does NOT populate it; it is caller-populated per design §4.
type SearchResult struct {
	DocIds     map[int64]struct{} `json:"docIds"`
	WildDocIds map[int64]struct{} `json:"wildDocIds,omitempty"`
}

// Search returns the live docids of every keyword that has the lowercased query as a PREFIX,
// in the given table. It is the prefix scan of design §4/§6:
//
//  1. Snapshot the head + the live segment set under the RLock: copy the head's matching deltas
//     out of the live maps and copy the segment slice, then release. (The head is mutated only on
//     the worker under the write lock, so it MUST be read under the RLock; the segment FILES are
//     immutable, so the segment scan runs lock-free on the snapshot.)
//  2. Prefix-scan by ([I], tableId, lowercased query) over the head, then over the segments
//     NEWEST -> OLDEST.
//  3. Resolve each (keyword, docid) newest-wins: the FIRST source to mention a (keyword,docid)
//     — an add OR a tombstone — decides it; older mentions are ignored. The head is newest, then
//     segments in reverse order. Within ONE source's value for a keyword, a tombstone wins over
//     an add (a spilled value never holds both for a docid, but a del still claims the pair so an
//     older add can't resurrect it).
//  4. Apply filterKeyword (skip a whole keyword whose string the caller rejects) and limit.
//
// An absent/deleted tableId returns an empty result immediately (no segment touched). WildDocIds
// is left nil — the store never populates it.
func (s *Store) Search(tableId int, query string, limit int, filterKeyword func(string) bool) SearchResult {
	res := SearchResult{DocIds: map[int64]struct{}{}}
	if _, ok := s.tableInfo(tableId); !ok {
		return res
	}
	tid := uint32(tableId)
	lo := invertedKey(tid, strings.ToLower(query))
	hi := prefixUpper(lo)

	// Per-keyword newest-wins resolution. seen[keyword] holds the set of docids already DECIDED
	// for that keyword (an add that survived or a tombstone that killed it); the first source to
	// touch a (keyword,docid) wins, so a docid already in seen[keyword] is skipped by every older
	// source. live holds the surviving (present) docids per keyword. We resolve per keyword (not a
	// flat docid set) because Search unions MANY keywords under one prefix, and a tombstone for kw1
	// must not suppress an add of the same docid under a different kw2.
	seen := map[string]map[int64]struct{}{}
	live := map[string]map[int64]struct{}{}
	ensure := func(m map[string]map[int64]struct{}, kw string) map[int64]struct{} {
		s := m[kw]
		if s == nil {
			s = map[int64]struct{}{}
			m[kw] = s
		}
		return s
	}

	// merge applies one source's postings for a keyword under newest-wins. dels are processed
	// FIRST so a tombstone claims the (kw,docid) before an add in the SAME source could mark it
	// live; then adds add only docids not yet decided. (Mirrors the spike search closure, which
	// stamps the del-set before the add-set.)
	merge := func(kw string, adds, dels []int64) {
		if filterKeyword != nil && !filterKeyword(kw) {
			return
		}
		sk := ensure(seen, kw)
		for _, d := range dels {
			if _, ok := sk[d]; ok {
				continue // older source already decided this (kw,docid)
			}
			sk[d] = struct{}{} // tombstone wins; not live
		}
		lk := ensure(live, kw)
		for _, d := range adds {
			if _, ok := sk[d]; ok {
				continue
			}
			sk[d] = struct{}{}
			lk[d] = struct{}{}
		}
	}

	// headPosting is a keyword's head deltas COPIED out of the live head maps under the RLock, so
	// the (immutable) segment scan below can run lock-free without racing the worker that mutates
	// the head under s.mu.Lock() (head.go addPosting/tombstonePosting).
	type headPosting struct {
		kw         string
		adds, dels []int64
	}

	// 1. Snapshot. The head is mutated only on the worker under s.mu.Lock(), so we MUST read its
	//    matching deltas (range h.inv + setToSlice of the per-keyword add/del sets) WHILE holding
	//    the RLock — copying them into local slices — and acquire the segment snapshot's reader refs
	//    in the SAME RLock window (P9 acquireSnapshotLocked), so the head-copy and the segment set are
	//    a single consistent point (a spill that moves a posting head->segment can never make it
	//    vanish from BOTH). The segment FILES are immutable so the scan runs lock-free after RUnlock;
	//    releaseSnapshot drops the refs (and unlinks a merged-away file once this was its last reader).
	q := strings.ToLower(query)
	s.mu.RLock()
	h := s.head[tableId]
	var headHits []headPosting
	if h != nil {
		for kw, pd := range h.inv {
			if !strings.HasPrefix(kw, q) {
				continue
			}
			headHits = append(headHits, headPosting{kw: kw, adds: setToSlice(pd.adds), dels: setToSlice(pd.dels)})
		}
	}
	segs := s.acquireSnapshotLocked()
	s.mu.RUnlock()
	defer s.releaseSnapshot(segs)

	// 2a. Head is the newest source: merge its already-copied per-keyword deltas.
	for _, hp := range headHits {
		merge(hp.kw, hp.adds, hp.dels)
	}

	// 2b. Segments newest -> oldest.
	for i := len(segs) - 1; i >= 0; i-- {
		segs[i].scanPrefix(lo, hi, func(key, value []byte) {
			kw := string(key[5:]) // keyType(1) + tableId(4 BE) then keyword
			ab, db := splitInvertedValue(value)
			var adds, dels []int64
			decodeDocs(ab, func(d int64) { adds = append(adds, d) })
			decodeDocs(db, func(d int64) { dels = append(dels, d) })
			merge(kw, adds, dels)
		})
	}

	// 3. Union the surviving docids across all matched keywords, honoring limit (limit <= 0 = all).
	for _, lk := range live {
		for d := range lk {
			if limit > 0 && len(res.DocIds) >= limit {
				if _, dup := res.DocIds[d]; !dup {
					return res
				}
				continue
			}
			res.DocIds[d] = struct{}{}
		}
	}
	return res
}

// GetDocs returns the live docids of the EXACT keyword key (no lowercasing, no filterKeyword, no
// limit). It is kept separate from Search precisely so the fixed-width 4-byte tableId prefix can
// never leak a longer keyword: a Search by prefix "a" matches "a", "ab", "abc", … but GetDocs("a")
// must match ONLY the keyword "a". We enforce this by resolving a SINGLE exact key — scanPrefix
// over [key, key++) still visits the whole prefix block, so we compare the visited key for exact
// byte-equality and ignore any "a"+suffix record.
func (s *Store) GetDocs(tableId int, key string) SearchResult {
	res := SearchResult{DocIds: map[int64]struct{}{}}
	if _, ok := s.tableInfo(tableId); !ok {
		return res
	}
	tid := uint32(tableId)
	want := invertedKey(tid, key)
	hi := prefixUpper(want)

	seen := map[int64]struct{}{}
	live := map[int64]struct{}{}
	merge := func(adds, dels []int64) {
		for _, d := range dels {
			if _, ok := seen[d]; ok {
				continue
			}
			seen[d] = struct{}{}
		}
		for _, d := range adds {
			if _, ok := seen[d]; ok {
				continue
			}
			seen[d] = struct{}{}
			live[d] = struct{}{}
		}
	}

	// Snapshot. Copy the head's matching deltas out of the live maps WHILE holding the RLock (the
	// worker mutates h.inv[key].adds/dels under s.mu.Lock()), and acquire the segment snapshot's
	// reader refs in the SAME RLock window (P9). Segment files are immutable, so the segment scan
	// below runs lock-free on the refcounted snapshot; releaseSnapshot drops the refs afterward.
	s.mu.RLock()
	h := s.head[tableId]
	var headAdds, headDels []int64
	headHit := false
	if h != nil {
		if pd := h.inv[key]; pd != nil {
			headHit = true
			headAdds = setToSlice(pd.adds)
			headDels = setToSlice(pd.dels)
		}
	}
	segs := s.acquireSnapshotLocked()
	s.mu.RUnlock()
	defer s.releaseSnapshot(segs)

	// Head is newest.
	if headHit {
		merge(headAdds, headDels)
	}

	// Segments newest -> oldest; compare each visited key for EXACT equality so a longer keyword
	// sharing the prefix (the "a" vs "ab" leak) is rejected.
	for i := len(segs) - 1; i >= 0; i-- {
		segs[i].scanPrefix(want, hi, func(key, value []byte) {
			if string(key) != string(want) {
				return
			}
			ab, db := splitInvertedValue(value)
			var adds, dels []int64
			decodeDocs(ab, func(d int64) { adds = append(adds, d) })
			decodeDocs(db, func(d int64) { dels = append(dels, d) })
			merge(adds, dels)
		})
	}

	res.DocIds = live
	return res
}
