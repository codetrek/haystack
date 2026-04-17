# HAY-007 MmapStore Review — Round 2

> Reviewer: Claude (Opus 4)  
> 日期: 2026-04-17  
> 文档版本: Draft v2

---

## P0 逐条验证

| # | P0 | 状态 | 验证 |
|---|-----|------|------|
| 1 | BatchableStore | **已修复** | §4.2.1 新增完整实现：`batchMode`/`batchDepth` 嵌套计数，CommitBatch 时一次 `syncAll()`。功能目标也明确列出 BatchableStore。方案合理。 |
| 2 | GetVectorRef 安全性 | **已修复** | §4.1 vectors.dat 明确"初版 GetVectorRef 返回 copy"，Phase 5 再用 epoch-based reclamation 做零拷贝。接口映射表一致。正确选择了原 Review 的方案 B。 |
| 3 | Freelist 持久化 | **已修复** | §4.6 明确"启动时扫描 nodes.dat tombstone 重建，O(N)，50K < 1ms"。不引入独立文件，避免一致性问题。采用了原 Review 的最简方案，合理。 |
| 4 | WAL LSN (Copilot P0) | **已修复** | WAL record 新增 `LSN: uint64`，meta.bin 新增 `WalCheckpointLSN`，恢复流程从 `lastLSN+1` replay。CRC 覆盖含 LSN。完整。 |
| 5 | Page alignment (Copilot P0) | **已修复** | 所有 .dat header 统一 4096 bytes page-aligned（vectors/nodes/graph_l0/graph_upper 均可见 `[4096]byte` padding）。数据 slot 从 offset 4096 开始。 |

**结论：5/5 P0 全部已修复。**

---

## P1 修复情况（附带检查）

| P1 | 状态 |
|----|------|
| WAL 缺 SET_NORM | **已修复** — Type 新增 `SET_NORM=5`，SetNorm 方法写 WAL |
| 并发模型文档错误 | **已修复** — §4.4 明确 "Search 只持读锁，可并发" |
| graph_upper 全预分配浪费 | **已修复** — 按需分配，UpperSlot 间接引用，50K 只需 ~2.5K slot |
| idmap 恢复无 CRC | **已修复** — 每条 entry 带 CRC32，加载时不匹配则丢弃 |

## P2 修复情况

- mmap 库选型：**已修复** — 自封装 syscall.Mmap（~80 行）
- NodeId 从 0 开始：**未修复** — 仍从 0 开始（风险低，可实现时确认）
- Phase 拆分：graph_upper 仍在 Phase 5，但影响不大

---

## 新发现（非阻塞）

1. **BeginBatch 缺并发保护**：`batchMode`/`batchDepth` 是普通 bool/int，无 mutex/atomic。虽然 HNSW h.mu 保证串行调用，但建议加注释说明前置条件，或用 atomic 防御性编程。
2. **DiscardBatch 不回滚**：当前 DiscardBatch 只重置标志，不撤销已写入 mmap 的数据。这对 HNSW 当前用法无影响（CommitBatch 总是成功），但接口语义上不完整。建议文档注明 DiscardBatch 不保证回滚。

---

## 最终判定

**通过** — 所有 5 个 P0 和 4 个 P1 均已在 v2 中修复，设计可以进入实现阶段。
