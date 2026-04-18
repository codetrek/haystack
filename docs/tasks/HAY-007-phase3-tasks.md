# HAY-007 Phase 3: Checkpoint & Recovery — Task Breakdown

> 对应设计文档 Phase 4: 崩溃恢复 + Checkpoint
> 分支: `feat/hay-007-phase3-checkpoint`

---

## 已有基础（Phase 2）

| 能力 | 文件 | 状态 |
|------|------|------|
| WAL append (LSN + CRC32) | `mmap_wal.go:124` Append() | ✅ |
| WAL replay (afterLSN filter) | `mmap_wal.go:196` Replay() | ✅ |
| WAL scanLSN (启动扫描最大 LSN) | `mmap_wal.go:68` scanLSN() | ✅ |
| WAL truncate (CRC 不匹配截断) | `mmap_wal.go:112,250` | ✅ |
| replayWAL (5 种 record 全处理) | `mmap_store.go:394` | ✅ |
| meta.bin 原子写 (tmp→fsync→rename) | `mmap_format.go:110` writeMetaHeader() | ✅ |
| MetaHeader.WalCheckpointLSN 字段 | `mmap_format.go:38` | ✅ |
| Open 时 replay from WalCheckpointLSN | `mmap_store.go:144-150` | ✅ |
| Close 时 writeMetaHeader | `mmap_store.go:176` | ✅ |

---

## Task 1: WAL Checkpoint 方法

**目标**: 实现 `(*MmapStore).Checkpoint()` — msync 所有 mmap → writeMetaHeader(含当前 walLSN) → truncate WAL

**文件**: `mmap_store_write.go`（新增方法）, `mmap_wal.go`（新增 `Truncate(offset)` 或 `Reset()` 方法）

**具体步骤**:
1. 在 `mmap_wal.go` 新增 `(*WAL).Reset()` — truncate 文件到 0 并 seek 到 0，重置 buf，**但保留 nextLSN 不变**（即 Reset 后 nextLSN = checkpointLSN + 1，确保 LSN 单调递增）
2. 在 `mmap_store_write.go` 新增 `(*MmapStore).Checkpoint() error`:
   - msync 所有 4 个 mmap 区域（vectors, nodes, graphL0, graphUpper）
   - `s.meta.WalCheckpointLSN = s.wal.LSN()` — 在 WAL 上新增 `LSN() uint64` 访问器
   - `writeMetaHeader(s.dir, &s.meta)`
   - `s.wal.Reset()` — truncate WAL
   - `s.idmap.Compact()` — 对 idmap 做 compaction，回收已删除条目的空间
3. 在 `mmap_wal.go` 新增 `(*WAL).LSN() uint64` 返回当前 lastLSN

**预估行数**: ~40 行
**验证方式**:
- 单元测试：写 N 条记录 → Checkpoint() → 验证 meta.bin 中 WalCheckpointLSN = N → 验证 wal.bin 大小 = 0
- 单元测试：Checkpoint 后继续写 → 再次 Replay 只看到新记录
- 单元测试：Reset 后新写入的 LSN > checkpoint LSN（验证 LSN 不清零）

---

## Task 2: Close 调用 Checkpoint

**目标**: `Close()` 在 munmap 前执行完整 checkpoint，确保 clean shutdown 后 WAL 为空

**文件**: `mmap_store.go`（修改 Close）

**具体步骤**:
1. 将 Close() 中现有的 msync + writeMetaHeader 替换为 `s.Checkpoint()` 调用
2. Checkpoint 已包含 msync + writeMeta + WAL truncate，Close 后续只做 WAL.Close + idmap.Close + munmap
3. Open 中 replay 完成后自动调用 `s.Checkpoint()`，将 replay 恢复的状态持久化，避免每次 Open 都重放相同 WAL

**预估行数**: ~10 行（净减少，逻辑合并）
**验证方式**:
- 现有测试全部通过（行为不变，只是多了 WAL truncate）
- 新测试：Open → 写 N 条 → Close → 检查 wal.bin 大小 = 0，meta.bin WalCheckpointLSN = N
- 新测试：Open → 写 N 条 → crash → 重新 Open → 验证 replay 后自动 checkpoint（WAL 被 truncate，meta 更新）

---

## Task 3: 自动 Checkpoint（每 N 次操作）

**目标**: 所有写路径（batch 和非 batch）在累计操作数超过阈值时自动 checkpoint

**文件**: `mmap_store_write.go`（修改 CommitBatch 及所有非 batch 写方法）, `mmap_store.go`（新增 opsSinceCheckpoint 字段）

**具体步骤**:
1. MmapStore 新增 `opsSinceCheckpoint uint64` 字段和 `checkpointInterval uint64`（默认 1000）
2. `MmapStoreOptions` 新增 `CheckpointInterval` 可配置字段
3. WAL Append 时递增 opsSinceCheckpoint（这样所有通过 WAL 的写路径都自动计数）
4. CommitBatch(sync=true) 末尾：if opsSinceCheckpoint >= checkpointInterval → Checkpoint()
5. 非 batch 写方法（PutNode, SetNeighbors, DeleteNode 等直接写路径）末尾同理检查阈值并触发 checkpoint

**预估行数**: ~25 行
**验证方式**:
- 单元测试：设 interval=10，写 15 条，验证中间触发了 checkpoint（WAL 被 truncate 过）
- 单元测试：设 interval=1000，写 5 条，验证未触发 checkpoint（WAL 保留）

---

## Task 4: 崩溃恢复集成测试 — 基本 replay

**目标**: 验证 crash 后 Open 能正确恢复：写入 → 不调 Close（模拟 crash）→ 重新 Open → 数据完整

**文件**: `mmap_checkpoint_test.go`（新文件）

**具体步骤**:
1. TestCrashRecovery_BasicReplay：
   - Open → PutNode × N（带 BeginBatch/CommitBatch）→ 直接关闭 fd（不调 Close）
   - 重新 Open → 验证所有 N 个节点可读且数据正确
2. TestCrashRecovery_AfterCheckpoint：
   - Open → 写 N 条 → Checkpoint() → 写 M 条 → crash
   - 重新 Open → 验证 N+M 条数据完整

**预估行数**: ~80 行
**验证方式**: `go test -run TestCrashRecovery -v`

---

## Task 5: Crash Point 注入框架

**目标**: 提供 crash point hook，允许测试在特定写操作之间注入 panic 来模拟 5 类崩溃

**文件**: `mmap_store.go`（新增 hook 字段）, `mmap_checkpoint_test.go`

**具体步骤**:
1. MmapStore 新增 `testing` 用 hook：`crashAfterMsync func()`, `crashAfterMeta func()`, `crashBeforeTruncate func()`
2. 在 Checkpoint() 流程中插入 hook 调用点（仅非 nil 时调用，零生产开销）
3. WAL Append 中同理：`crashAfterWALWrite func()`

**预估行数**: ~30 行
**验证方式**: 下一个 task 使用这些 hook

---

## Task 6: 8 类 Crash Point 测试

**目标**: 覆盖设计文档要求的 5 类崩溃场景 + grow/SetNeighbors/DeleteNode 中途 crash

**文件**: `mmap_checkpoint_test.go`

**具体测试**:

### 6a. WAL 写后 msync 前
- 写 WAL record → crash → Open → WAL replay 恢复数据到 mmap

### 6b. msync 后 meta.bin 写前
- Checkpoint: msync 完成 → crash → Open → meta 仍是旧 checkpoint LSN → WAL replay 补回
- 因为 mmap 已 msync，replay 是幂等的（覆盖相同数据），不会损坏

### 6c. meta.bin 写后 WAL truncate 前
- Checkpoint: meta 已更新 → crash → Open → WalCheckpointLSN 已更新 → replay 跳过已 checkpoint 的记录
- WAL 有残留旧记录但 LSN ≤ checkpoint 会被跳过

### 6d. Partial WAL record（写到一半的记录）
- 手动向 wal.bin 追加不完整字节 → Open → scanLSN/Replay truncate 不完整记录 → 数据完整

### 6e. Partial meta.bin write（meta 写坏）
- 将 meta.bin 截断为 32 字节 → Open 应 fallback 或报错
- 或：meta.bin.tmp 存在但 rename 未完成 → 旧 meta.bin 仍有效

### 6f. grow 中途 crash
- 写入触发 grow（mmap 扩容）→ grow 完成前 crash → Open → 验证数据完整且 mmap 区域一致
- 测试 grow 后 meta 未更新场景：旧 capacity 的 meta + WAL 中有超出旧 capacity 的记录 → replay 时需重新 grow

### 6g. SetNeighbors 中途 crash
- 写 WAL SetNeighbors record → crash（msync 前）→ Open → WAL replay 恢复 neighbor 关系正确

### 6h. DeleteNode 中途 crash
- 写 WAL DeleteNode record → crash（msync 前）→ Open → WAL replay 恢复删除状态，节点不可见

**预估行数**: ~220 行
**验证方式**: `go test -run TestCrashPoint -v`，每个子测试独立 pass

---

## Task 7: kill -9 验收测试

**目标**: 端到端验收 — 真实进程 kill 后重启索引完整

**文件**: `mmap_checkpoint_test.go`（或独立 `mmap_kill_test.go`）

**具体步骤**:
1. 使用 `os/exec` 启动子进程写入索引（helper binary 或 TestMain 模式）
2. 子进程写入 1000 个向量后 sleep
3. 父进程 kill -9 子进程
4. 父进程 Open 同一目录 → 验证至少 checkpoint 过的数据完整（部分未 checkpoint 的通过 WAL replay 恢复）

**预估行数**: ~60 行
**验证方式**: `go test -run TestKill9Recovery -v -count=3`（多次运行验证稳定性）

---

## 依赖关系

```
Task 1 (Checkpoint 方法)
  ↓
Task 2 (Close 调 Checkpoint)     Task 3 (自动 Checkpoint)
  ↓                                ↓
Task 4 (基本 replay 测试) ←────────┘
  ↓
Task 5 (crash hook 框架)
  ↓
Task 6 (5 类 crash 测试)
  ↓
Task 7 (kill -9 验收)
```

## 总预估

| Task | 行数 | 文件 |
|------|------|------|
| 1. Checkpoint 方法 | ~40 | mmap_wal.go, mmap_store_write.go |
| 2. Close 调 Checkpoint | ~10 | mmap_store.go |
| 3. 自动 Checkpoint | ~30 | mmap_store_write.go, mmap_store.go |
| 4. 基本 replay 测试 | ~80 | mmap_checkpoint_test.go |
| 5. Crash hook 框架 | ~30 | mmap_store.go, mmap_checkpoint_test.go |
| 6. 8 类 crash 测试 | ~220 | mmap_checkpoint_test.go |
| 7. kill -9 验收 | ~60 | mmap_kill_test.go |
| **总计** | **~470** | |
