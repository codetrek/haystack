# HAY-007 MmapStore 设计 Review

> Reviewer: Claude (Opus 4)
> 日期: 2026-04-17
> 文档版本: Draft

---

## 总体评价

设计方向正确——mmap flat file 是解决 PebbleStore 18.5x 性能差距的正确选择。文件布局清晰，性能分析到位，WAL 方案务实。以下按严重程度列出需要解决的问题。

---

## P0 — 必须修复，否则实现会出严重问题

### P0-1: 缺少 `BatchableStore` 接口支持

**现状**: HNSW 的 `Insert` 方法会对 store 做类型断言 `BatchableStore`，调用 `BeginBatch/CommitBatch` 来批量提交写操作（包括嵌套调用）。设计文档完全没有提到 BatchableStore。

**影响**: 如果 MmapStore 不实现 BatchableStore，HNSW Insert 会退化为每次写操作都单独 fsync WAL，50K 构建会产生数十万次 fsync，性能目标无法达成。

**建议**: MmapStore 必须实现 BatchableStore。对于 mmap 写入，batch 语义可以简化为：BeginBatch 时开始缓冲 WAL 记录，CommitBatch 时一次性 flush + fsync WAL。mmap 数据文件的写入本身不需要 batch（直接写 mmap 区域），batch 控制的是 WAL 的 sync 粒度。

### P0-2: `GetVectorRef` 返回 mmap slice 的安全性问题

**现状**: 设计中 `GetVectorRef` 直接返回 mmap 内存的 slice。

**问题**:
1. **grow 时 slice 失效**: 当 grow 触发 munmap + 重新 mmap 后，之前返回的 slice 指向已释放内存，读取会 SIGBUS/SIGSEGV。即使 caller 持有 RLock，如果 caller 保存了 ref 并在 RLock 释放后使用，就会崩溃。
2. **HNSW 的实际用法**: Insert 过程中 `searchLayer` 会调用 `GetVectorRef` 获取候选向量计算距离。在 HNSW 的 h.mu 写锁保护下，不会有并发 grow（因为 Insert 是串行的）。但 Search（RLock）期间如果另一个 goroutine 触发 grow（需要等 Search 完成），理论上安全——但要验证 Search 内部不会跨越 grow 边界保存旧 ref。

**建议**:
- 方案 A（推荐）: grow 时不释放旧 mmap，而是保持旧映射存活直到所有 reader 完成。可以用引用计数或 epoch-based reclamation。
- 方案 B（简单）: GetVectorRef 对 MmapStore 仍然做 copy（性能损失很小，128d × 4B = 512B copy 约 50ns，相比 MemStore 的 map lookup 差距可接受）。
- 方案 C: 预分配足够大的 mmap（如 2x 预期容量），避免 grow。对于已知数据集大小的场景可行。

### P0-3: Freelist 持久化不完整

**现状**: "Checkpoint 时持久化到 meta 区域"。但 meta.bin 只有 64 bytes，没有 freelist 字段。

**影响**: 崩溃重启后 freelist 丢失，已删除的 slot 无法复用，导致：
1. 空间泄漏（deleted slot 永远不会被回收）
2. NodeId 与 slot 映射不一致

**建议**: 要么在 meta.bin 中扩展 freelist 存储（变长，不适合 64B 固定头），要么增加 `freelist.dat` 文件在 checkpoint 时写出。或者在恢复时通过扫描 nodes.dat 的 tombstone 标志重建 freelist（更简单，O(N) 但只在启动时执行一次）。

---

## P1 — 应该修复，影响正确性或显著影响性能

### P1-1: WAL 记录缺少 `SET_NORM` 类型

**现状**: WAL 类型只有 INSERT, DELETE, SET_NEIGHBORS, SET_ENTRY 四种。NodeStore 接口有独立的 `SetNorm` 方法。

**影响**: 如果 SetNorm 被单独调用（而不是作为 PutNode 的一部分），这次写入不会被 WAL 记录。崩溃后 norm 值回退。

**建议**: 要么增加 SET_NORM WAL 类型，要么明确 SetNorm 只在 PutNode 内部调用（INSERT WAL 已包含 norm）。查看 HNSW 代码确认调用模式。

### P1-2: 并发模型文档与现实不符

**现状**: 文档说"HNSW 已有全局 h.mu 互斥锁，所有 Insert/Delete/Search 串行化"。

**实际**: HNSW 使用 `sync.RWMutex`：
- Insert/Delete 持写锁（互相串行）
- Search 持读锁（多个 Search 并发执行）
- Search 与 Insert 互斥

**影响**: 并发安全分析基于错误前提。实际上 MmapStore 的读操作可能被多个 Search goroutine 并发调用，这不影响正确性（多个 reader 可以同时读 mmap），但 grow 的写锁策略需要考虑等待多个 reader 完成。

**建议**: 更正文档，并验证 grow 在多个并发 Search 下的行为。

### P1-3: `graph_upper.dat` 为所有节点预分配上层 — 浪费约 38MB

**现状**: 50K 节点 × 792B = 38MB，但只有 ~5% 节点有上层。

**问题**: 这不仅浪费磁盘，更关键的是浪费 page cache。38MB 的上层图文件大部分是零页，会污染 OS page cache，挤出真正需要的 vectors.dat 热页。

**建议**: 用一个单独的内存 map `upperNodes map[uint64]int` 记录哪些节点有上层（启动时从 nodes.dat 的 level 字段重建），graph_upper.dat 只给这些节点分配 slot。或者初版接受浪费，但在文档中明确 Phase 5 应该优化。50K 规模问题不大，500K × 792B = 378MB 时问题会很显著。

### P1-4: `idmap.dat` 变长记录追加方案的恢复问题

**现状**: idmap.dat 是追加写，修改时追加新记录覆盖旧映射。

**问题**:
1. 追加写没有通过 WAL 保护，崩溃时可能写入不完整的记录（DocIdLen 写了但 DocId 没写完）。
2. 启动加载时如何检测不完整记录？没有 CRC 校验。

**建议**: 每条 idmap 记录也加 CRC32，或者把 idmap 变更也记入 WAL（INSERT WAL 已包含 docId，可以从 WAL replay 重建 idmap）。

---

## P2 — 建议改进，非阻塞

### P2-1: NodeId 从 0 开始 vs 现有 PebbleStore 从 1 开始

**现状**: 设计说 "Node ID = slot index（从 0 开始）"。PebbleStore 的 `NextNodeId` 从 1 开始。

**影响**: 如果 HNSW 用 0 作为"无效 ID"的哨兵值（常见模式），NodeId=0 会冲突。需要检查 HNSW 代码中是否有 `id == 0` 的特殊判断。

**建议**: 保持从 1 开始，slot 0 作为保留位。

### P2-2: `edsrzf/mmap-go` 库已不活跃

**现状**: 设计推荐使用 `github.com/edsrzf/mmap-go`。

**问题**: 该库最后一次 commit 是 2022 年，有已知的 Windows 和 ARM 问题。

**建议**: 考虑使用 `golang.org/x/exp/mmap`（只读）或直接用 `syscall.Mmap` / `golang.org/x/sys/unix.Mmap`。对于读写 mmap，直接封装 syscall 只需 ~50 行代码，且避免第三方依赖（符合"单 binary 零 CGo"目标）。

### P2-3: 实施计划中 Phase 1 和 Phase 5 拆分上层图不合理

**现状**: Phase 1 实现 graph_l0.dat 的读取，Phase 5 才实现 graph_upper.dat。

**问题**: 没有上层图就无法做 HNSW 搜索（搜索从最高层开始），Phase 1 的"只读路径"验收标准（50K 随机向量读 benchmark）可以验证，但无法做端到端 HNSW 搜索测试。

**建议**: 把 graph_upper.dat 提前到 Phase 2，与写入路径一起实现。或者 Phase 1 就包含所有文件的读取（上层图的读取逻辑与 L0 几乎相同）。

### P2-4: meta.bin 原子写的 Windows 兼容性

**现状**: "写临时文件 → fsync → rename"。

**问题**: Windows 上 `os.Rename` 不是原子操作，且如果目标文件被其他进程打开会失败。需要用 `MoveFileEx` 的 `MOVEFILE_REPLACE_EXISTING` 标志。Go 标准库的 `os.Rename` 在 Windows 上已经使用了这个标志，但要注意文件锁。

**建议**: 在 Windows CI 中专门测试 meta.bin 的原子写。

### P2-5: WAL checkpoint 频率（每 1000 次操作）可能偏低

**现状**: 50K Insert 期间，每次 Insert 产生 ~35 次 SetNeighbors WAL 记录（修改被 insert 节点影响的邻居），总计约 175 万条 WAL 记录，触发 1750 次 checkpoint。

**问题**: 每次 checkpoint 都要写 meta.bin（原子 rename）+ 截断 WAL。1750 次 fsync 额外增加约 17 秒（假设每次 fsync 10ms），可能影响 90s 目标。

**建议**: checkpoint 间隔改为按 WAL 文件大小（如每 16MB）而非操作次数。或者 checkpoint 时不 fsync meta.bin，而是在 WAL 头部写 checkpoint position（WAL 本身已经 fsync）。

### P2-6: 缺少迁移方案

**现状**: 没有说明从 PebbleStore 迁移到 MmapStore 的路径。

**建议**: Phase 1 提到"从现有 PebbleStore 导出数据到 mmap 文件格式"，应该在设计中明确迁移工具/流程，包括是否需要 PebbleStore 和 MmapStore 共存一段时间。

---

## 设计亮点

- vectors.dat 定长 arena 设计简洁高效，O(1) 寻址
- 读写比 86:1 的分析精准，准确定位了 PebbleStore 的瓶颈
- WAL 方案务实——对本地工具来说，重建兜底 + WAL 防频繁重建是正确的权衡
- 文件分离（vectors / nodes / graph）利于独立增长和 page cache 优化

---

## 结论

**有条件通过** — P0 三项必须在实现前解决（尤其是 BatchableStore 支持，这直接决定能否达到性能目标）。P1 项建议在 Phase 2 前修复。P2 项可以在实现过程中逐步改进。

| 级别 | 数量 | 概要 |
|------|------|------|
| P0 | 3 | BatchableStore 缺失、GetVectorRef 安全性、Freelist 持久化 |
| P1 | 4 | WAL SET_NORM 缺失、并发模型文档错误、上层图浪费、idmap 恢复 |
| P2 | 6 | NodeId 起始值、mmap 库选择、Phase 拆分、Windows 兼容、checkpoint 频率、迁移方案 |
