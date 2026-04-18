# HAY-007 Phase 2：写入路径 — 详细执行计划

> 日期：2026-04-18
> 基于：`docs/design/HAY-007-mmap-store.md` v2 + `docs/tasks/HAY-007-task-breakdown.md`
> 前置：Phase 1 已合入 main（mmap 封装、文件格式、Open/Close、只读路径、导出）

---

## Task 2.1：WAL 实现（append / CRC32 / LSN / replay）

**目标**：实现 append-only WAL，支持 5 种记录类型、CRC32 校验、LSN 单调递增、顺序 replay。

**文件**：`internal/core/vectorindex/mmap_wal.go`（新建，~250 行）

**步骤**：

1. **定义 WAL 常量与记录类型**（~20 行）
   - `WalRecordType` 枚举：`WalInsert=1, WalDelete=2, WalSetNeighbors=3, WalSetEntry=4, WalSetNorm=5`
   - WAL record 磁盘布局：`LSN(8) + Length(4) + Type(1) + Payload(var) + CRC32(4)`

2. **WAL 结构体**（~30 行）
   ```go
   type WAL struct {
       file   *os.File
       lsn    uint64
       mu     sync.Mutex
       buf    *bufio.Writer  // buffered writes for batch mode
   }
   ```
   - `OpenWAL(path string) (*WAL, error)` — 打开/创建 wal.bin，扫描确定当前 LSN
   - `Close() error` — flush + close

3. **Append 方法**（~50 行）
   - `Append(typ WalRecordType, payload []byte) (uint64, error)` — 写 header + payload + CRC32，返回 LSN
   - CRC32 范围覆盖 `LSN + Length + Type + Payload`（与设计文档一致）
   - 非 batch 模式下 Append 后立即 fsync；batch 模式下只写 buffer

4. **Flush / Sync 方法**（~20 行）
   - `Flush() error` — flush buffer
   - `Sync() error` — flush + fsync

5. **Payload 编码辅助**（~60 行）
   - `EncodeInsert(nodeId uint64, level int, vec []float32, norm float32, docId string) []byte`
   - `EncodeDelete(nodeId uint64, docId string) []byte`
   - `EncodeSetNeighbors(nodeId uint64, layer int, neighbors []uint64) []byte`
   - `EncodeSetEntry(entryId uint64, maxLevel int) []byte`
   - `EncodeSetNorm(nodeId uint64, norm float32) []byte`
   - 对应的 `Decode*` 函数

6. **Replay 方法**（~70 行）
   - `Replay(afterLSN uint64, fn func(lsn uint64, typ WalRecordType, payload []byte) error) error`
   - 顺序扫描 wal.bin，跳过 `lsn <= afterLSN`
   - CRC32 校验：不匹配或不完整记录 → 丢弃（截断到最后一条有效记录）
   - replay 完成后，根据已扫描记录恢复 meta 字段：
     - `NodeCount`：统计 INSERT 记录数
     - `NextNodeId`：max(已见 nodeId) + 1，避免重启后 ID 冲突
     - `TotalSlots`：max(已见 nodeId) + 1
     - `EntryPoint` / `EntryLevel`：从最后一条 SET_ENTRY 记录恢复
     - `MaxLevel`：从 INSERT 记录的 level 字段取最大值
     - `GraphUpperNextSlot`：从 INSERT level>0 记录推算
   - **注意**：docToNode / nodeToDoc 映射由 idmap.dat 加载恢复，WAL replay 不负责映射恢复

**预估行数**：~250 行

**验证方式**：
- 单元测试 `mmap_wal_test.go`（~150 行）：
  - 写 N 条各类型记录 → Replay → LSN 连续、CRC 正确、payload 解码一致
  - 截断文件模拟不完整写入 → Replay 丢弃最后一条 → 前 N-1 条完整
  - 篡改中间记录的 1 字节 → CRC 校验失败 → Replay 在该记录处停止
  - `afterLSN` 过滤：Replay(afterLSN=5) 只回放 LSN>5 的记录

---

## Task 2.2：写方法实现（PutNode / SetNeighbors / SetNorm / SetNodeMapping / SetEntryPoint）

**目标**：实现 `NodeStore` 接口的所有写方法，每个写操作先 WAL 再写 mmap。

**文件**：`internal/core/vectorindex/mmap_store_write.go`（新建，~220 行）

**步骤**：

1. **MmapStore 集成 WAL 字段**（修改 `mmap_store.go`，~15 行）
   - 在 `MmapStore` struct 添加 `wal *WAL`、`batchMode bool`、`batchDepth int`
   - `OpenMmapStore` 中 `OpenWAL(dir)` + 加载 idmap.dat 恢复 docToNode/nodeToDoc（顺序读取所有 entry，CRC 校验，填充内存 map）+ 调用 replay 恢复状态
   - `Close()` 完整步骤序列（顺序严格）：
     1. msync 所有 mmap 区域（确保数据页落盘）
     2. `wal.Sync()`（flush + fsync WAL）
     3. `writeMetaHeader()`（写入最终 meta，含 WalCheckpointLSN）
     4. `wal.Close()`
     5. munmap 所有映射
     6. close 所有文件

2. **PutNode(id uint64, level int, vec []float32) error**（~50 行）
   - 计算 norm = L2Norm(vec)
   - WAL Append(INSERT, EncodeInsert(id, level, vec, norm, docId))
   - **注意**：docId 必须由调用方传入（不能为空），因 WAL replay 需要 docId 恢复 meta 中的 NodeCount 等字段。映射恢复虽依赖 idmap.dat，但 INSERT 记录的完整性要求包含 docId。PutNode 签名需增加 docId 参数：`PutNode(id uint64, level int, vec []float32, docId string) error`
   - 如果 `id >= vecCapacity` → 调用 growFile（Task 2.4）
   - 写 `vectors.dat[pageSize + id*slotSize]`：逐 float32 LittleEndian 写入
   - 写 `nodes.dat[pageSize + id*nodeSlotSize]`：Level=level, Flags=0, Norm=norm
   - 如果 `level > 0`：分配 upper slot（`meta.GraphUpperNextSlot++`），写 `nodes.dat` UpperSlot 字段
   - 更新 `meta.TotalSlots`（如果 id >= TotalSlots）、`meta.NodeCount++`
   - 非 batch 模式 → msync

3. **SetNeighbors(id uint64, layer int, neighbors []uint64) error**（~50 行）
   - WAL Append(SET_NEIGHBORS, EncodeSetNeighbors(id, layer, neighbors))
   - layer==0：写 `graphL0[pageSize + id*l0SlotSize]`：count(uint32) + neighbors([]uint64)
   - layer>0：读 nodes.dat UpperSlot → 写 `graphUpper[pageSize + slot*upperSlotSz + (layer-1)*layerSize]`
   - 非 batch 模式 → msync

4. **SetNorm(id uint64, norm float32) error**（~20 行）
   - WAL Append(SET_NORM, EncodeSetNorm(id, norm))
   - 写 `nodes.dat[pageSize + id*nodeSlotSize + 4]`：Norm(float32)
   - 非 batch 模式 → msync

5. **SetEntryPoint(id uint64, maxLayer int) error**（~15 行）
   - WAL Append(SET_ENTRY, EncodeSetEntry(id, maxLayer))
   - 更新 `s.meta.EntryPoint = id`、`s.meta.EntryLevel = maxLayer`、`s.meta.MaxLevel = max(meta.MaxLevel, maxLayer)`

6. **SetNodeMapping(docId string, nodeId uint64) error**（~40 行）
   - 更新内存 map：`s.docToNode[docId] = nodeId`、`s.nodeToDoc[nodeId] = docId`
   - 追加写 `idmap.dat`：`NodeId(8) + DocIdLen(2) + DocId(var) + CRC32(4)`
   - CRC32 覆盖整条 entry（NodeId + DocIdLen + DocId）
   - 需要 `s.idmapFile *os.File` 字段（在 Open 时打开）

7. **DeleteNodeMapping(docId string) error**（~15 行）
   - 从内存 map 删除对应条目
   - idmap.dat 暂不写删除标记（Phase 3 compact 时清理）

8. **msync 辅助**（~15 行）
   - `syncRegion(data []byte) error` — 仅在非 batch 模式下调用 `mmapSync`
   - 需要在 mmap.go / mmap_unix.go / mmap_windows.go 中增加 `mmapSync` 函数

**文件变更明细**：
| 文件 | 变更 | 行数 |
|------|------|------|
| `mmap_store_write.go` | 新建，所有写方法 | ~200 |
| `mmap_store.go` | 添加 WAL/batch/idmap 字段，修改 Open/Close | ~20 |
| `mmap.go` | 添加 `mmapSync` 签名 | ~3 |
| `mmap_unix.go` | 添加 `mmapSync` 实现（`syscall.Msync`） | ~10 |
| `mmap_windows.go` | 添加 `mmapSync` 实现（`FlushViewOfFile`） | ~15 |

**预估行数**：~250 行（含 msync 跨平台）

**验证方式**：
- 单元测试 `mmap_store_write_test.go`（~200 行）：
  - PutNode → GetVector 返回写入值
  - PutNode → GetNorm 返回正确 norm
  - PutNode(level=2) → GetNodeLevel 返回 2，upper slot 已分配
  - SetNeighbors(id, 0, nbs) → GetNeighbors(id, 0) 返回一致
  - SetNeighbors(id, 2, nbs) → GetNeighbors(id, 2) 返回一致（需先 PutNode level>=2）
  - SetNorm → GetNorm 返回新值
  - SetEntryPoint → GetEntryPoint 返回一致
  - SetNodeMapping("doc1", 42) → GetNodeId("doc1") 返回 (42, true)
  - SetNodeMapping → Close → Open → GetNodeId 返回正确（idmap.dat 持久化验证）
  - DeleteNodeMapping → GetNodeId 返回 (_, false)

---

## Task 2.3：BatchableStore 接口实现

**目标**：实现 `BeginBatch / CommitBatch / DiscardBatch / BatchDepth`，batch 模式下延迟 sync。

**文件**：`mmap_store_write.go` 追加（~60 行）

**步骤**：

1. **BeginBatch()**（~5 行）
   - `s.batchDepth++`、`s.batchMode = true`

2. **CommitBatch(sync bool) error**（~30 行）
   - `s.batchDepth--`
   - 如果 `batchDepth == 0`：
     - `s.batchMode = false`
     - `s.wal.Flush()` — flush WAL buffer
     - 如果 `sync == true`：`s.wal.Sync()` + msync 所有 mmap 区域
   - 返回 error

3. **DiscardBatch()**（~10 行）
   - `s.batchDepth = 0`、`s.batchMode = false`
   - 注意：mmap 写入无法回滚（已写入页面），但 WAL 可以截断——初版不做 WAL 回滚，与设计文档一致

4. **BatchDepth() int**（~3 行）

5. **syncAll() error**（~15 行）
   - msync vectors + nodes + graphL0 + graphUpper

**预估行数**：~60 行

**验证方式**：
- 单元测试（追加到 `mmap_store_write_test.go`，~60 行）：
  - BeginBatch → 多次 PutNode/SetNeighbors → CommitBatch → 数据可读
  - 验证 batch 模式下 WAL 只在 CommitBatch 时 fsync（可通过 mock 或计数器验证）
  - BatchDepth 嵌套：BeginBatch×2 → CommitBatch → depth=1 → CommitBatch → depth=0
  - DiscardBatch 重置 depth 为 0

---

## Task 2.4：文件增长（grow）+ 并发安全

**目标**：当写入超过当前 capacity 时自动 2x 扩展文件，保证并发读取安全。

**文件**：`internal/core/vectorindex/mmap_store_grow.go`（新建，~150 行）

**步骤**：

1. **通用 growFile 方法**（~60 行）
   ```go
   func (s *MmapStore) growFile(which fileType, requiredCap uint64) error
   ```
   - `fileType` 枚举：`fileVectors, fileNodes, fileGraphL0, fileGraphUpper`
   - 计算 newCap = 当前 cap × 2（循环直到 >= requiredCap）
   - 获取对应文件的写锁（muVec / muNodes / muGraph）
   - munmap 旧映射
   - ftruncate 扩展文件到 `pageSize + newCap * slotSize`
   - 更新文件 header 中的 Capacity 字段
   - 重新 mmap（mmapAlloc，读写模式）
   - 更新 MmapStore 中的 slice 引用和 capacity 字段
   - 释放写锁

2. **按文件类型分发**（~40 行）
   - `growVectors(requiredCap)` — 锁 muVec
   - `growNodes(requiredCap)` — 锁 muNodes
   - `growGraphL0(requiredCap)` — 锁 muGraph
   - `growGraphUpper(requiredCap)` — 锁 muGraph
   - 各函数调用通用 growFile 逻辑，传入对应的 file/data/cap 指针

3. **ensureCapacity 辅助**（~20 行）
   - `ensureVecCapacity(id uint64) error` — if id >= cap → growVectors(id+1)
   - `ensureNodeCapacity(id uint64) error` — 同上
   - `ensureL0Capacity(id uint64) error` — 同上
   - PutNode 调用这些方法确保 slot 存在

4. **并发安全模型**（~30 行）
   - 读操作已持 RLock（Phase 1 实现），grow 需要 Lock → 自然等待所有 reader 完成
   - grow 完成后释放 Lock，新 reader 获取 RLock 看到新的 mmap slice
   - 注意：grow 期间同一文件的读操作阻塞，但不同文件的读操作不受影响

**预估行数**：~150 行

**验证方式**：
- 单元测试 `mmap_store_grow_test.go`（~120 行）：
  - 初始 cap=1024 → 插入 id=1024 → 触发 grow → cap=2048 → GetVector(1024) 正确
  - 插入 id=5000 → 多次 grow → 最终 cap >= 5001
  - 验证 grow 后旧数据（id=0..1023）仍可正确读取
  - 并发测试：10 goroutine 循环 GetVector + 1 goroutine 连续 PutNode 超 capacity × 3 → 零 panic，所有读写正确
  - grow graphUpper：PutNode(level=3) 当 upper slot 满时 → auto grow → 正确

---

## Task 2.5：NextNodeId（简单自增）

**目标**：实现 `NextNodeId()` 返回单调递增的 node ID，Phase 3 才加 freelist。

**文件**：`mmap_store_write.go` 追加（~15 行）

**步骤**：

1. **NextNodeId() (uint64, error)**（~10 行）
   - `id := s.meta.NextNodeId`
   - `s.meta.NextNodeId++`
   - 返回 id
   - 注意：无需锁保护——HNSW Insert 持 h.mu 互斥锁，NextNodeId 只在 Insert 中调用

2. **Open 时恢复**（~5 行）
   - `s.meta.NextNodeId` 从 meta.bin 读取（已在 Phase 1 Open 中实现）

**预估行数**：~15 行

**验证方式**：
- 单元测试（追加到 `mmap_store_write_test.go`，~20 行）：
  - 新 store → NextNodeId() 连续返回 0, 1, 2, ...
  - Close → Open → NextNodeId() 从上次的 next 值继续

---

## Task 2.6：50K Insert 集成 benchmark

**目标**：验证 50K×128d 仅 level-0 路径的 Insert 性能 < 90s。

**文件**：`internal/core/vectorindex/mmap_store_bench_test.go`（新建，~100 行）

**步骤**：

1. **Benchmark 搭建**（~40 行）
   - 创建临时目录 → OpenMmapStore(dim=128, M=16)
   - 预生成 50K 随机 float32[128] 向量

2. **Level-0 only Insert 循环**（~30 行）
   - BeginBatch
   - 循环 50K 次：
     - NextNodeId
     - PutNode(id, level=0, vec)
     - SetNodeMapping(fmt.Sprintf("doc-%d", id), id)
     - SetNeighbors(id, 0, randomNeighbors) — 模拟 HNSW 连接（随机 M*2 个已有节点）
   - CommitBatch(true)
   - 记录耗时

3. **输出与断言**（~30 行）
   - `b.ReportMetric(elapsed.Seconds(), "total_sec")`
   - `b.ReportMetric(float64(50000)/elapsed.Seconds(), "inserts/sec")`
   - 验证：随机抽样 100 个节点 GetVector 正确
   - 验证：GetNodeId 往返正确
   - **注意**：此 benchmark 不含 HNSW 搜索邻居的 GetVectorRef 调用，仅测写入路径吞吐。真实 E2E 性能在 Phase 5 Task 5.3 验证

**预估行数**：~100 行

**验证方式**：
- `go test -bench BenchmarkMmapStore50KInsert -benchtime 1x -timeout 180s`
- 目标：< 90s（仅 level-0 写入，无搜索开销，预期远低于 90s）
- 输出 inserts/sec 指标供后续对比

---

## 执行顺序与依赖关系

```
2.1 WAL ──────────┐
                   ├──→ 2.2 写方法 ──→ 2.3 Batch ──→ 2.6 Benchmark
2.4 grow ─────────┘         │
                            │
2.5 NextNodeId ─────────────┘
```

- **2.1 WAL** 和 **2.4 grow** 可并行开发（无依赖）
- **2.2 写方法** 依赖 2.1（WAL）和 2.4（grow），是核心集成点
- **2.5 NextNodeId** 逻辑独立但被 2.2 调用，可与 2.2 同步实现
- **2.3 Batch** 依赖 2.2（需要写方法存在才能测试 batch 效果）
- **2.6 Benchmark** 依赖 2.2 + 2.3 + 2.5 全部完成

---

## 总变更汇总

| 文件 | 操作 | 预估行数 |
|------|------|----------|
| `mmap_wal.go` | 新建 | ~250 |
| `mmap_store_write.go` | 新建 | ~275（写方法 + Batch + NextNodeId） |
| `mmap_store_grow.go` | 新建 | ~150 |
| `mmap_store.go` | 修改（添加字段、修改 Open/Close） | ~25 |
| `mmap.go` | 修改（添加 mmapSync 签名） | ~3 |
| `mmap_unix.go` | 修改（添加 mmapSync） | ~10 |
| `mmap_windows.go` | 修改（添加 mmapSync） | ~15 |
| `mmap_wal_test.go` | 新建 | ~150 |
| `mmap_store_write_test.go` | 新建 | ~280 |
| `mmap_store_grow_test.go` | 新建 | ~120 |
| `mmap_store_bench_test.go` | 新建 | ~100 |
| **总计（生产代码）** | | **~728** |
| **总计（测试代码）** | | **~650** |
