// Package vectorstore is the Phase-1 records layer of the vector store engine.
//
// It stores records as (string id, vector, payload) in a single in-memory "head"
// segment and answers k-nearest-neighbor queries by brute force, synchronously.
// Vectors are kept in their metric-natural stored form (cosine: unit vector +
// norm; dot/euclidean: raw + norm 0); the original vector is reconstructed (as a
// fresh copy) on Get. A string id maps to a stable int64 docId via core/idtable.
//
// Durability: each Put/Delete is written to a CRC-checked write-ahead log and
// fsynced before any in-memory state changes, and the WAL record carries BOTH
// the records data AND the string id, so the WAL is the single crash-safe source
// of truth for the id-to-docId mapping. On reopen the head and all derived maps
// are rebuilt exactly by replaying the log in order (which also re-drives the
// allocator to reconstruct identical docIds), so a Put/Delete is durable on
// return even after an unclean crash.
//
// Two-level id model (architecture §4.6): a global docId-to-segId map plus a
// per-segment docId-to-slot map. In Phase 1 there is exactly one segment (the
// head), so the global level is the identity {every docId -> head} and is
// intentionally not materialized; the per-segment map is segment.docToSlot.
//
// Search returns results in docId space ([]SearchResult{DocID, Distance}).
// Mapping docId back to the caller's string id is the caller's responsibility in
// Phase 1, because idtable has no reverse map. If string-id results are required,
// that is a scope addition beyond Phase 1.
//
// Phase 1 deliberately excludes: sealing the head into immutable on-disk
// segments, the on-disk mmap segment file format + manifest atomic-swap, per-
// segment HNSW/IVF index building, N-way segment merge, compaction / space
// reclaim (Delete only tombstones), attribute filtering, and multiple indexes.
// Those arrive in later phases (see core/docs/vectorstore/architecture.md §8;
// the standalone "payload" phase in §8 is subsumed here because §8.1 folds
// payload into the records layer). The existing core/vectorindex (HNSW) package
// is independent and unaffected.
package vectorstore
