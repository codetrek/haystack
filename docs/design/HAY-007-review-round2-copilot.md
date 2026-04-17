# HAY-007 MmapStore Design Review — Round 2 (Copilot)

> Reviewer: GitHub Copilot (simulated)  
> Date: 2026-04-17  
> Base: v2 design vs Round 1 review  
> Verdict: **Pass** — all P0 resolved, no new blockers

---

## P0 逐条验证

### P0-1: WAL 缺 LSN ✅ Fixed

v2 WAL record 格式（§4.1 wal.bin）：
```
LSN: uint64  ←  已加
Length: uint32
Type: uint8
Payload: [Length]byte
CRC32: uint32  // 覆盖 LSN+Length+Type+Payload
```

MetaHeader 新增 `WalCheckpointLSN uint64`，恢复从 `WalCheckpointLSN + 1` replay。格式合理，LSN 单调递增、CRC 覆盖全 record（同时修复了 Round 1 P1-3）。

### P0-2: 向量 slot 未 page-aligned ✅ Fixed

v2 vectors.dat header = **4096 bytes**（含 4072 bytes padding）。所有 `.dat` 文件 header 统一 4096 bytes page-aligned。Slot 起始于 offset 4096，满足要求。

### P0-3: CGO_ENABLED=0 未验证 ✅ Fixed

v2 §5 明确：**自封装 `syscall.Mmap`（~80 行）**，不依赖 edsrzf/mmap-go。Phase 1 验收条件含"三平台 `CGO_ENABLED=0` 编译通过"。同时修复了 Round 1 P1-6（mmap-go 停止维护）。

---

## Round 1 P1 验证

| # | 问题 | 状态 | 备注 |
|---|------|------|------|
| P1-1 | 单锁 → 每区域独立 RWMutex | ✅ Fixed | v2 §4.2 有 `muVec`/`muGraph`/`muNodes` 三把独立锁 |
| P1-2 | GetVectorRef 悬垂引用 | ✅ Fixed | v2 明确"初版返回 copy"，Phase 5 再优化零拷贝（epoch-based） |
| P1-3 | CRC 不覆盖 header | ✅ Fixed | 随 P0-1 一并修复，CRC 覆盖 LSN+Length+Type+Payload |
| P1-4 | 字节序未声明 | ✅ Fixed | v2 §2 约束："所有二进制数据 **little-endian** 字节序" |
| P1-5 | 缺 crash recovery 测试方案 | ✅ Fixed | v2 §6 末尾列出 5 类 crash point + 注入方案 |
| P1-6 | mmap-go 停止维护 | ✅ Fixed | 随 P0-3 修复 |

---

## Round 1 P2 验证

| # | 问题 | 状态 | 备注 |
|---|------|------|------|
| P2-1 | MetaHeader 手动 padding | ⚠️ Open | 仍手动 `[4]byte`，未加编译期断言。低风险，可 Phase 1 补 |
| P2-2 | freelist 持久化位置 | ✅ Fixed | v2 §4.6：启动扫描 nodes.dat tombstone 重建，不需独立文件 |
| P2-3 | graph_l0 slot 260B 未对齐 | ⚠️ Open | 仍 260B。影响有限（L0 读不如 vectors 热） |
| P2-4 | WAL checkpoint 按次数 | ⚠️ Open | 仍"每 N 次操作"。可后续调优 |
| P2-5 | grow 崩溃窗口 | ⚠️ Open | 未显式处理。WAL replay 可兜底 |
| P2-6 | 代码量低估 | ✅ Fixed | v2 总估算调整为 ~2200 行 |
| P2-7 | benchmark suite | ⚠️ Open | Phase 1 验收有 benchmark 目标，但无 regression 框架 |

---

## Round 1 补充项验证

| 问题 | 状态 | 备注 |
|------|------|------|
| BatchableStore 接口 | ✅ Fixed | v2 §4.2.1 完整实现 BeginBatch/CommitBatch/DiscardBatch |
| NextNodeId 起始值 0 vs 1 | ⚠️ Open | 未提及与 PebbleStore 对齐，迁移时需注意 |
| 并发模型文档化 | ✅ Fixed | v2 §4.4 明确：HNSW h.mu 串行化写，Search 读锁并发 |

---

## 新发现

### P2-NEW-1: DiscardBatch 未 rollback WAL

`DiscardBatch` 只重置 `batchDepth`/`batchMode`，但 batch 期间已 append 的 WAL record 不会撤回。如果 DiscardBatch 是因为 Insert 失败调用的，WAL 中会残留部分写入。replay 时可能产生孤儿节点。

**缓解**：当前 HNSW 层不会在 Insert 失败时 DiscardBatch（只在 CommitBatch 中 sync），所以实际不触发。建议在 DiscardBatch 注释中记录此限制。

### P2-NEW-2: idmap.dat header 只有 16 bytes，未 page-aligned

其他 `.dat` 文件 header 统一 4096，但 idmap.dat 只有 16 bytes。不影响正确性（idmap 启动时全量加载到内存，非 mmap），但格式不一致。低优先级。

---

## 结论

v2 完整修复了 Round 1 的 **3 个 P0** 和 **6 个 P1**。剩余 open 项均为 P2（低风险），可在实现过程中逐步处理。无新 P0/P1。

**建议：可以开始 Phase 1 实现。**
