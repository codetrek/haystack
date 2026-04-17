# HNSW Mmap Storage: Industry Survey & Haystack Design

**Date:** 2026-04-17
**Scope:** How production vector databases store HNSW indexes with mmap, supporting incremental insert/delete/upsert — informing Haystack's own mmap flat file design.

---

## 1. Weaviate (Go)

**Source:** `weaviate/weaviate` — `adapters/repos/db/vector/hnsw/`

### File Format

| Component | Format |
|-----------|--------|
| **Mutations** | Append-only binary commit log (WAL) |
| **Persisted graph** | Condensed mmap flat file (`.condensed`) |
| **Vectors** | Stored separately from HNSW graph structure |

**Commit log record types** (16 total): Each record = 1 byte type tag + little-endian payload:

| Record | Layout | Size |
|--------|--------|------|
| `AddNode` | type(1B) + ID(8B) + level(2B) | 11B |
| `AddLinkAtLevel` | type(1B) + src(8B) + level(2B) + target(8B) | 19B |
| `ReplaceLinksAtLevel` | type(1B) + src(8B) + level(2B) + count(2B) + targets(8B * N) | variable |
| `AddTombstone` | type(1B) + ID(8B) | 9B |
| `RemoveTombstone` | type(1B) + ID(8B) | 9B |
| `SetEntryPointMaxLevel` | type(1B) + ID(8B) + level(2B) | 11B |
| `DeleteNode` / `ClearLinks` / `ResetIndex` | similar compact encodings | — |

**Condensed flat file layout:** The `MmapCondensorAnalyzer` replays commit logs and builds an index of `{id uint64, offset uint64, maxLevel uint16}` entries sorted by node ID. Each node's block is sized as:

```
nodeBlockSize = overhead(uint16 length indicators) + connectionsPerLevel * (maxLevel + 1)
// level 0 uses maxM0 = 2*M (standard HNSW)
```

Mapped with `mmap.MapRegion` using copy-on-write semantics.

### Incremental Update

- **Insert:** Appends `AddNode` + `AddLinkAtLevel`/`ReplaceLinksAtLevel` to commit log. Graph built in-memory.
- **Delete:** Two-phase tombstone system:
  1. `addTombstone()` — writes to both in-memory set and commit log (fast, O(1))
  2. `CleanUpTombstonedNodes()` — periodic background job with parallel workers:
     - Reassigns neighbor edges (removes dangling links, reconnects orphaned neighbors)
     - Replaces entry point if tombstoned
     - Removes tombstones from memory and commit log
     - Aborts if memory pressure exceeds 100MB

### Crash Recovery

Replays commit logs sequentially on startup to reconstruct in-memory HNSW graph. Commit log is the source of truth.

### Compaction

Two-level pipeline:

1. **Condensation:** Replay commit log into `.condensed` mmap flat file (net state only, no tombstones)
2. **Combination:** `CommitLogCombiner` merges consecutive `.condensed` files when `file1.size + file2.size <= threshold`. Writes to temp file, fsyncs, atomic rename, delete originals.

### Mmap Usage

Graph link structure is mmap'd after condensation. Vectors stored separately with configurable in-memory cache (`vectorCacheMaxObjects`). The HNSW index itself is primarily in-memory; condensed files provide persistence.

### Lock Discipline

`compressActionLock`, `deleteVsInsertLock`, `deleteLock` coordinate concurrent insert/delete/compaction.

---

## 2. Qdrant (Rust)

**Source:** `qdrant/qdrant` — `lib/segment/src/index/hnsw_index/`, `lib/segment/src/vector_storage/`

### File Format

**Segment architecture:** All data divided into independent segments, each with its own vector storage, payload index, and HNSW index.

| Segment type | Properties |
|-------------|-----------|
| **Appendable** | Read/write, accepts new inserts |
| **Non-appendable** | Read + soft delete only, optimized for search |

**Vector storage backends:**
- In-memory (backed by RocksDB for persistence)
- **Memmap:** Chunked appendable mmap format (`chunked_vectors`). Creates virtual address space mapped to disk.

**Graph links:** Flattened hierarchical structure. `GraphLinksEnum` has two backends:
- `Ram(Vec<u8>)` — fully in-memory
- `Mmap(Arc<Mmap>)` — memory-mapped, with `Advice::Random` access pattern and optional `populate` for page cache preloading

Three compression formats: Plain, Compressed, CompressedWithVectors.

### Incremental Update

- **Insert:** Written to appendable segment. When segment size crosses `indexing_threshold_kb`, indexing optimizer triggers HNSW construction.
- **Delete:** Soft delete via bit markers in ID tracker. Mutable and immutable tracker variants. Deleted records accumulate until vacuum threshold reached.
- **Upsert:** Insert with version tracking — newer version wins.

### Crash Recovery

**WAL with version tracking:** All operations get sequential numbers. On recovery, WAL is replayed; version numbers prevent applying stale operations to segments that already contain newer data.

### Compaction — Three Optimizers

| Optimizer | Trigger | Action |
|-----------|---------|--------|
| **Vacuum** | `deleted_count / total > deleted_threshold` | Rebuild segment excluding deleted records |
| **Merge** | `segment_count > default_segment_number` | Consolidate small segments |
| **Indexing** | Segment size exceeds `indexing_threshold_kb` | Migrate brute-force segment to HNSW + memmap |

All optimizers use **copy-on-write** to maintain read availability during optimization.

### Mmap Usage

Extensive. Both vector data and graph links support mmap backends. Configurable advice (Random, Sequential) and populate/prefetch settings. Chunked mmap format allows appending new vectors without remapping entire file.

---

## 3. hnswlib (C++)

**Source:** `nmslib/hnswlib` — `hnswlib/hnswalg.h`

### File Format

Single binary file, sequential layout:

```
[Metadata header]
  offsetLevel0_, max_elements_, cur_element_count,
  size_data_per_element_, label_offset_, offsetData_,
  maxlevel_, enterpoint_node_, maxM_, maxM0_, M_,
  mult_, ef_construction_
  (raw POD types, no versioning)

[Level-0 data block]
  cur_element_count * size_data_per_element_ bytes
  Each element = [links_level0 | vector_data | label]
  Links and vectors are INTERLEAVED in same flat array

[Higher-level link lists]
  For each element:
    uint size (0 if element has no higher levels)
    link list data (variable length)
```

Vectors are **inline** with level-0 links — no separation.

### Incremental Update

- **Insert:** `addPoint()` up to pre-allocated `max_elements`. **Cannot grow beyond this limit** without full rebuild.
- **Delete:** `markDelete()` sets a bit in byte 2 of level-0 link list memory: `*(ll_cur + 2) |= 0x01`. Soft delete only — **no graph repair**, edges to deleted nodes persist. Deleted nodes skipped during search.
- **No incremental persistence.** `saveIndex()` / `loadIndex()` are full serialization. No WAL.

### Crash Recovery

None. Crash during `saveIndex()` corrupts the file. No checksums, no WAL.

### Compaction

None. Deleted elements waste space permanently until full rebuild.

### Mmap Usage

None. All memory allocated via `malloc`. Entire index must fit in RAM.

### Assessment

hnswlib is a reference algorithm implementation, not a storage engine. Its format is the simplest to understand but lacks every production feature: no WAL, no mmap, no incremental persistence, no compaction, fixed capacity.

---

## 4. Vald (Go)

**Source:** `vdaas/vald` — `pkg/agent/core/ngt/service/`

### Architecture

Distributed vector database on Kubernetes, built on Yahoo Japan's **NGT** (Neighborhood Graph and Tree), **not HNSW**. Microservice architecture with agent pods.

### Storage

| Component | Purpose |
|-----------|---------|
| `kvs` (BidiMap) | Bidirectional UUID <-> internal object ID mapping |
| `vqueue` | Virtual queue buffering insert/delete before indexing |
| `core` | NGT index (vectors + graph) |
| `fmap` | Failed operation tracker for recovery |

### Insert/Delete

- **Insert:** Validates no duplicate UUID, pushes to `vqueue`. Periodically, vectors extracted from queue, inserted into NGT core, UUID-to-ID mapping stored in KVS.
- **Delete:** Also queued in `vqueue`, processed during periodic index creation. Removes both KVS entry and NGT core object.

**Key pattern:** Mutations are buffered in a virtual queue and batch-applied during periodic index creation, reducing rebuild frequency.

### Persistence

- Serialization via **GOB encoding** (Go-native binary format)
- **Copy-on-write save:** Write to temp directory, then atomically swap to production path
- On startup, attempts loading from multiple fallback locations
- Distributed resilience via Kubernetes replication

### Mmap Usage

None. Full in-memory index with periodic full serialization snapshots.

---

## 5. Chroma

**Source:** `chroma-core/chroma` — `rust/index/src/`

### Architecture

Wraps hnswlib through a Rust `HnswIndexProvider` that manages index lifecycle. Recent versions moved from Python hnswlib bindings to Rust layer.

### File Format

Splits hnswlib's single file into **four binary files:**

| File | Content |
|------|---------|
| `header.bin` | Index metadata (M, ef, dimensions, etc.) |
| `data_level0.bin` | Level-0 data (vectors + level-0 links) |
| `length.bin` | Element count/sizing info |
| `link_lists.bin` | Higher-level link lists |

Stored in S3 or local filesystem with optional encryption.

### Insert/Delete

Inherits hnswlib's mechanisms:
- Insert adds to in-memory index
- Delete uses `markDelete` (soft delete, no graph repair)
- **No incremental persistence** — index must be fully flushed

### Crash Recovery

- **Flush:** Serialize in-memory state to four files, upload with priority, supports parallel uploads
- **Load:** Double-checked locking to avoid redundant loads
- **Fork:** Copy serialized data from source with new UUID
- No WAL. Crash loses unflushed data.

### Mmap Usage

None. Full deserialization into memory.

### Assessment

Chroma's main contribution is splitting the single hnswlib file into four parallel-loadable files. Otherwise inherits all of hnswlib's limitations.

---

## Comparative Summary

| Feature | Weaviate | Qdrant | hnswlib | Vald | Chroma |
|---------|----------|--------|---------|------|--------|
| Language | Go | Rust | C++ | Go | Rust |
| Algorithm | HNSW | HNSW | HNSW | NGT | HNSW (via hnswlib) |
| Graph persist | Commit log -> mmap flat | Mmap or RAM, flattened | Single binary blob | GOB snapshot | 4 binary files |
| Vector persist | Separate store | Mmap chunks or RAM | Inline with level-0 | GOB snapshot | Inline with level-0 |
| Delete | Tombstone + background edge repair | Soft delete + vacuum rebuild | Bit flag, no repair | Queue batch | Bit flag, no repair |
| WAL | Typed binary commit log | Versioned WAL | None | None | None |
| Crash recovery | Commit log replay | WAL replay with versions | None | COW snapshots | None |
| Compaction | Condense + combine | Vacuum/merge/indexing optimizers | None | Full rebuild | None |
| Mmap graph | Yes (after condensation) | Yes (optional backend) | No | No | No |
| Mmap vectors | No (separate cache) | Yes (chunked) | No | No | No |
| Dynamic capacity | Yes | Yes (segment growth) | No (fixed max_elements) | Yes | No (hnswlib limit) |

---

## Recommended Design for Haystack

### Principles

1. **Separate vectors from graph structure** — different access patterns, different file management
2. **WAL-first** — all mutations hit the write-ahead log before anything else
3. **Two-phase delete** — tombstone immediately, repair graph in background
4. **Condense for mmap** — replay WAL into position-indexed flat files for mmap access
5. **Grow incrementally** — no fixed max_elements; use chunked or segment-based expansion

### File Layout

```
index_dir/
  wal/
    000001.wal          # append-only binary WAL segments
    000002.wal
  vectors.dat           # mmap'd flat vector storage
  vectors.idx           # ID -> offset mapping for vectors
  graph_l0.dat          # mmap'd level-0 connections (fixed-size blocks)
  graph_upper.dat       # mmap'd upper-level connections
  graph_upper.idx       # node ID -> offset into graph_upper.dat
  meta.json             # index metadata (M, efConstruction, dim, entryPoint, maxLevel)
  tombstones.bin        # active tombstone set
```

### File Formats — Detail

#### WAL Records

Same philosophy as Weaviate's commit log: typed binary records, append-only.

```
Record = TypeTag(1B) + Payload

Types:
  0x01  InsertVector     nodeID(8B) + level(2B) + vector(dim*4 B)
  0x02  SetLinks         nodeID(8B) + level(2B) + count(2B) + targetIDs(8B * count)
  0x03  SetEntryPoint    nodeID(8B) + maxLevel(2B)
  0x04  AddTombstone     nodeID(8B)
  0x05  RemoveTombstone  nodeID(8B)
  0x06  DeleteNode       nodeID(8B)

Each WAL segment = sequence of records + CRC32 per record for integrity
```

WAL segments are numbered sequentially. Fsync after each write (or batch of writes with configurable flush interval for throughput).

#### vectors.dat (Mmap'd)

Flat file of fixed-size vector blocks:

```
[Header: 32B]
  magic(4B) + version(2B) + dimensions(4B) + count(8B) + reserved(14B)

[Vector blocks]
  Block N at offset = 32 + N * (dim * 4)
  Each block = float32[dim]
```

Node IDs map directly to block offsets: `offset = headerSize + nodeID * dim * sizeof(float32)`. This gives O(1) random access. Deleted vectors leave gaps (reclaimed by compaction).

For growth beyond initial allocation: **chunked mmap** (inspired by Qdrant). Each chunk is a fixed-size mmap region (e.g., 64MB). New chunks allocated as needed. A chunk table maps nodeID ranges to chunks.

#### graph_l0.dat (Mmap'd)

Fixed-size blocks for level-0 connections:

```
[Header: 16B]
  maxM0(2B) + count(8B) + reserved(6B)

[Connection blocks]
  Block N at offset = 16 + N * blockSize
  blockSize = 2B (numLinks) + maxM0 * 8B (neighborIDs) + 1B (flags: tombstoned, etc.)
```

O(1) access: `offset = headerSize + nodeID * blockSize`. Same dense layout as hnswlib level-0 but separated from vectors.

#### graph_upper.dat + graph_upper.idx (Mmap'd)

Upper levels are sparse (few nodes have level > 0), so use offset index:

```
graph_upper.idx:
  [nodeID(8B) + offset(8B) + numLevels(2B)] * count
  Sorted by nodeID for binary search

graph_upper.dat:
  Per node: [level1_links | level2_links | ...]
  level_N_links = count(2B) + neighborIDs(8B * count)
```

### Insert Flow

```
1. Acquire insert lock (shared, allows concurrent inserts)
2. Append InsertVector to WAL
3. Write vector to vectors.dat (extend or fill gap from free list)
4. Run HNSW neighbor search (in-memory graph + mmap reads)
5. For each affected node, append SetLinks to WAL
6. Update in-memory graph (level-0 and upper-level connections)
7. Update graph_l0.dat and graph_upper.dat in-place (mmap writes)
```

**Batch insert optimization:** Buffer multiple inserts, write WAL records in batch, fsync once.

### Delete Flow

```
Phase 1 — Mark (immediate, O(1)):
  1. Append AddTombstone to WAL
  2. Add nodeID to in-memory tombstone set
  3. Set tombstone flag in graph_l0.dat block

Phase 2 — Cleanup (background, periodic):
  1. For each tombstoned node:
     a. Collect its neighbors
     b. For each neighbor, remove the tombstoned node from their link list
     c. Reconnect orphaned neighbors to maintain graph quality
        (find best replacement links via local search)
     d. If tombstoned node was entry point, elect new entry point
  2. Add nodeID to free list for vector slot reuse
  3. Append RemoveTombstone + DeleteNode to WAL
  4. Remove from in-memory tombstone set
```

Cleanup runs with configurable parallelism. Respects memory pressure. Can be interrupted and resumed.

### Upsert Flow

```
1. If nodeID exists and not tombstoned:
   a. Update vector in vectors.dat (overwrite in-place)
   b. Append InsertVector to WAL (marks as update)
   c. If vector changed significantly, re-link:
      - Run HNSW search with new vector
      - Update connections for affected nodes
2. If nodeID doesn't exist or is tombstoned:
   a. Remove tombstone if present
   b. Insert as new (standard insert flow)
```

### Crash Recovery

```
On startup:
  1. Read meta.json for index parameters
  2. Mmap vectors.dat, graph_l0.dat, graph_upper.dat (read-only initially)
  3. Load tombstones.bin into memory
  4. Replay WAL from last checkpoint:
     - For each record, apply to in-memory state
     - Verify against mmap'd files (skip if already applied)
  5. Re-mmap files as read-write
  6. Resume normal operation
```

**Checkpoint mechanism:** After condensation/compaction, record the WAL offset in meta.json. On recovery, only replay from checkpoint forward.

**CRC32 per WAL record** detects partial writes from crashes. Truncate WAL at first corrupted record.

### Compaction Strategy

Inspired by Weaviate's condense + combine, simplified:

**Level 1 — WAL Condensation (frequent):**
```
1. Snapshot current WAL position
2. Replay WAL records into mmap files (apply net state)
3. Fsync all mmap files
4. Update checkpoint in meta.json
5. Delete WAL segments before checkpoint
```

**Level 2 — Space Reclamation (periodic):**
```
Trigger: free_slots / total_slots > 20% (configurable)
1. Create new vectors.dat, graph_l0.dat, graph_upper.dat
2. Copy live (non-tombstoned) nodes with compacted IDs
3. Build ID remapping table (old ID -> new ID)
4. Fsync new files
5. Atomic swap (rename) new files over old
6. Delete old files
```

This is the most expensive operation. Run during low-traffic periods. Use copy-on-write during compaction so reads continue from old files.

**Level 3 — Upper-level defrag (rare):**
```
Rewrite graph_upper.dat + graph_upper.idx to remove gaps from deleted nodes.
Much cheaper than full compaction since upper levels are small.
```

### Mmap Strategy

| File | Access pattern | Mmap advice | Writability |
|------|---------------|-------------|-------------|
| vectors.dat | Random (search), sequential (scan) | `MADV_RANDOM` | Read-write |
| graph_l0.dat | Random (graph traversal) | `MADV_RANDOM` | Read-write |
| graph_upper.dat | Random, infrequent | `MADV_RANDOM` | Read-write |
| WAL segments | Sequential append | `MADV_SEQUENTIAL` | Append-only |

**Growth strategy:** Use Go's `syscall.Mmap` or `golang.org/x/exp/mmap`. For growth:
- Pre-allocate with headroom (e.g., 2x current size)
- When full, unmap, truncate file to new size (ftruncate), remap
- Or use chunked approach: multiple mmap regions of fixed size

**Page cache management:** For large indexes exceeding RAM, the OS page cache handles eviction. `MADV_DONTNEED` can be used to hint that compaction source pages are no longer needed after copying.

### Concurrency

```
Locks:
  insertMu    sync.RWMutex   // shared for concurrent inserts
  deleteMu    sync.Mutex     // exclusive for tombstone cleanup
  compactMu   sync.RWMutex   // write-lock during file swap, read-lock for normal ops
```

Insert and search can proceed concurrently. Tombstone cleanup acquires deleteMu to prevent concurrent cleanups. Compaction file swap briefly acquires compactMu write-lock.

### What We Borrow From Each Project

| Source | Borrowed idea |
|--------|--------------|
| **Weaviate** | Typed binary commit log, two-phase tombstone delete with background edge repair, condense + combine compaction pipeline, lock discipline pattern |
| **Qdrant** | Separate mutable/immutable storage concepts, chunked mmap for growth, mmap advice configuration, version-tracked WAL replay, three-tier optimization |
| **hnswlib** | Fixed-size level-0 block layout (O(1) offset calculation), interleaved link + metadata format as baseline reference |
| **Vald** | Virtual queue pattern for batch mutations, COW directory swap for atomic persistence, BidiMap for external-to-internal ID mapping |
| **Chroma** | Splitting index into multiple files for parallel I/O, provider/cache lifecycle management |

### Implementation Priority

1. **Phase 1 — WAL + in-memory graph** (foundation)
   - Binary WAL with typed records and CRC32
   - In-memory HNSW graph (current implementation)
   - WAL replay on startup

2. **Phase 2 — Mmap vector storage**
   - vectors.dat with O(1) access by nodeID
   - Chunked growth support

3. **Phase 3 — Mmap graph storage**
   - graph_l0.dat with fixed-size blocks
   - graph_upper.dat with offset index
   - Transition from fully in-memory to mmap-backed

4. **Phase 4 — Compaction**
   - WAL condensation (level 1)
   - Space reclamation (level 2)

5. **Phase 5 — Production hardening**
   - Concurrent insert/delete/search stress testing
   - Crash recovery fuzz testing
   - Memory pressure handling
