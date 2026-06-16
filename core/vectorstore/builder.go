package vectorstore

import "math/rand"

// graphConfig is the per-index HNSW config. In Phase 2 there is one index, so a
// single graphConfig per store. Defaults match the migrated graph.
type graphConfig struct {
	M, EfConstruction, EfSearch int
	Seed                        int64
}

func (c graphConfig) withDefaults() graphConfig {
	if c.M == 0 {
		c.M = defaultGraphM
	}
	if c.EfConstruction == 0 {
		c.EfConstruction = defaultGraphEfConstruction
	}
	if c.EfSearch == 0 {
		c.EfSearch = defaultGraphEfSearch
	}
	return c
}

// buildSegmentGraph builds an HNSW over the live slots of seg, persists it to
// segDir/graph.dat (fsync), and returns the reopened read-only graph store. This
// is the unit the background builder schedules per pending segment. It is a pure
// function of the (immutable) segment + cfg, so it is safe to run off the write
// path with no lock on the store.
func buildSegmentGraph(segDir string, seg *sealedSegment, cfg graphConfig) (*segGraphStore, error) {
	cfg = cfg.withDefaults()
	gs := newSegGraphStore(seg)
	idx := newHNSWIndex(gs,
		withGraphM(cfg.M),
		withGraphEfConstruction(cfg.EfConstruction),
		withGraphEfSearch(cfg.EfSearch),
		withGraphRand(rand.New(rand.NewSource(cfg.Seed))),
	)
	b := idx.newBatch()
	seg.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		gs.bindSlot(docID, slot)
		b.put(docID, stored)
	})
	if err := b.commit(); err != nil {
		return nil, err
	}
	if err := writeGraphFile(segDir, gs); err != nil {
		return nil, err
	}
	// Reopen from disk so the returned store is exactly what recovery would load
	// (no reliance on the in-memory build state lingering).
	return openGraphFile(segDir, seg)
}
