package invertedstore

import "testing"

// A doc whose forward is in the spilling tier (NOT the live head, NOT a segment) must still resolve
// via forwardKeywords — the B1 read. Without the spilling tier this returns (nil,false) and a re-post
// would drop no tombstones (silent corruption).
func TestSpillingTier_ForwardKeywordsReadsDetachedHead(t *testing.T) {
	s, tid := newForwardSkipStore(t, Options{CapBytes: 1 << 20})
	s.applyForTest(tid, 1, []string{"alpha", "beta"})
	s.injectSpillingHeadForTest(tid) // doc 1's forward now lives ONLY in spilling
	if len(s.SegmentsForTest()) != 0 {
		t.Fatalf("inject must not seal a segment")
	}
	got, del := s.forwardKeywordsForTest(tid, 1)
	if del {
		t.Fatal("doc 1 is live, not deleted")
	}
	if len(got) != 2 {
		t.Fatalf("forward for doc 1 = %v, want [alpha beta] (read from the spilling tier)", got)
	}
}

// Search (prefix) must consult the spilling tier newest-wins: a keyword tombstoned ONLY in the
// detached head must suppress the SAME keyword's stale add still living in a sealed segment. Without
// the spilling tier in Search, the segment's add resurfaces and "alpha" resurrects for doc 1.
func TestSpillingTier_SearchReadsDetachedHead(t *testing.T) {
	s, tid := newForwardSkipStore(t, Options{CapBytes: 1 << 20})
	// Seal a segment where doc 1 has keyword "alpha".
	s.applyForTest(tid, 1, []string{"alpha"})
	s.spillForTest(tid)
	if len(s.SegmentsForTest()) != 1 {
		t.Fatalf("want 1 sealed segment after spill")
	}
	// Re-post doc 1 dropping "alpha" (tombstones the posting), then detach into spilling.
	s.q.RunFunc(func() error {
		s.mu.Lock()
		h := s.head[tid]
		if h == nil {
			h = newHeadTable()
			s.head[tid] = h
		}
		h.tombstonePosting("alpha", 1)
		h.setForward(1, nil)
		s.mu.Unlock()
		return nil
	})
	s.injectSpillingHeadForTest(tid) // the "alpha" tombstone now lives ONLY in spilling
	if len(s.SegmentsForTest()) != 1 {
		t.Fatalf("inject must not seal another segment")
	}
	r := s.Search(tid, "alpha", 0, nil)
	if _, ok := r.DocIds[1]; ok {
		t.Fatalf("doc 1 resurrected for prefix 'alpha' — Search did not consult the spilling tier (newest-wins tombstone)")
	}
}

// GetDocs (exact keyword) must consult the spilling tier newest-wins: same shape as Search but for the
// single exact key. The spilling head's tombstone of "alpha" for doc 1 must beat the sealed add.
func TestSpillingTier_GetDocsReadsDetachedHead(t *testing.T) {
	s, tid := newForwardSkipStore(t, Options{CapBytes: 1 << 20})
	s.applyForTest(tid, 1, []string{"alpha"})
	s.spillForTest(tid)
	if len(s.SegmentsForTest()) != 1 {
		t.Fatalf("want 1 sealed segment after spill")
	}
	s.q.RunFunc(func() error {
		s.mu.Lock()
		h := s.head[tid]
		if h == nil {
			h = newHeadTable()
			s.head[tid] = h
		}
		h.tombstonePosting("alpha", 1)
		h.setForward(1, nil)
		s.mu.Unlock()
		return nil
	})
	s.injectSpillingHeadForTest(tid)
	if got := searchDocidsForTest(t, s, tid, "alpha"); len(got) != 0 {
		t.Fatalf("GetDocs('alpha') = %v, want [] — GetDocs did not consult the spilling tier (newest-wins tombstone)", got)
	}
}

// ForwardDocids must consult the spilling tier newest-wins: a doc LIVE only in spilling is yielded, and
// a doc TOMBSTONED in spilling (but live in an older segment) is NOT yielded. Without the spilling tier
// in ForwardDocids the live-only-in-spilling doc is missed and the spilling-tombstoned doc resurrects.
func TestSpillingTier_ForwardDocidsReadsDetachedHead(t *testing.T) {
	s, tid := newForwardSkipStore(t, Options{CapBytes: 1 << 20})
	// Seal a segment where doc 2 is live (so an older tier holds a forward for it).
	s.applyForTest(tid, 2, []string{"gamma"})
	s.spillForTest(tid)
	if len(s.SegmentsForTest()) != 1 {
		t.Fatalf("want 1 sealed segment after spill")
	}
	// Detach a head where doc 1 is live (only in spilling) and doc 2 is tombstoned (overriding the seg).
	s.applyForTest(tid, 1, []string{"alpha"})
	s.applyForTest(tid, 2, nil) // deleteForward(2): forward-tombstone
	s.injectSpillingHeadForTest(tid)
	if len(s.SegmentsForTest()) != 1 {
		t.Fatalf("inject must not seal another segment")
	}
	live := map[int64]bool{}
	s.ForwardDocids(tid, func(d int64) bool { live[d] = true; return true })
	if !live[1] {
		t.Fatalf("doc 1 (live only in spilling) not yielded — ForwardDocids did not consult the spilling tier")
	}
	if live[2] {
		t.Fatalf("doc 2 (tombstoned in spilling) yielded — ForwardDocids let an older segment forward resurrect it")
	}
}

