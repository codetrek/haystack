# HAY-007 Phase 2 Code Review — Round 2 (Post P1 Fix)

> Reviewer: 飞马（Pegasus）  
> Date: 2026-04-18  
> PR: #55  
> Scope: Phase 2 写入路径 + WAL + Batch + Grow + Delete(tombstone)

---

## Spec 对照表

| Spec 要求 | 实现状态 | 备注 |
|-----------|---------|------|
| PutNode | ✅ 已实现 | WAL → ensure capacity → write vec/node/upper |
| SetNeighbors | ✅ 已实现 | L0 + Upper 两条路径 |
| SetNorm | ✅ 已实现 | WAL + nodes.dat 写入 |
| SetNodeMapping | ✅ 已实现 | 内存 map + idmap.dat 追加（带 CRC） |
| WAL with LSN | ✅ 已实现 | append + CRC32 + LSN 单调递增 |
| WAL replay | ✅ 已实现 | 5 种 record type 全覆盖 |
| WAL CRC32 | ✅ 已实现 | 覆盖 LSN+Length+Type+Payload |
| BatchableStore | ✅ 已实现 | BeginBatch / CommitBatch / DiscardBatch |
| 文件增长 grow | ✅ 已实现 | munmap→truncate→re-mmap，独立锁 |
| DeleteNode tombstone | ✅ 已实现 | flags |= nodeFlagDeleted |
| NextNodeId | ✅ 已实现 | auto-increment，freelist TODO Phase 3 |
| rebuildNodeCount | ✅ 已实现（R1 P1 fix） | WAL replay 后扫描 nodes.dat |
| WAL payload length cap | ✅ 已实现（R1 P1 fix） | 64 MiB maxWalPayloadSize |
| 50K Insert < 90s | ✅ 0.38s | 远超目标 |
| WAL replay 正确 | ✅ | 测试覆盖 |

---

## Findings

### P1 — Must Fix

#### P1-1: `scanLSN()` 缺少 `maxWalPayloadSize` 检查

**文件**: `mmap_wal.go:66-97` (scanLSN)

`Replay()` 正确检查了 `length > maxWalPayloadSize`，但 `scanLSN()` 没有。如果 WAL 文件损坏导致 length 字段是一个巨大值，`scanLSN` 会 `make([]byte, length)` 导致 OOM。

```go
// scanLSN line ~82
length := binary.LittleEndian.Uint32(header[8:12])
// 缺少: if length > maxWalPayloadSize { break }
payload := make([]byte, length)  // ← OOM if corrupted
```

**建议**: 在 `scanLSN` 中加入与 `Replay` 相同的 `maxWalPayloadSize` 检查。

#### P1-2: `PutNode` 用 `muVec.RLock()` 做写入

**文件**: `mmap_store_write.go:40-48`

`PutNode` 写入 vectors.dat 时使用 `muVec.RLock()`，写入 nodes.dat 时使用 `muNodes.RLock()`。虽然 HNSW 的 `h.mu` 保证了写操作串行化，但这意味着写入期间 `growVectors` 可以获取 `muVec.Lock()` 而此时另一个 goroutine 正在写同一个 mmap region。

更根本的问题是：`RLock` 语义上表达的是"共享读"，但这里实际在做写入。这会误导未来维护者。且如果 grow 发生在写入中间（munmap 旧映射 → 写入到已 munmap 的内存 → SIGSEGV），虽然当前 `h.mu` 序列化了 Insert，但 `PutNode` 本身没有防御。

**建议**: 至少添加注释说明为什么 RLock 足够（依赖 h.mu 外部序列化），或改为用 Lock() 在写路径上。

#### P1-3: WAL replay INSERT 的 `NodeCount++` 不幂等

**文件**: `mmap_store.go:431` (replayWAL, WalInsert case)

Replay 每遇到一条 INSERT record 就 `s.meta.NodeCount++`。如果 WAL replay 被执行两次（例如 replay 后写 meta 失败，再次重启），NodeCount 会被重复计数。

虽然 `rebuildNodeCount()` 在 replay 后修正了最终值（R1 P1-1 fix），**当前代码是安全的**。但 replay 中的 `NodeCount++` 是死代码——它的值总是被 `rebuildNodeCount` 覆盖。建议删除 replay 中的 `s.meta.NodeCount++` 和 `s.meta.NodeCount--` 以避免混淆。

降级为 P2（代码清洁度），因为 `rebuildNodeCount` 保证了正确性。

### P2 — Should Fix

#### P2-1: `growUpper` 和 `growL0` 共用 `muGraph` 锁

**文件**: `mmap_store_grow.go:93,113`

`growL0` 和 `growUpper` 都持有 `muGraph.Lock()`。如果 L0 需要 grow 而 upper 正在被读取，整个 graph 读取会被阻塞。Spec 说"独立锁"，但实际 L0 和 upper 共享同一把锁。

目前可接受（grow 频率极低），但与 spec 的"独立锁"描述不完全一致。

**建议**: 文档化这个决策，或未来拆分为 `muL0` 和 `muUpper`。

#### P2-2: `CommitBatch` 的 `sync` 参数被忽略

**文件**: `mmap_store_write.go:89-90`

```go
func (s *MmapStore) CommitBatch(sync bool) error {
    ...
    sync = true  // 参数被覆盖
```

`sync` 参数声称"kept for interface compatibility"但直接被覆盖为 `true`。要么删除参数改为 `CommitBatch() error`，要么尊重参数。当前行为正确（always sync），但 API 有误导性。

#### P2-3: `idmapFile` 写入没有 fsync

**文件**: `mmap_store_write.go:119-121`

`SetNodeMapping` 追加写 `idmap.dat` 后没有 fsync（非 batch 模式下也没有）。WAL 已经记录了 INSERT（含 docId），所以 idmap 丢失可以从 WAL 恢复。但 `replayWAL` 中 INSERT case 没有调用 `SetNodeMapping`，意味着 replay 后 docToNode 映射不会被恢复。

**分析**: `loadIdmap` 在 `replayWAL` 之前执行，所以 idmap.dat 中已有的映射会被加载。但如果 crash 发生在 WAL INSERT 写入后、idmap.dat 写入前，该映射会丢失。WAL replay 没有恢复 id mapping 的逻辑。

**建议**: 在 `replayWAL` 的 INSERT case 中恢复 docToNode/nodeToDoc 映射。

#### P2-4: `replayWAL` DELETE case 没有清理 id mapping

**文件**: `mmap_store.go:473-479`

Replay DELETE 只设置 tombstone flag 和减 NodeCount，没有从 docToNode/nodeToDoc 中删除映射。如果 crash 发生在 DeleteNode WAL 之后、内存映射删除之前，replay 后会有 stale mapping 指向 tombstoned node。

#### P2-5: 测试缺少 WAL replay 完整性测试

没有测试验证：写入 N 条数据 → Close → Reopen → 数据完整（端到端 crash recovery 测试）。`TestMmapStoreNodeMappingPersistence` 和 `TestMmapStoreNextNodeIdPersistence` 覆盖了部分，但缺少 PutNode + SetNeighbors 的完整 reopen 验证。

### P3 — Nice to Have

#### P3-1: `EncodeInsert` 和 `DecodeInsert` 没有边界检查

Decode 函数假设 payload 格式正确。如果 payload 被截断（虽然 CRC 应该已经拦截），会 panic。防御性编程建议加 bounds check。

#### P3-2: `mmapSync` 每次 sync 整个 region

非 batch 模式下，每次 PutNode/SetNeighbors/SetNorm 都 msync 整个 mmap region（可能 24MB+）。Linux 的 `MS_SYNC` 会 flush 所有 dirty pages，不仅仅是刚写的那一页。在 batch 模式下无影响（延迟到 CommitBatch），但非 batch 单条写入会很慢。

---

## 第 1 轮 P1 修复验证

| R1 Finding | 修复状态 | 验证 |
|------------|---------|------|
| P1-1: rebuildNodeCount | ✅ 已修复 | `rebuildNodeCount()` 在 replayWAL 后调用，扫描 nodes.dat 重建 |
| P1-5: WAL payload length cap | ✅ 已修复 | `maxWalPayloadSize = 64 MiB`，Replay 中检查 |

---

## Checklist 总结

| 检查项 | 结论 |
|--------|------|
| 符合设计 | ✅ Phase 2 所有功能已实现 |
| 测试覆盖 | ⚠️ 缺少 PutNode reopen 端到端测试 |
| 错误处理 | ✅ 错误信息清晰，逐层 wrap |
| 代码质量 | ⚠️ replay 中有死代码（NodeCount++），sync 参数被忽略 |
| 性能 | ✅ 50K Insert 0.38s 远超目标 |
| 安全 | ⚠️ scanLSN 缺少 payload size cap（P1-1） |
| 文档 | ✅ Spec 一致 |
| 锁顺序 | ✅ muGraph→muNodes 在 PutNode 中遵守 |
| WAL replay 幂等 | ⚠️ 形式上不幂等（NodeCount++），但 rebuildNodeCount 兜底 |
| grow 安全 | ✅ munmap→truncate→re-mmap，double-check under lock |
| 并发安全 | ⚠️ PutNode 用 RLock 写入（依赖外部 h.mu，需文档化） |

---

## 结论

**REQUEST CHANGES** — 1 个 P1 必须修复：

1. **P1-1**: `scanLSN` 缺少 `maxWalPayloadSize` 检查 → OOM 风险

P1-2 和 P1-3 降级为 P2（P1-2 因有 h.mu 保护实际安全，P1-3 因 rebuildNodeCount 兜底正确）。

P2-3（replay 不恢复 id mapping）值得在 Phase 3 之前修复，但不阻塞合并。

修复 P1-1 后可 APPROVE。
