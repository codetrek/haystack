package invertedstore

import (
	"sort"
	"testing"
)

// collectForward drains ForwardDocids into a sorted slice for easy assertion.
func collectForward(s *Store, tbl int) []int64 {
	var got []int64
	s.ForwardDocids(tbl, func(d int64) bool {
		got = append(got, d)
		return true
	})
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	return got
}

func eqInt64s(a, b []int64) bool {
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

// TestForwardDocids_HeadOnly: live forwards buffered in the head (no spill) are enumerated.
func TestForwardDocids_HeadOnly(t *testing.T) {
	s, tbl := newUpdateStore(t)
	defer s.CloseAndWait()

	s.Update(tbl, 10, []string{"alpha", "beta"})
	s.Update(tbl, 20, []string{"gamma"})
	s.sync()

	if got := collectForward(s, tbl); !eqInt64s(got, []int64{10, 20}) {
		t.Fatalf("head-only forward docids = %v, want [10 20]", got)
	}
}

// TestForwardDocids_AbsentTable: an unknown/deleted table yields nothing (no panic).
func TestForwardDocids_AbsentTable(t *testing.T) {
	s, _ := newUpdateStore(t)
	defer s.CloseAndWait()

	if got := collectForward(s, 9999); len(got) != 0 {
		t.Fatalf("absent table forward docids = %v, want empty", got)
	}
}

// TestForwardDocids_AcrossSegments: forwards sealed into segments are enumerated, and a docid that
// lives in multiple segments (re-Updated then re-spilled) is yielded exactly once.
func TestForwardDocids_AcrossSegments(t *testing.T) {
	s, tbl := newUpdateStore(t)
	defer s.CloseAndWait()

	s.Update(tbl, 10, []string{"alpha"})
	s.Update(tbl, 20, []string{"beta"})
	s.sync()
	s.forceSpill(tbl) // seg 1: forwards for 10, 20

	s.Update(tbl, 10, []string{"alpha", "gamma"}) // re-post 10 (forward in a newer segment too)
	s.Update(tbl, 30, []string{"delta"})
	s.sync()
	s.forceSpill(tbl) // seg 2: forwards for 10 (again), 30

	if got := collectForward(s, tbl); !eqInt64s(got, []int64{10, 20, 30}) {
		t.Fatalf("cross-segment forward docids = %v, want [10 20 30] (10 deduped)", got)
	}
}

// TestForwardDocids_NewestWinsDelete: a delete (forward-tombstone) in a newer source must suppress
// an older live forward for the same docid, whether the delete is in the head or in a newer segment.
func TestForwardDocids_NewestWinsDelete(t *testing.T) {
	s, tbl := newUpdateStore(t)
	defer s.CloseAndWait()

	// Doc 10: live in seg 1, deleted in the head (newest) -> must NOT be yielded.
	s.Update(tbl, 10, []string{"alpha"})
	s.Update(tbl, 40, []string{"omega"})
	s.sync()
	s.forceSpill(tbl)
	s.Update(tbl, 10, nil) // delete 10 (head forward-tombstone, newest)
	s.sync()

	if got := collectForward(s, tbl); !eqInt64s(got, []int64{40}) {
		t.Fatalf("head-delete forward docids = %v, want [40] (10 deleted)", got)
	}

	// Now seal the delete into a newer segment and confirm the tombstone still wins across segments.
	s.forceSpill(tbl) // seg 2: forward-tombstone for 10
	if got := collectForward(s, tbl); !eqInt64s(got, []int64{40}) {
		t.Fatalf("segment-delete forward docids = %v, want [40] (10 tombstoned newest-wins)", got)
	}
}

// TestForwardDocids_EarlyStop: returning false from fn stops enumeration.
func TestForwardDocids_EarlyStop(t *testing.T) {
	s, tbl := newUpdateStore(t)
	defer s.CloseAndWait()

	for d := int64(1); d <= 50; d++ {
		s.Update(tbl, d, []string{"alpha"})
	}
	s.sync()
	s.forceSpill(tbl)

	n := 0
	s.ForwardDocids(tbl, func(d int64) bool {
		n++
		return n < 3 // stop after the 3rd yield
	})
	if n != 3 {
		t.Fatalf("early-stop visited %d docids, want exactly 3", n)
	}
}

// TestForwardDocids_TableIsolation: ForwardDocids(t) yields only table t's docids, never another
// table's, even when both share the same docid values.
func TestForwardDocids_TableIsolation(t *testing.T) {
	s, tblA := newUpdateStore(t)
	defer s.CloseAndWait()
	tblB, err := s.CreateTable("other")
	if err != nil {
		t.Fatal(err)
	}

	s.Update(tblA, 10, []string{"alpha"})
	s.Update(tblB, 10, []string{"beta"})
	s.Update(tblB, 11, []string{"gamma"})
	s.sync()
	s.forceSpill(tblA)
	s.forceSpill(tblB)

	if got := collectForward(s, tblA); !eqInt64s(got, []int64{10}) {
		t.Fatalf("table A forward docids = %v, want [10]", got)
	}
	if got := collectForward(s, tblB); !eqInt64s(got, []int64{10, 11}) {
		t.Fatalf("table B forward docids = %v, want [10 11]", got)
	}
}
