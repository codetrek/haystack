package invertedstore

import (
	"sort"
	"sync"
	"testing"
)

// dictcache_test.go — P5 (design §8 resolution + §6 chunk LRU; task T3).
//
// Covers the newest-wins forward lookup forwardKeywords and the Store-level dict-chunk LRU:
//   - a doc only in the head resolves to its exact keyword set;
//   - a deleted doc (head delForward, AND a sealed forward-tombstone) reads empty;
//   - a doc resolved from a sealed segment equals the exact keyword set;
//   - the chunk LRU never exceeds its byte budget;
//   - newest-wins: the head's pending forward beats a stale sealed copy;
//   - ordinal-0 keyword is present (not mistaken for the nKw=0 tombstone);
//   - purge drops a retired segment's cached chunks.

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = sortedCopy(a), sortedCopy(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestForwardKeywords_HeadOnlyDoc: a doc that lives ONLY in the in-memory head (never spilled)
// resolves directly from the head's pending forward — no segment read needed.
func TestForwardKeywords_HeadOnlyDoc(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	defer s.CloseAndWait()
	tbl, _ := s.CreateTable("files")

	want := []string{"alpha", "gamma", "delta"}
	s.applyForTest(tbl, 10, want) // stays in the head; no spill
	words, deleted := s.forwardKeywords(tbl, 10)
	if deleted {
		t.Fatal("head-only doc 10 wrongly reported deleted")
	}
	if !eqStrings(words, want) {
		t.Fatalf("head-only forwardKeywords = %v, want %v", sortedCopy(words), sortedCopy(want))
	}
}

// TestForwardKeywords_HeadDeletedDoc: a doc deleted in the head (delForward) reads empty —
// deleted=true and a nil keyword set — even though it was never sealed.
func TestForwardKeywords_HeadDeletedDoc(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	defer s.CloseAndWait()
	tbl, _ := s.CreateTable("files")

	s.applyForTest(tbl, 20, []string{"alpha", "beta"}) // present...
	s.applyForTest(tbl, 20, nil)                        // ...then deleted (head delForward)
	words, deleted := s.forwardKeywords(tbl, 20)
	if !deleted {
		t.Fatalf("head-deleted doc 20 should read deleted, got words=%v", words)
	}
	if words != nil {
		t.Fatalf("deleted doc should have nil keyword set, got %v", words)
	}
}

// TestForwardKeywords_SealedDoc_ResolvesExactSet: after spill, a doc's forward resolves (via the
// segment term-dict region + chunk LRU) to its EXACT keyword set.
func TestForwardKeywords_SealedDoc_ResolvesExactSet(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	defer s.CloseAndWait()
	tbl, _ := s.CreateTable("files")

	doc10 := []string{"alpha", "gamma"}
	doc11 := []string{"beta"}
	s.applyForTest(tbl, 10, doc10)
	s.applyForTest(tbl, 11, doc11)
	s.spillForTest(tbl) // forward now lives in a sealed segment, head empty

	if w, del := s.forwardKeywords(tbl, 10); del || !eqStrings(w, doc10) {
		t.Fatalf("sealed doc 10 = (%v, del=%v), want %v", sortedCopy(w), del, sortedCopy(doc10))
	}
	if w, del := s.forwardKeywords(tbl, 11); del || !eqStrings(w, doc11) {
		t.Fatalf("sealed doc 11 = (%v, del=%v), want %v", sortedCopy(w), del, sortedCopy(doc11))
	}
}

// TestForwardKeywords_SealedTombstone_ReadsEmpty: a sealed forward-tombstone (nKw=0) in a NEWER
// segment must win over an older non-empty forward record — the doc reads empty (no resurrection).
func TestForwardKeywords_SealedTombstone_ReadsEmpty(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	defer s.CloseAndWait()
	tbl, _ := s.CreateTable("files")

	// Segment 1 (older): doc 30 present with keywords.
	s.applyForTest(tbl, 30, []string{"alpha", "beta"})
	s.spillForTest(tbl)
	// Segment 2 (newer): doc 30 deleted -> a sealed forward-tombstone record.
	s.applyForTest(tbl, 30, nil)
	s.spillForTest(tbl)

	if len(s.segs) != 2 {
		t.Fatalf("expected 2 sealed segments, got %d", len(s.segs))
	}
	words, deleted := s.forwardKeywords(tbl, 30)
	if !deleted {
		t.Fatalf("doc 30 should read deleted from the newer sealed tombstone, got words=%v", words)
	}
	if words != nil {
		t.Fatalf("deleted doc should have nil keyword set, got %v", words)
	}
}

// TestForwardKeywords_HeadWinsOverStaleSegment: the head's pending forward is NEWER than any
// sealed copy, so a re-edit within a later window resolves to the head's keywords, not the segment's.
func TestForwardKeywords_HeadWinsOverStaleSegment(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	defer s.CloseAndWait()
	tbl, _ := s.CreateTable("files")

	s.applyForTest(tbl, 40, []string{"old1", "old2"})
	s.spillForTest(tbl)                            // stale copy sealed in segment
	s.applyForTest(tbl, 40, []string{"new1", "new2", "new3"}) // re-edit, now in the head
	w, del := s.forwardKeywords(tbl, 40)
	if del {
		t.Fatal("re-edited doc 40 wrongly deleted")
	}
	if !eqStrings(w, []string{"new1", "new2", "new3"}) {
		t.Fatalf("head should win over stale segment: got %v", sortedCopy(w))
	}
}

// TestForwardKeywords_SealedHeadDeleteWinsOverSegment: a head delete (delForward) after a sealed
// non-empty forward reads empty (the head is the newest action, beats the sealed copy).
func TestForwardKeywords_SealedHeadDeleteWinsOverSegment(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	defer s.CloseAndWait()
	tbl, _ := s.CreateTable("files")

	s.applyForTest(tbl, 50, []string{"alpha"})
	s.spillForTest(tbl)         // sealed non-empty
	s.applyForTest(tbl, 50, nil) // delete pending in the head
	if w, del := s.forwardKeywords(tbl, 50); !del || w != nil {
		t.Fatalf("head delete should win over sealed copy: got (%v, del=%v)", w, del)
	}
}

// TestForwardKeywords_OrdinalZeroPresent: a doc whose single keyword is ordinal 0 must read back
// PRESENT, not be mistaken for the nKw=0 forward-tombstone (a live doc encodes 0x01 0x00).
func TestForwardKeywords_OrdinalZeroPresent(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	defer s.CloseAndWait()
	tbl, _ := s.CreateTable("files")

	// "aaa" sorts first => ordinal 0 in the segment term dict. Make it the ONLY keyword of doc 60.
	s.applyForTest(tbl, 60, []string{"zzz"}) // some other term so the dict isn't single-element
	s.applyForTest(tbl, 61, []string{"aaa"}) // doc 61's only keyword is ordinal 0
	s.spillForTest(tbl)

	w, del := s.forwardKeywords(tbl, 61)
	if del {
		t.Fatal("ordinal-0 doc 61 wrongly read as deleted (tombstone alias)")
	}
	if !eqStrings(w, []string{"aaa"}) {
		t.Fatalf("ordinal-0 doc 61 = %v, want [aaa]", sortedCopy(w))
	}
}

// TestForwardKeywords_UnknownDocMiss: a docid never written (cold) misses everywhere => not
// deleted, empty set (the cold-build write-only case relies on this miss).
func TestForwardKeywords_UnknownDocMiss(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	defer s.CloseAndWait()
	tbl, _ := s.CreateTable("files")

	w, del := s.forwardKeywords(tbl, 999)
	if del || w != nil {
		t.Fatalf("unknown doc should miss (nil, false), got (%v, del=%v)", w, del)
	}
}

// TestChunkLRU_NeverExceedsBudget: drive resolution of many distinct ordinals across many spilled
// segments through a tiny-budget LRU and assert the cache footprint never exceeds the budget
// after any resolve (eviction keeps it bounded).
func TestChunkLRU_NeverExceedsBudget(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	defer s.CloseAndWait()
	tbl, _ := s.CreateTable("files")

	// Tiny budget so even a few decompressed chunks force eviction.
	budget := int64(8 << 10)
	s.dictCache = newChunkLRU(budget)

	// Build several segments, each with a fat vocabulary so the term-dict region is many chunks.
	const segs, perSeg = 6, 200
	docid := int64(1)
	for sg := 0; sg < segs; sg++ {
		for d := 0; d < 40; d++ {
			kws := make([]string, perSeg/4)
			for k := range kws {
				kws[k] = uniqueWord(sg, d, k)
			}
			s.applyForTest(tbl, docid, kws)
			docid++
		}
		s.spillForTest(tbl)
	}

	// Resolve every doc's forward (touches chunks across all segments); check the budget holds.
	for d := int64(1); d < docid; d++ {
		if _, del := s.forwardKeywords(tbl, d); del {
			t.Fatalf("doc %d wrongly deleted during budget sweep", d)
		}
		if used := s.dictCache.usedBytes(); used > budget {
			t.Fatalf("chunk LRU exceeded budget: used=%d > budget=%d", used, budget)
		}
	}
	if used := s.dictCache.usedBytes(); used > budget {
		t.Fatalf("chunk LRU over budget at end: used=%d > budget=%d", used, budget)
	}

	// Non-vacuity guard: the corpus must hold far more distinct decompressed dict-chunk bytes
	// than the budget, so eviction genuinely fired (otherwise the bound above is trivially met
	// and the test proves nothing). Sum each segment's raw dict-chunk bytes via ensureDictIndex.
	var totalRaw int64
	for _, seg := range s.segs {
		seg.ensureDictIndex()
		for _, dc := range seg.dictChunks {
			totalRaw += int64(dc.rawLen)
		}
	}
	if totalRaw <= budget {
		t.Fatalf("test is vacuous: total dict-chunk bytes %d <= budget %d (no eviction forced)", totalRaw, budget)
	}
}

// TestForwardKeywords_ConcurrentResolveRace drives many goroutines calling forwardKeywords against
// freshly-spilled (NOT yet dict-indexed) segments through a shared tiny-budget LRU. The first touch
// of each segment lazily builds its dict-chunk index (segment.ensureDictIndex), so without the
// build being one-time-safe (sync.Once) concurrent first-touches race on s.dictChunks — this test
// fails under `-race`. It also asserts every goroutine resolves each doc to its EXACT keyword set,
// so a torn lazy build (partial dictChunks) would surface as a wrong/garbled resolution too.
func TestForwardKeywords_ConcurrentResolveRace(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	defer s.CloseAndWait()
	tbl, _ := s.CreateTable("files")

	// Build several fresh segments, each with a fat vocabulary (many dict chunks). Record each
	// doc's expected keyword set for the concurrent correctness check below.
	tiny := int64(8 << 10)
	s.dictCache = newChunkLRU(tiny) // tiny shared budget -> concurrent evictions too

	const segs, docsPerSeg, kwsPerDoc = 5, 12, 30
	want := map[int64][]string{}
	docid := int64(1)
	for sg := 0; sg < segs; sg++ {
		for d := 0; d < docsPerSeg; d++ {
			kws := make([]string, kwsPerDoc)
			for k := range kws {
				kws[k] = uniqueWord(sg, d, k)
			}
			s.applyForTest(tbl, docid, kws)
			want[docid] = kws
			docid++
		}
		s.spillForTest(tbl)
	}
	if len(s.segs) != segs {
		t.Fatalf("expected %d sealed segments, got %d", segs, len(s.segs))
	}
	// The segments are freshly opened/spilled — their dict indexes are NOT yet built, so the
	// concurrent first-touch below exercises the lazy build under contention. (Do not call any
	// resolve here first, or the race window closes.)

	maxDoc := docid - 1
	const workers = 8
	var wg sync.WaitGroup
	errCh := make(chan string, workers)
	start := make(chan struct{})
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			<-start // release all goroutines together to maximize first-touch overlap
			for iter := 0; iter < 3; iter++ {
				for d := int64(1); d <= maxDoc; d++ {
					w, del := s.forwardKeywords(tbl, d)
					if del {
						select {
						case errCh <- "doc wrongly deleted":
						default:
						}
						return
					}
					if !eqStrings(w, want[d]) {
						select {
						case errCh <- "doc resolved to wrong keyword set under concurrency":
						default:
						}
						return
					}
				}
			}
		}(int64(g))
	}
	close(start)
	wg.Wait()
	close(errCh)
	if msg, bad := <-errCh; bad {
		t.Fatalf("concurrent forwardKeywords: %s", msg)
	}
	if used := s.dictCache.usedBytes(); used > tiny {
		t.Fatalf("chunk LRU exceeded budget under concurrency: used=%d > budget=%d", used, tiny)
	}
}

// TestChunkLRU_PurgeDropsSegmentChunks: after caching chunks for a segment, purge(segId) drops
// exactly that segment's entries (a merged-away segment is evicted on the MANIFEST swap, §6).
func TestChunkLRU_PurgeDropsSegmentChunks(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	defer s.CloseAndWait()
	tbl, _ := s.CreateTable("files")

	s.applyForTest(tbl, 70, []string{"alpha", "beta", "gamma"})
	s.spillForTest(tbl)
	s.applyForTest(tbl, 71, []string{"delta", "epsilon"})
	s.spillForTest(tbl)
	if len(s.segs) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(s.segs))
	}
	seg0, seg1 := s.segs[0], s.segs[1]

	// Resolve both docs to populate the cache with chunks from both segments.
	s.forwardKeywords(tbl, 70)
	s.forwardKeywords(tbl, 71)
	if cnt := s.dictCache.countForSeg(seg0.id); cnt == 0 {
		t.Fatal("expected cached chunks for seg0 after resolve")
	}
	if cnt := s.dictCache.countForSeg(seg1.id); cnt == 0 {
		t.Fatal("expected cached chunks for seg1 after resolve")
	}

	s.dictCache.purge(seg0.id)
	if cnt := s.dictCache.countForSeg(seg0.id); cnt != 0 {
		t.Fatalf("purge(seg0) left %d chunks", cnt)
	}
	if cnt := s.dictCache.countForSeg(seg1.id); cnt == 0 {
		t.Fatal("purge(seg0) must NOT drop seg1's chunks")
	}
}

// uniqueWord builds a deterministic distinct keyword per (segment,doc,slot) so each segment has a
// fat, distinct vocabulary (many dict chunks).
func uniqueWord(sg, d, k int) string {
	const alpha = "abcdefghijklmnopqrstuvwxyz"
	enc := func(n int) string {
		if n == 0 {
			return "a"
		}
		var b []byte
		for n > 0 {
			b = append(b, alpha[n%26])
			n /= 26
		}
		return string(b)
	}
	return "w_" + enc(sg) + "_" + enc(d) + "_" + enc(k)
}
