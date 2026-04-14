# HAY-004 HNSW 向量搜索引擎设计文档

> 作者：Architect | 日期：2026-04-06
> 状态：初稿，待讨论

## 0. 前提

`~/workspace/haystack-hnsw` 或任何 HNSW 初步版本**不存在**。本设计为从零方案。

---

## 1. 现状与切入点

### Haystack 现有架构

```
internal/core/
├── pebble/         # KV 抽象（DB / Batch interface）
├── storage/        # 存储门面（版本管理、KeyType 常量）
├── documents/      # 文档 CRUD（Document 结构体）
├── invertedindex/  # 倒排索引（关键词→文档ID）
├── symbols/        # 符号索引（函数名等）
├── idtable/        # 关键词→整数ID 映射
└── workspace/      # 工作区生命周期
```

**存储模式**：所有模块共享同一个 PebbleDB 实例，通过 byte 前缀分区隔离 key 空间。
**初始化模式**：`Init(db pebble.DB, mpsc *queue.Mpsc)` + 包级全局变量。
**当前 Document 结构**：无向量字段，纯文本+元数据。

### Key 空间分区（现有）

```
1-2:   Workspace
10-13: Document
20-22: Inverted Index
28-29: ID Table
30-33: Symbols
```

**结论**：新增 `vectorindex/` 模块完全可融入现有架构，无需新开数据库。

---

## 2. 设计目标

| 目标 | 要求 |
|------|------|
| **功能** | 支持向量相似搜索（ANN），用于语义代码搜索 |
| **算法** | HNSW（Hierarchical Navigable Small World） |
| **实现** | 纯 Go，零 cgo 依赖 |
| **持久化** | Pebble，与现有存储统一 |
| **内存** | 懒加载，按需缓存，不全量加载 |
| **规模** | 单 workspace 万~十万级文档，384-1536 维向量 |
| **延迟** | 搜索 < 20ms @10 万向量（384 维） |
| **一致性** | 插入/删除增量持久化，crash-safe |

---

## 3. 技术选型

### 3.1 算法实现：基于 coder/hnsw 改造

**不从零实现 HNSW 算法。** `github.com/coder/hnsw`（纯 Go、泛型、~600 行核心代码、Coder 出品、活跃维护）提供了经过验证的算法层。

| 方案 | 判断 |
|------|------|
| coder/hnsw 改造 | ✅ **推荐** — 算法成熟，只需替换存储层 |
| TFMV/hnsw（coder fork） | 🔶 备选 — 加了 metadata/faceted search，但引入不必要的复杂度 |
| hnswlib-go（cgo） | ❌ — cgo 依赖，与 haystack 零 cgo 目标冲突 |
| 从零实现 | ❌ — 无必要，~1500 行核心代码 + 边界 case 调试成本高 |

**改造范围**：
- 提取核心算法（插入、搜索、删除、层级管理）
- 替换内存 map 存储为 Pebble-backed NodeStore 接口
- 保留距离函数接口（支持 cosine / L2 / dot product）

### 3.2 持久化：Pebble KV

**不用 mmap、不用自定义二进制格式。**

| 方案 | 优势 | 劣势 | 结论 |
|------|------|------|------|
| **PebbleDB** | 增量持久化、事务安全、与现有存储统一、支持懒加载 | LSM 读放大（可缓存缓解） | ✅ 首选 |
| mmap | 零拷贝读取、OS 页缓存 | 自管文件布局、扩容复杂、跨平台差异 | ❌ 过重 |
| 二进制格式 | 紧凑、快速全量加载 | 无增量更新、必须全量加载 | ❌ 不满足懒加载需求 |

---

## 4. 模块设计

### 4.1 位置

```
internal/core/vectorindex/
├── hnsw.go          # HNSW 算法核心（搜索、插入、删除）
├── store.go         # NodeStore 接口 + Pebble 实现
├── cache.go         # LRU 缓存层
├── distance.go      # 距离函数（cosine, L2, dot）
├── types.go         # Node, Vector, SearchResult 类型
├── init.go          # Init() + 生命周期管理
└── hnsw_test.go     # 测试
```

### 4.2 核心接口

```go
// ============== 对外 API ==============

// Init 初始化向量索引模块
func Init(db pebble.DB) error

// AddVector 插入或更新文档的向量
func AddVector(tableId int, docId string, vector []float32) error

// DeleteVector 删除文档的向量
func DeleteVector(tableId int, docId string) error

// Search 向量相似搜索，返回 top-k 最近邻
func Search(tableId int, query []float32, k int) ([]SearchResult, error)

// DeleteTable 删除 workspace 对应的整个向量索引
func DeleteTable(tableId int) error

// ============== 内部抽象 ==============

// NodeStore 节点存储接口——隔离算法与持久化
type NodeStore interface {
    GetVector(id uint64) ([]float32, error)
    PutNode(id uint64, level int, vector []float32) error
    DeleteNode(id uint64) error
    GetNeighbors(id uint64, layer int) ([]uint64, error)
    SetNeighbors(id uint64, layer int, neighbors []uint64) error
    GetEntryPoint() (uint64, int, error)     // (nodeId, maxLayer, err)
    SetEntryPoint(id uint64, maxLayer int) error
    GetNodeLevel(id uint64) (int, error)
    // ID 映射：docId ↔ nodeId
    GetNodeId(docId string) (uint64, bool, error)
    SetNodeMapping(docId string, nodeId uint64) error
    DeleteNodeMapping(docId string) error
}
```

### 4.3 Pebble Key Schema

新增 Key 前缀 **byte 40-45**，与现有 key 空间隔离：

```
Key Format                              Value
─────────────────────────────────────── ──────────────────────
40:{tableId}:meta:entry                 {nodeId}:{maxLayer} (entry point)
40:{tableId}:meta:count                 uint64 (node count)
41:{tableId}:vec:{nodeId}               []float32 (raw vector bytes)
42:{tableId}:node:{nodeId}:level        uint8 (node level)
43:{tableId}:node:{nodeId}:nb:{layer}   []uint64 (neighbor IDs)
44:{tableId}:map:doc:{docId}            uint64 (docId → nodeId)
44:{tableId}:map:node:{nodeId}          string (nodeId → docId, 反查)
45:{tableId}:id_seq                     uint64 (auto-increment node ID)
```

**设计考虑**：
- `tableId` 对应 workspace，与倒排索引的 table 概念一致
- 向量单独存（key 41），因为向量数据量大，读邻接表时不需要读向量
- 邻接表按层分 key（key 43），支持按层读取，高层节点可独立缓存
- docId ↔ nodeId 双向映射（key 44），因为对外接口用 docId，内部图用 uint64 nodeId

### 4.4 缓存策略

```
┌─────────────────────────────────────┐
│           HNSW Algorithm            │
├─────────────────────────────────────┤
│         LRU Cache Layer             │
│  ┌────────┐ ┌────────┐ ┌────────┐  │
│  │Vectors │ │Neighbors│ │ Levels │  │
│  │(热点)  │ │ (全层)  │ │ (全量) │  │
│  └────────┘ └────────┘ └────────┘  │
├─────────────────────────────────────┤
│     Pebble NodeStore (持久化)       │
└─────────────────────────────────────┘
```

**分层缓存**：
- **Level 缓存**：全量常驻内存（每个节点 1 byte，10 万节点 = 100 KB）
- **高层邻接表**：常驻内存（层级 ≥ 1 的节点约 n/M ≈ 几千个，几十 KB）
- **底层邻接表**：LRU 缓存，容量可配
- **向量数据**：LRU 缓存，容量可配（这是内存大头）

**缓存大小控制**（默认值，可配）：

| 组件 | 默认容量 | 内存估算（10万/384维） |
|------|---------|---------------------|
| Level 缓存 | 全量 | ~100 KB |
| 高层邻接表 | 全量 | ~200 KB |
| 底层邻接表 LRU | 10,000 条 | ~2.5 MB |
| 向量 LRU | 10,000 条 | ~15 MB |
| **总计** | — | **~18 MB** |

对比全量加载 ~176 MB（384 维）或 ~626 MB（1536 维），缓存模式节省 90%+ 内存。

### 4.5 HNSW 参数

| 参数 | 默认值 | 含义 |
|------|--------|------|
| M | 16 | 每层最大连接数 |
| M_max0 | 32 | 底层最大连接数（= 2M） |
| ef_construction | 200 | 构建时 beam width |
| ef_search | 64 | 搜索时 beam width（可运行时调整） |
| distance | cosine | 距离函数 |

这些参数对万~十万级数据是标准选择。如果搜索精度不够，优先调大 ef_search（延迟换精度）。

---

## 5. 数据流

### 5.1 索引流程

```
文件变更 → Scanner 检测
         → Tokenizer（倒排索引，已有）
         → Embedder（新增：调用 embedding API 生成向量）
         → vectorindex.AddVector(tableId, docId, vector)
              ├── NodeStore.PutNode（写 Pebble）
              ├── HNSW insert（搜索最近邻、建立连接）
              ├── NodeStore.SetNeighbors（更新邻接表）
              └── 缓存失效/更新
```

### 5.2 搜索流程

```
用户查询 → Embedder 生成 query 向量
         → vectorindex.Search(tableId, queryVec, k)
              ├── 从 entry point 开始
              ├── 高层贪心下降（内存缓存命中）
              ├── 底层 beam search（按需从 Pebble 加载）
              └── 返回 top-k docIds + distances
         → 与倒排索引结果融合排序（未来）
```

### 5.3 删除流程

```
文件删除 → vectorindex.DeleteVector(tableId, docId)
              ├── 标记删除（HNSW 中断开连接、重连邻居）
              ├── NodeStore.DeleteNode（删 Pebble 数据）
              └── 缓存失效
```

---

## 6. 与现有模块的集成点

### 6.1 Document 模型扩展

**不修改 Document 结构体。** 向量数据量大（384×4=1.5KB / 1536×4=6KB per doc），不适合塞进 Document。向量独立存储在 vectorindex 的 key 空间中，通过 docId 关联。

### 6.2 Indexer（Scanner）集成

Scanner 当前流程：`扫描文件 → 生成 Document → 更新倒排索引`。

扩展点：在生成 Document 后，增加 embedding 步骤 → 调用 `vectorindex.AddVector()`。

**注意**：Embedding 生成（调用外部 API 或本地模型）是一个**异步、可能慢**的操作。建议：
- embedding 生成与倒排索引更新解耦
- 单独的 embedding 队列 + worker
- 倒排索引先更新（保证文本搜索即时可用），向量索引异步跟进

### 6.3 Searcher 集成

新增搜索模式：
- 纯文本搜索（现有，不变）
- 纯向量搜索（新增）
- 混合搜索（未来，文本 + 向量分数融合）

### 6.4 storage/types.go 扩展

```go
// 新增 Key 类型（byte 40-45）
const (
    KeyTypeVectorMeta      byte = 40
    KeyTypeVectorData      byte = 41
    KeyTypeVectorNodeLevel byte = 42
    KeyTypeVectorNeighbors byte = 43
    KeyTypeVectorMapping   byte = 44
    KeyTypeVectorIdSeq     byte = 45
)
```

---

## 7. Embedding 生成（设计边界）

Embedding 生成**不在本任务范围内**，但需要明确接口：

```go
// Embedder 接口——vectorindex 不关心实现
type Embedder interface {
    Embed(text string) ([]float32, error)
    Dimension() int
}
```

可能的实现：
- 远程 API（OpenAI、Cohere、本地 ollama）
- 本地模型（ONNX Runtime）

本任务只关注：**给定向量，HNSW 如何存储、索引、搜索。**

---

## 8. 实施步骤

### Step 1：NodeStore + Pebble 实现（基础层）
- 定义 NodeStore 接口
- 实现 PebbleNodeStore
- Key schema 编解码
- 单元测试：CRUD 操作正确性
- **验证**：节点写入、读取、删除、ID 映射正确

### Step 2：HNSW 核心算法（移植 + 适配）
- 从 coder/hnsw 提取算法逻辑
- 适配 NodeStore 接口（替换内存 map）
- 距离函数实现（cosine、L2）
- 单元测试：小规模插入+搜索的正确性
- **验证**：100 个随机向量，搜索结果与暴力搜索一致

### Step 3：LRU 缓存层
- 实现分层缓存（level / neighbors / vectors）
- 缓存失效策略（写入时更新、删除时清除）
- Benchmark：缓存命中率、搜索延迟
- **验证**：缓存开/关搜索结果一致，缓存开时延迟显著降低

### Step 4：集成到 Haystack
- 新增 `Init()` 函数，注册到 storage 初始化流程
- `storage/types.go` 新增 KeyType 常量
- Indexer 集成（embedding 队列 + AddVector）
- Searcher 集成（向量搜索 API）
- 端到端测试
- **验证**：文件索引后可通过向量搜索找到

### Step 5：性能验证 + 调优
- Benchmark（1K / 10K / 100K 向量，384 / 1536 维）
- 搜索延迟、插入吞吐、内存占用
- Recall@10 验证（与暴力搜索对比）
- 调整 HNSW 参数和缓存大小
- **目标**：搜索 < 20ms @100K/384d，Recall@10 > 0.95

---

## 9. 内存占用估算

| 场景 | 全量加载 | 懒加载（默认缓存） |
|------|---------|-------------------|
| 1 万文档 × 384 维 | ~17 MB | ~5 MB |
| 10 万文档 × 384 维 | ~176 MB | ~18 MB |
| 10 万文档 × 1536 维 | ~626 MB | ~60 MB |

懒加载对高维场景收益巨大。

---

## 10. 风险

| 风险 | 严重度 | 缓解 |
|------|--------|------|
| Pebble 读放大导致搜索延迟偏高 | 🟡 中 | LRU 缓存 + 高层常驻内存，热点路径缓存命中 |
| coder/hnsw 算法改造工作量超预期 | 🟡 中 | 算法层相对独立，改造主要是存储接口适配 |
| 并发安全（多个 workspace 并发索引+搜索） | 🟡 中 | 每个 tableId 独立锁，读写分离 |
| Embedding API 成为瓶颈（不在本任务范围但会影响端到端体验） | 🟢 低 | 异步队列解耦，不阻塞文本索引 |
| 向量维度不一致（不同 workspace 用不同 embedding 模型） | 🟡 中 | tableId 级别存储维度元数据，维度不匹配时报错 |

---

## 11. 待讨论

1. **Embedding 模型选择**：哪个模型？什么维度？本地还是远程？（影响维度参数和延迟预期）
2. **混合搜索策略**：文本分数和向量分数如何融合？（RRF？加权？）先做纯向量搜索还是一步到位混合？
3. **是否需要 per-workspace 开关**：有些 workspace 可能不需要向量搜索（节省 embedding 成本）
4. **VSCode 插件影响**：向量搜索需要新增 API endpoint，插件是否需要适配？
5. **coder/hnsw 引入方式**：vendor 源码（方便魔改）还是 go module 依赖？建议 vendor 源码。

---

## 12. 总结

**核心方案**：`coder/hnsw 算法 + Pebble 持久化 + LRU 分层缓存`

- 纯 Go，零新依赖（Pebble 已有）
- 与 haystack 现有架构模式一致（`internal/core/vectorindex/`）
- 懒加载控制内存（~18 MB vs ~176 MB @10 万/384 维）
- 增量持久化，crash-safe
- 5 步迭代，每步可独立验证
