package vectorstore

import (
	"container/heap"
	"sort"
)

// SearchResult holds one nearest-neighbor hit in docId space. The caller maps
// docId back to its string id (the records layer is docId-keyed; see §4.4/§7).
type SearchResult struct {
	DocID    int64
	Distance float32
}

// topK keeps the k smallest-distance results seen, using a max-heap (largest
// distance at the root) so the worst kept result is O(1) to inspect and evict.
type topK struct {
	k int
	h maxHeap
}

func newTopK(k int) *topK { return &topK{k: k} }

// offer adds r if there is room or it beats the current worst kept result.
func (t *topK) offer(r SearchResult) {
	if t.k <= 0 {
		return
	}
	if t.h.Len() < t.k {
		heap.Push(&t.h, r)
		return
	}
	if r.Distance < t.h[0].Distance {
		t.h[0] = r
		heap.Fix(&t.h, 0)
	}
}

// sorted returns the kept results ascending by distance (ties broken by docId
// for determinism).
func (t *topK) sorted() []SearchResult {
	out := make([]SearchResult, len(t.h))
	copy(out, t.h)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Distance != out[j].Distance {
			return out[i].Distance < out[j].Distance
		}
		return out[i].DocID < out[j].DocID
	})
	return out
}

type maxHeap []SearchResult

func (h maxHeap) Len() int           { return len(h) }
func (h maxHeap) Less(i, j int) bool { return h[i].Distance > h[j].Distance } // max-heap
func (h maxHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *maxHeap) Push(x any)        { *h = append(*h, x.(SearchResult)) }
func (h *maxHeap) Pop() any {
	old := *h
	n := len(old)
	r := old[n-1]
	*h = old[:n-1]
	return r
}
