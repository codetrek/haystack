package vectorindex

import (
	"container/heap"
	"math"
	"math/rand"
	"sync"
)

// Default HNSW parameters per arXiv:1603.09320.
const (
	DefaultM              = 16
	DefaultMmax0          = 2 * DefaultM // 32
	DefaultEfConstruction = 200
	DefaultEfSearch       = 64
)

// HNSWIndex is a Hierarchical Navigable Small World graph for approximate
// nearest-neighbor search. It implements Algorithm 1-5 from the paper.
type HNSWIndex struct {
	mu             sync.RWMutex
	store          NodeStore
	distance       DistanceFunc
	M              int
	Mmax0          int
	efConstruction int
	efSearch       int
	mL             float64
	rng            *rand.Rand
}

// Option configures HNSWIndex construction.
type Option func(*HNSWIndex)

// WithM sets M (max connections per non-zero layer). Mmax0 is set to 2*M.
func WithM(m int) Option {
	return func(h *HNSWIndex) {
		h.M = m
		h.Mmax0 = 2 * m
	}
}

// WithEfConstruction sets the beam width during insert.
func WithEfConstruction(ef int) Option {
	return func(h *HNSWIndex) { h.efConstruction = ef }
}

// WithEfSearch sets the beam width during search.
func WithEfSearch(ef int) Option {
	return func(h *HNSWIndex) { h.efSearch = ef }
}

// WithRand sets the random source for level generation (useful for tests).
func WithRand(r *rand.Rand) Option {
	return func(h *HNSWIndex) { h.rng = r }
}

// NewHNSWIndex creates a new HNSW index.
func NewHNSWIndex(store NodeStore, distance DistanceFunc, opts ...Option) *HNSWIndex {
	h := &HNSWIndex{
		store:          store,
		distance:       distance,
		M:              DefaultM,
		Mmax0:          DefaultMmax0,
		efConstruction: DefaultEfConstruction,
		efSearch:       DefaultEfSearch,
		rng:            rand.New(rand.NewSource(42)),
	}
	for _, opt := range opts {
		opt(h)
	}
	h.mL = 1.0 / math.Log(float64(h.M))
	return h
}

// randomLevel returns a random level using the formula from the paper:
// floor(-ln(uniform(0,1)) * mL)
func (h *HNSWIndex) randomLevel() int {
	r := h.rng.Float64()
	if r == 0 {
		r = 1e-18 // avoid log(0)
	}
	return int(math.Floor(-math.Log(r) * h.mL))
}

// Insert adds a document's vector to the index (Algorithm 1).
func (h *HNSWIndex) Insert(docId string, vector []float32) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Allocate a new node ID.
	nodeId, err := h.store.NextNodeId()
	if err != nil {
		return err
	}

	l := h.randomLevel()

	// Persist node and mapping.
	if err := h.store.PutNode(nodeId, l, vector); err != nil {
		return err
	}
	if err := h.store.SetNodeMapping(docId, nodeId); err != nil {
		return err
	}
	// Initialize empty neighbor lists for all layers.
	for layer := 0; layer <= l; layer++ {
		if err := h.store.SetNeighbors(nodeId, layer, nil); err != nil {
			return err
		}
	}

	// Get current entry point.
	epId, maxLayer, err := h.store.GetEntryPoint()
	if err != nil {
		// First node — set as entry point and return.
		return h.store.SetEntryPoint(nodeId, l)
	}

	// Verify entry point node still exists (may have been deleted).
	if _, err := h.store.GetVector(epId); err != nil {
		return h.store.SetEntryPoint(nodeId, l)
	}

	// Phase 1: From top layer down to l+1, greedy search with ef=1.
	curEp := epId
	for layer := maxLayer; layer > l; layer-- {
		changed := true
		for changed {
			changed = false
			neighbors, nerr := h.store.GetNeighbors(curEp, layer)
			if nerr != nil {
				return nerr
			}
			curDist, derr := h.nodeDistance(curEp, vector)
			if derr != nil {
				return derr
			}
			for _, nb := range neighbors {
				nbDist, derr := h.nodeDistance(nb, vector)
				if derr != nil {
					continue // node may have been deleted
				}
				if nbDist < curDist {
					curEp = nb
					curDist = nbDist
					changed = true
				}
			}
		}
	}

	// Phase 2: From min(l, maxLayer) down to 0, search with ef=efConstruction.
	topLayer := l
	if maxLayer < topLayer {
		topLayer = maxLayer
	}
	for layer := topLayer; layer >= 0; layer-- {
		candidates, err := h.searchLayer(vector, curEp, h.efConstruction, layer)
		if err != nil {
			return err
		}

		// Select neighbors using heuristic (Algorithm 4).
		mMax := h.M
		if layer == 0 {
			mMax = h.Mmax0
		}
		selected := h.selectNeighborsHeuristic(vector, candidates, mMax)

		// Create bidirectional edges.
		neighborIds := make([]uint64, len(selected))
		for i, s := range selected {
			neighborIds[i] = s.id
		}
		if err := h.store.SetNeighbors(nodeId, layer, neighborIds); err != nil {
			return err
		}

		// Add reverse edges and shrink if needed.
		for _, nb := range selected {
			nbNeighbors, err := h.store.GetNeighbors(nb.id, layer)
			if err != nil {
				continue // neighbor may have been deleted
			}
			nbNeighbors = append(nbNeighbors, nodeId)
			if len(nbNeighbors) > mMax {
				// Shrink using heuristic.
				nbVec, err := h.store.GetVector(nb.id)
				if err != nil {
					continue // neighbor may have been deleted
				}
				nbCandidates := make([]distItem, 0, len(nbNeighbors))
				for _, cid := range nbNeighbors {
					cVec, err := h.store.GetVector(cid)
					if err != nil {
						continue // node may have been deleted
					}
					nbCandidates = append(nbCandidates, distItem{
						id:   cid,
						dist: h.distance(nbVec, cVec),
					})
				}
				shrunk := h.selectNeighborsHeuristic(nbVec, nbCandidates, mMax)
				newNb := make([]uint64, len(shrunk))
				for i, s := range shrunk {
					newNb[i] = s.id
				}
				nbNeighbors = newNb
			}
			if err := h.store.SetNeighbors(nb.id, layer, nbNeighbors); err != nil {
				return err
			}
		}

		// Update curEp for next lower layer: use the closest result.
		if len(candidates) > 0 {
			best := candidates[0]
			for _, c := range candidates[1:] {
				if c.dist < best.dist {
					best = c
				}
			}
			curEp = best.id
		}
	}

	// If new node's level is higher than current max, update entry point.
	if l > maxLayer {
		return h.store.SetEntryPoint(nodeId, l)
	}
	return nil
}

// Search returns the k nearest neighbors of query (Algorithm 2).
func (h *HNSWIndex) Search(query []float32, k int) ([]SearchResult, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	epId, maxLayer, err := h.store.GetEntryPoint()
	if err != nil {
		// No entry point — empty index.
		return nil, nil
	}

	// Verify entry point node still exists (may have been deleted).
	if _, err := h.store.GetVector(epId); err != nil {
		return nil, nil
	}

	// Phase 1: From top layer down to 1, greedy search with ef=1.
	curEp := epId
	for layer := maxLayer; layer >= 1; layer-- {
		changed := true
		for changed {
			changed = false
			neighbors, nerr := h.store.GetNeighbors(curEp, layer)
			if nerr != nil {
				return nil, nerr
			}
			curDist, derr := h.nodeDistance(curEp, query)
			if derr != nil {
				return nil, derr
			}
			for _, nb := range neighbors {
				nbDist, derr := h.nodeDistance(nb, query)
				if derr != nil {
					continue // node may have been deleted
				}
				if nbDist < curDist {
					curEp = nb
					curDist = nbDist
					changed = true
				}
			}
		}
	}

	// Phase 2: Layer 0 search with ef=max(efSearch, k).
	ef := h.efSearch
	if k > ef {
		ef = k
	}
	results, err := h.searchLayer(query, curEp, ef, 0)
	if err != nil {
		return nil, err
	}

	// Sort by distance and return top-k.
	sortDistItems(results)
	if len(results) > k {
		results = results[:k]
	}

	out := make([]SearchResult, len(results))
	for i, r := range results {
		out[i] = SearchResult{
			ID:       r.id,
			Distance: r.dist,
		}
	}
	return out, nil
}

// Delete removes a document from the index.
func (h *HNSWIndex) Delete(docId string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	nodeId, found, err := h.store.GetNodeId(docId)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}

	level, err := h.store.GetNodeLevel(nodeId)
	if err != nil {
		return err
	}

	// For each layer, remove this node from its neighbors' neighbor lists and
	// try to reconnect former neighbors through each other.
	for layer := 0; layer <= level; layer++ {
		neighbors, err := h.store.GetNeighbors(nodeId, layer)
		if err != nil {
			return err
		}

		// Remove nodeId from each neighbor's list.
		for _, nb := range neighbors {
			// Skip neighbors that may have been deleted previously.
			nbVec, err := h.store.GetVector(nb)
			if err != nil {
				continue
			}

			nbNeighbors, err := h.store.GetNeighbors(nb, layer)
			if err != nil {
				continue
			}
			filtered := removeId(nbNeighbors, nodeId)

			// Attempt reconnection: gather candidates from neighbors-of-neighbors.
			candidateSet := make(map[uint64]bool)
			for _, id := range filtered {
				candidateSet[id] = true
			}
			for _, id := range filtered {
				nn, err := h.store.GetNeighbors(id, layer)
				if err != nil {
					continue
				}
				for _, nnId := range nn {
					if nnId != nb && nnId != nodeId {
						candidateSet[nnId] = true
					}
				}
			}

			// Build candidate list for heuristic selection.
			candidates := make([]distItem, 0, len(candidateSet))
			for cid := range candidateSet {
				cVec, err := h.store.GetVector(cid)
				if err != nil {
					continue // node may have been deleted
				}
				candidates = append(candidates, distItem{
					id:   cid,
					dist: h.distance(nbVec, cVec),
				})
			}

			mMax := h.M
			if layer == 0 {
				mMax = h.Mmax0
			}
			selected := h.selectNeighborsHeuristic(nbVec, candidates, mMax)
			newNb := make([]uint64, len(selected))
			for i, s := range selected {
				newNb[i] = s.id
			}
			if err := h.store.SetNeighbors(nb, layer, newNb); err != nil {
				return err
			}
		}
	}

	// Update entry point if we deleted it.
	epId, _, err := h.store.GetEntryPoint()
	if err == nil && epId == nodeId {
		// Find a new entry point: pick a neighbor from the highest possible layer.
		var newEp uint64
		newLevel := -1
		for layer := level; layer >= 0; layer-- {
			neighbors, err := h.store.GetNeighbors(nodeId, layer)
			if err != nil {
				continue
			}
			for _, nb := range neighbors {
				nbLevel, err := h.store.GetNodeLevel(nb)
				if err != nil {
					continue
				}
				if nbLevel > newLevel {
					newEp = nb
					newLevel = nbLevel
				}
			}
			if newLevel >= 0 {
				break
			}
		}
		if newLevel >= 0 {
			_ = h.store.SetEntryPoint(newEp, newLevel)
		}
		// If newLevel < 0, the index is empty. Delete the entry point marker
		// so Search correctly returns empty results.
	}

	// Remove the node data.
	if err := h.store.DeleteNode(nodeId); err != nil {
		return err
	}
	return h.store.DeleteNodeMapping(docId)
}

// --- searchLayer: Algorithm 5 ---

// searchLayer performs a beam search on a single layer with given ef width.
// Uses a min-heap for candidates and a max-heap for the result set.
func (h *HNSWIndex) searchLayer(query []float32, entryId uint64, ef int, layer int) ([]distItem, error) {
	entryDist, err := h.nodeDistance(entryId, query)
	if err != nil {
		return nil, err
	}

	visited := map[uint64]bool{entryId: true}

	// candidates: min-heap (closest first)
	cands := &minDistHeap{}
	heap.Push(cands, distItem{id: entryId, dist: entryDist})

	// result: max-heap bounded by ef (farthest first at top)
	results := &maxDistHeap{}
	heap.Push(results, distItem{id: entryId, dist: entryDist})

	for cands.Len() > 0 {
		c := heap.Pop(cands).(distItem)

		// If closest candidate is farther than the farthest result, stop.
		farthest := (*results)[0] // top of max-heap = farthest
		if c.dist > farthest.dist {
			break
		}

		neighbors, err := h.store.GetNeighbors(c.id, layer)
		if err != nil {
			return nil, err
		}

		for _, nbId := range neighbors {
			if visited[nbId] {
				continue
			}
			visited[nbId] = true

			nbDist, err := h.nodeDistance(nbId, query)
			if err != nil {
				continue // node may have been deleted
			}

			farthest = (*results)[0]
			if results.Len() < ef || nbDist < farthest.dist {
				heap.Push(cands, distItem{id: nbId, dist: nbDist})
				heap.Push(results, distItem{id: nbId, dist: nbDist})
				if results.Len() > ef {
					heap.Pop(results) // remove the farthest
				}
			}
		}
	}

	// Collect results.
	out := make([]distItem, results.Len())
	for i := range out {
		out[i] = (*results)[i]
	}
	return out, nil
}

// --- selectNeighborsHeuristic: Algorithm 4 ---

// selectNeighborsHeuristic selects up to M neighbors using the heuristic
// from the paper. It ensures diversity by only adding a candidate if it
// is closer to the query than to any already-selected neighbor.
func (h *HNSWIndex) selectNeighborsHeuristic(query []float32, candidates []distItem, m int) []distItem {
	if len(candidates) <= m {
		return candidates
	}

	// Sort candidates by distance to query.
	sortDistItems(candidates)

	selected := make([]distItem, 0, m)
	for _, c := range candidates {
		if len(selected) >= m {
			break
		}
		// Check if c is closer to query than to any already-selected neighbor.
		good := true
		cVec, err := h.store.GetVector(c.id)
		if err != nil {
			continue
		}
		for _, s := range selected {
			sVec, err := h.store.GetVector(s.id)
			if err != nil {
				continue
			}
			if h.distance(cVec, sVec) < c.dist {
				good = false
				break
			}
		}
		if good {
			selected = append(selected, c)
		}
	}

	// If we didn't get enough from heuristic, fill from remaining by distance.
	if len(selected) < m {
		selectedSet := make(map[uint64]bool, len(selected))
		for _, s := range selected {
			selectedSet[s.id] = true
		}
		for _, c := range candidates {
			if len(selected) >= m {
				break
			}
			if !selectedSet[c.id] {
				selected = append(selected, c)
				selectedSet[c.id] = true
			}
		}
	}

	return selected
}

// nodeDistance computes distance between a stored node and a query vector.
func (h *HNSWIndex) nodeDistance(nodeId uint64, query []float32) (float32, error) {
	vec, err := h.store.GetVector(nodeId)
	if err != nil {
		return 0, err
	}
	return h.distance(vec, query), nil
}

func removeId(ids []uint64, target uint64) []uint64 {
	result := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if id != target {
			result = append(result, id)
		}
	}
	return result
}

// --- distItem and heap types ---

type distItem struct {
	id   uint64
	dist float32
}

// minDistHeap is a min-heap (closest first).
type minDistHeap []distItem

func (h minDistHeap) Len() int            { return len(h) }
func (h minDistHeap) Less(i, j int) bool   { return h[i].dist < h[j].dist }
func (h minDistHeap) Swap(i, j int)        { h[i], h[j] = h[j], h[i] }
func (h *minDistHeap) Push(x interface{})  { *h = append(*h, x.(distItem)) }
func (h *minDistHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// maxDistHeap is a max-heap (farthest first).
type maxDistHeap []distItem

func (h maxDistHeap) Len() int            { return len(h) }
func (h maxDistHeap) Less(i, j int) bool   { return h[i].dist > h[j].dist }
func (h maxDistHeap) Swap(i, j int)        { h[i], h[j] = h[j], h[i] }
func (h *maxDistHeap) Push(x interface{})  { *h = append(*h, x.(distItem)) }
func (h *maxDistHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// sortDistItems sorts distItems by distance ascending.
func sortDistItems(items []distItem) {
	// Simple insertion sort — fast for small slices typical in HNSW.
	for i := 1; i < len(items); i++ {
		key := items[i]
		j := i - 1
		for j >= 0 && items[j].dist > key.dist {
			items[j+1] = items[j]
			j--
		}
		items[j+1] = key
	}
}
