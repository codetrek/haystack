package invertedstore

import (
	"sync"
	"testing"

	"github.com/codetrek/haystack/core/queue"
)

// newSearchStore opens a fresh store with a created table, returning the store and tableId.
func newSearchStore(t *testing.T) (*Store, int) {
	t.Helper()
	dir := t.TempDir()
	q := queue.NewMpsc("invsearch")
	q.Start()
	s, err := Open(dir, q, Options{})
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}
	return s, tbl
}

// tombstoneForTest tombstones (keyword,docid) in the head on the worker, mirroring what the P7
// Update apply does when a keyword is removed from a doc. Used to build add->del->add states
// across un-merged L0 segments without the full Update path.
func (s *Store) tombstoneForTest(tableId int, keyword string, docid int64) {
	s.q.RunFunc(func() error {
		s.mu.Lock()
		h := s.head[tableId]
		if h == nil {
			h = newHeadTable()
			s.head[tableId] = h
		}
		h.tombstonePosting(keyword, docid)
		s.mu.Unlock()
		return nil
	})
}

// addPostingForTest adds a single (keyword,docid) posting in the head on the worker, without
// touching the forward map, so a test can re-add a posting after a tombstone in an older segment.
func (s *Store) addPostingForTest(tableId int, keyword string, docid int64) {
	s.q.RunFunc(func() error {
		s.mu.Lock()
		h := s.head[tableId]
		if h == nil {
			h = newHeadTable()
			s.head[tableId] = h
		}
		h.addPosting(keyword, docid)
		s.mu.Unlock()
		return nil
	})
}

func hasDoc(r SearchResult, d int64) bool {
	_, ok := r.DocIds[d]
	return ok
}

// --- Head participates in the union -----------------------------------------

// TestSearch_HeadParticipates: a posting that lives ONLY in the unspilled head is found, and a
// posting in a sealed segment is also found — the union spans head + segments.
func TestSearch_HeadParticipates(t *testing.T) {
	s, tbl := newSearchStore(t)
	defer s.CloseAndWait()

	// doc 10 spilled to a segment; doc 11 stays in the head (no spill).
	s.applyForTest(tbl, 10, []string{"alpha"})
	s.spillForTest(tbl)
	s.applyForTest(tbl, 11, []string{"alpha"})

	r := s.Search(tbl, "alpha", 0, nil)
	if !hasDoc(r, 10) {
		t.Errorf("doc 10 (segment) missing from union: %v", r.DocIds)
	}
	if !hasDoc(r, 11) {
		t.Errorf("doc 11 (head) missing from union: %v", r.DocIds)
	}
}

// --- Tombstoned doc absent --------------------------------------------------

// TestSearch_TombstonedDocAbsent: a doc tombstoned (in a newer segment than its add) is absent.
func TestSearch_TombstonedDocAbsent(t *testing.T) {
	s, tbl := newSearchStore(t)
	defer s.CloseAndWait()

	s.applyForTest(tbl, 10, []string{"alpha"}) // add in seg A
	s.spillForTest(tbl)
	s.tombstoneForTest(tbl, "alpha", 10) // del in seg B (newer)
	s.spillForTest(tbl)

	r := s.Search(tbl, "alpha", 0, nil)
	if hasDoc(r, 10) {
		t.Errorf("tombstoned doc 10 should be absent, got %v", r.DocIds)
	}
}

// TestSearch_TombstonedDocAbsentFromHead: tombstone in the head (newest) suppresses an add in an
// older segment.
func TestSearch_TombstonedDocAbsentFromHead(t *testing.T) {
	s, tbl := newSearchStore(t)
	defer s.CloseAndWait()

	s.applyForTest(tbl, 10, []string{"alpha"}) // add in seg A
	s.spillForTest(tbl)
	s.tombstoneForTest(tbl, "alpha", 10) // del in the unspilled head

	r := s.Search(tbl, "alpha", 0, nil)
	if hasDoc(r, 10) {
		t.Errorf("head tombstone should suppress doc 10, got %v", r.DocIds)
	}
}

// --- add -> del -> add across UN-MERGED L0 segments -------------------------

// TestSearch_AddDelAdd_PresentAcrossUnmergedSegments: a doc added (seg A), tombstoned (seg B),
// then RE-ADDED (seg C, newest) resolves PRESENT at read — the newer add wins over the older
// tombstone, with NO merge.
func TestSearch_AddDelAdd_PresentAcrossUnmergedSegments(t *testing.T) {
	s, tbl := newSearchStore(t)
	defer s.CloseAndWait()

	s.addPostingForTest(tbl, "alpha", 10) // seg A: add
	s.spillForTest(tbl)
	s.tombstoneForTest(tbl, "alpha", 10) // seg B: del
	s.spillForTest(tbl)
	s.addPostingForTest(tbl, "alpha", 10) // seg C: add (newest)
	s.spillForTest(tbl)

	if len(s.segs) != 3 {
		t.Fatalf("expected 3 un-merged L0 segments, got %d", len(s.segs))
	}
	r := s.Search(tbl, "alpha", 0, nil)
	if !hasDoc(r, 10) {
		t.Errorf("add->del->add should resolve PRESENT (newest add wins), got %v", r.DocIds)
	}
}

// TestSearch_AddDelAdd_SymmetricAbsent: the symmetric case — add (seg A), re-add (seg B),
// tombstone (seg C, newest) resolves ABSENT (newest tombstone wins over older adds), NO merge.
func TestSearch_AddDelAdd_SymmetricAbsent(t *testing.T) {
	s, tbl := newSearchStore(t)
	defer s.CloseAndWait()

	s.addPostingForTest(tbl, "alpha", 10) // seg A: add
	s.spillForTest(tbl)
	s.addPostingForTest(tbl, "alpha", 10) // seg B: add again
	s.spillForTest(tbl)
	s.tombstoneForTest(tbl, "alpha", 10) // seg C: del (newest)
	s.spillForTest(tbl)

	if len(s.segs) != 3 {
		t.Fatalf("expected 3 un-merged L0 segments, got %d", len(s.segs))
	}
	r := s.Search(tbl, "alpha", 0, nil)
	if hasDoc(r, 10) {
		t.Errorf("add->add->del should resolve ABSENT (newest del wins), got %v", r.DocIds)
	}
}

// --- prefix union of many keywords; a tombstone of kw1 must not suppress kw2's add of same doc -

func TestSearch_PrefixUnionPerKeyword(t *testing.T) {
	s, tbl := newSearchStore(t)
	defer s.CloseAndWait()

	// doc 10: alpha added (seg A), then alpha tombstoned (seg B). doc 10 ALSO in alphabet (seg A).
	s.addPostingForTest(tbl, "alpha", 10)
	s.addPostingForTest(tbl, "alphabet", 10)
	s.spillForTest(tbl)
	s.tombstoneForTest(tbl, "alpha", 10) // only alpha tombstoned
	s.spillForTest(tbl)

	// prefix "alph" matches both "alpha" (doc 10 tombstoned) and "alphabet" (doc 10 live).
	r := s.Search(tbl, "alph", 0, nil)
	if !hasDoc(r, 10) {
		t.Errorf("doc 10 still live under keyword alphabet, must be present: %v", r.DocIds)
	}
}

// --- filterKeyword + limit --------------------------------------------------

func TestSearch_FilterKeyword(t *testing.T) {
	s, tbl := newSearchStore(t)
	defer s.CloseAndWait()

	s.applyForTest(tbl, 10, []string{"alpha"})
	s.applyForTest(tbl, 11, []string{"alphabet"})
	s.spillForTest(tbl)

	// reject "alphabet"; only "alpha" docs survive.
	r := s.Search(tbl, "alph", 0, func(kw string) bool { return kw == "alpha" })
	if !hasDoc(r, 10) || hasDoc(r, 11) {
		t.Errorf("filterKeyword should keep only alpha's doc 10, got %v", r.DocIds)
	}
}

func TestSearch_Limit(t *testing.T) {
	s, tbl := newSearchStore(t)
	defer s.CloseAndWait()

	for d := int64(1); d <= 10; d++ {
		s.applyForTest(tbl, d, []string{"alpha"})
	}
	s.spillForTest(tbl)

	r := s.Search(tbl, "alpha", 3, nil)
	if len(r.DocIds) != 3 {
		t.Errorf("limit=3 should cap result at 3, got %d", len(r.DocIds))
	}
}

// --- WildDocIds preserved but never populated -------------------------------

func TestSearch_WildDocIdsNotPopulated(t *testing.T) {
	s, tbl := newSearchStore(t)
	defer s.CloseAndWait()

	s.applyForTest(tbl, 10, []string{"alpha"})
	s.spillForTest(tbl)

	r := s.Search(tbl, "alpha", 0, nil)
	if r.WildDocIds != nil {
		t.Errorf("store must NOT populate WildDocIds, got %v", r.WildDocIds)
	}
}

// --- absent/deleted table returns empty -------------------------------------

func TestSearch_AbsentTableEmpty(t *testing.T) {
	s, tbl := newSearchStore(t)
	defer s.CloseAndWait()

	s.applyForTest(tbl, 10, []string{"alpha"})
	s.spillForTest(tbl)

	if r := s.Search(999, "alpha", 0, nil); len(r.DocIds) != 0 {
		t.Errorf("absent table should be empty, got %v", r.DocIds)
	}
	if err := s.DeleteTable(tbl); err != nil {
		t.Fatal(err)
	}
	if r := s.Search(tbl, "alpha", 0, nil); len(r.DocIds) != 0 {
		t.Errorf("deleted table should be empty, got %v", r.DocIds)
	}
}

// --- GetDocs: exact key, no prefix leak -------------------------------------

// TestGetDocs_NoPrefixLeak: GetDocs("a") must match ONLY keyword "a", NOT "a"+suffix. Guards the
// fixed-width-tableId prefix from leaking a longer keyword (design §4/T4).
func TestGetDocs_NoPrefixLeak(t *testing.T) {
	s, tbl := newSearchStore(t)
	defer s.CloseAndWait()

	s.applyForTest(tbl, 10, []string{"a"})  // keyword "a"
	s.applyForTest(tbl, 11, []string{"ab"}) // keyword "ab" (shares the prefix "a")
	s.applyForTest(tbl, 12, []string{"abc"})
	s.spillForTest(tbl)

	r := s.GetDocs(tbl, "a")
	if !hasDoc(r, 10) {
		t.Errorf("GetDocs(\"a\") must include doc 10 (keyword \"a\"), got %v", r.DocIds)
	}
	if hasDoc(r, 11) || hasDoc(r, 12) {
		t.Errorf("GetDocs(\"a\") must NOT leak \"ab\"/\"abc\" docs, got %v", r.DocIds)
	}
}

// TestGetDocs_ExactNoLowercasing: GetDocs is exact — it does NOT lowercase, so an upper-cased
// query does not match a lower-cased keyword (Search would).
func TestGetDocs_ExactNoLowercasing(t *testing.T) {
	s, tbl := newSearchStore(t)
	defer s.CloseAndWait()

	s.applyForTest(tbl, 10, []string{"alpha"})
	s.spillForTest(tbl)

	if r := s.GetDocs(tbl, "ALPHA"); hasDoc(r, 10) {
		t.Errorf("GetDocs is exact (no lowercasing); ALPHA must not match keyword alpha: %v", r.DocIds)
	}
	if r := s.GetDocs(tbl, "alpha"); !hasDoc(r, 10) {
		t.Errorf("GetDocs(alpha) must match keyword alpha, got %v", r.DocIds)
	}
}

// TestGetDocs_HeadAndTombstone: GetDocs also unions head + segments newest-wins.
func TestGetDocs_HeadAndTombstone(t *testing.T) {
	s, tbl := newSearchStore(t)
	defer s.CloseAndWait()

	s.addPostingForTest(tbl, "alpha", 10)
	s.spillForTest(tbl)
	s.tombstoneForTest(tbl, "alpha", 10) // tombstone in head (newest)

	if r := s.GetDocs(tbl, "alpha"); hasDoc(r, 10) {
		t.Errorf("head tombstone must suppress doc 10 in GetDocs, got %v", r.DocIds)
	}
}

// --- concurrency: head read under RLock vs worker write -----------------------

// TestSearch_ConcurrentReadVsHeadWrite drives Search and GetDocs CONCURRENTLY with worker-side
// addPosting/tombstonePosting on the SAME keyword/table, so a reader that scanned the head map
// outside the RLock would race the worker's map writes. Under -race this fails (and without -race
// Go can escalate it to `fatal error: concurrent map iteration and map write`); with the head
// deltas copied INSIDE the RLock it is clean. This guards the §6 "readers RLock to scan the head"
// invariant that Search/GetDocs claim. Run via: GOWORK=off go test ./invertedstore/ -race.
func TestSearch_ConcurrentReadVsHeadWrite(t *testing.T) {
	s, tbl := newSearchStore(t)
	defer s.CloseAndWait()

	// Seed a sealed segment so reads also scan an immutable segment alongside the live head.
	s.applyForTest(tbl, 1, []string{"alpha"})
	s.spillForTest(tbl)

	const iters = 2000
	var wg sync.WaitGroup

	// Writer: hammer the head with adds/tombstones on "alpha" (and a sibling so the prefix scan
	// in Search ranges multiple keywords) on the worker, the only legal head mutator.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := int64(0); i < iters; i++ {
			s.addPostingForTest(tbl, "alpha", i)
			s.addPostingForTest(tbl, "alphabet", i)
			if i%2 == 0 {
				s.tombstoneForTest(tbl, "alpha", i)
			}
		}
	}()

	// Two reader goroutines: a prefix Search (ranges the whole head inv map) and an exact GetDocs
	// (reads h.inv[key].adds/dels), both racing the writer.
	for r := 0; r < 2; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				_ = s.Search(tbl, "alph", 0, nil)
				_ = s.GetDocs(tbl, "alpha")
			}
		}()
	}

	wg.Wait()
}

