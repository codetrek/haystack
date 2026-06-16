package vectorstore

// SearchResult holds one nearest-neighbor hit in docId space. The caller maps
// docId back to its string id (the records layer is docId-keyed; see §4.4/§7).
type SearchResult struct {
	DocID    int64
	Distance float32
}

// VectorIndexInfo is a read-only snapshot of one named index's configuration and
// build progress (architecture §7 ListVectorIndexes).
type VectorIndexInfo struct {
	Name           string
	Type           string
	Metric         Metric
	M              int
	EfConstruction int
	EfSearch       int
	Segments       int // total sealed segments
	Indexed        int // segments whose graph is built (pending = Segments-Indexed)
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
		t.h.push(r)
		return
	}
	if t.h.worse(t.h[0], r) {
		t.h[0] = r
		t.h.down(0, t.h.Len())
	}
}

// sorted returns the kept results ascending by distance (ties broken by docId
// for determinism). It drains the max-heap (which yields results worst-first)
// into a reverse-filled slice, so the result comes out best-first.
func (t *topK) sorted() []SearchResult {
	n := t.h.Len()
	out := make([]SearchResult, n)
	for i := n - 1; i >= 0; i-- {
		out[i] = t.h.pop()
	}
	return out
}

// maxHeap is a binary max-heap of SearchResult keyed by "worst kept first":
// the root is the largest distance, ties broken so the larger docId is treated
// as worse. Draining the heap therefore yields results in descending
// (distance, docId) order, the reverse of sorted()'s output. It provides typed
// push/pop/up/down helpers instead of going through container/heap, whose
// Push(any)/Pop() any signatures box every SearchResult onto the heap and leave
// an uncoverable Pop interface method; see vectorindex/hnsw.go minDistHeap.
type maxHeap []SearchResult

func (h maxHeap) Len() int { return len(h) }

// worse reports whether a is a worse (later-evicted, popped-earlier) result
// than b: farther away, or equally far but with a larger docId.
func (h maxHeap) worse(a, b SearchResult) bool {
	if a.Distance != b.Distance {
		return a.Distance > b.Distance
	}
	return a.DocID > b.DocID
}

func (h maxHeap) less(i, j int) bool { return h.worse(h[i], h[j]) }

func (h *maxHeap) push(r SearchResult) {
	*h = append(*h, r)
	h.up(h.Len() - 1)
}

func (h *maxHeap) pop() SearchResult {
	old := *h
	n := len(old) - 1
	old[0], old[n] = old[n], old[0]
	h.down(0, n)
	r := old[n]
	*h = old[:n]
	return r
}

func (h maxHeap) up(j int) {
	for {
		i := (j - 1) / 2 // parent
		if i == j || !h.less(j, i) {
			break
		}
		h[i], h[j] = h[j], h[i]
		j = i
	}
}

func (h maxHeap) down(i0, n int) {
	i := i0
	for {
		j1 := 2*i + 1
		if j1 >= n || j1 < 0 {
			break
		}
		j := j1
		if j2 := j1 + 1; j2 < n && h.less(j2, j1) {
			j = j2 // prefer the worse child
		}
		if !h.less(j, i) {
			break
		}
		h[i], h[j] = h[j], h[i]
		i = j
	}
}
