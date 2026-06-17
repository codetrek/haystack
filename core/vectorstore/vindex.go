package vectorstore

// VectorIndexConfig is the public, per-index configuration (architecture §7). Type
// is "hnsw" in v1 ("ivfpq" reserved). Metric is this index's distance metric: it
// may differ from the store's primary (records) metric, in which case the builder
// reconstructs raw vectors from records via the primary metric's restore() before
// computing this index's distances (§3.4 reconstruct-raw, Tasks 11-12). M/
// EfConstruction/EfSearch are the HNSW params (zero → package defaults).
type VectorIndexConfig struct {
	Type           string
	Metric         Metric
	M              int
	EfConstruction int
	EfSearch       int
}

// graphConfigFromCfg derives the internal graphConfig (HNSW params) from a public
// VectorIndexConfig, applying the same defaults graphConfig{}.withDefaults() does.
// The metric is NOT part of graphConfig (it is carried on the vindex / segment),
// so it is dropped here and threaded separately.
func graphConfigFromCfg(cfg VectorIndexConfig) graphConfig {
	return graphConfig{
		M:              cfg.M,
		EfConstruction: cfg.EfConstruction,
		EfSearch:       cfg.EfSearch,
	}.withDefaults()
}

// vindex is one named vector index: its HNSW config, its metric, and its per-
// segment built graphs (segId → builtIndex). An ABSENT key in graphs means that
// (index, segment) is pending → served by the brute fallback (§4.7). graphs is
// keyed by segID (not by s.sealed slice position), so it is robust to the merge
// path reordering the parallel sealed slices (gotcha 6). This is the exact value
// shape Phase 2 had as Store.graphs/gcfg, lifted into a per-name struct.
type vindex struct {
	cfg    graphConfig
	metric Metric
	graphs map[segID]*builtIndex
}

// newVindex builds an empty vindex from a public config: every segment starts
// pending (graphs is initialized but empty). The builder fills it per segment.
func newVindex(cfg VectorIndexConfig) *vindex {
	return &vindex{
		cfg:    graphConfigFromCfg(cfg),
		metric: cfg.Metric,
		graphs: make(map[segID]*builtIndex),
	}
}
