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
// Durability has two persistent faces (architecture §4.8): a per-write head WAL
// (Put/Delete fsync before mutation) and a single-file manifest atomically
// rewritten on each structural change (tmp+fsync+rename+dir-fsync) listing the
// sealed segments, their index states, and the head segId. Recovery loads the
// manifest, mmaps the sealed segments, rebuilds the global docId→segId map from
// each segment's on-disk slot→docId, reopens indexed graphs, replays the head
// WAL, resumes any pending build, and sweeps orphan seg dirs not referenced by
// the manifest (half-written seal files from a crash). Sealed segments are
// immutable except for their tombstone bitmaps.
//
// The two-level id model (architecture §4.6) spans segments: a stable int64
// docId (via core/idtable) maps to an owning segId, and each segment maps
// docId↔slot. Search returns docId-space results; mapping docId back to the
// caller's string id is the caller's responsibility (idtable has no reverse map).
//
// Phase 2 has exactly ONE index (the default HNSW over the store's metric). The
// HNSW graph algorithm + NodeStore seam are migrated (copied and slimmed so the
// graph stores only topology and resolves vectors from the owning sealed segment
// by slot) from core/vectorindex, which remains independent and unmodified.
//
// Out of scope (later phases): compaction / segment merge / space reclaim,
// attribute filtering, and multiple indexes (different metrics/params).
package vectorstore
