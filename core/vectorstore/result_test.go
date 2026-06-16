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
