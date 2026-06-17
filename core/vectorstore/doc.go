// Package vectorstore is the segmented (LSM-style) vector store engine.
//
// Records are (string id, vector, payload). They live in one mutable in-memory
// "head" segment plus an ordered set of immutable on-disk SEALED segments. The
// head is brute-searched and never gets a graph; when it reaches maxSegSize (or
// on Seal) it is frozen into a sealed records-segment (mmap'd vectors in
// metric-natural form + norm, slot→docId, a persisted tombstone bitmap, and
// payload) under seg-<id>-<gen>/, and a fresh head starts. A background builder
// then builds a per-segment HNSW graph over the sealed segment and flips its
// state pending→indexed.
//
// Search is an N-way merge: each indexed sealed segment via its HNSW (results
// post-filtered by that segment's tombstone bitmap, since the immutable graph
// can still return tombstoned nodes), each pending sealed segment by brute, and
// the head by brute — all into one shared top-k heap in docId space.
//
// Multiple named indexes (architecture §4.7). A store carries N named vector
// indexes in s.indexes (name → *vindex{cfg, metric, graphs}), all sharing ONE
// set of records-segments — records are sealed/merged exactly once, never copied
// per index. The store is born with a single index named "default" that carries
// the store's primary (records) metric and reproduces the single-index behavior
// byte-identically. CreateVectorIndex / DropVectorIndex / RebuildVectorIndex /
// ListVectorIndexes / IndexLag manage the rest; Search(index, q, k, filter)
// selects which index answers. Each named index owns one HNSW graph PER sealed
// segment, so the state lives at (index, segment) granularity: an absent key in
// vindex.graphs means that pair is PENDING and is served by the same brute
// fallback the head uses; a present key means INDEXED. A new index is therefore
// born pending for every existing segment — immediately queryable via brute —
// and a background builder fills its per-segment graphs (pending→indexed). Seal
// and merge build N graphs per output segment; a merge defers until a segment is
// indexed in EVERY index (the close-during-build guard, generalized). Per index,
// the graph file is graph-<name>.dat within the shared seg dir, so N indexes
// coexist and DropVectorIndex deletes exactly one index's files;
// RebuildVectorIndex clears an index's graphs, deletes those files, and respawns
// its builds (a param/metric-change repair or torn-graph rebuild from records).
//
// Per-index metric via reconstruct-raw (architecture §3.4). The records store ONE
// metric-natural form (the primary metric, manifest.Metric). An index whose
// metric DIFFERS reconstructs the raw vector from the stored form + norm
// (primaryMetric.restore, ~1e-7) and re-prepares it under its own metric, both
// when BUILDING its graph (so the inserted node and its neighbors share one
// space) and at SEARCH time across every brute distance leg — vectors are never
// stored a second time. The "default"/same-metric path skips reconstruction and
// is byte-identical to the single-index engine.
//
// Durability lives in a single embedded bbolt control store (control.db,
// architecture §4.8): every Put/Delete commits the durable head row (head bucket)
// before mutating the in-memory head, and every structural change commits one
// bbolt write-txn (copy-on-write page swap, fsync) that snapshots the sealed
// records-segments + head segId, the N index configs, and a per-(index, segment)
// {gen, pending|indexed} state. Recovery loads the control plane in one read-txn,
// mmaps the sealed segments, rebuilds the global docId→segId map from each
// segment's on-disk slot→docId, reopens every index's indexed graphs, rebuilds the
// head from the head bucket, resumes any pending build for ALL indexes, and sweeps
// orphan seg dirs not referenced by the segments bucket (half-written seal files
// from a crash). Sealed segments are immutable except for their tombstone bitmaps.
//
// The two-level id model (architecture §4.6) spans segments: a stable int64
// docId (via core/idtable) maps to an owning segId, and each segment maps
// docId↔slot. Search returns docId-space results; mapping docId back to the
// caller's string id is the caller's responsibility (idtable has no reverse map).
//
// The HNSW graph algorithm + NodeStore seam are migrated (copied and slimmed so
// the graph stores only topology and resolves vectors from the owning sealed
// segment by slot) from core/vectorindex, which remains independent and
// unmodified.
//
// Out of scope (留口, reserved but not built): IVF-PQ (the Type field is reserved
// only), partial-coverage indexes (every index covers every segment),
// OR/NOT boolean filters, and per-index WaitForIndex.
package vectorstore
