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
	neighbors   []map[int][]uint64 // nodeId → layer → neighbor ids
	nodeSlot    []int              // nodeId → segment slot (for GetVectorRef)
	nodeDoc     []int64            // nodeId → docId
	docToNode   map[int64]uint64   // docId → nodeId
	pendingSlot map[int64]int      // docId → slot, set by bindSlot, consumed by PutNode

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
	g.neighbors[id] = make(map[int][]uint64)
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
	if id >= uint64(len(g.neighbors)) || g.neighbors[id] == nil {
		return nil, nil
	}
	nb := g.neighbors[id][layer]
	cp := make([]uint64, len(nb))
	copy(cp, nb)
	return cp, nil
}

func (g *segGraphStore) SetNeighbors(id uint64, layer int, neighbors []uint64) error {
	if id >= uint64(len(g.neighbors)) || g.neighbors[id] == nil {
		return fmt.Errorf("segGraphStore: SetNeighbors on unknown node %d", id)
	}
	cp := make([]uint64, len(neighbors))
	copy(cp, neighbors)
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
