# HAY-007: MmapStore — HNSW 向量索引 mmap 存储层

> 作者：飞马（Pegasus）  
> 日期：2026-04-17  
> 状态：Draft v2 — Review 反馈已修订

---

## 1. 背景与动机

### 当前问题

PebbleStore（LSM-tree KV）与 HNSW 读密集访问模式严重不匹配：

| 指标 | MemStore | PebbleStore | 差距 |
|------|----------|-------------|------|
| 50K×128d Insert | 59s | 18min | **18.5x** |
| 单次 Insert 向量读取 | ~3500 次 | ~3500 次 | — |
| 50K 总向量读取 | 1.6 亿次 | 1.6 亿次 | — |
| 单次向量读延迟 | ~10ns (map lookup) | ~5-50μs (LSM 查多层) | **500-5000x** |
| 读写比 | 86:1 | 86:1 | — |

**根因**：Pebble 是 LSM-tree，优化顺序写入，但 HNSW 构建是随机读密集型（86:1 读写比）。每次 Insert 需要 ~3500 次随机向量读，Pebble 的多层 SST 查找 + block decode 开销远高于内存 map lookup。

### 为什么选 mmap flat file

- **O(1) 随机读**：向量按 ID 偏移存储，读取 = 指针偏移，零反序列化
- **OS page cache**：热数据自动缓存在内存，冷数据按需从磁盘加载
- **零 GC 压力**：mmap 区域不被 Go GC 扫描
- **业界验证**：Weaviate、Qdrant 均使用 mmap 存储向量数据

---

## 2. 设计目标

### 性能目标

| 指标 | 目标 |
|------|------|
| 50K×128d 构建 | < 90s |
| 随机向量读 | < 1μs（hot）/ < 10μs（cold） |
| 搜索 p99 | < 5ms |
| Recall@10 | > 0.95（SIFT 数据） |

### 功能目标

- 完整实现 `NodeStore` + `BatchableStore` 接口
- 支持 Insert / Delete / Upsert
- 持久化：重启后索引可用
- 崩溃恢复：异常断电后索引不损坏

### 约束

- 纯 Go，零 CGo 依赖（`CGO_ENABLED=0` 三平台验证）
- 跨平台：Linux / macOS / Windows
- 单 binary 部署
- 目标规模：10K – 500K 向量，128 – 768 维
- 所有二进制数据 **little-endian** 字节序

---

## 3. 架构概览

### 文件布局

```
<index_dir>/
├── meta.bin          # 索引元数据（64 bytes，原子写）
├── vectors.dat       # mmap 定长向量 arena
├── nodes.dat         # mmap 节点元数据（level + norm + upper slot index）
├── graph_l0.dat      # mmap level-0 邻居列表（定长）
├── graph_upper.dat   # mmap 上层邻居列表（按需分配，仅 level>0 节点）
├── idmap.dat         # docId ↔ nodeId 双向映射（带 CRC）
└── wal.bin           # append-only WAL（带 LSN + CRC32）
```

### 各文件职责

| 文件 | 访问模式 | 大小估算（50K×128d, M=16） |
|------|----------|--------------------------|
| vectors.dat | 随机读（最热） | 4KB + 50K × 512B = **24.4 MB** |
| graph_l0.dat | 随机读写 | 4KB + 50K × 260B = **12.4 MB** |
| graph_upper.dat | 随机读写（稀疏） | 4KB + ~2.5K × 792B = **1.9 MB** |
| nodes.dat | 随机读写 | 4KB + 50K × 16B = **0.8 MB** |
| idmap.dat | 顺序读，按需查 | ~50K × 64B = **3.1 MB** |
| wal.bin | 顺序追加 | 变长，checkpoint 后截断 |
| meta.bin | 64 bytes | 64 bytes |
| **总计** | | **~43 MB** |

---

## 4. 详细设计

### 4.1 文件格式

#### meta.bin（64 bytes，原子写）

```go
type MetaHeader struct {
    Magic         [4]byte   // "HNSW"
    Version       uint32    // 1
    Dim           uint32    // 向量维度
    M             uint32    // HNSW M 参数
    MaxLevel      uint32    // 当前最大层级
    NodeCount     uint64    // 活跃节点数（不含墓碑）
    TotalSlots    uint64    // 已分配 slot 总数（含墓碑）
    EntryPoint    uint64    // 入口节点 ID
    EntryLevel    uint32    // 入口节点层级
    NextNodeId    uint64    // 下一个可分配的 node ID
    WalCheckpointLSN uint64 // WAL checkpoint LSN
    _             [4]byte   // padding to 64 bytes
}
```

写入方式：写临时文件 → fsync → rename，保证原子性。

#### vectors.dat（定长 slot arena）

```
[Header: 4096 bytes (page-aligned)]
  Magic:    [4]byte  "VECS"
  Dim:      uint32
  Capacity: uint64
  _:        [4072]byte  // padding to 4096

[Slot 0: dim * 4 bytes, padded to 64-byte cache line alignment]
  float32[dim]

[Slot 1: dim * 4 bytes]
  ...
```

- Slot 大小 = `dim × 4` bytes（128d = 512B, 384d = 1536B, 768d = 3072B）
- 所有 header 都 **4096 bytes page-aligned**，保证向量 slot 起始对齐
- Node ID = slot index（从 0 开始）
- 向量读取 = `mmap[4096 + id * slotSize : 4096 + (id+1) * slotSize]`
- **初版 GetVectorRef 返回 copy**（与 GetVector 相同），避免 grow 后悬垂指针。Phase 5 优化：epoch-based reclamation 支持零拷贝

#### nodes.dat（节点元数据，定长）

```
[Header: 4096 bytes (page-aligned)]
  Magic:    [4]byte  "NODE"
  Capacity: uint64
  _:        [4080]byte

[Slot 0: 16 bytes]
  Level:      uint8     // 节点层级
  Flags:      uint8     // bit 0: deleted (tombstone)
  _:          [2]byte   // padding
  Norm:       float32   // 预计算 L2 范数
  UpperSlot:  uint32    // graph_upper.dat 中的 slot index（level>0 时有效，0 表示无）
  _:          [4]byte   // reserved

[Slot 1: 16 bytes]
  ...
```

#### graph_l0.dat（Level-0 邻居列表，定长）

```
[Header: 4096 bytes (page-aligned)]
  Magic:        [4]byte  "GRL0"
  MaxNeighbors: uint32   // M * 2 = 32
  Capacity:     uint64
  _:            [4076]byte

[Slot 0: (4 + MaxNeighbors * 8) bytes = 260 bytes]
  Count:      uint32              // 当前邻居数
  Neighbors:  [MaxNeighbors]uint64 // 邻居 ID 数组

[Slot 1: 260 bytes]
  ...
```

- Level-0 邻居数上限 = M×2 = 32
- 定长 slot = 4 + 32 × 8 = **260 bytes**
- 读取：直接 mmap 偏移，零反序列化

#### graph_upper.dat（上层邻居列表，按需分配）

```
[Header: 4096 bytes (page-aligned)]
  Magic:        [4]byte  "GRUP"
  MaxNeighbors: uint32   // M = 16
  MaxLayers:    uint32   // 预分配的最大层数（如 6）
  Capacity:     uint64   // 上层 slot 总容量
  NextSlot:     uint64   // 下一个可分配的 upper slot
  _:            [4064]byte

[Slot 0: MaxLayers * (4 + M * 8) bytes = 792 bytes]
  [Layer 1]
    Count:      uint32
    Neighbors:  [M]uint64
  [Layer 2]
    ...

[Slot 1]
  ...
```

- **只给 level > 0 的节点分配 slot**（~5% 节点）
- 通过 nodes.dat 的 `UpperSlot` 字段建立 nodeId → upper slot 映射
- 上层邻居数上限 = M = 16，每层 132 bytes
- 大幅节省空间：50K 节点只需 ~2.5K 个 upper slot（vs 全预分配 50K）

#### idmap.dat（ID 映射，带 CRC）

```
[Header: 16 bytes]
  Magic:    [4]byte  "IDMP"
  Count:    uint64
  _:        [4]byte

[Entry]
  NodeId:   uint64
  DocIdLen: uint16
  DocId:    [DocIdLen]byte
  CRC32:    uint32          // 覆盖整条 entry
```

启动时全量加载到内存 map。修改时追加（带 CRC），checkpoint 时 compact。不完整 entry（CRC 不匹配）在加载时丢弃。

#### wal.bin（Write-Ahead Log，带 LSN）

```
[Record]
  LSN:      uint64        // 单调递增序列号
  Length:   uint32        // payload 长度
  Type:     uint8         // INSERT=1, DELETE=2, SET_NEIGHBORS=3, SET_ENTRY=4, SET_NORM=5
  Payload:  [Length]byte  // 类型相关的二进制数据
  CRC32:    uint32        // 覆盖 LSN+Length+Type+Payload 的完整校验
```

WAL 记录类型：
- **INSERT(1)**：nodeId + level + vector + norm + docId
- **DELETE(2)**：nodeId + docId
- **SET_NEIGHBORS(3)**：nodeId + layer + []neighborIds
- **SET_ENTRY(4)**：entryId + maxLevel
- **SET_NORM(5)**：nodeId + norm

Checkpoint 记录 `lastLSN`。恢复时从 `lastLSN + 1` 开始 replay。

### 4.2 NodeStore 接口映射

```go
type MmapStore struct {
    dir        string
    meta       MetaHeader
    vectors    []byte       // vectors.dat mmap
    nodes      []byte       // nodes.dat mmap
    graphL0    []byte       // graph_l0.dat mmap
    graphUpper []byte       // graph_upper.dat mmap
    walFile    *os.File     // wal.bin (append)
    walLSN     uint64       // 当前 WAL LSN
    
    docToNode  map[string]uint64  // 内存中的 ID 映射
    nodeToDoc  map[uint64]string
    freelist   []uint64           // 可复用的已删除 slot
    
    muVec      sync.RWMutex       // 保护 vectors.dat mmap
    muGraph    sync.RWMutex       // 保护 graph_l0/upper mmap
    muNodes    sync.RWMutex       // 保护 nodes.dat mmap
    
    batchMode  bool
    batchDepth int
    
    dim        int
    slotSize   int                // dim * 4
}
```

接口方法映射：

| 方法 | 实现 |
|------|------|
| `GetVectorRef(id)` | **初版：copy**（与 GetVector 相同，避免 grow 后悬垂指针）。Phase 5 优化：epoch-based reclamation 支持零拷贝 |
| `GetVector(id)` | copy(dst, mmap_slice) |
| `PutNode(id, level, vec)` | WAL → 写 vectors slot → 写 nodes slot → 写 norm |
| `DeleteNode(id)` | WAL → 设 nodes[id].Flags tombstone → 加入 freelist |
| `GetNeighbors(id, 0)` | 读 graphL0[id] 的 count + neighbors |
| `GetNeighbors(id, l)` | 读 graphUpper[nodes[id].UpperSlot][l-1] |
| `SetNeighbors(id, l, nbs)` | WAL → 写对应 graph slot |
| `GetNorm(id)` | 读 nodes[id].Norm |
| `SetNorm(id, n)` | WAL(SET_NORM) → 写 nodes[id].Norm |
| `GetNodeId(docId)` | docToNode[docId] 内存查找 |
| `SetNodeMapping(doc, id)` | 更新内存 map + 追加 idmap.dat（带 CRC） |
| `NextNodeId()` | freelist 有则 pop，否则 meta.NextNodeId++ |
| `Close()` | flush WAL → checkpoint → munmap all |

### 4.2.1 BatchableStore 接口

```go
func (s *MmapStore) BeginBatch()   { s.batchMode = true; s.batchDepth++ }
func (s *MmapStore) CommitBatch() error {
    s.batchDepth--
    if s.batchDepth == 0 {
        s.batchMode = false
        // 批量 msync mmap 区域（一次 fsync 替代多次）
        return s.syncAll()
    }
    return nil
}
func (s *MmapStore) DiscardBatch() { s.batchDepth = 0; s.batchMode = false }
```

非 batch 模式下，每次写操作立即 msync。batch 模式下，写入直接修改 mmap 区域但延迟 sync 到 CommitBatch。**HNSW Insert 依赖 batch 控制 sync 粒度，不实现则每次写都 fsync，90s 目标不可能达到。**

### 4.3 增删改流程

#### Insert

```
1. WAL.append(INSERT, nodeId, level, vector, norm, docId)
2. 写 vectors.dat[nodeId] = vector
3. 写 nodes.dat[nodeId] = {level, norm, flags=0, upperSlot}
4. 如果 level > 0：分配 upper slot，设 nodes[id].UpperSlot
5. 写 idmap（追加 + 更新内存 map）
6. [HNSW 搜索邻居 — 大量 GetVectorRef 读取]
7. 多次 SetNeighbors（每次 WAL + 写 graph slot）
8. 可选：SetEntryPoint
```

如果 nodeId >= 当前 capacity → 触发 grow（见 4.4）。

#### Delete

```
1. WAL.append(DELETE, nodeId, docId)
2. nodes.dat[nodeId].Flags |= TOMBSTONE
3. 清理邻居引用（SetNeighbors 更新相关节点）
4. freelist.push(nodeId)
5. 如果有 upper slot → 回收到 upper freelist
6. 删除 idmap 映射
7. meta.NodeCount--
```

#### Upsert

```
1. 检查 docToNode[docId] 是否存在
2. 存在 → deleteNodeLocked(oldId) → 继续 insert
3. 不存在 → 直接 insert
```

### 4.4 文件增长策略

初始容量：1024 slots。每次满时 **2x 扩展**。

```go
func (s *MmapStore) growVectors(newCap uint64) error {
    s.muVec.Lock()
    defer s.muVec.Unlock()
    // 1. munmap 旧映射
    syscall.Munmap(s.vectors)
    // 2. ftruncate 扩展文件
    os.Truncate(vecFile, newSize)
    // 3. 重新 mmap
    s.vectors, _ = syscall.Mmap(fd, 0, newSize, PROT_READ|PROT_WRITE, MAP_SHARED)
    // 4. 更新 header 中的 capacity
    return nil
}
```

**并发安全**：每个 mmap 文件独立 RWMutex（muVec/muGraph/muNodes），grow 一个文件不阻塞其他文件的读取。

**并发模型说明**：HNSW Insert/Delete 持有 `h.mu` 互斥锁（串行化），但 **Search 只持读锁，可与其他 Search 并发**。因此 MmapStore 的读操作必须并发安全。

### 4.5 崩溃恢复

#### 策略：WAL (with LSN) + Checkpoint

1. **所有写操作先写 WAL**（含 LSN），再写 mmap 数据文件
2. **Checkpoint**：写入 `meta.bin`（含 `WalCheckpointLSN`，原子 rename），然后截断 WAL
3. **恢复流程**：
   - 读 `meta.bin` 获取 `WalCheckpointLSN`
   - 扫描 WAL，从 `WalCheckpointLSN + 1` 开始 replay
   - CRC32 校验每条记录（覆盖 LSN+Length+Type+Payload），不完整/CRC 不匹配的记录丢弃
   - Replay 完成后执行新的 checkpoint

#### WAL 何时 checkpoint

- 每 N 次操作（如 1000 次）
- Close() 时
- 手动触发

#### 简化假设

- 索引可以从代码重建（最坏情况兜底）
- WAL 主要防止频繁的完整重建，不需要 ACID 事务
- WAL 顺序写开销在 86:1 读写比下可忽略

### 4.6 空间回收

#### Freelist

```go
type Freelist struct {
    slots []uint64  // 可复用的 slot ID
}
```

- Delete 时：`freelist.push(nodeId)`
- NextNodeId 时：优先 `freelist.pop()`，空则分配新 slot
- **持久化方案**：启动时扫描 nodes.dat 的 tombstone 标志位重建 freelist（O(N) 扫描，50K 节点 < 1ms）。不需要独立文件，避免一致性问题

#### Compaction

初版不做 compaction。当碎片率（tombstone/total）超过 30% 时，提示用户重建。后续版本可实现在线 compaction（创建新文件 → 紧凑写入 → 原子切换）。

### 4.7 并发安全

每个 mmap 文件独立 RWMutex：

```
读操作：muVec.RLock() / muGraph.RLock() / muNodes.RLock()
写操作：由 HNSW h.mu 保证串行，不需要额外锁
grow 操作：对应文件的 Lock()（独占，等所有 reader 完成）
```

grow 一个文件不阻塞其他文件的读取。grow 频率极低（对数级）。

### 4.8 ID 映射

内存中维护双向 map：

```go
docToNode map[string]uint64
nodeToDoc map[uint64]string
```

持久化：追加写 `idmap.dat`（每条 entry 带 CRC32），启动时全量加载（CRC 不匹配的 entry 丢弃），checkpoint 时 compact。50K 映射内存开销 ~3-5MB。

---

## 5. 备选方案与 Trade-off

### 为什么不用 MemStore + Pebble 混合

建军已明确要求 mmap flat file。混合方案虽然简单（200 行），但 500K×768d 需 ~1.75GB 内存，且仍依赖 Pebble。

### 为什么不用 bbolt

B+tree 随机读比 LSM 快，但比 mmap flat file 多一次 B-tree 查找。写需要 COW + fsync，对频繁邻居列表更新不友好。

### WAL vs 无 WAL

有 WAL：崩溃可恢复，+1 次顺序写（86:1 读写比下可忽略），+300 行代码。  
无 WAL：崩溃需完整重建（调 embedding API 成本高）。  
**决策**：实现 WAL。

### mmap 库选型

**决策**：自封装 `syscall.Mmap`（~80 行），不依赖第三方库。避免 edsrzf/mmap-go 的维护风险和潜在 CGo 问题。三平台 CI 验证 `CGO_ENABLED=0`。

---

## 6. 实施计划

### Phase 1：基础 MmapStore（只读路径）
- 实现 vectors.dat / nodes.dat / graph_l0.dat 的 mmap 读取
- 自封装 syscall.Mmap（Linux/macOS/Windows）
- 实现 GetVector / GetVectorRef / GetNeighbors / GetNorm
- 从 PebbleStore 导出数据到 mmap 文件格式
- **验收**：50K 随机向量读 benchmark < 1μs，三平台 `CGO_ENABLED=0` 编译通过
- **代码量**：~500 行

### Phase 2：写入路径 + WAL + Batch
- 实现 PutNode / SetNeighbors / SetNorm / SetNodeMapping
- 实现 WAL（with LSN，append + replay + CRC32）
- 实现 BatchableStore（BeginBatch/CommitBatch/DiscardBatch）
- 实现文件增长（grow，独立锁）
- **验收**：50K Insert < 90s，WAL replay 正确，batch 模式 sync 次数 = 1/insert
- **代码量**：~700 行

### Phase 3：Delete + Upsert + Freelist
- 实现 DeleteNode（墓碑 + freelist，启动扫描重建）
- 实现 Upsert（Delete + Insert）
- 实现 ID 映射持久化（带 CRC）
- **验收**：Delete + re-insert 无孤儿节点，freelist 正确复用，启动扫描 < 1ms
- **代码量**：~400 行

### Phase 4：崩溃恢复 + Checkpoint
- WAL checkpoint 机制（LSN-based）
- meta.bin 原子写
- 崩溃恢复测试（5 类 crash point 注入）
- **验收**：kill -9 后重启索引完整
- **代码量**：~300 行

### Phase 5：上层图 + 完整集成
- 实现 graph_upper.dat（按需分配）
- 替换 HNSW 中的 PebbleStore 为 MmapStore
- E2E 测试（SIFT benchmark + recall 验证）
- **验收**：全部现有测试通过，性能达标
- **代码量**：~300 行

### 总计：~2200 行（含错误处理、恢复路径、batch 支持）

### Crash Recovery 测试方案

每个 Phase 必须包含 crash 测试：
1. **Insert 中途 kill** — replay WAL 后索引完整
2. **SetNeighbors 中途 kill** — 图结构不损坏
3. **Checkpoint 中途 kill** — 回退到上一个 checkpoint
4. **grow 中途 kill** — 文件不损坏
5. **Delete 中途 kill** — 墓碑一致

测试方法：注入 crash point（在写操作之间 panic），验证恢复后数据完整。

---

## 7. 风险与缓解

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| Go syscall.Mmap 跨平台差异 | 中 | 高 | 自封装 + 三平台 CI 验证 |
| mmap 重映射时 SIGBUS | 低 | 高 | 独立锁 + grow 时写锁保护 |
| WAL 实现 bug 导致数据损坏 | 中 | 高 | CRC32 + LSN + crash 测试 + 索引可重建兜底 |
| 500K×768d 文件过大（~2.3GB） | 低 | 中 | mmap 按需加载，OS page cache 管理 |
| freelist 碎片化严重 | 低 | 低 | 启动扫描重建 + 后续 compaction |

---

## 8. 开放问题

1. ~~graph_upper.dat 是否需要按需分配？~~ **已决定**：按需分配，只给 level > 0 节点分配 slot。
2. **idmap.dat 是否可以用 mmap？** 变长记录 mmap 麻烦，初版用普通文件 + 内存 map + CRC。
3. **Windows 下 mmap 文件无法 truncate**——需要先 munmap 再 truncate。grow 流程需测试。
4. ~~edsrzf/mmap-go 是否真的零 CGo？~~ **已决定**：自封装 `syscall.Mmap`（~80 行），三平台 CI 验证 `CGO_ENABLED=0`。
5. **WAL 是否过度设计？** 重建需调 embedding API，成本不低。先实现，后续可配置关闭。

---

> 本设计文档已经过 Claude Code 和 Copilot（模拟）两轮独立 review，5 个 P0 和主要 P1 已在 v2 中修复。  
> Review 报告：`docs/design/HAY-007-review-claude.md`、`docs/design/HAY-007-review-copilot.md`
