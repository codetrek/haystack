# HAY-007: MmapStore Task Breakdown

> 基于 `docs/design/HAY-007-mmap-store.md` v2 分解
> 日期：2026-04-17
> 修订：2026-04-17 — 根据 Round 1 Review 修复 C1-C3, H1-H5

---

## Phase 1：核心基础设施（只读路径 + mmap 封装）

### Task 1.1：跨平台 mmap 封装

**目标**：自封装 `syscall.Mmap/Munmap`，支持 Linux/macOS/Windows，零 CGo。

**改动文件**：
- `internal/core/vectorindex/mmap_unix.go`（~40 行，build tag `//go:build !windows`）
- `internal/core/vectorindex/mmap_windows.go`（~50 行，build tag `//go:build windows`）
- `internal/core/vectorindex/mmap.go`（~30 行，公共类型/接口）

**预估行数**：~120 行

**已知限制（H4）**：Windows 下 mmap 文件无法直接 truncate，grow 必须按 munmap→truncate→mmap 顺序执行。此为已知平台限制，不阻塞 Phase 1/2 交付；Task 2.4 grow 实现时在 Windows 路径中显式处理此顺序。

**验证方式**：
- 单元测试：创建临时文件 → mmap → 读写 → munmap → 验证内容
- `CGO_ENABLED=0 go build ./...` 编译通过
- `go test -run TestMmap` 通过

---

### Task 1.2：文件格式常量与 MetaHeader

**目标**：定义所有文件 magic、header 结构体、序列化/反序列化工具函数。

**改动文件**：
- `internal/core/vectorindex/mmap_format.go`（~120 行）
  - `MetaHeader` 结构体（64 bytes）
  - `VectorsHeader`、`NodesHeader`、`GraphL0Header`、`GraphUpperHeader` 结构体
  - magic 常量 `"HNSW"`, `"VECS"`, `"NODE"`, `"GRL0"`, `"GRUP"`, `"IDMP"`
  - `readMetaHeader()` / `writeMetaHeader()` （原子 rename）
  - header 序列化辅助（`binary.LittleEndian`）

**预估行数**：~120 行

**验证方式**：
- 单元测试：写 MetaHeader → 读回 → 字段一致
- 测试 MetaHeader 精确 64 bytes（`unsafe.Sizeof`）
- 原子写测试：写入后读回正确

---

### Task 1.3：MmapStore 结构体与 Open/Close

**目标**：实现 `MmapStore` 结构体，`Open(dir, dim, M)` 创建/打开索引目录，`Close()` 释放资源。

**改动文件**：
- `internal/core/vectorindex/mmap_store.go`（~180 行）
  - `MmapStore` 结构体定义（所有字段）
  - `OpenMmapStore(dir string, opts MmapStoreOptions) (*MmapStore, error)`
    - 创建目录、初始化各文件（header + 初始容量 1024 slots）
    - **显式包含 graph_upper.dat 的创建/mmap（C2 fix）**
    - mmap 所有文件（vectors.dat, nodes.dat, graph_l0.dat, graph_upper.dat, meta.dat）
  - `Close() error`：munmap all → flush meta
  - 辅助：`initFile(path, magic, headerSize, slotSize, capacity)`

**预估行数**：~180 行

**验证方式**：
- 单元测试：Open 空目录 → 文件全部创建（含 graph_upper.dat）→ Close → 再次 Open → meta 一致
- Close 后 re-open 不报错
- 文件大小 = headerSize + capacity × slotSize

---

### Task 1.4：只读方法实现（GetVector / GetVectorRef / GetNeighbors / GetNorm / GetNodeLevel / GetEntryPoint）

**目标**：实现 `NodeStore` 接口中的所有只读方法。

**改动文件**：
- `internal/core/vectorindex/mmap_store.go`（追加 ~120 行）
  - `GetVector(id)` — copy from mmap slice
  - `GetVectorRef(id)` — 初版同 GetVector（copy）
  - `GetNeighbors(id, layer)` — layer=0 读 graphL0，**layer>0 读 graph_upper.dat（C2 fix：依赖 Task 1.3 的 graph_upper.dat mmap）**
  - `GetNorm(id)` — 读 nodes.dat slot
  - `GetNodeLevel(id)` — 读 nodes.dat slot
  - `GetEntryPoint()` — 从 meta 返回

**预估行数**：~120 行

**验证方式**：
- 单元测试：手动写入 mmap 区域的已知数据 → 调用读方法 → 验证返回值
- **Upper layer 读取测试：写入 graph_upper.dat 已知数据 → GetNeighbors(id, layer>0) 返回正确（C2 fix）**
- Benchmark：50K 随机 GetVector 调用 < 1μs/op（hot path）
- 边界测试：id=0、id=capacity-1、id 超范围返回 error

---

### Task 1.5：从现有数据导出到 mmap 文件格式（测试辅助）

**目标**：实现 `ExportFromMemStore(ms *MemNodeStore, dir string)` 工具函数，将 MemStore 数据导出为 mmap 文件格式，供读路径测试和 benchmark 使用。

**改动文件**：
- `internal/core/vectorindex/mmap_export_test.go`（~80 行，仅测试代码）
  - 遍历 MemStore → 写入各 mmap 文件
  - 用于生成测试数据

**预估行数**：~80 行

**验证方式**：
- 集成测试：MemStore 插入 1K 向量 → 导出 → MmapStore Open → 逐个 GetVector 对比
- Recall 对比：同一数据集 MemStore vs MmapStore 搜索结果一致

---

### Phase 1 总验收

- [ ] `CGO_ENABLED=0 GOOS=linux go build ./...` 通过
- [ ] `CGO_ENABLED=0 GOOS=darwin go build ./...` 通过
- [ ] `CGO_ENABLED=0 GOOS=windows go build ./...` 通过
- [ ] 50K 随机向量读 benchmark < 1μs（hot）
- [ ] 所有单元测试通过
- [ ] `go vet ./...` 和 `staticcheck ./...` 无新增告警

---

## Phase 2：写入路径 + WAL + Batch（后续）

### Task 2.1：WAL 实现（append / CRC32 / LSN / **replay**）
- 文件：`mmap_wal.go`（~250 行）
- **WAL replay 基础逻辑包含在此任务中（C1 fix）**：顺序扫描 + CRC 校验 + 不完整记录丢弃 + 回调接口
- replay INSERT 记录时恢复内存中的 docId→nodeId 映射（H1 fix：即使 idmap.dat compact 推迟到 Phase 3，replay 必须恢复内存 map）
- 验证：写 N 条记录 → 读回 → LSN 连续、CRC 正确、payload 一致
- 验证：模拟写入后不 Close → replay → 数据完整恢复（含 ID 映射）
- Phase 4 Task 4.2 聚焦 Open 流程集成和 crash-point 注入，不再重复 replay 基础逻辑

### Task 2.2：写方法实现（PutNode / SetNeighbors / SetNorm / SetNodeMapping）
- 文件：`mmap_store.go` 追加（~170 行）
- **SetNodeMapping 包含 idmap.dat 追加写（append + CRC）（H1/H5 fix）**：确保正常 Close→Open 不丢 ID 映射；Phase 3 Task 3.4 只做 compact + 损坏恢复
- 验证：PutNode → GetVector 返回写入值、SetNeighbors → GetNeighbors 返回一致
- 验证：SetNodeMapping → Close → Open → GetNodeId 返回正确映射

### Task 2.3：BatchableStore 接口实现
- 文件：`mmap_store.go` 追加（~60 行）
- 验证：batch 模式下多次写操作只触发 1 次 sync

### Task 2.4：文件增长（grow）+ 并发安全
- 文件：`mmap_store.go` 追加（~150 行）
- **并发安全方案（C3 fix）**：grow 期间采用 copy-on-read 策略——grow 前获取写锁，将旧 mmap slice 引用保存为 stale snapshot；grow 完成后释放写锁。并发 reader 通过 atomic.Pointer 获取当前 mmap slice，grow 切换为新 slice 后旧 reader 自然排空（epoch-based）。独立子任务：
  - 2.4a：grow 基本实现（munmap → ftruncate → mmap，atomic slice 切换）
  - 2.4b：grow 并发压力测试（多 goroutine 持续读 + 触发 grow，验证无 SIGBUS/panic）
- 验证：插入超过初始 capacity → 自动 grow → 数据完整
- 验证：并发压力测试 10 goroutine 读 + 1 goroutine 连续 grow × 100 次，零 panic

### Task 2.5：NextNodeId + ID 映射写入
- 文件：`mmap_store.go` 追加（~80 行）
- **初版策略（H2 fix）**：只做 `meta.NextNodeId++` 简单自增分配，不预留 freelist 接口。Phase 3 Task 3.1/3.2 实现 freelist 后再修改 NextNodeId 加入 freelist pop 逻辑。
- 验证：NextNodeId 单调递增、SetNodeMapping → GetNodeId 往返一致

### Task 2.6：50K Insert 集成 benchmark
- 文件：`mmap_store_bench_test.go`（~100 行）
- **Benchmark scope（H3 fix）**：Phase 2 尚无 graph_upper.dat 写入（Phase 5 Task 5.1），因此本 benchmark 仅验证 level-0 写入路径。结果为部分验证，不代表完整 E2E 性能。Phase 5 Task 5.3 为完整 E2E benchmark。
- 验证：50K×128d Insert < 90s（仅 level-0 路径）

**Phase 2 预估总行数**：~750 行

---

## Phase 3：Delete + Upsert + Freelist（后续）

### Task 3.1：DeleteNode（墓碑 + freelist）
- 文件：`mmap_store.go` 追加（~120 行）
- 验证：Delete 后 GetNodeLevel 返回 error、freelist 包含回收 ID

### Task 3.2：Freelist 启动扫描重建
- 文件：`mmap_store.go` Open 流程追加（~60 行）
- 验证：插入 → 删除部分 → Close → Open → freelist 正确重建 < 1ms

### Task 3.3：Upsert 实现
- 文件：`mmap_store.go` 追加（~50 行）
- 验证：同 docId Upsert → slot 复用、无孤儿节点

### Task 3.4：idmap.dat compact + 损坏恢复
- 文件：`mmap_idmap.go`（~170 行）
- 基础 idmap.dat 追加写已在 Phase 2 Task 2.2 实现（H1/H5 fix）；本任务聚焦 compact（合并重复 entry 缩减文件）和损坏 entry 跳过恢复
- 验证：大量写入 → compact → 文件缩小、映射完整、损坏 entry 被跳过

**Phase 3 预估总行数**：~400 行

---

## Phase 4：崩溃恢复 + Checkpoint（后续）

### Task 4.1：WAL checkpoint 机制
- 文件：`mmap_wal.go` 追加（~80 行）
- 验证：checkpoint 后 WAL 截断、meta.WalCheckpointLSN 更新

### Task 4.2：崩溃恢复流程（Open 集成 WAL replay）
- 文件：`mmap_store.go` Open 流程追加（~100 行）
- WAL replay 基础逻辑已在 Phase 2 Task 2.1 实现（C1 fix）；本任务将 replay 集成到 Open 流程（从 WalCheckpointLSN+1 开始）
- 验证：写入 N 条 → 模拟 crash（不 Close）→ Open → 数据完整

### Task 4.3：Crash point 注入测试（5 类场景）
- 文件：`mmap_crash_test.go`（~120 行）
- 验证：Insert/SetNeighbors/Checkpoint/Grow/Delete 中途 panic → 恢复正确

**Phase 4 预估总行数**：~300 行

---

## Phase 5：上层图 + 完整集成（后续）

### Task 5.1：graph_upper.dat 按需分配写入
- 文件：`mmap_store.go` 追加（~100 行）
- graph_upper.dat 的 Open/mmap 和只读路径已在 Phase 1 Task 1.3/1.4 实现（C2 fix）；本任务聚焦写入：level>0 节点的 upper slot 按需分配
- 验证：level>0 节点 upper slot 分配正确、level=0 节点不占 upper slot

### Task 5.2：HNSW 集成（替换 PebbleStore）
- 文件：`hnsw.go` / `hnsw_test.go` 修改（~80 行）
- 验证：全部现有 HNSW 测试通过

### Task 5.3：E2E benchmark + Recall 验证
- 文件：`mmap_store_e2e_test.go`（~120 行）
- 验证：SIFT 50K recall@10 > 0.95、搜索 p99 < 5ms

**Phase 5 预估总行数**：~300 行

---

## 总计

| Phase | 行数 | 状态 |
|-------|------|------|
| Phase 1：核心基础设施 | ~620 行 | **当前** |
| Phase 2：写入 + WAL + Batch | ~750 行 | 后续 |
| Phase 3：Delete + Upsert | ~400 行 | 后续 |
| Phase 4：崩溃恢复 | ~300 行 | 后续 |
| Phase 5：上层图 + 集成 | ~300 行 | 后续 |
| **总计** | **~2370 行** | |
