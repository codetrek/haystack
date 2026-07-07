package invertedstore

import (
	"container/list"
	"encoding/binary"
	"sort"
	"sync"
)

// dictcache.go — P5 (design §8 resolution, §6 chunk LRU; task T3).
//
// The forward map is stored as segment-local term-ids (ordinals into the segment's own sorted
// inverted term dict, §8). Resolving a doc's ordinals back to keyword STRINGS — which the Update
// diff (P7) needs — reads the winning segment's compact term-dict region: ord -> chunk via a
// binary search on firstOrd, then decompress that ~4 KiB chunk and slice out the string.
//
// chunkLRU is the Store-level cache of those DECOMPRESSED dict chunks, keyed by
// (segmentId, chunkIdx) so the same hot chunk shared across docs (common terms, recently-edited
// files) stays resident under real editing locality. It is byte-budgeted (Options.ChunkCacheBytes,
// default 32 MiB) and mutex-guarded; entries of a merged-away segment are purged on a MANIFEST
// swap (P8 merge), so the cache never pins a segment that is gone. Search never touches it — it is
// read on the Update (forward) path only.

// chunkCacheKey identifies one decompressed dict chunk by its OWNING segment id and chunk index.
// Keying on the stable seal-sequence id (not the *segment pointer) lets purge() drop a merged-away
// segment's chunks even after its handle is replaced, and keeps the key comparable for the map.
type chunkCacheKey struct {
	segId    uint64
	chunkIdx int
}

// chunkCacheEntry is one cached decompressed chunk; raw is the chunk's uncompressed bytes
// ((uvarint(klen) keyword)* in ordinal order). key is stored so an LRU eviction from the back of
// the list can delete the matching map entry.
type chunkCacheEntry struct {
	key chunkCacheKey
	raw []byte
}

// chunkLRU is a byte-budgeted LRU over decompressed term-dict chunks, keyed by
// (segmentId, chunkIdx). Most-recently-used at the FRONT; eviction pops the BACK until used <=
// budget (always keeping at least one entry so a single oversized chunk can still be served).
type chunkLRU struct {
	mu     sync.Mutex
	budget int64
	used   int64
	ll     *list.List
	m      map[chunkCacheKey]*list.Element
}

func newChunkLRU(budget int64) *chunkLRU {
	if budget <= 0 {
		budget = 32 << 20
	}
	return &chunkLRU{budget: budget, ll: list.New(), m: map[chunkCacheKey]*list.Element{}}
}

// get returns the decompressed bytes of segment s's dict chunk ci, hitting the cache or
// reading+decompressing on a miss and inserting (then evicting LRU back entries to honor the
// budget). The caller must have already run s.ensureDictIndex() (resolveOrdsCached does) so
// s.dictChunks is fully built and immutable; this method's mutex guards only the LRU map/list,
// while concurrent reads of s.dictChunks[ci] are safe because ensureDictIndex builds that slice
// once (sync.Once) and never mutates it again.
func (c *chunkLRU) get(s *segment, ci int) []byte {
	k := chunkCacheKey{segId: s.id, chunkIdx: ci}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[k]; ok {
		c.ll.MoveToFront(e)
		return e.Value.(*chunkCacheEntry).raw
	}
	dc := s.dictChunks[ci]
	comp := make([]byte, dc.compLen)
	mustReadAt(s.f, comp, dc.compOff)
	raw := s.dictCodec.decompress(comp, dc.rawLen)
	c.m[k] = c.ll.PushFront(&chunkCacheEntry{k, raw})
	c.used += int64(len(raw))
	for c.used > c.budget && c.ll.Len() > 1 {
		back := c.ll.Back()
		ce := back.Value.(*chunkCacheEntry)
		c.used -= int64(len(ce.raw))
		c.ll.Remove(back)
		delete(c.m, ce.key)
	}
	return raw
}

// purge drops every cached chunk owned by segId. Called on a MANIFEST swap when a merge retires
// segId so the cache never holds (or serves) bytes for a segment that is gone. Concurrency-safe.
func (c *chunkLRU) purge(segId uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.m {
		if k.segId == segId {
			ce := e.Value.(*chunkCacheEntry)
			c.used -= int64(len(ce.raw))
			c.ll.Remove(e)
			delete(c.m, k)
		}
	}
}

// usedBytes is the cache's current decompressed-byte footprint (test/observability helper).
func (c *chunkLRU) usedBytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used
}

// countForSeg returns how many cached chunks currently belong to segId (test/observability helper).
func (c *chunkLRU) countForSeg(segId uint64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for k := range c.m {
		if k.segId == segId {
			n++
		}
	}
	return n
}

// resolveOrdsCached maps requested ordinals -> keyword strings for ONE segment, reading each
// needed dict chunk through the Store-level chunk LRU (so hot chunks are shared/resident). A
// straight port of the spike's resolveOrdsChunk, but the LRU is keyed by (segmentId, chunkIdx).
// s.ensureDictIndex builds the (tiny) per-chunk firstOrd/offset index lazily.
func (s *segment) resolveOrdsCached(need map[uint32]struct{}, lru *chunkLRU) map[uint32]string {
	s.ensureDictIndex()
	res := make(map[uint32]string, len(need))
	byChunk := map[int][]uint32{}
	for o := range need {
		i := sort.Search(len(s.dictChunks), func(i int) bool { return s.dictChunks[i].firstOrd > o }) - 1
		if i < 0 {
			continue
		}
		byChunk[i] = append(byChunk[i], o)
	}
	for ci, ords := range byChunk {
		raw := lru.get(s, ci)
		c := s.dictChunks[ci]
		want := make(map[uint32]struct{}, len(ords))
		for _, o := range ords {
			want[o] = struct{}{}
		}
		cur := c.firstOrd
		for q := 0; q < len(raw); {
			kl, m := binary.Uvarint(raw[q:])
			q += m
			if _, ok := want[cur]; ok {
				res[cur] = string(raw[q : q+int(kl)])
			}
			q += int(kl)
			cur++
		}
	}
	return res
}

// forwardKeywords reads a doc's CURRENT keyword set (newest-wins forward lookup, §6/§8): the
// HEAD's pending forward FIRST (so a doc edited twice within one spill window diffs against its
// live keywords, not a stale sealed copy), then sealed segments newest -> oldest. The winning
// forwardValue is decoded; nKw==0 is the forward-tombstone => (nil, deleted=true). Otherwise the
// term-ids are resolved to strings via the winning segment's term-dict region through the chunk
// LRU. A miss everywhere => (nil, false): the doc is unknown (a cold doc on the build path).
//
// This is the read the Update diff (P7) pays under term-id; Search never calls it.
func (s *Store) forwardKeywords(tableId int, docid int64) (words []string, deleted bool) {
	s.mu.RLock()
	// 1. HEAD first (newest of all): an explicit pending delete is a tombstone; a pending
	//    forward set wins over any sealed record for this docid. Finding either in the head is a
	//    real forward read (the doc was seen before), so the hook fires.
	if w, del, found := headForwardLookup(s.head[tableId], docid); found {
		s.mu.RUnlock()
		s.noteForwardRead()
		return w, del
	}
	// 1b. The spilling tier (item F, B1): heads DETACHED for off-worker encode, between the live head
	//     and the sealed segments, newest -> oldest by detach order. A doc whose forward lives only in a
	//     detached head must still resolve here, or a re-post would diff against an empty old set and
	//     drop no tombstone (silent corruption). Resolved (copied) inside the SAME RLock as the live
	//     head read, before RUnlock — acquireSnapshot below re-takes the RLock, so this must NOT nest.
	for i := len(s.spilling) - 1; i >= 0; i-- { // newest detached head wins
		e := s.spilling[i]
		if e.tableId != tableId {
			continue
		}
		// Task 7C inserts the docid-range skip here: if docid < e.minDocid || docid > e.maxDocid { continue }
		if w, del, found := headForwardLookup(e.head, docid); found {
			s.mu.RUnlock()
			s.noteForwardRead()
			return w, del
		}
	}
	s.mu.RUnlock()

	// Snapshot the live segments under the refcount model (P9): forwardKeywords runs ON the worker
	// (from applyBatch), so no concurrent merge can retire a segment mid-scan, but it uses the same
	// acquire/release path as Search for uniformity — the refs just bump and drop on the worker. The
	// scan below (file I/O + decompress) runs without holding the Store lock. The head RLock above is
	// released FIRST (acquireSnapshot re-takes the RLock; holding it recursively could deadlock a
	// waiting writer); forwardKeywords is on the worker so the head can't change between the windows.
	segs := s.acquireSnapshot()
	defer s.releaseSnapshot(segs)

	// A cold build has no sealed segments and missed the head above, so there is NOTHING to read
	// — return write-only WITHOUT firing the hook (design §6: "all docs are new ⇒ the forward read
	// misses ⇒ write-only"). Only a scan that actually touches segment I/O counts as a forward read.
	if len(segs) == 0 {
		return nil, false
	}

	tid := uint32(tableId)
	probed := false
	for i := len(segs) - 1; i >= 0; i-- { // newest wins
		seg := segs[i]
		if !seg.coversDocid(docid) {
			continue // B: no forward record for docid can exist in this segment — skip, no I/O
		}
		if !probed {
			s.noteForwardRead() // first segment we actually touch = the first real forward read
			probed = true
		}
		s.noteForwardProbe()
		val, ok := seg.lookupForward(forwardKey(tid, docid))
		if !ok {
			continue
		}
		ords, del := decodeForward(val)
		if del {
			return nil, true // forward-tombstone: the doc is deleted, older records cannot win
		}
		need := make(map[uint32]struct{}, len(ords))
		for _, o := range ords {
			need[o] = struct{}{}
		}
		res := seg.resolveOrdsCached(need, s.dictCache)
		out := make([]string, 0, len(ords))
		for _, o := range ords {
			w, ok := res[o]
			if !ok {
				// Every forward ordinal MUST resolve: the spill invariant writes a doc's
				// forward only after emitting each of its keywords as an [I] term, so the
				// ordinal is always in this segment's term dict. An unresolvable ordinal means
				// a corrupt/inconsistent segment; fail loud rather than silently injecting an
				// empty keyword that would corrupt the Update diff (consistent with mustReadAt).
				panic("invertedstore: unresolvable forward ordinal in segment")
			}
			out = append(out, w)
		}
		return out, false
	}
	return nil, false
}
