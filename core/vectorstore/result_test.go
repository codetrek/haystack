package vectorstore

import "testing"

func TestTopK_KeepsSmallestDistances(t *testing.T) {
	tk := newTopK(2)
	tk.offer(SearchResult{DocID: 1, Distance: 0.5})
	tk.offer(SearchResult{DocID: 2, Distance: 0.1})
	tk.offer(SearchResult{DocID: 3, Distance: 0.9}) // worse than both — dropped
	tk.offer(SearchResult{DocID: 4, Distance: 0.3}) // evicts docId 1 (0.5)
	out := tk.sorted()
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].DocID != 2 || out[1].DocID != 4 {
		t.Fatalf("sorted = %+v, want docIds [2 4] ascending by distance", out)
	}
}

func TestTopK_FewerThanK(t *testing.T) {
	tk := newTopK(5)
	tk.offer(SearchResult{DocID: 7, Distance: 0.2})
	out := tk.sorted()
	if len(out) != 1 || out[0].DocID != 7 {
		t.Fatalf("sorted = %+v, want single docId 7", out)
	}
}

func TestTopK_Empty(t *testing.T) {
	if out := newTopK(3).sorted(); len(out) != 0 {
		t.Fatalf("empty topK sorted = %+v, want empty", out)
	}
}

func TestTopK_TieBrokenByDocID(t *testing.T) {
	tk := newTopK(3)
	tk.offer(SearchResult{DocID: 5, Distance: 0.2})
	tk.offer(SearchResult{DocID: 2, Distance: 0.2})
	tk.offer(SearchResult{DocID: 9, Distance: 0.2})
	out := tk.sorted()
	if out[0].DocID != 2 || out[1].DocID != 5 || out[2].DocID != 9 {
		t.Fatalf("equal distances must sort by docId: %+v", out)
	}
}

// TestTopK_NonPositiveK exercises the t.k <= 0 guard in offer: a zero-capacity
// topK keeps nothing.
func TestTopK_NonPositiveK(t *testing.T) {
	tk := newTopK(0)
	tk.offer(SearchResult{DocID: 1, Distance: 0.1})
	if out := tk.sorted(); len(out) != 0 {
		t.Fatalf("k=0 topK must keep nothing, got %+v", out)
	}
}

// TestTopK_SiftDownThroughRightChild forces the root-eviction path to sift the
// replacement down a heap large enough that a node has two children and the
// RIGHT child is the worse one (so down() picks j2 and keeps descending across
// multiple levels). Distances are chosen so eviction of the root, then re-sift,
// must traverse the right subtree.
func TestTopK_SiftDownThroughRightChild(t *testing.T) {
	tk := newTopK(7)
	// Fill with descending distances so the heap is populated across 3 levels.
	in := []SearchResult{
		{DocID: 1, Distance: 0.10},
		{DocID: 2, Distance: 0.20},
		{DocID: 3, Distance: 0.30},
		{DocID: 4, Distance: 0.40},
		{DocID: 5, Distance: 0.50},
		{DocID: 6, Distance: 0.60},
		{DocID: 7, Distance: 0.70}, // worst (heap root)
	}
	for _, r := range in {
		tk.offer(r)
	}
	// Evict the worst (0.70) with a new best (0.05). The replacement at the root
	// must sift all the way down, choosing the worse (larger-distance) child at
	// each step — exercising both children and the multi-level loop.
	tk.offer(SearchResult{DocID: 8, Distance: 0.05})
	out := tk.sorted()
	if len(out) != 7 {
		t.Fatalf("len = %d, want 7", len(out))
	}
	want := []int64{8, 1, 2, 3, 4, 5, 6} // 0.05,0.10,...0.60 ascending; 0.70 evicted
	for i, w := range want {
		if out[i].DocID != w {
			t.Fatalf("sorted docIds = %v, want %v (full: %+v)", docIDs(out), want, out)
		}
	}
}

func docIDs(rs []SearchResult) []int64 {
	ids := make([]int64, len(rs))
	for i, r := range rs {
		ids[i] = r.DocID
	}
	return ids
}
