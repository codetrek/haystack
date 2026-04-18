# HAY-007 Phase 2 Task Plan Review

> 审核人：Claude Code  
> 日期：2026-04-18  
> 对照：`docs/design/HAY-007-mmap-store.md` v2（Phase 2 部分）  
> 对象：`docs/tasks/HAY-007-phase2-tasks.md`

---

## CRITICAL-1：WAL Replay 恢复 meta 字段遗漏

**位置**：Task 2.1 步骤 6（Replay 方法）

**问题**：Replay 回调仅提到恢复 `docToNode / nodeToDoc` 映射，但未提及恢复以下 meta 字段：

- `NodeCount`（需统计 INSERT 记录数）
- `TotalSlots`（需追踪 max nodeId + 1）
- `NextNodeId`（需为 max nodeId + 1，否则重启后 ID 冲突）
- `EntryPoint` / `EntryLevel`（需从 SET_ENTRY 记录恢复）
- `MaxLevel`（需从 INSERT 记录的 level 字段推算）
- `GraphUpperNextSlot`（需从 INSERT level>0 记录推算）

**影响**：非正常关闭后 WAL replay 无法恢复正确的 meta 状态。虽然 Phase 1 Close() 已有 `writeMetaHeader`，但若 Close() 未执行（crash），replay 是唯一恢复路径。如果 replay 不更新 meta，重启后 `NextNodeId` 可能从旧值开始分配导致 ID 覆盖、`NodeCount` 不准确、`EntryPoint` 丢失。

**建议**：Task 2.1 步骤 6 增加 replay 回调中 meta 字段恢复逻辑。可在 replay 完成后统一根据已扫描记录计算 meta，而非每条记录都更新。

---

## HIGH-1：Task 2.2 Close() 缺少 WAL flush 与 meta 写入的顺序说明

**位置**：Task 2.2 步骤 1（MmapStore 集成 WAL 字段）

**问题**：Close() 描述为 "wal.Sync() + wal.Close()"，但未提及：
1. 需要先 msync 所有 mmap 区域（确保数据页落盘）
2. 再 writeMetaHeader（Phase 1 已有，但 Task 2.2 未提及保留此行为且需在 WAL sync 之后执行）
3. 顺序必须为：msync mmap → wal.Sync() → writeMetaHeader → wal.Close() → munmap → close files

Phase 1 的 Close() 已调用 `writeMetaHeader`，但 Phase 2 修改 Close() 加入 WAL 后，需明确 WAL sync 与 meta 写入的先后顺序，否则可能出现 meta.WalCheckpointLSN 与实际 WAL 状态不一致。

**建议**：Task 2.2 步骤 1 明确 Close() 的完整步骤序列和顺序约束。

---

## HIGH-2：idmap.dat 加载时机未明确

**位置**：Task 2.2 步骤 6（SetNodeMapping）

**问题**：Task 2.2 引入了 idmap.dat 的写入逻辑（追加写 + CRC），但未提及 Open() 时加载 idmap.dat 到内存 map 的步骤。Phase 1 是只读路径，Open() 中可能不含 idmap.dat 加载逻辑。

如果 Open() 不加载 idmap.dat，则 Close() → Open() 后 `docToNode`/`nodeToDoc` 为空，GetNodeId 失败。Task 2.2 验证用例第 10 条（"SetNodeMapping → Close → Open → GetNodeId 返回正确"）会暴露此问题，但实现步骤中遗漏了此工作。

**建议**：Task 2.2 步骤 1（修改 Open）中明确增加 idmap.dat 加载步骤：顺序读取所有 entry，CRC 校验，填充 docToNode/nodeToDoc。估计 ~30 行。

---

## HIGH-3：PutNode 写 WAL 的 docId 字段语义不清

**位置**：Task 2.2 步骤 2（PutNode）

**问题**：PutNode 的 WAL INSERT 记录 `EncodeInsert(id, level, vec, norm, "")` 中 docId 为空字符串。但设计文档 §4.3 Insert 流程第 5 步将 idmap 写入作为 Insert 流程的一部分。

如果 WAL INSERT 记录不包含 docId，则 replay 时无法恢复 docToNode/nodeToDoc 映射——但 Task 2.1 步骤 6 却说 "replay INSERT 时回调中恢复 docToNode / nodeToDoc 映射"。两处矛盾。

有两种正确方案：
- (A) PutNode 接受 docId 参数，WAL INSERT 包含完整 docId，replay 可恢复映射
- (B) WAL INSERT 不含 docId，映射恢复完全依赖 idmap.dat 加载（replay 不恢复映射）

**建议**：明确选择方案并统一 Task 2.1 步骤 6 与 Task 2.2 步骤 2 的描述。推荐方案 B（映射从 idmap.dat 恢复），因为 idmap.dat 已有 CRC 校验且是映射的 source of truth。此时 Task 2.1 步骤 6 应删除 "恢复 docToNode / nodeToDoc 映射" 的描述。

---

**总结**：1 CRITICAL + 3 HIGH，均为遗漏或描述矛盾，无架构方向问题。修复后可执行。
