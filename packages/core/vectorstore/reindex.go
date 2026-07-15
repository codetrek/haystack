package vectorstore

import "math/rand"

// reindexNodeStore wraps a segGraphStore for an index whose metric DIFFERS from the
// store's primary (records) metric. The records store vectors in the primary
// metric's natural form (cosine: unit + norm); this index needs them in ITS metric's
// form. reindexNodeStore overrides exactly two methods of the embedded base store:
//
//   - Metric() returns the INDEX metric (so the HNSW computes this index's distance).
//   - GetVectorRef(id) reconstructs the RAW vector from the records via the primary
//     metric's restore() (cosine unit·norm → raw, ~1e-7, §3.4), then re-prepares it
//     under the index metric. No vector is re-stored on disk (§3 "向量只存一份").
//
// All other graphNodeStore methods (topology, nodeId↔slot/docId, entry point) are
// promoted from the embedded *segGraphStore unchanged, so buildSegmentGraph,
// newBuiltIndex, search, and searchFiltered all work verbatim against it.
type reindexNodeStore struct {
	*segGraphStore
	primary Metric
	index   Metric
}

// newReindexNodeStore builds a reindex store over seg. primary is the records
// (store) metric; index is this index's metric.
func newReindexNodeStore(seg *sealedSegment, primary, index Metric) *reindexNodeStore {
	return &reindexNodeStore{
		segGraphStore: newSegGraphStore(seg),
		primary:       primary,
		index:         index,
	}
}

// Metric returns the INDEX metric, overriding the embedded store's primary metric.
func (r *reindexNodeStore) Metric() Metric { return r.index }

// GetVectorRef resolves nodeId→slot, reads the stored (primary-form) vector + norm,
// reconstructs the raw vector via the primary metric, and re-prepares it under the
// index metric. The result is the form the index's HNSW distance expects, for both
// the build's NEIGHBOR reads and the search-time distance evaluations.
func (r *reindexNodeStore) GetVectorRef(id uint64) ([]float32, error) {
	base, err := r.segGraphStore.GetVectorRef(id) // stored (primary) form for the row
	if err != nil {
		return nil, err
	}
	slot := r.segGraphStore.nodeSlot[id]
	raw := r.primary.restore(base, r.segGraphStore.seg.norm(slot)) // unit·norm → raw (~1e-7)
	prepared, _ := r.index.prepare(raw)
	return prepared, nil
}

// reindexVector is the index-metric form of one stored (primary-form) row: restore
// raw via the primary metric, then prepare under the index metric. It is the single
// source of truth used at BOTH the build's self-vector (the b.put arg) and the
// GetVectorRef neighbor reads, so the inserted node and its candidate neighbors are
// always measured in the SAME space (appendix #2-7 — the HNSW insert path takes the
// new node's own vector from b.put at hnsw.go:219, NOT from GetVectorRef, so feeding
// b.put the primary stored form would split build space and corrupt the graph).
func reindexVector(stored []float32, norm float32, primary, index Metric) []float32 {
	prepared, _ := index.prepare(primary.restore(stored, norm))
	return prepared
}

// buildSegmentGraphReindex builds an index's graph over seg using a metric that
// differs from the records' primary metric, reconstructing raw per node (§3.4). It
// returns a plain *segGraphStore (the reindex wrapper is a build-time concern; the
// persisted graph is topology only, reopened normally — but the SEARCH leg must also
// reconstruct, via newBuiltIndexFor in Task 12). The new node's own vector handed to
// b.put is in the SAME index-metric space the GetVectorRef override returns for its
// neighbors (appendix #2-7), so the graph topology is correct for the index metric.
func buildSegmentGraphReindex(segDir, name string, seg *sealedSegment, primary, index Metric, cfg graphConfig) (*segGraphStore, error) {
	cfg = cfg.withDefaults()
	rs := newReindexNodeStore(seg, primary, index)
	idx := newHNSWIndex(rs,
		withGraphM(cfg.M),
		withGraphEfConstruction(cfg.EfConstruction),
		withGraphEfSearch(cfg.EfSearch),
		withGraphRand(rand.New(rand.NewSource(cfg.Seed))),
	)
	b := idx.newBatch()
	seg.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		rs.bindSlot(docID, slot)
		// Feed the batch the INDEX-metric form of the node's OWN vector: insertOneLocked
		// measures the new node via this arg (hnsw.go:219/262) while its neighbors come
		// from GetVectorRef (also index-form). Passing `stored` (primary form) here would
		// place the node in primary space while neighbors live in index space → a wrong
		// graph (appendix #2-7). reindexVector mirrors the GetVectorRef override exactly.
		b.put(docID, reindexVector(stored, norm, primary, index))
	})
	if err := b.commit(); err != nil {
		return nil, err
	}
	if err := writeGraphFile(segDir, name, rs.segGraphStore); err != nil {
		return nil, err
	}
	return openGraphFile(segDir, name, seg)
}

// newBuiltIndexFor wraps a reopened graph store in a search-ready index under the
// INDEX metric. When index == primary it is exactly newBuiltIndex (the segment's
// own metric, byte-identical to Phases 1-5). When they differ, the reopened topology
// store is re-wrapped in a reindexNodeStore so GetVectorRef reconstructs raw +
// re-prepares under the index metric at SEARCH time too (symmetric with the build,
// §3.4). builtIndex.store stays the plain *segGraphStore (the filtered search leg's
// nodeSlot access in searchLocked uses bi.store.nodeSlot — unchanged); only bi.idx's
// node store is the reindex wrapper, so distance computations reconstruct.
func newBuiltIndexFor(gs *segGraphStore, seg *sealedSegment, primary, index Metric, cfg graphConfig) *builtIndex {
	if index == primary {
		return newBuiltIndex(gs, cfg)
	}
	cfg = cfg.withDefaults()
	rs := &reindexNodeStore{segGraphStore: gs, primary: primary, index: index}
	return &builtIndex{
		store: gs,
		idx: newHNSWIndex(rs,
			withGraphM(cfg.M),
			withGraphEfConstruction(cfg.EfConstruction),
			withGraphEfSearch(cfg.EfSearch),
		),
	}
}
