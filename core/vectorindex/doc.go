// Package vectorindex is a standalone HNSW (Hierarchical Navigable Small World)
// approximate nearest-neighbor index over a pluggable NodeStore (in-memory or
// mmap-backed).
//
// Deprecated: vectorindex is superseded by core/vectorstore. The vectorstore
// engine embeds the same HNSW (copied and slimmed) and measures at search
// parity with vectorindex+MmapStore while building a graph with ~⅓ fewer
// allocations; on top of that it is a full segmented (LSM) vector store —
// durable (crash-safe via a bbolt control plane), with multiple named indexes,
// per-index metric, metadata-filtered search, and background space reclamation.
// Use core/vectorstore for all new code. vectorindex is retained only as the
// HNSW reference implementation and for the cmd/insert-analysis tool.
package vectorindex
