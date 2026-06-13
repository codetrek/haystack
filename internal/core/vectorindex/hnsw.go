package vectorindex

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"sync"

	"github.com/viterin/vek/vek32"
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
	isCosine       bool // true when distance == CosineDistance, enables norm caching
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

// WithCosineDistance marks the index as using cosine distance, enabling
// precomputed norm optimizations. Pass this option when using CosineDistance
// as the distance function.
func WithCosineDistance() Option {
	return func(h *HNSWIndex) {
		h.isCosine = true
	}
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

	// Upsert: if docId already exists, delete the old node first to avoid
	// orphan nodes and inconsistent mappings (HAY-005).
	if existingId, found, err := h.store.GetNodeId(docId); err != nil {
		return fmt.Errorf("failed to check existing docId %q: %v", docId, err)
	} else if found {
		if err := h.deleteNodeLocked(existingId, docId); err != nil {
			return fmt.Errorf("failed to delete existing node for docId %q: %v", docId, err)
		}
	}

	// If the store supports batching, wrap this insert in a batch.
	// Nested calls (e.g. from InsertBatch) just increment depth.
	bs, batchable := h.store.(BatchableStore)
	batchStarted := false
	if batchable {
		bs.BeginBatch()
		batchStarted = true
		defer func() {
			if batchStarted {
				bs.DiscardBatch()
			}
		}()
	}

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
		if err := h.store.SetEntryPoint(nodeId, l); err != nil {
			return err
		}
		if batchable {
			batchStarted = false
			return bs.CommitBatch(true)
		}
		return nil
	}

	// Verify entry point node still exists (may have been deleted).
	if _, err := h.store.GetVectorRef(epId); err != nil {
		if err := h.store.SetEntryPoint(nodeId, l); err != nil {
			return err
		}
		if batchable {
			batchStarted = false
			return bs.CommitBatch(true)
		}
		return nil
	}

	// Phase 1: From top layer down to l+1, greedy search with ef=1.
	curEp := epId
	var queryNorm float32
	if h.isCosine {
		queryNorm = vek32.Norm(vector)
	}
	for layer := maxLayer; layer > l; layer-- {
		changed := true
		for changed {
			changed = false
			neighbors, nerr := h.store.GetNeighbors(curEp, layer)
			if nerr != nil {
				return nerr
			}
			curDist, derr := h.nodeDistCalc(curEp, vector, queryNorm)
			if derr != nil {
				return derr
			}
			for _, nb := range neighbors {
				nbDist, derr := h.nodeDistCalc(nb, vector, queryNorm)
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
		selected := h.selectNeighborsHeuristic(vector, candidates, mMax, nil)

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
				nbVec, err := h.store.GetVectorRef(nb.id)
				if err != nil {
					continue // neighbor may have been deleted
				}
				// Pre-load vectors for all candidates so selectNeighborsHeuristic
				// can read from the cache without repeating these lookups.
				nbCandidates := make([]distItem, 0, len(nbNeighbors))
				shrinkVecCache := make(map[uint64][]float32, len(nbNeighbors))
				for _, cid := range nbNeighbors {
					cVec, err := h.store.GetVectorRef(cid)
					if err != nil {
						continue // node may have been deleted
					}
					shrinkVecCache[cid] = cVec
					nbCandidates = append(nbCandidates, distItem{
						id:   cid,
						dist: h.distance(nbVec, cVec),
					})
				}
				shrunk := h.selectNeighborsHeuristic(nbVec, nbCandidates, mMax, shrinkVecCache)
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
		if err := h.store.SetEntryPoint(nodeId, l); err != nil {
			return err
		}
	}

	if batchable {
		batchStarted = false
		return bs.CommitBatch(true)
	}
	return nil
}

// InsertItem holds a document ID and its vector for batch insertion.
type InsertItem struct {
	DocId  string
	Vector []float32
}

// InsertBatch inserts multiple items in a single batch when the store
// supports batching. The outer batch commits once at the end.
func (h *HNSWIndex) InsertBatch(items []InsertItem) error {
	bs, batchable := h.store.(BatchableStore)
	if batchable {
		bs.BeginBatch()
		defer bs.DiscardBatch()
	}
	for _, item := range items {
		if err := h.Insert(item.DocId, item.Vector); err != nil {
			return err
		}
	}
	if batchable {
		return bs.CommitBatch(true)
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
	if _, err := h.store.GetVectorRef(epId); err != nil {
		return nil, nil
	}

	// Phase 1: From top layer down to 1, greedy search with ef=1.
	curEp := epId
	var queryNorm float32
	if h.isCosine {
		queryNorm = vek32.Norm(query)
	}
	for layer := maxLayer; layer >= 1; layer-- {
		changed := true
		for changed {
			changed = false
			neighbors, nerr := h.store.GetNeighbors(curEp, layer)
			if nerr != nil {
				return nil, nerr
			}
			curDist, derr := h.nodeDistCalc(curEp, query, queryNorm)
			if derr != nil {
				return nil, derr
			}
			for _, nb := range neighbors {
				nbDist, derr := h.nodeDistCalc(nb, query, queryNorm)
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

	return h.deleteNodeLocked(nodeId, docId)
}

// deleteNodeLocked removes a node from the HNSW graph. Caller must hold h.mu.
func (h *HNSWIndex) deleteNodeLocked(nodeId uint64, docId string) error {
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
			nbVec, err := h.store.GetVectorRef(nb)
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
				cVec, err := h.store.GetVectorRef(cid)
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
			selected := h.selectNeighborsHeuristic(nbVec, candidates, mMax, nil)
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
	// Precompute query norm for cosine distance optimization.
	var queryNorm float32
	if h.isCosine {
		queryNorm = vek32.Norm(query)
	}

	entryDist, err := h.nodeDistCalc(entryId, query, queryNorm)
	if err != nil {
		return nil, err
	}

	visited := visitedPool.Get().(*visitedSet)
	visited.begin()
	defer visitedPool.Put(visited)
	visited.mark(entryId)

	// candidates: min-heap (closest first)
	cands := &minDistHeap{}
	cands.push(distItem{id: entryId, dist: entryDist})

	// result: max-heap bounded by ef (farthest first at top)
	results := &maxDistHeap{}
	results.push(distItem{id: entryId, dist: entryDist})

	for cands.Len() > 0 {
		c := cands.pop()

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
			if visited.seen(nbId) {
				continue
			}
			visited.mark(nbId)

			nbDist, err := h.nodeDistCalc(nbId, query, queryNorm)
			if err != nil {
				continue // node may have been deleted
			}

			farthest = (*results)[0]
			if results.Len() < ef || nbDist < farthest.dist {
				cands.push(distItem{id: nbId, dist: nbDist})
				results.push(distItem{id: nbId, dist: nbDist})
				if results.Len() > ef {
					results.pop() // remove the farthest
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
// If vecCache is non-nil, vectors are read from it instead of the store.
func (h *HNSWIndex) selectNeighborsHeuristic(query []float32, candidates []distItem, m int, vecCache map[uint64][]float32) []distItem {
	if len(candidates) <= m {
		return candidates
	}

	// Sort candidates by distance to query.
	sortDistItems(candidates)

	// Resolve each candidate's vector (and norm) once into position-indexed
	// slices. The diversity loop below is O(candidates*selected); reading from
	// these slices instead of id-keyed maps removes the map hashing that
	// dominated the insert CPU profile. vecs[i]/norms[i] correspond to the
	// just-sorted candidates[i]. A nil vecs[i] means the vector was
	// unavailable; norms[i] < 0 means the norm was unavailable (mirroring the
	// previous "missing from cache" checks).
	n := len(candidates)
	vecs := make([][]float32, n)
	for i, c := range candidates {
		if vecCache != nil {
			vecs[i] = vecCache[c.id]
		} else if v, err := h.store.GetVectorRef(c.id); err == nil {
			vecs[i] = v
		}
	}
	var norms []float32
	if h.isCosine {
		norms = make([]float32, n)
		for i, c := range candidates {
			if nrm, err := h.store.GetNorm(c.id); err == nil {
				norms[i] = nrm
			} else {
				norms[i] = -1 // sentinel: norm unavailable
			}
		}
	}

	selected := make([]int, 0, m) // indices into candidates/vecs/norms
	for i := range candidates {
		if len(selected) >= m {
			break
		}
		cVec := vecs[i]
		if cVec == nil {
			continue
		}
		// Check if c is closer to query than to any already-selected neighbor.
		good := true
		for _, sIdx := range selected {
			sVec := vecs[sIdx]
			if sVec == nil {
				continue
			}
			var dist float32
			if h.isCosine && norms[i] >= 0 && norms[sIdx] >= 0 {
				dist = CosineDistanceWithNorms(cVec, sVec, norms[i], norms[sIdx])
			} else {
				dist = h.distance(cVec, sVec)
			}
			if dist < candidates[i].dist {
				good = false
				break
			}
		}
		if good {
			selected = append(selected, i)
		}
	}

	// If we didn't get enough from heuristic, fill from remaining by distance.
	if len(selected) < m {
		inSel := make([]bool, n)
		for _, idx := range selected {
			inSel[idx] = true
		}
		for i := range candidates {
			if len(selected) >= m {
				break
			}
			if !inSel[i] {
				selected = append(selected, i)
				inSel[i] = true
			}
		}
	}

	out := make([]distItem, len(selected))
	for j, idx := range selected {
		out[j] = candidates[idx]
	}
	return out
}

// nodeDistance computes distance between a stored node and a query vector.
func (h *HNSWIndex) nodeDistance(nodeId uint64, query []float32) (float32, error) {
	vec, err := h.store.GetVectorRef(nodeId)
	if err != nil {
		return 0, err
	}
	return h.distance(vec, query), nil
}

// nodeDistanceWithNorm computes cosine distance between a stored node and a
// query vector, using precomputed norms to avoid redundant norm calculations.
// queryNorm is the precomputed L2 norm of the query vector.
func (h *HNSWIndex) nodeDistanceWithNorm(nodeId uint64, query []float32, queryNorm float32) (float32, error) {
	vec, err := h.store.GetVectorRef(nodeId)
	if err != nil {
		return 0, err
	}
	nodeNorm, err := h.store.GetNorm(nodeId)
	if err != nil {
		// Fallback to standard distance if norm not available.
		return h.distance(vec, query), nil
	}
	return CosineDistanceWithNorms(vec, query, nodeNorm, queryNorm), nil
}

// nodeDistCalc computes distance, using precomputed norms when available.
func (h *HNSWIndex) nodeDistCalc(nodeId uint64, query []float32, queryNorm float32) (float32, error) {
	if h.isCosine {
		return h.nodeDistanceWithNorm(nodeId, query, queryNorm)
	}
	return h.nodeDistance(nodeId, query)
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

// visitedSet is a reusable, allocation-free set of node IDs based on version
// stamping: an ID counts as visited iff versions[id] == epoch. Resetting the
// set is O(1) (bump the epoch) instead of clearing a map, and node IDs are
// dense 0-based so a flat slice indexes them directly. This replaces the
// per-call map[uint64]bool that dominated searchLayer's CPU profile.
//
// Buffers are pooled (visitedPool) rather than stored on HNSWIndex because
// searchLayer runs concurrently under RLock; each call borrows its own set.
type visitedSet struct {
	versions []uint32
	epoch    uint32
}

var visitedPool = sync.Pool{New: func() any { return &visitedSet{} }}

// begin prepares the set for a fresh search by advancing the epoch, which
// logically clears all prior marks. On epoch overflow it zeroes the backing
// array so stale stamps cannot alias the wrapped-around epoch.
func (v *visitedSet) begin() {
	if v.epoch == math.MaxUint32 {
		for i := range v.versions {
			v.versions[i] = 0
		}
		v.epoch = 0
	}
	v.epoch++
}

// seen reports whether id was marked during the current epoch.
func (v *visitedSet) seen(id uint64) bool {
	return id < uint64(len(v.versions)) && v.versions[id] == v.epoch
}

// mark records id as visited in the current epoch, growing the backing array
// (with doubling to amortize) when id is out of range.
func (v *visitedSet) mark(id uint64) {
	if id >= uint64(len(v.versions)) {
		newLen := id + 1
		if double := uint64(len(v.versions)) * 2; double > newLen {
			newLen = double
		}
		grown := make([]uint32, newLen)
		copy(grown, v.versions)
		v.versions = grown
	}
	v.versions[id] = v.epoch
}

// minDistHeap is a min-heap (closest first). It provides typed push/pop helpers
// instead of going through container/heap, whose Push(interface{}) / Pop()
// interface{} signatures box every distItem onto the heap — that boxing
// dominated searchLayer's allocation profile. The up/down sift logic mirrors
// container/heap exactly, so ordering (including tie-breaking) is identical.
type minDistHeap []distItem

func (h minDistHeap) Len() int           { return len(h) }
func (h minDistHeap) less(i, j int) bool { return h[i].dist < h[j].dist }

func (h *minDistHeap) push(it distItem) {
	*h = append(*h, it)
	h.up(len(*h) - 1)
}

func (h *minDistHeap) pop() distItem {
	old := *h
	n := len(old) - 1
	old[0], old[n] = old[n], old[0]
	h.down(0, n)
	it := old[n]
	*h = old[:n]
	return it
}

func (h minDistHeap) up(j int) {
	for {
		i := (j - 1) / 2 // parent
		if i == j || !h.less(j, i) {
			break
		}
		h[i], h[j] = h[j], h[i]
		j = i
	}
}

func (h minDistHeap) down(i0, n int) {
	i := i0
	for {
		j1 := 2*i + 1
		if j1 >= n || j1 < 0 {
			break
		}
		j := j1
		if j2 := j1 + 1; j2 < n && h.less(j2, j1) {
			j = j2 // prefer the smaller child
		}
		if !h.less(j, i) {
			break
		}
		h[i], h[j] = h[j], h[i]
		i = j
	}
}

// maxDistHeap is a max-heap (farthest first). See minDistHeap for why it avoids
// container/heap.
type maxDistHeap []distItem

func (h maxDistHeap) Len() int           { return len(h) }
func (h maxDistHeap) less(i, j int) bool { return h[i].dist > h[j].dist }

func (h *maxDistHeap) push(it distItem) {
	*h = append(*h, it)
	h.up(len(*h) - 1)
}

func (h *maxDistHeap) pop() distItem {
	old := *h
	n := len(old) - 1
	old[0], old[n] = old[n], old[0]
	h.down(0, n)
	it := old[n]
	*h = old[:n]
	return it
}

func (h maxDistHeap) up(j int) {
	for {
		i := (j - 1) / 2 // parent
		if i == j || !h.less(j, i) {
			break
		}
		h[i], h[j] = h[j], h[i]
		j = i
	}
}

func (h maxDistHeap) down(i0, n int) {
	i := i0
	for {
		j1 := 2*i + 1
		if j1 >= n || j1 < 0 {
			break
		}
		j := j1
		if j2 := j1 + 1; j2 < n && h.less(j2, j1) {
			j = j2 // prefer the larger child
		}
		if !h.less(j, i) {
			break
		}
		h[i], h[j] = h[j], h[i]
		i = j
	}
}

// sortDistItems sorts distItems by distance ascending.
func sortDistItems(items []distItem) {
	sort.Slice(items, func(i, j int) bool {
		return items[i].dist < items[j].dist
	})
}
