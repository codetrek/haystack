package vectorstore

import "fmt"

// segGraphStore is a slim graphNodeStore: it owns only graph topology (neighbor
// lists per layer, per-node level, entry point) and the nodeId↔docId/segment-slot
// maps. Vectors are NOT stored here — GetVectorRef resolves nodeId→slot→sealed
// segment row, so the segment's vectors are the single copy (§3 "向量只存一份").
//
// nodeId model (adversarial-review appendix #4): nodeId is NOT the segment slot.
// It is a DENSE live-only build index (0,1,2,...) assigned by NextNodeId over the
// segment's LIVE slots, so the migrated graph's dense-id assumptions (visitedSet
// indexes a flat slice by id) hold even when the segment has tombstone gaps.
// nodeSlot[nodeId] resolves the segment row, so a segment with interior holes
// neither aliases nor overflows visitedSet. bindSlot must be called for each live
// (docId, slot) BEFORE that docId is inserted, so PutNode/GetVectorRef can map the
// freshly allocated nodeId to its slot.
type segGraphStore struct {
	seg *sealedSegment

	nextID      uint64
	levels      []int              // nodeId → level
	neighbors   []map[int][]uint32 // nodeId → layer → neighbor ids (BUILD path only)
	nodeSlot    []int              // nodeId → segment slot (for GetVectorRef)
	nodeDoc     []int64            // nodeId → docId
	docToNode   map[int64]uint64   // docId → nodeId
	pendingSlot map[int64]int      // docId → slot, set by bindSlot, consumed by PutNode

	// Flat CSR neighbor topology, populated ONLY by openGraphFile (the read path).
	// When csrNodeBase != nil the store is a loaded, immutable, search-only graph and
	// the neighbor accessors read from these compact arrays instead of the per-node
	// maps above (which stay nil); this drops the per-node hmap/bucket/slice-header
	// overhead and the O(N) decode allocs. csrNodeBase[id]..[id+1] is node id's
	// half-open range of layer slots; csrLayerStart[ls]..[ls+1] is that slot's
	// half-open range of edges in csrPool. csrPool may be a non-nil empty slice (an
	// edge-less loaded graph), so csrNodeBase — always make([]uint32, N+1) — is the
	// loaded/build discriminator. The build path never sets these, so its mutating
	// methods are unaffected.
	csrNodeBase   []uint32 // nodeId → first layer slot (len N+1)
	csrLayerStart []uint32 // layer slot → first edge in csrPool (len LayerSlots+1)
	csrPool       []uint32 // all neighbor ids, node ascending then layer ascending

	entryID  uint64
	maxLayer int
	hasEntry bool
}

func newSegGraphStore(seg *sealedSegment) *segGraphStore {
	return &segGraphStore{
		seg:         seg,
		docToNode:   make(map[int64]uint64),
		pendingSlot: make(map[int64]int),
	}
}

// bindSlot records the segment slot for docId so the next PutNode(... docId) can
// associate the new nodeId with that slot. Called by the builder per live row.
func (g *segGraphStore) bindSlot(docID int64, slot int) { g.pendingSlot[docID] = slot }

func (g *segGraphStore) Metric() Metric { return g.seg.metric }
func (g *segGraphStore) Dim() int       { return g.seg.dim }

func (g *segGraphStore) GetVectorRef(id uint64) ([]float32, error) {
	if id >= uint64(len(g.nodeSlot)) || g.nodeSlot[id] < 0 {
		return nil, fmt.Errorf("segGraphStore: node %d not found", id)
	}
	return g.seg.getVectorRef(g.nodeSlot[id]), nil
}

func (g *segGraphStore) PutNode(id uint64, level int, vector []float32, docId int64) error {
	slot, ok := g.pendingSlot[docId]
	if !ok {
		return fmt.Errorf("segGraphStore: PutNode docId %d without bindSlot", docId)
	}
	for uint64(len(g.levels)) <= id {
		g.levels = append(g.levels, 0)
		g.neighbors = append(g.neighbors, nil)
		g.nodeSlot = append(g.nodeSlot, -1)
		g.nodeDoc = append(g.nodeDoc, 0)
	}
	g.levels[id] = level
	g.neighbors[id] = make(map[int][]uint32)
	g.nodeSlot[id] = slot
	g.nodeDoc[id] = docId
	g.docToNode[docId] = id
	return nil
}

func (g *segGraphStore) DeleteNode(id uint64) error {
	if id >= uint64(len(g.nodeSlot)) || g.nodeSlot[id] < 0 {
		return nil
	}
	doc := g.nodeDoc[id]
	if g.docToNode[doc] == id {
		delete(g.docToNode, doc)
	}
	g.nodeSlot[id] = -1
	g.neighbors[id] = nil
	return nil
}

func (g *segGraphStore) GetNeighbors(id uint64, layer int) ([]uint64, error) {
	nb := g.getNeighborsRef(id, layer)
	cp := make([]uint64, len(nb))
	for i, v := range nb {
		cp[i] = uint64(v)
	}
	return cp, nil
}

// getNeighborsRef returns the layer's neighbor ids without copying (read-only; see
// interface). A sealed segment's graph is immutable once searched, so no lock is
// needed.
//
// Two representations back this slice, never both at once:
//   - LOADED (search) stores set csrPool (openGraphFile): the slice is a sub-range
//     of the flat csrPool, addressed via the csrNodeBase/csrLayerStart prefix sums.
//     Those arrays are validated monotonic and in-bounds at open, so the slice
//     expression cannot panic.
//   - BUILD stores leave csrPool nil and use the per-node neighbors maps.
//
// Either way the slice is owned Go heap, NOT mmap — only the segment's VECTORS
// (vectors.dat) are mmap'd, resolved separately via GetVectorRef. The neighbor
// TOPOLOGY in graph-<name>.dat is read into a heap buffer (readWholeFile uses
// ReadAt) and decoded into fresh heap slices, so the ref cannot be unmapped. The
// graph is built-once / read-only thereafter and the search holds this store for its
// whole duration, so the ref is never reallocated or mutated under the caller.
func (g *segGraphStore) getNeighborsRef(id uint64, layer int) []uint32 {
	if g.csrNodeBase != nil {
		if id >= uint64(len(g.csrNodeBase)-1) {
			return nil
		}
		base := g.csrNodeBase[id]
		nLayers := g.csrNodeBase[id+1] - base
		if layer < 0 || uint32(layer) >= nLayers {
			return nil
		}
		ls := base + uint32(layer)
		return g.csrPool[g.csrLayerStart[ls]:g.csrLayerStart[ls+1]]
	}
	if id >= uint64(len(g.neighbors)) || g.neighbors[id] == nil {
		return nil
	}
	return g.neighbors[id][layer]
}

func (g *segGraphStore) SetNeighbors(id uint64, layer int, neighbors []uint64) error {
	if id >= uint64(len(g.neighbors)) || g.neighbors[id] == nil {
		return fmt.Errorf("segGraphStore: SetNeighbors on unknown node %d", id)
	}
	cp := make([]uint32, len(neighbors))
	for i, v := range neighbors {
		cp[i] = uint32(v)
	}
	g.neighbors[id][layer] = cp
	return nil
}

func (g *segGraphStore) GetEntryPoint() (uint64, int, error) {
	if !g.hasEntry {
		return 0, 0, errNoEntryPoint
	}
	return g.entryID, g.maxLayer, nil
}

func (g *segGraphStore) SetEntryPoint(id uint64, maxLayer int) error {
	g.entryID = id
	g.maxLayer = maxLayer
	g.hasEntry = true
	return nil
}

func (g *segGraphStore) ClearEntryPoint() error {
	g.hasEntry = false
	g.entryID = 0
	g.maxLayer = 0
	return nil
}

func (g *segGraphStore) HighestLiveNodeExcluding(exclude uint64) (uint64, int, bool, error) {
	bestID := uint64(0)
	bestLevel := -1
	found := false
	for id := uint64(0); id < uint64(len(g.nodeSlot)); id++ {
		if id == exclude || g.nodeSlot[id] < 0 {
			continue
		}
		lvl := g.levels[id]
		if !found || lvl > bestLevel || (lvl == bestLevel && id < bestID) {
			bestID, bestLevel, found = id, lvl, true
		}
	}
	if !found {
		return 0, 0, false, nil
	}
	return bestID, bestLevel, true, nil
}

func (g *segGraphStore) GetNodeLevel(id uint64) (int, error) {
	if id >= uint64(len(g.levels)) || g.nodeSlot[id] < 0 {
		return 0, fmt.Errorf("segGraphStore: node %d not found", id)
	}
	return g.levels[id], nil
}

func (g *segGraphStore) GetNodeId(docId int64) (uint64, bool, error) {
	id, ok := g.docToNode[docId]
	return id, ok, nil
}

func (g *segGraphStore) GetDocId(id uint64) (int64, bool, error) {
	if id >= uint64(len(g.nodeDoc)) || g.nodeSlot[id] < 0 {
		return 0, false, nil
	}
	return g.nodeDoc[id], true, nil
}

func (g *segGraphStore) NextNodeId() (uint64, error) {
	id := g.nextID
	g.nextID++
	return id, nil
}

func (g *segGraphStore) txnBegin() error            { return nil }
func (g *segGraphStore) txnCommit() error           { return nil }
func (g *segGraphStore) txnAbort(cause error) error { return cause }

// builtIndex couples a segment's durable graph topology (store) with a single,
// long-lived hnswIndex wrapper built once from the index config (appendix #26:
// do NOT reconstruct a fresh hnswIndex per query per segment — that re-derives
// mL/M, can diverge from build-time params, and allocates on the hottest path).
// idx.search is RLock-safe via the index's own mutex, so one shared instance is
// reused across concurrent Searches.
type builtIndex struct {
	store *segGraphStore
	idx   *hnswIndex
}

// newBuiltIndex wraps a (reopened or freshly built) graph store in a search-ready
// index using the store's HNSW config.
func newBuiltIndex(gs *segGraphStore, cfg graphConfig) *builtIndex {
	cfg = cfg.withDefaults()
	return &builtIndex{
		store: gs,
		idx: newHNSWIndex(gs,
			withGraphM(cfg.M),
			withGraphEfConstruction(cfg.EfConstruction),
			withGraphEfSearch(cfg.EfSearch),
		),
	}
}
