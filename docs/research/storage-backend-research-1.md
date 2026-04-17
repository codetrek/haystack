# HNSW 向量索引存储后端选型研究

> **日期**: 2026-04-17
> **关联**: HAY-007 PebbleStore 构建性能优化
> **状态**: 调研完成

---

## 1. 问题陈述

Haystack 的 HNSW 向量索引在构建阶段是**读密集型**工作负载：

| 指标 | 数值 |
|------|------|
| 每次 Insert 平均读向量 | ~3,500 次 |
| 每次 Insert 平均写操作 | ~37 次 |
| **读写比** | **86:1** |
| 50K 向量总随机读 | 1.6 亿次 |
| 50K 向量总写入 | 186 万次 |
| 单条向量大小 (128d float32) | 512 bytes |
| 单条向量大小 (768d float32) | 3,072 bytes |

当前 PebbleStore（LSM-tree）是**写优化**结构，与 HNSW 构建的读密集模式严重不匹配：

| 后端 | 50K 耗时 | ops/sec | 相对速度 |
|------|----------|---------|----------|
| MemStore | 59s (1.18ms/op) | 847 | **1x (基准)** |
| PebbleStore | 18min (21.85ms/op) | 46 | **18.5x 慢** |

---

## 2. 当前 Pebble 配置分析

```
internal/core/pebble/db.go:
  Cache:                    调用方传入 cacheSize
  BlockSize:                32 KB
  FilterPolicy:             Bloom(10)
  MemTableSize:             4 MB
  MaxOpenFiles:             8192
  L0CompactionThreshold:    12
  MaxConcurrentCompactions: 2
```

```
internal/core/vectorindex/store.go:
  LRU cache 容量:            10,000 条向量
  GetVectorRef():           零拷贝读取
```

**瓶颈诊断**：
- 32KB BlockSize 意味着读一个 512B 向量要解压一整个 32KB block
- LRU cache 10K 条在 50K 数据集下命中率仅约 20%
- LSM 多层查找：Bloom filter → L0 → L1 → ... 每次随机读可能触发多次磁盘 I/O
- 每次 Get 返回的 value 需要 Pebble 内部拷贝（即使 GetVectorRef 做了应用层零拷贝）

---

## 3. 方案详细评估

### 方案 A：MemStore 构建 + Pebble 持久化（混合模式）

**架构**：
```
[构建阶段]  Insert → MemNodeStore (全内存)
[持久化]    构建完成 → 批量 dump 到 Pebble
[启动]      Pebble scan → 加载到 MemNodeStore
[搜索]      查询 → MemNodeStore (内存)
```

**性能预期**：

| 指标 | 估算 |
|------|------|
| 50K 构建耗时 | ~59s（等同 MemStore） |
| 持久化耗时 (50K) | ~2-5s（顺序批量写入） |
| 启动加载耗时 (50K) | ~3-8s（Pebble scan + 反序列化） |
| 搜索延迟 | <1ms（纯内存） |

**内存占用**：

| 规模 | 128d | 768d |
|------|------|------|
| 10K | ~9 MB | ~35 MB |
| 50K | ~44 MB | ~175 MB |
| 100K | ~88 MB | ~350 MB |
| 500K | ~440 MB | ~1.75 GB |

> 含向量 + neighbors + metadata 开销，约为纯向量大小的 1.5-1.7x

**优点**：
- 构建速度等同纯内存，18.5x 提升
- 实现简单：复用现有 MemNodeStore + PebbleNodeStore
- 搜索延迟最优
- 渐进式实现，不破坏现有接口
- Pebble 批量写入非常高效（顺序写，一次 fsync）

**缺点**：
- 内存占用与数据规模线性增长，500K x 768d 需要 ~1.75 GB
- 启动时需要全量加载，冷启动较慢
- 构建过程中宕机丢失未持久化数据（可通过 checkpoint 缓解）
- 增量更新（单条 upsert）需要特殊处理

**实现复杂度**：**低** (~200-300 行代码)
- 需要实现 `MemNodeStore.DumpTo(PebbleNodeStore)` 方法
- 需要实现 `PebbleNodeStore.LoadInto(MemNodeStore)` 方法
- 修改 HNSWIndex 初始化逻辑，支持两阶段模式
- 已有 MemNodeStore 和 PebbleNodeStore 完整实现

---

### 方案 B：mmap 文件存储

**架构**：
```
vectors.bin:   [vec_0: 512B][vec_1: 512B]...[vec_N: 512B]  ← 固定大小记录
neighbors.bin: [node_0_layer_0][node_0_layer_1]...          ← 变长，需索引
meta.bin:      entry point, levels, mappings                ← 小文件
```

向量按 ID 偏移访问：`offset = id * dim * 4`，O(1) 随机读。

**性能预期**：

| 指标 | 估算 |
|------|------|
| 热读延迟 | <1 us（等同内存指针访问） |
| 冷读延迟 | ~10-100 us（page fault） |
| 50K 构建耗时 | ~60-70s（接近 MemStore，page cache 覆盖） |
| 写入延迟 | 需 munmap/mmap 周期扩展文件 |

**参考实现**：Weaviate 使用 mmap 存储 HNSW 图和向量数据，读延迟 <1us。

**优点**：
- 随机读性能理论最优
- OS page cache 自动管理热数据
- 内存占用由 OS 管理，不占 Go heap（无 GC 压力）
- 500K 向量在 page cache 热时等同内存访问

**缺点**：
- **实现复杂度高**：固定大小向量尚可，变长 neighbors 需要复杂索引
- **并发写入困难**：扩展文件需要 munmap→truncate→mmap，期间所有读失效
- **跨平台差异**：Windows 的 mmap 行为不同（不能 truncate mmap'd 文件）
- **崩溃恢复复杂**：无 ACID 保证，需自行实现 WAL 或 checkpoint
- **删除/upsert 复杂**：需要空洞管理或紧凑化
- golang.org/x/exp/mmap 只支持只读，写入必须用 syscall

**实现复杂度**：**高** (~1000-1500 行代码)
- 向量文件 + neighbors 文件 + metadata 文件
- 空间分配器（处理删除产生的空洞）
- 跨平台 mmap 封装（build tags for linux/darwin/windows）
- 崩溃恢复机制
- 文件增长策略

---

### 方案 C：bbolt (B+tree)

**架构**：单文件 B+tree，mmap 读取，copy-on-write 写入。

```go
db, _ := bbolt.Open("index.db", 0600, nil)
db.View(func(tx *bbolt.Tx) error {
    b := tx.Bucket([]byte("vectors"))
    vec := b.Get(uint64ToBytes(id))  // mmap 直接读取
    return nil
})
```

**性能预期**：

| 指标 | 估算 |
|------|------|
| 热读延迟 | 1-5 us（mmap B+tree 查找） |
| 冷读延迟 | 50-200 us |
| 写入延迟 | 50-200 us/tx（含 fsync） |
| 50K 构建耗时 | ~90-150s（估算，batch 写入 + mmap 读） |

**优点**：
- B+tree 读优化，比 LSM 随机读快
- 单文件存储，部署简单
- mmap 读取，OS page cache 自动管理
- 成熟稳定（etcd 核心组件）
- API 简洁，事务模型清晰

**缺点**：
- **单写者模型**：写事务互斥，虽然 86:1 比例可接受但构建时仍有写
- **写入较慢**：每个写事务 fsync，批量写需要大事务
- B+tree 开销比 mmap 直接偏移访问大（需要树遍历）
- 文件只增不缩（需要 Compact 回收空间）
- 不支持压缩

**实现复杂度**：**中** (~400-500 行代码)
- 实现新的 BboltNodeStore
- Bucket 设计（vectors / neighbors / metadata / mappings）
- 读事务池化（避免每次分配）
- 写事务批量化

---

### 方案 D：modernc.org/sqlite（纯 Go SQLite）

**架构**：纯 Go 转译的 SQLite，B-tree 存储，WAL 模式。

```sql
CREATE TABLE vectors (id INTEGER PRIMARY KEY, data BLOB);
CREATE TABLE neighbors (node_id INTEGER, layer INTEGER, neighbors BLOB, PRIMARY KEY(node_id, layer));
```

**性能预期**：

| 指标 | 估算 |
|------|------|
| 热读延迟 | 5-15 us（SQL 解析 + B-tree 查找） |
| 冷读延迟 | 100-500 us |
| 50K 构建耗时 | ~150-300s（估算） |

**优点**：
- SQL 查询灵活，方便调试
- WAL 模式支持并发读写
- 成熟的 ACID 保证
- 单文件部署

**缺点**：
- **纯 Go 版比 CGo 慢 2-4x**：c2go 转译的代码无法享受 Go 编译器优化
- SQL 解析开销：KV 场景下 SQL 层是纯开销
- 内存分配多（C-to-Go 内存模型转换）
- 对 KV 场景而言过于重量级
- 社区反馈性能波动较大

**实现复杂度**：**中** (~400-500 行代码)
- Schema 设计 + prepared statements
- 事务管理
- Blob 编解码

---

### 方案 E：Pebble 优化（当前架构调优）

**可调参数**：

| 参数 | 当前值 | 建议值 | 影响 |
|------|--------|--------|------|
| Cache | 调用方决定 | 256-512 MB | 全数据集缓存 |
| BlockSize | 32 KB | 4 KB | 减少读放大 (32KB→4KB = 8x) |
| Compression | Snappy (默认) | NoCompression | 省去解压 CPU |
| LRU cache (应用层) | 10,000 | 50,000-100,000 | 直接命中率翻倍 |
| BloomFilter | 10 bits | 保持 | 已足够 |

**性能预期（调优后）**：

| 场景 | 预期延迟 |
|------|----------|
| 全数据集缓存热 | 1-3 us/read |
| 50K 构建耗时（预热后） | ~120-180s（2-3x 提升，从 18min 降到 ~3min） |
| 首次冷启动 | 仍然 18min |

**关键问题**：构建阶段是逐步插入新向量，cache 从冷到热是渐进的。前期数据少时 cache 命中率高，后期数据增长超过 cache 容量后性能下降。**无法根本解决构建阶段的性能问题**。

**优点**：
- 改动最小，只调参数
- 搜索阶段（cache 热）性能接近内存
- 不需要新的 Store 实现

**缺点**：
- 构建阶段改善有限（3-5x 提升 vs 方案 A 的 18.5x）
- 大规模场景 cache 内存开销与方案 A 相当，但性能更差
- BlockSize 修改需要重建数据库
- 不解决根本的架构不匹配问题

**实现复杂度**：**极低** (~20-50 行代码)

---

### 方案 F：BadgerDB（KV 分离 LSM）

**架构**：Key-value 分离，key 在 LSM-tree，value 在 append-only vlog。

**评估**：
- 512B 向量低于 BadgerDB v4 的 ValueThreshold (1MB)，会被 **inline 存储在 LSM 中**
- 等同于另一个 LSM-tree，随机读性能与 Pebble 相当（3-10 us 热读）
- **无优势**，额外增加 vlog GC 的运维复杂度
- 社区活跃度下降，Pebble 是更好的 LSM 选择

**结论**：**不推荐**，对本场景无收益。

---

## 4. 方案对比总表

| 维度 | A: 混合模式 | B: mmap | C: bbolt | D: SQLite | E: Pebble 调优 |
|------|-------------|---------|----------|-----------|----------------|
| **构建速度** | ★★★★★ ~59s | ★★★★★ ~65s | ★★★☆☆ ~120s | ★★☆☆☆ ~200s | ★★★☆☆ ~150s |
| **搜索延迟** | ★★★★★ <1ms | ★★★★★ <1ms | ★★★★☆ <2ms | ★★★☆☆ <5ms | ★★★★☆ <2ms |
| **内存占用** | ★★☆☆☆ 全量 | ★★★★☆ OS 管理 | ★★★★☆ OS 管理 | ★★★☆☆ 中等 | ★★★☆☆ cache 依赖 |
| **持久化安全** | ★★★★☆ Pebble | ★★☆☆☆ 需自建 | ★★★★★ ACID | ★★★★★ ACID | ★★★★★ Pebble |
| **实现复杂度** | ★★★★★ 低 | ★★☆☆☆ 高 | ★★★☆☆ 中 | ★★★☆☆ 中 | ★★★★★ 极低 |
| **跨平台** | ★★★★★ | ★★★☆☆ | ★★★★★ | ★★★★★ | ★★★★★ |
| **500K 可行性** | ★★★☆☆ ~1.7GB | ★★★★★ | ★★★★☆ | ★★★★☆ | ★★★☆☆ cache 大 |
| **增量更新** | ★★★☆☆ 需特殊处理 | ★★☆☆☆ 复杂 | ★★★★☆ 自然支持 | ★★★★☆ 自然支持 | ★★★★★ 自然支持 |

---

## 5. 推荐排序

### 第一推荐：方案 A — MemStore 构建 + Pebble 持久化

**理由**：
1. **性能最优**：构建速度直接等同 MemStore（18.5x 提升），这是理论上限
2. **实现最简**：200-300 行代码，复用现有 MemNodeStore + PebbleNodeStore
3. **风险最低**：两个 Store 实现已经过充分测试（HAY-006 完整覆盖）
4. **渐进式**：不破坏现有 NodeStore 接口，可与当前代码并存

**适用规模**：10K-100K 向量（128d），10K-50K 向量（768d）。超过此规模内存成为瓶颈。

**增量更新策略**：
- 少量 upsert（<100 条）：直接操作 MemStore（已在内存中）
- 大批量更新：重建索引（HNSW 大批量更新本身效率低）

### 第二推荐：方案 A + E 组合 — 混合模式 + Pebble 参数调优

**理由**：
- 方案 A 负责构建阶段性能
- Pebble 参数调优（BlockSize 4KB、NoCompression）优化持久化层效率
- 两者互补，无冲突

### 第三推荐：方案 B — mmap（长期演进方向）

**理由**：
- 如果 500K x 768d 规模成为刚需，mmap 是唯一不需要全量内存的高性能方案
- 可作为 v2 架构，在方案 A 验证后再启动
- 参考 Weaviate 的成熟实践

### 不推荐：方案 C (bbolt)、方案 D (SQLite)、方案 F (BadgerDB)

**理由**：
- bbolt 和 SQLite 比 Pebble 快但比 MemStore 慢很多，属于"中间地带"——付出迁移成本却拿不到最优性能
- BadgerDB 对此场景无收益
- 如果要换存储引擎，不如直接跳到 mmap（方案 B）获取根本性提升

---

## 6. 实施路线图

```
Phase 1 (HAY-007): 方案 A — MemStore + Pebble 混合模式
├── 实现 DumpTo / LoadFrom 方法
├── 修改 HNSWIndex 支持双阶段模式
├── 基准测试验证 50K 性能
└── 预期: 构建 18min → 60s

Phase 1.5: 方案 E — Pebble 参数调优
├── BlockSize 32KB → 4KB
├── 禁用向量数据压缩
├── 优化应用层 LRU cache 大小
└── 预期: 持久化/加载 2-3x 提升

Phase 2 (未来): 方案 B — mmap 存储（如需 500K+ 规模）
├── 固定大小向量文件 + mmap
├── 替换 Pebble 作为向量存储层
├── 保留 Pebble 存 metadata/mappings
└── 预期: 内存降至 OS page cache 管理
```

---

## 7. 关键风险与缓解

| 风险 | 影响 | 缓解 |
|------|------|------|
| 方案 A 内存不足 (500K x 768d = 1.75GB) | 无法在小内存机器运行 | 设置内存上限，超限时回退到 Pebble 直接构建 |
| 构建中宕机丢失数据 | 需要重新构建全部索引 | 每 N 条 checkpoint 一次到 Pebble |
| MemStore → Pebble dump 期间阻塞搜索 | 搜索不可用 | dump 在后台执行，搜索仍走 MemStore |
| 冷启动加载时间随数据增长 | 大数据集启动慢 | 并行加载 + 进度反馈；考虑 mmap 作为长期方案 |
