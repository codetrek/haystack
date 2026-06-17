package vectorstore

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"slices"
	"sync"
)

// defaultMaxLayers caps the random level a node can be assigned. vectorindex
// keeps this in mmap_format.go (not copied here), so the migrated graph defines
// it locally; graphfile.go (Task 6) reuses it. randomLevel clamps to it.
const defaultMaxLayers = 6

// Default HNSW parameters per arXiv:1603.09320.
const (
	defaultGraphM              = 16
	defaultGraphMmax0          = 2 * defaultGraphM // 32
	defaultGraphEfConstruction = 200
	defaultGraphEfSearch       = 64
)

// hnswIndex is a Hierarchical Navigable Small World graph for approximate
// nearest-neighbor search. It implements Algorithm 1-5 from the paper. Migrated
// (copied) from vectorindex; vectors live in the graphNodeStore (for a sealed
// segment, in the segment itself), the graph holds only topology.
type hnswIndex struct {
	mu             sync.RWMutex
	store          graphNodeStore
	metric         Metric // taken from the store; immutable for the index's life
	M              int
	Mmax0          int
	efConstruction int
	efSearch       int
	mL             float64
	rng            *rand.Rand
}

// graphOption configures hnswIndex construction.
type graphOption func(*hnswIndex)

// withGraphM sets M (max connections per non-zero layer). Mmax0 is set to 2*M.
func withGraphM(m int) graphOption {
	return func(h *hnswIndex) {
		h.M = m
		h.Mmax0 = 2 * m
	}
}

// withGraphEfConstruction sets the beam width during insert.
func withGraphEfConstruction(ef int) graphOption {
	return func(h *hnswIndex) { h.efConstruction = ef }
}

// withGraphEfSearch sets the beam width during search.
func withGraphEfSearch(ef int) graphOption {
	return func(h *hnswIndex) { h.efSearch = ef }
}

// withGraphRand sets the random source for level generation (useful for tests).
func withGraphRand(r *rand.Rand) graphOption {
	return func(h *hnswIndex) { h.rng = r }
}

// The distance metric is a property of the store, not the index: it is chosen
// when the store is created, persisted in the store metadata, and read back via
// store.Metric(). The index cannot override it, so the metric, the on-disk
// vector form, and the graph can never disagree.

// newHNSWIndex creates a new HNSW index over store. The distance metric is taken
// from store.Metric().
func newHNSWIndex(store graphNodeStore, opts ...graphOption) *hnswIndex {
	h := &hnswIndex{
		store:          store,
		metric:         store.Metric(),
		M:              defaultGraphM,
		Mmax0:          defaultGraphMmax0,
		efConstruction: defaultGraphEfConstruction,
		efSearch:       defaultGraphEfSearch,
		rng:            rand.New(rand.NewSource(42)),
	}
	for _, opt := range opts {
		opt(h)
	}
	h.mL = 1.0 / math.Log(float64(h.M))
	return h
}

// randomLevel returns a random level using the formula from the paper:
// floor(-ln(uniform(0,1)) * mL), clamped to defaultMaxLayers.
func (h *hnswIndex) randomLevel() int {
	r := h.rng.Float64()
	if r == 0 {
		r = 1e-18 // avoid log(0)
	}
	level := int(math.Floor(-math.Log(r) * h.mL))
	if level > defaultMaxLayers {
		level = defaultMaxLayers
	}
	return level
}

// runInTxnLocked brackets apply() in a store transaction. On apply error it
// aborts (which faults a persistent store) and returns the original error;
// otherwise it commits. Caller must hold h.mu.
func (h *hnswIndex) runInTxnLocked(apply func() error) error {
	if err := h.store.txnBegin(); err != nil {
		return err
	}
	// A panic inside apply (e.g. a SIMD kernel on malformed input that slipped
	// past validation) must not leave the store's in-txn flag set, which would
	// brick every subsequent write. Abort to clear it, then re-panic.
	defer func() {
		if r := recover(); r != nil {
			_ = h.store.txnAbort(fmt.Errorf("panic during transaction: %v", r))
			panic(r)
		}
	}()
	if err := apply(); err != nil {
		_ = h.store.txnAbort(err)
		return err
	}
	return h.store.txnCommit()
}

// validateVector rejects inputs that would corrupt the store or panic the
// distance kernels: empty vectors, and (when the store has a fixed dimension)
// vectors whose length disagrees with it. Called at every public write/search
// entry before any state is mutated, so bad input returns an error instead of
// faulting the store, panicking a SIMD kernel, or over-writing an adjacent slot.
func (h *hnswIndex) validateVector(v []float32) error {
	if len(v) == 0 {
		return fmt.Errorf("vectorstore: vector must be non-empty")
	}
	if d := h.store.Dim(); d > 0 && len(v) != d {
		return fmt.Errorf("vectorstore: vector dimension mismatch: got %d, want %d", len(v), d)
	}
	// For cosine, a non-finite norm means a NaN/Inf component or a magnitude
	// whose L2 norm overflows float32 — none can be normalized to a finite unit
	// vector, so reject instead of persisting NaN/Inf or poisoning a search
	// (audit #6/#10). norm() uses the SIMD fast path, so this is cheap.
	if h.metric == Cosine {
		n := h.metric.norm(v)
		if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
			return fmt.Errorf("vectorstore: cosine vector norm is not finite (NaN/Inf component or overflow)")
		}
		if n != 0 && math.IsInf(float64(1.0/n), 0) {
			return fmt.Errorf("vectorstore: cosine vector norm %g is too small to normalize without overflow", n)
		}
	}
	return nil
}

// insert adds (or replaces) a document's vector. Equivalent to a single-op
// batch: it wraps one insert in a store transaction.
func (h *hnswIndex) insert(docId int64, vector []float32) error {
	if err := h.validateVector(vector); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.runInTxnLocked(func() error { return h.insertOneLocked(docId, vector) })
}

// insertOneLocked performs upsert-delete (if docId exists) followed by a fresh
// HNSW insert (Algorithm 1). Caller must hold h.mu AND have an open store
// transaction; it performs no batch/transaction management itself.
func (h *hnswIndex) insertOneLocked(docId int64, vector []float32) error {
	// Upsert: if docId already exists, delete the old node first to avoid
	// orphan nodes and inconsistent mappings (HAY-005). This now runs inside
	// the same transaction as the insert, so an upsert is atomic.
	if existingId, found, err := h.store.GetNodeId(docId); err != nil {
		return fmt.Errorf("failed to check existing docId %d: %v", docId, err)
	} else if found {
		if err := h.deleteNodeLocked(existingId); err != nil {
			return fmt.Errorf("failed to delete existing node for docId %d: %v", docId, err)
		}
	}

	// Allocate a new node ID.
	nodeId, err := h.store.NextNodeId()
	if err != nil {
		return err
	}

	l := h.randomLevel()

	// Persist node (docId stored in slot).
	if err := h.store.PutNode(nodeId, l, vector, docId); err != nil {
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
		if !errors.Is(err, errNoEntryPoint) {
			return err // e.g. a faulted store — don't mistake it for the first node
		}
		// First node — set as entry point and return.
		return h.store.SetEntryPoint(nodeId, l)
	}

	// Verify entry point node still exists (may have been deleted).
	if _, err := h.store.GetVectorRef(epId); err != nil {
		return h.store.SetEntryPoint(nodeId, l)
	}

	// Phase 1: From top layer down to l+1, greedy search with ef=1.
	curEp := epId
	// Compare against stored neighbors in stored form (cosine: unit vectors).
	prepared, _ := h.metric.prepare(vector)
	for layer := maxLayer; layer > l; layer-- {
		changed := true
		for changed {
			changed = false
			neighbors, nerr := h.store.GetNeighbors(curEp, layer)
			if nerr != nil {
				return nerr
			}
			curDist, derr := h.nodeDist(curEp, prepared)
			if derr != nil {
				return derr
			}
			for _, nb := range neighbors {
				nbDist, derr := h.nodeDist(nb, prepared)
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
		candidates, err := h.searchLayer(prepared, curEp, h.efConstruction, layer)
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
				nbVec, err := h.store.GetVectorRef(nb.id)
				if err != nil {
					continue // neighbor may have been deleted
				}
				// Build the shrink candidate set. getVectorRef is zero-copy now, so
				// reading each vector directly (nil cache below) is cheaper than the
				// per-shrink map this used to allocate.
				nbCandidates := make([]distItem, 0, len(nbNeighbors))
				for _, cid := range nbNeighbors {
					cVec, err := h.store.GetVectorRef(cid)
					if err != nil {
						continue // node may have been deleted
					}
					nbCandidates = append(nbCandidates, distItem{
						id:   cid,
						dist: h.metric.distance(nbVec, cVec),
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
		if err := h.store.SetEntryPoint(nodeId, l); err != nil {
			return err
		}
	}
	return nil
}

// deleteOneLocked removes a docId from the graph if present. Caller must hold
// h.mu AND have an open store transaction.
func (h *hnswIndex) deleteOneLocked(docId int64) error {
	nodeId, found, err := h.store.GetNodeId(docId)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	return h.deleteNodeLocked(nodeId)
}

// search returns the k nearest neighbors of query (Algorithm 2). It is
// searchFiltered with a nil member predicate (accept all) — the unfiltered path
// is byte-identical to the pre-Phase-5 behavior, so every existing 2-arg caller
// and test stays green.
func (h *hnswIndex) search(query []float32, k int) ([]SearchResult, error) {
	return h.searchFiltered(query, k, nil)
}

// searchFiltered is search with an optional member predicate keyed by nodeId. A
// nil member means "accept all" (identical to the unfiltered search). When
// non-nil, layer-0 traversal still EXPANDS THROUGH non-member neighbors (graph
// connectivity is preserved), but only member nodes are ADMITTED to the result
// heap (filter-during-traversal, architecture §6/§7). The caller raises k (via
// fetchK = k + tombs + slack) so a selective filter does not under-return: the
// layer-0 ef is max(efSearch, k), so a larger fetchK widens the beam enough to
// reach graph-distant members a post-filter would miss. The member func resolves
// nodeId→slot via the segment's nodeSlot table (graphstore.go) and tests S_seg.
func (h *hnswIndex) searchFiltered(query []float32, k int, member func(nodeId uint64) bool) ([]SearchResult, error) {
	if k <= 0 {
		return nil, fmt.Errorf("vectorstore: k must be > 0, got %d", k)
	}
	if err := h.validateVector(query); err != nil {
		return nil, err
	}
	h.mu.RLock()
	defer h.mu.RUnlock()

	epId, maxLayer, err := h.store.GetEntryPoint()
	if err != nil {
		if errors.Is(err, errNoEntryPoint) {
			return nil, nil // empty index
		}
		return nil, err // e.g. a faulted store — surface it, don't mask as an empty index
	}

	// Verify entry point node still exists (may have been deleted).
	if _, err := h.store.GetVectorRef(epId); err != nil {
		return nil, nil
	}

	// Phase 1: From top layer down to 1, greedy search with ef=1. The descent is
	// pure navigation (no filtering): it must reach the query's neighborhood even
	// when the entry region holds no members.
	curEp := epId
	// Compare against stored vectors in stored form (cosine: unit vectors).
	prepared, _ := h.metric.prepare(query)
	for layer := maxLayer; layer >= 1; layer-- {
		changed := true
		for changed {
			changed = false
			neighbors, nerr := h.store.GetNeighbors(curEp, layer)
			if nerr != nil {
				return nil, nerr
			}
			curDist, derr := h.nodeDist(curEp, prepared)
			if derr != nil {
				return nil, derr
			}
			for _, nb := range neighbors {
				nbDist, derr := h.nodeDist(nb, prepared)
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

	// Phase 2: Layer 0 search with ef=max(efSearch, k), gated by member.
	ef := h.efSearch
	if k > ef {
		ef = k
	}
	results, err := h.searchLayerFiltered(prepared, curEp, ef, 0, member)
	if err != nil {
		return nil, err
	}

	// Sort by distance and return top-k.
	sortDistItems(results)
	if len(results) > k {
		results = results[:k]
	}

	out := make([]SearchResult, 0, len(results))
	for _, r := range results {
		docId, ok, err := h.store.GetDocId(r.id)
		if err != nil || !ok {
			continue // node may have been deleted concurrently
		}
		out = append(out, SearchResult{
			DocID:    docId,
			Distance: r.dist,
		})
	}
	return out, nil
}

// delete removes a document from the index inside a store transaction.
func (h *hnswIndex) delete(docId int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.runInTxnLocked(func() error { return h.deleteOneLocked(docId) })
}

// deleteNodeLocked removes a node from the HNSW graph. Caller must hold h.mu.
func (h *hnswIndex) deleteNodeLocked(nodeId uint64) error {
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
					dist: h.metric.distance(nbVec, cVec),
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
		// Fast path: pick the highest-level node from the deleted node's own
		// neighbor lists (likely link-reachable from the rest of the graph).
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
			if err := h.store.SetEntryPoint(newEp, newLevel); err != nil {
				return err
			}
		} else {
			// The deleted EP had no live neighbors. Reseat the EP to any other
			// live node (highest level preferred) so Search still returns the
			// remaining nodes; if none remain, clear the EP marker. nodeId is
			// still occupied here (DeleteNode runs below), so exclude it from
			// the scan.
			//
			// KNOWN LIMITATION (VEC-007, out of scope): the reseated EP is not
			// guaranteed link-reachable from every other live node. Search is a
			// pure graph traversal from the EP, so if the reseat picks an
			// isolated node, Search reaches only that node's connected component
			// — other components' true neighbors are deterministically and
			// silently dropped (results stay numerically correct) until a later
			// Insert re-links the graph. We restore availability here, not full
			// graph connectivity — still strictly better than the prior bug,
			// where a stale EP made the WHOLE index return empty.
			candID, candLevel, ok, err := h.store.HighestLiveNodeExcluding(nodeId)
			if err != nil {
				return err
			}
			if ok {
				if err := h.store.SetEntryPoint(candID, candLevel); err != nil {
					return err
				}
			} else {
				if err := h.store.ClearEntryPoint(); err != nil {
					return err
				}
			}
		}
	}

	// Remove the node data (also clears docId from slot and forward map).
	return h.store.DeleteNode(nodeId)
}

// --- searchLayer: Algorithm 5 ---

// searchLayer performs a beam search on a single layer with given ef width.
// Uses a min-heap for candidates and a max-heap for the result set.
// query is already in prepared (stored) form.
func (h *hnswIndex) searchLayer(query []float32, entryId uint64, ef int, layer int) ([]distItem, error) {
	entryDist, err := h.nodeDist(entryId, query)
	if err != nil {
		return nil, err
	}

	visited := visitedPool.Get().(*visitedSet)
	visited.begin()
	defer visitedPool.Put(visited)
	visited.mark(entryId)

	// candidates: min-heap (closest first). Pre-size both heaps to ef: otherwise
	// they regrow ~log2(ef) times via append, which dominated searchLayer's alloc
	// count. cands may exceed ef (it accumulates), so this is a hint, not a bound.
	candsBuf := make(minDistHeap, 0, ef)
	cands := &candsBuf
	cands.push(distItem{id: entryId, dist: entryDist})

	// result: max-heap bounded by ef (farthest first at top)
	resultsBuf := make(maxDistHeap, 0, ef+1)
	results := &resultsBuf
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

			nbDist, err := h.nodeDist(nbId, query)
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

// searchLayerFiltered is searchLayer with an optional member gate
// (filter-during-traversal). It keeps TWO bounded max-heaps:
//
//   - beam: every visited node (member AND non-member), bounded by ef. It governs
//     traversal — the expansion frontier and the stop condition — so the search
//     EXPANDS THROUGH non-member nodes and stays connected. This is the single most
//     error-prone point: gating the beam/candidate push (not just the result push)
//     would sever connectivity and under-return for selective filters.
//   - results: MEMBER nodes only, bounded by ef. This is the admission set returned.
//
// A node enters the beam (and thus becomes a traversal candidate) by the usual
// closeness test against the beam's farthest; it additionally enters results only
// when member==nil or member(nodeId) is true. With member==nil the two heaps stay
// identical and the function reduces exactly to searchLayer (regression-guarded).
func (h *hnswIndex) searchLayerFiltered(query []float32, entryId uint64, ef int, layer int, member func(nodeId uint64) bool) ([]distItem, error) {
	if member == nil {
		return h.searchLayer(query, entryId, ef, layer)
	}

	entryDist, err := h.nodeDist(entryId, query)
	if err != nil {
		return nil, err
	}

	visited := visitedPool.Get().(*visitedSet)
	visited.begin()
	defer visitedPool.Put(visited)
	visited.mark(entryId)

	// candidates: min-heap (closest first) — the unexpanded frontier. Pre-size all
	// three heaps to ef to avoid the ~log2(ef) append regrowths per call.
	candsBuf := make(minDistHeap, 0, ef)
	cands := &candsBuf
	cands.push(distItem{id: entryId, dist: entryDist})

	// beam: max-heap bounded by ef over ALL visited nodes — drives traversal.
	beamBuf := make(maxDistHeap, 0, ef+1)
	beam := &beamBuf
	beam.push(distItem{id: entryId, dist: entryDist})

	// results: max-heap bounded by ef over MEMBER nodes only — the admission set.
	resultsBuf := make(maxDistHeap, 0, ef+1)
	results := &resultsBuf
	if member(entryId) {
		results.push(distItem{id: entryId, dist: entryDist})
	}

	for cands.Len() > 0 {
		c := cands.pop()

		// Stop when the closest unexpanded candidate is farther than the farthest
		// node in the (full) beam: no closer region remains to expand into. Using
		// the BEAM (not results) here is what lets traversal cross non-member gaps.
		if beam.Len() >= ef && c.dist > (*beam)[0].dist {
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

			nbDist, err := h.nodeDist(nbId, query)
			if err != nil {
				continue // node may have been deleted
			}

			// Beam admission (traversal): closer than the beam's farthest, or beam
			// not yet full. Always expand through it regardless of membership.
			if beam.Len() < ef || nbDist < (*beam)[0].dist {
				cands.push(distItem{id: nbId, dist: nbDist})
				beam.push(distItem{id: nbId, dist: nbDist})
				if beam.Len() > ef {
					beam.pop()
				}
				// Result admission (the gate): members only.
				if member(nbId) {
					results.push(distItem{id: nbId, dist: nbDist})
					if results.Len() > ef {
						results.pop()
					}
				}
			}
		}
	}

	out := make([]distItem, results.Len())
	for i := range out {
		out[i] = (*results)[i]
	}
	return out, nil
}

// selectNeighborsHeuristic selects up to M neighbors using the heuristic
// from the paper. It ensures diversity by only adding a candidate if it
// is closer to the query than to any already-selected neighbor.
// If vecCache is non-nil, vectors are read from it instead of the store.
func (h *hnswIndex) selectNeighborsHeuristic(query []float32, candidates []distItem, m int) []distItem {
	if len(candidates) <= m {
		return candidates
	}

	// Sort candidates by distance to query.
	sortDistItems(candidates)

	// Resolve each candidate's stored vector once into a position-indexed slice.
	// getVectorRef is zero-copy, so reading directly here (no id-keyed cache) costs
	// only a slice header. vecs[i] corresponds to the just-sorted candidates[i]; a
	// nil vecs[i] means the vector was unavailable. Vectors are in stored form, so
	// metric.distance compares them directly with no norm lookup.
	n := len(candidates)
	vecs := make([][]float32, n)
	for i, c := range candidates {
		if v, err := h.store.GetVectorRef(c.id); err == nil {
			vecs[i] = v
		}
	}

	selected := make([]int, 0, m) // indices into candidates/vecs
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
			if h.metric.distance(cVec, sVec) < candidates[i].dist {
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

// nodeDist computes the distance between a stored node and an already-prepared
// query vector. Both operands are in stored form (cosine: unit vectors), so the
// metric handles cosine as a plain 1 - dot with no norm lookup.
func (h *hnswIndex) nodeDist(nodeId uint64, query []float32) (float32, error) {
	vec, err := h.store.GetVectorRef(nodeId)
	if err != nil {
		return 0, err
	}
	return h.metric.distance(vec, query), nil
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
// Buffers are pooled (visitedPool) rather than stored on hnswIndex because
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

// sortDistItems sorts distItems by distance ascending. Uses the generic
// slices.SortFunc (not sort.Slice) to avoid the reflect-based swapper allocation
// that dominated insert-path allocs in the build profile.
func sortDistItems(items []distItem) {
	slices.SortFunc(items, func(a, b distItem) int {
		if a.dist < b.dist {
			return -1
		}
		if a.dist > b.dist {
			return 1
		}
		return 0
	})
}
