# HNSW 存储后端选型调研

> Date: 2026-04-17 | Context: HAY-007 PebbleStore build performance

## 1. 问题本质

HNSW 构建的 I/O 模式极其特殊：**随机点查为主，写极少**。

| 指标 | 数值 |
|------|------|
| 50K 向量总读次数 | ~1.6 亿次 |
| 每次 insert 平均读 | 3,500 次向量 |
| 每次 insert 平均写 | 37 次 |
| 读写比 | **86:1** |
| 单次读取大小 | 512 bytes (128d × 4B) |
| MemStore 耗时 | 59s (1.18ms/op) |
| PebbleStore 耗时 | 18min (21.85ms/op) |
| 性能差距 | **18.5x** |

核心瓶颈：Pebble 的 LSM-tree 为写优化设计，每次随机读需要遍历 memtable + 多级 SST，对 HNSW 的随机点查 pattern 天然不利。

## 2. 业界 HNSW 实现的存储方案

### hnswlib (原始实现, C++)
- **方案**: 单一连续内存块，向量按 ID 顺序排列
- **持久化**: 整体 `save_index()` / `load_index()` 写入/读取单个二进制文件
- **特点**: 纯内存操作，O(1) 向量访问（基地址 + offset），加载时整块读入内存
- **结论**: 全量内存，不做增量持久化

### Weaviate (Go)
- **方案**: 自研 mmap 文件格式
  - 向量存储：固定大小记录的 flat file，mmap 映射，按 ID 直接偏移寻址
  - 图连接：commitlog + 内存中的 slice-of-slices
  - HNSW 层级数据全部在内存
- **持久化**: commitlog (WAL) 用于 crash recovery，定期 compaction
- **特点**: 向量通过 mmap 实现 O(1) 访问，内存由 OS page cache 管理
- **启动**: mmap 映射即可，无需反序列化

### Milvus (Go + C++)
- **方案**: Segment-based 架构
  - Growing segment（写入缓冲）在内存
  - Sealed segment（只读）flush 到对象存储/本地文件
  - 向量数据以列式存储，连续排列
- **特点**: 读写分离，sealed segment 可 mmap 加载

### Qdrant (Rust)
- **方案**: 自研存储
  - 向量: mmap'd flat file，固定大小记录
  - Graph: mmap'd flat file
  - Payload: RocksDB (仅用于元数据过滤，不用于向量)
- **特点**: 热数据内存，冷数据 mmap，向量绝不经过 KV store

### Chroma / LanceDB
- **方案**: 基于 Arrow/Lance 列式格式存储向量
- **特点**: 批量加载高效，但非 HNSW 的主流选择

### 关键共识

> **没有一个成熟的向量数据库用通用 KV store (LSM-tree 或 B+tree) 存储向量数据。**
>
> 所有高性能实现都选择：连续内存/文件 + O(1) offset 寻址。

## 3. 候选方案评估

### 方案 A：MemStore + Snapshot（推荐 #1）

**原理**: 构建和搜索全在内存，后台定期序列化到文件。

| 维度 | 评价 |
|------|------|
| 随机点查 | **最优** — 直接 map 查找，~50ns |
| 构建性能 | **最优** — 与当前 MemStore 一致 (59s/50K) |
| 内存占用 | 50K×128d ≈ 25MB 向量 + 图结构 ≈ **40-50MB** |
| 500K×768d ≈ | **~1.5GB 向量 + 图 ≈ 2GB** |
| 持久化 | snapshot 文件，gob/binary 序列化 |
| 启动时间 | 反序列化加载，50K ≈ <1s, 500K ≈ 5-10s |
| 增量更新 | 原生支持（内存 map 操作） |
| 实现复杂度 | **低** — 新增 ~200 行 snapshot 逻辑 |
| 风险 | crash 时丢失最近写入（可接受 WAL 缓解） |

**内存占用分析**（目标规模）:

| 规模 | 维度 | 向量内存 | 图+元数据 | 总计 |
|------|------|----------|-----------|------|
| 10K | 128 | 5 MB | 5 MB | ~10 MB |
| 50K | 128 | 25 MB | 15 MB | ~40 MB |
| 100K | 384 | 150 MB | 40 MB | ~190 MB |
| 500K | 768 | 1.5 GB | 200 MB | ~1.7 GB |

对于代码搜索工具，10K-100K 是最常见规模，内存完全可接受。500K×768d 的 1.7GB 在现代开发机上也合理。

### 方案 B：mmap 自定义文件格式（推荐 #2）

**原理**: 向量连续存于 flat file，mmap 映射，O(1) 偏移寻址。图结构单独文件。

```
vectors.bin:  [header][vec_0: 512B][vec_1: 512B]...[vec_N]
graph.bin:    [header][node_0: level + neighbors per layer]...
meta.bin:     [entry_point][doc_id mappings]...
```

| 维度 | 评价 |
|------|------|
| 随机点查 | **极优** — mmap offset, ~100-200ns (page cache 命中) |
| 构建性能 | 接近 MemStore（热数据在 page cache） |
| 内存占用 | OS 管理 page cache，实际驻留按需，低于全内存方案 |
| 持久化 | 天然持久，写即落盘 |
| 启动时间 | **最优** — mmap 映射 <10ms，无需反序列化 |
| 增量更新 | 中等 — 向量可原地更新（固定大小），图结构需要处理变长 neighbor list |
| 实现复杂度 | **中高** — ~500-800 行，需处理文件增长、delete 空洞、graph 变长记录 |
| 风险 | 跨平台 mmap 行为差异；变长 graph 记录需要设计 |

**graph 变长问题解法**: 按 Mmax0 预分配固定大小 slot（每个 node = 4 + 32×8 + 16×8×maxLayer ≈ 640B），浪费一些空间换取 O(1) 寻址。

### 方案 C：bbolt (B+tree)

| 维度 | 评价 |
|------|------|
| 随机点查 | **中** — B+tree 读优于 LSM，但仍有 page 遍历开销，~1-5μs |
| 构建性能 | 预估 3-5x 慢于 MemStore（好于 Pebble 的 18.5x） |
| 内存占用 | 低 — mmap 读，OS page cache 管理 |
| 持久化 | 原生 |
| 启动时间 | 快 — mmap 打开文件即可 |
| 增量更新 | 原生支持（事务） |
| 实现复杂度 | **低** — 接口适配 ~300 行 |
| 风险 | 写性能差（单 writer 锁）；1.6亿次点查仍有显著开销 |

**数据**: bbolt 随机读 ~1-5μs/op（page cache 热时），50K 构建预估 3-5 分钟。比 Pebble 好但远逊内存方案。

### 方案 D：Badger (LSM, value 分离)

| 维度 | 评价 |
|------|------|
| 随机点查 | **中** — value log 直接 seek 读，~2-10μs，优于 Pebble 的多级查找 |
| 构建性能 | 预估 5-8x 慢于 MemStore |
| 内存占用 | 低-中 — key 在内存，value 在 vlog |
| 持久化 | 原生 |
| 启动时间 | 中 — 需要加载 key 索引 |
| 增量更新 | 原生支持 |
| 实现复杂度 | **低** — 接口适配 ~300 行 |
| 风险 | GC 带来的写放大；仍是通用 KV 的开销 |

Badger 的 value 分离设计（WiscKey 论文）对大 value 随机读有帮助，但对 HNSW 的亿级点查仍不够快。

### 方案 E：自研 flat file + index

| 维度 | 评价 |
|------|------|
| 随机点查 | **最优** — 等同方案 B |
| 构建性能 | **最优** |
| 内存占用 | 可控 |
| 持久化 | 自行实现 |
| 启动时间 | 取决于实现 |
| 增量更新 | 需要自行实现所有逻辑 |
| 实现复杂度 | **极高** — 1000+ 行，需实现 crash recovery、compaction、并发控制 |
| 风险 | 大量 edge case；实质上在写一个专用数据库 |

除非 mmap 方案无法满足需求，否则不建议走这条路。方案 B 的 mmap 已覆盖其核心优势。

### 方案 F：保持 Pebble + 大 cache

| 维度 | 评价 |
|------|------|
| 随机点查 | 取决于 cache 命中率 |
| 构建性能 | cache 全命中 → 接近 MemStore；cache miss → 回退到 21ms/op |
| 内存占用 | cache 50K 向量 ≈ 25MB（可接受），但此时与方案 A 等价 |
| 持久化 | 原生 |
| 启动时间 | 中 — 需要预热 cache |
| 增量更新 | 原生支持 |
| 实现复杂度 | **极低** — 调大 cache size |
| 风险 | 如果 cache 住所有向量，Pebble 只是个昂贵的持久化层 |

**关键洞察**: 如果 cache 足够大到覆盖所有向量（构建时 100% 命中率所需），那么 Pebble 退化为纯粹的持久化写入层。此时不如直接用方案 A（MemStore + Snapshot），更简单且无 Pebble 的 compaction/WAL 开销。

## 4. 对比总结

| 方案 | 构建性能 | 随机读延迟 | 内存效率 | 启动速度 | 增量更新 | 实现复杂度 | 综合评分 |
|------|----------|-----------|----------|----------|---------|-----------|---------|
| **A: MemStore+Snapshot** | ★★★★★ | ★★★★★ | ★★★☆☆ | ★★★★☆ | ★★★★★ | ★★★★★ | **#1** |
| **B: mmap flat file** | ★★★★★ | ★★★★☆ | ★★★★★ | ★★★★★ | ★★★☆☆ | ★★★☆☆ | **#2** |
| C: bbolt | ★★★☆☆ | ★★★☆☆ | ★★★★☆ | ★★★★☆ | ★★★★☆ | ★★★★☆ | #4 |
| D: Badger | ★★☆☆☆ | ★★★☆☆ | ★★★★☆ | ★★★☆☆ | ★★★★☆ | ★★★★☆ | #5 |
| E: 自研 flat file | ★★★★★ | ★★★★★ | ★★★★★ | ★★★★★ | ★★★☆☆ | ★☆☆☆☆ | #3 |
| F: Pebble+大cache | ★★★★☆ | ★★★★☆ | ★★★☆☆ | ★★★☆☆ | ★★★★★ | ★★★★★ | #6 |

## 5. 推荐路径

### 阶段 1（立即）：方案 A — MemStore + Snapshot

**理由**:
1. **最小改动，最大收益** — 18.5x 性能提升，~200 行代码
2. **已验证** — MemStore 已经是生产质量代码，只需加持久化
3. **内存可接受** — 目标规模 10K-100K，内存 10-190MB
4. 所有成熟向量数据库的 HNSW 实现本质上都是内存方案

**实现计划**:
```go
type SnapshotStore struct {
    mem       *MemNodeStore
    path      string        // snapshot 文件路径
    dirty     atomic.Bool   // 有未持久化的变更
    interval  time.Duration // snapshot 间隔
}

// 启动: 从文件加载 → MemNodeStore
// 运行: 所有读写走 MemNodeStore
// 持久化: 后台 goroutine 定期 binary 序列化到文件
// 关闭: 最终 snapshot + close
```

Snapshot 格式（binary，非 gob —— 更快更紧凑）:
```
[magic: 4B][version: 4B][entryID: 8B][maxLayer: 4B][nodeCount: 4B]
[vectors section: id + dim + float32 data...]
[levels section: id + level...]
[norms section: id + float32...]
[neighbors section: id + layer + count + neighbor_ids...]
[mappings section: docId + nodeId...]
[checksum: 32B SHA-256]
```

### 阶段 2（如果需要）：方案 B — mmap 迁移

仅在以下情况考虑：
- 500K+ 向量场景内存不足
- 启动时间要求 <100ms
- 需要多进程共享索引

## 6. 快速 ROI 分析

| | 现状 (Pebble) | 方案 A (MemStore+Snapshot) | 提升 |
|--|--------------|--------------------------|------|
| 50K 构建 | 18 min | ~59s | **18x** |
| 100K 构建 | ~40 min (预估) | ~3 min | **13x** |
| 搜索延迟 | ~2ms (cache miss 更高) | ~0.1ms | **20x** |
| 启动时间 | ~1s (打开 DB) | ~1-2s (加载 snapshot) | 持平 |
| 内存 (50K) | 88MB + Pebble overhead | ~40MB | **更低** |
| 磁盘 (50K) | 36MB (Pebble SST) | ~30MB (binary snapshot) | 持平 |
| 代码变更 | 0 | ~200-300 行 | 极低 |

## 7. 结论

**用通用 KV store 存储 HNSW 向量是一个架构错配。** 业界共识明确：HNSW 需要 O(1) 随机访问，应该用内存或 mmap flat file，不应用 LSM-tree 或 B+tree。

对于 Haystack 的规模（10K-500K 向量），**MemStore + Snapshot 是性价比最高的方案**：实现简单、性能最优、已有成熟代码基础。mmap 方案作为未来演进路径保留。

不建议在 KV store 选型上花更多时间（bbolt/Badger/Pebble 调优），因为这是在错误的方向上优化。
