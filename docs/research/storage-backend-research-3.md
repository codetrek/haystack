# Storage Backend Research for HNSW Vector Index (HAY-007)

> Date: 2026-04-17
> Context: Haystack HNSW index currently uses Pebble; random point-read performance is the bottleneck (read:write = 86:1, ~3500 reads/insert, 1.6 亿 total reads for 50K build)

## Workload Profile

| Metric | Value |
|---|---|
| Dataset | 50K vectors, 128-dim |
| Value size | 512 B (128-dim) – 3072 B (768-dim) |
| Reads/insert | ~3,500 (neighbor traversal) |
| Writes/insert | ~37 |
| Total reads (50K build) | ~160,000,000 |
| Target read latency | < 1 μs (memory), < 10 μs (disk+cache) |
| Total dataset size | 50K × 3 KB ≈ 150 MB |

## Candidate Comparison

| | Pebble | bbolt | Badger v4 | BuntDB | NutsDB | Custom mmap |
|---|---|---|---|---|---|---|
| **Engine** | LSM-tree | B+ tree (mmap) | LSM + value log | In-memory + AOF | Bitcask variant | Flat file + mmap |
| **GitHub stars** | ~5.8K | ~9.5K | ~15.6K | ~4.8K | ~3.6K | N/A |
| **Last release** | Active (CockroachDB core) | v1.4.x (2025) | v4.9.1 (2026-02) | Maintenance mode | v1.1.0 (2025-12) | N/A |
| **Pure Go (zero CGo)** | Yes (default build) | Yes | Yes | Yes | Yes | Yes |
| **Random read latency** | 1–5 μs (warm cache), 10–50 μs (cold) | 0.5–2 μs (mmap warm) | 2–10 μs (value indirection) | < 0.5 μs (all in RAM) | 1–3 μs (key index in RAM) | 0.1–0.5 μs (direct pointer) |
| **Write latency** | ~2–5 μs (WAL batch) | 50–200 μs (COW + fsync) | ~3–8 μs (WAL) | ~1–3 μs (append AOF) | ~2–5 μs | ~1 μs (msync deferred) |
| **Memory efficiency** | Good (block cache tunable) | Good (OS page cache) | Poor (high base overhead) | Poor (all data in RAM) | OK (key index in RAM) | Excellent (OS page cache) |
| **Crash safety** | WAL + manifest | COW B+ tree (excellent) | WAL + value log | AOF replay | Bitcask merge | Manual (msync) |
| **API complexity** | High (many options) | Simple | Medium | Very simple | Simple | DIY |
| **Dep tree weight** | Heavy (CockroachDB) | Light | Medium | Minimal | Light | Zero |

## Per-Library Analysis

### 1. Pebble (current) — github.com/cockroachdb/pebble

**Architecture:** LSM-tree with WAL, bloom filters, block cache.

**Why it underperforms for HNSW:**
- Random point reads must check bloom filter → index block → data block per SST level
- 50K keys likely spans 1–3 SST levels; each read touches 2–4 blocks
- Block cache helps, but HNSW traversal has poor locality (random neighbor hops)
- Compaction background work competes for I/O

**When it's still OK:**
- If the entire dataset fits in block cache (~150 MB, easily tunable), reads hit cached blocks
- Already integrated — switching has engineering cost

**Verdict:** Solid but architecturally mismatched for random-read-dominant workloads.

### 2. bbolt — go.etcd.io/bbolt

**Architecture:** Single-file B+ tree, mmap'd pages, COW writes.

**Why it's better for reads:**
- B+ tree lookup = O(log_B N) page accesses; for 50K keys with 4 KB pages, ~3–4 page touches
- Entire file is mmap'd — reads go through OS page cache, no userspace buffering layer
- No bloom filter overhead, no SST level multiplier
- 150 MB file easily stays in page cache

**Write concern:**
- COW semantics: every write copies touched pages → new root
- Must `fsync` on commit → 50–200 μs per write tx
- Mitigated by batching: accumulate neighbor updates in one write tx per insert

**Concurrency model:**
- Single writer, multiple concurrent readers (MVCC via COW)
- Readers never block writers (and vice versa) — good for HNSW where reads dominate

**Verdict:** Strong candidate. Natural fit for read-heavy + batched-write workload.

### 3. Badger v4 — github.com/dgraph-io/badger

**Architecture:** WiscKey — keys in LSM, values in separate value log.

**Why it's NOT ideal here:**
- Values 512–3072 B sit at the awkward threshold boundary
  - Below `ValueThreshold` → stored inline in LSM (same as Pebble, no benefit)
  - Above → requires two seeks: LSM for key → value log for data
- Value log GC adds complexity and unpredictable latency spikes
- Higher base memory usage than Pebble or bbolt
- Known production stability issues in community reports

**When value separation helps:**
- Very large values (>16 KB) where keeping them out of LSM compaction saves I/O
- Not this workload.

**Verdict:** Not recommended. Adds complexity without read performance benefit at this value size.

### 4. BuntDB — github.com/tidwall/buntdb

**Architecture:** In-memory B-tree with optional AOF persistence.

**Reads:** All data in RAM → sub-microsecond. Benchmarks report ~4.6M reads/sec.

**Concerns:**
- **Maintenance:** 172 commits total, low recent activity, essentially one-person project
- **Persistence:** AOF append + periodic shrink. Not crash-safe in the same way as WAL-based stores
- **Memory:** Entire dataset in Go heap → GC pressure on larger datasets
- **Scale ceiling:** Fine at 150 MB, questionable at 1 GB+

**Verdict:** Fastest reads, but maintenance risk and GC pressure concern. Consider only if bbolt doesn't meet latency targets.

### 5. NutsDB — github.com/nutsdb/nutsdb

**Architecture:** Bitcask-inspired — all keys indexed in memory, values on disk.

**Reads:** Key lookup in RAM hash map → one disk read at known offset. Fast (~1–3 μs warm).

**Concerns:**
- Less battle-tested than bbolt/Pebble
- Compaction (merge) can cause latency spikes
- Smaller community, fewer production deployments

**Verdict:** OK option but no advantage over bbolt for this workload.

### 6. Custom mmap Flat File

**Design:**
```
vectors.dat:  [vec_0 (512B padded)] [vec_1] ... [vec_49999]
graph.dat:    [neighbors_0 (var)] [neighbors_1] ...
index:        map[uint64]offset  (in-memory or separate file)
```

Read path: `id → offset → mmap slice → done`. One pointer dereference + possible page fault.

**Advantages:**
- Absolute minimum overhead: no serialization, no key comparison, no tree traversal
- 150 MB file trivially fits in OS page cache
- Zero library dependencies, zero background goroutines
- Predictable, no GC interaction (mmap is outside Go heap)

**Disadvantages:**
- Must handle: variable-length records, crash safety, file growth, deletion/compaction
- No transactions, no key iteration, no range queries
- Testing and correctness burden is on you

**Implementation options:**
- `syscall.Mmap` / `golang.org/x/sys/unix` — direct, low-level
- `github.com/edsrzf/mmap-go` (~4K stars) — thin cross-platform wrapper

**Verdict:** Maximum performance, maximum engineering cost. Best when the KV store is proven to be the bottleneck.

### 7. Others Considered

| Library | Notes | Verdict |
|---|---|---|
| **RoseDB** (github.com/rosedblabs/rosedb, 5K stars) | Bitcask model, pure Go, clean API | Similar to NutsDB, no clear advantage |
| **LotusDB** (github.com/lotusdblabs/lotusdb, 2K stars) | B+ tree index + value log | Immature, small community |
| **go-memdb** (github.com/hashicorp/go-memdb, 3.5K stars) | In-memory immutable radix tree | No persistence — not suitable |
| **Bolt** (github.com/boltdb/bolt) | Original, archived | Use bbolt instead |

## Recommendation Ranking

### Tier 1: Recommended

| Rank | Library | Rationale |
|---|---|---|
| **1** | **bbolt** | Best balance of read performance, simplicity, and reliability. Mmap B+ tree is architecturally matched to random-read workloads. Batch writes to amortize COW cost. Battle-tested (etcd core). |
| **2** | **Custom mmap** | Maximum performance ceiling. Choose this if bbolt benchmarks show B+ tree overhead is still too high, or if you want zero-dependency storage. |

### Tier 2: Acceptable

| Rank | Library | Rationale |
|---|---|---|
| **3** | **Pebble (tuned)** | Already integrated. Before switching, try: increase block cache to 256 MB, use bloom filters, measure actual hit rate. If cache-warm reads are <2 μs, the switch may not be worth the engineering cost. |
| **4** | **BuntDB** | Fastest reads (in-memory). Acceptable if dataset stays small and maintenance risk is tolerable. |

### Tier 3: Not Recommended

| Rank | Library | Rationale |
|---|---|---|
| **5** | NutsDB | No advantage over bbolt |
| **6** | Badger | Wrong architecture for this value size and access pattern |

## Suggested Next Steps

1. **Benchmark Pebble with tuned cache** — set block cache = 256 MB, measure p50/p99 read latency for HNSW traversal. This is the cheapest experiment.
2. **Prototype bbolt backend** — implement `Store` interface with bbolt, batch writes per insert (one write tx with all ~37 neighbor updates).
3. **Compare** — run 50K build benchmark with both backends. If bbolt is 2×+ faster, switch.
4. **Consider mmap later** — only if bbolt still doesn't meet targets and profiling confirms storage is the bottleneck (not HNSW algorithm overhead).
