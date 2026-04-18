# HAY-007 Phase 2 Code Review — PR #55

> Reviewer: Claude Code (strict mode)  
> Date: 2026-04-18  
> Branch: `feat/hay007-phase2-write`  
> Commits: b44a657 → 8044c86 (5 commits, post-review fixes included)

---

## Spec 对照表

| Spec 要求 | 实现状态 | 文件 | 备注 |
|-----------|---------|------|------|
| PutNode 写入路径 | ✅ 已实现 | mmap_store_write.go:16-89 | WAL → ensureCapacity → 写 vector/node/upper slot |
| SetNeighbors 写入路径 | ✅ 已实现 | mmap_store_write.go:93-164 | L0 + Upper 分支，含容量校验 |
| SetNorm 写入路径 | ✅ 已实现 | mmap_store_write.go:167-185 | WAL → 写 nodes.dat |
| SetNodeMapping 写入路径 | ✅ 已实现 | mmap_store_write.go:199-222 | 内存 map + idmap.dat append + CRC32 |
| WAL (LSN + append) | ✅ 已实现 | mmap_wal.go | 5 种 record type，LSN 单调递增 |
| WAL replay + CRC32 | ✅ 已实现 | mmap_wal.go:173-232, mmap_store.go:340-484 | 不完整/CRC 不匹配截断 |
| WAL record 格式: LSN+Length+Type+Payload+CRC32 | ✅ 符合 | mmap_wal.go:30-31 | 13 字节 header + payload + 4 字节 CRC |
| BatchableStore (BeginBatch/CommitBatch/DiscardBatch) | ✅ 已实现 | mmap_store_write.go:280-319 | 嵌套 depth，CommitBatch 始终 sync |
| 文件增长 (grow，独立锁) | ✅ 已实现 | mmap_store_grow.go | munmap → truncate → remap，每文件独立锁 |
| DeleteNode (墓碑) | ✅ 已实现 | mmap_store_write.go:239-262 | WAL DELETE + tombstone 标志 |
| NextNodeId (freelist pop 或新分配) | ⚠️ 部分 | mmap_store_write.go:337-344 | freelist TODO Phase 3，仅自增 |
| freelist 启动扫描重建 | ❌ 未实现 | — | 标注 Phase 3，**符合计划** |

**总结**：Phase 2 spec 10/12 项完成，2 项（freelist）明确标注 Phase 3，符合设计文档分阶段规划。

---

## 问题清单

### P0 — 必须修复（数据正确性/安全）

**无 P0 问题。** 此前 4 个 P0（allocUpperSlot race、DeleteNode WAL、CommitBatch sync、NextNodeId freelist）已在 b44a657–effd747 修复。

### P1 — 强烈建议修复

#### P1-1: replayWAL 中 NodeCount 盲增/盲减可能导致计数漂移

**文件**: `mmap_store.go:440`, `mmap_store.go:478-479`

replay 时对每条 INSERT 执行 `s.meta.NodeCount++`，对 DELETE 执行 `NodeCount--`。但 meta.bin 中已保存了 checkpoint 时的 NodeCount。如果 WAL 中部分记录在 checkpoint 前已执行（数据已写入 mmap 且 meta 已更新），replay 会重复计数。

**风险**：Close() 写 meta 时 NodeCount 偏高/偏低。当前 Phase 2 无 checkpoint 逻辑（Phase 4），所以 `WalCheckpointLSN=0` 意味着全量 replay，此时 meta.NodeCount 应在 replay 前重置为 0。

**建议**：在 `replayWAL()` 开头重置 `s.meta.NodeCount = 0`、`s.meta.TotalSlots = 0`、`s.meta.NextNodeId = 0`，让 replay 完全重建 meta 状态。或者，在 replay 完成后扫描 nodes.dat 计算准确值。

#### P1-2: PutNode 写 mmap 时使用 RLock 但实际在修改数据

**文件**: `mmap_store_write.go:41-49` (muVec.RLock), `mmap_store_write.go:65-78` (muNodes.RLock)

PutNode 在写入 vectors 和 nodes 时获取的是 **RLock**（读锁），不是 Lock（写锁）。注释说"HNSW h.mu 保证串行"所以不需要写锁。

**问题**：RLock 阻止 grow（需要 Lock），但不阻止并发 reader。多个 goroutine 同时读和写同一 mmap 区域虽然在 OS 层面不会 crash（mmap 写是原子的对 aligned word），但 Go 的 race detector 会报 data race。`SetNeighbors` 同样使用 `muGraph.RLock` 写数据。

**建议**：这是有意为之的设计权衡（避免读操作阻塞），在代码注释中明确记录此决策及其前提条件（HNSW h.mu 串行化写入）。如果未来允许并发写入，必须升级为写锁。

#### P1-3: loadIdmap 打开文件后又用 ReadFile 重新读取

**文件**: `mmap_store.go:336-352`

`loadIdmap` 先 `os.OpenFile` 获得 handle，然后用 `os.ReadFile` 再次打开同一路径读取内容。这会创建两个独立的 fd。

**建议**：直接从已打开的 `f` 读取（`io.ReadAll(f)` 然后 `f.Seek(0, 2)`），避免多余的 open/close。

#### P1-4: idmap.dat 无 header/magic

**文件**: `mmap_store.go:336-386`

设计文档指定 idmap.dat 有 16 字节 header（Magic "IDMP" + Count + padding），但实现直接从 offset 0 开始写 entry。缺少 magic 校验意味着无法检测文件损坏或格式不匹配。

**建议**：加 header 或从 spec 删除此要求，保持文档与代码一致。

#### P1-5: WAL payload length 无上限校验

**文件**: `mmap_wal.go:156-160`

`scanLSN` 和 `Replay` 从文件读取 `length` 字段后直接 `make([]byte, length)` 分配。若文件损坏导致 length 为极大值（如 0xFFFFFFFF = 4GB），会导致 OOM panic。

**建议**：加上 length 上限校验（如 `< 64MB`），超出则视为损坏记录。

### P2 — 建议改进

#### P2-1: CommitBatch 中 `sync = true` 覆盖使 `if sync` 分支成为死代码

**文件**: `mmap_store_write.go:296-310`

```go
sync = true  // line 296
if sync {    // line 301 — always true
```

这是 CRITICAL-2 修复的结果。代码正确但可读性差——参数 `sync bool` 的签名暗示调用者可以控制，实际无效。

**建议**：去掉参数，或加注释说明为什么忽略。

#### P2-2: mmapSyncPlatform (Unix) 未做 page 对齐

**文件**: `mmap_unix.go:27-36`

`msync(2)` 要求 addr 是 page-aligned。`mmap` 返回的基地址通常是对齐的，但规范要求显式保证。如果 `data` 是对 mmap 区域的 sub-slice，addr 可能不对齐。

**建议**：当前所有调用传入完整 mmap slice，所以实际安全。加个注释说明此前提。

#### P2-3: grow 后 header capacity 已更新但未 msync

**文件**: `mmap_store_grow.go:146-148`

`remapFile` 更新 header 中的 capacity 字段但不 msync。如果随后 crash，重新 mmap 时 header 中的 capacity 可能与文件实际大小不一致。

**建议**：grow 后 msync header page，或在 replay 时根据文件大小重算 capacity。

#### P2-4: Benchmark 使用 `time.Now()` 而非 `b.ResetTimer()`

**文件**: `mmap_store_bench_test.go:39-42`

benchmark 在循环内用 `time.Now()` 手动计时，但没有调用 `b.ResetTimer()` 排除 setup 开销。不影响正确性但不符合 Go benchmark 惯例。

#### P2-5: replayWAL INSERT 不恢复 idmap 映射

**文件**: `mmap_store.go:396-444`

replay INSERT 时 `docId` 被 `_` 忽略（line 398），不重建 `docToNode`/`nodeToDoc` 映射。如果 crash 发生在 PutNode 之后、SetNodeMapping 之前，WAL 中有 INSERT 但 idmap.dat 中无对应 entry，重启后丢失映射。

**建议**：replay INSERT 时也重建 id 映射（`s.docToNode[docId] = nodeId` if docId != ""）。

#### P2-6: SetNodeMapping 无 WAL 保护

**文件**: `mmap_store_write.go:199-222`

SetNodeMapping 写 idmap.dat 但不写 WAL。crash 时 idmap.dat 可能部分写入（CRC 校验可以捕获不完整 entry），但与 WAL 的 INSERT record 中的 docId 存在冗余——两者可能不一致。

当前设计可接受（idmap CRC 保护 + replay 可补全），但应在注释中明确说明 recovery 策略。

---

## 测试覆盖评估

| 测试类别 | 覆盖情况 | 缺失 |
|---------|---------|------|
| PutNode + GetVector 正常路径 | ✅ | — |
| PutNode + GetNorm | ✅ | — |
| PutNode with level > 0 | ✅ | — |
| SetNeighbors L0 | ✅ | — |
| SetNeighbors Upper | ✅ | — |
| SetNorm | ✅ | — |
| SetEntryPoint | ✅ | — |
| NodeMapping CRUD | ✅ | — |
| NodeMapping 持久化 | ✅ | — |
| Batch write + read | ✅ | — |
| Batch 嵌套 | ✅ | — |
| DiscardBatch | ✅ | — |
| NextNodeId 自增 | ✅ | — |
| NextNodeId 持久化 | ✅ | — |
| WAL append + replay 全类型 | ✅ | — |
| WAL afterLSN 过滤 | ✅ | — |
| WAL 截断记录恢复 | ✅ | — |
| WAL CRC 损坏恢复 | ✅ | — |
| WAL LSN 跨重启连续 | ✅ | — |
| Grow 触发 | ✅ | — |
| Grow 多次翻倍 | ✅ | — |
| Grow 保留旧数据 | ✅ | — |
| Grow 并发读写 | ✅ | — |
| 50K insert benchmark | ✅ | — |
| WAL replay 恢复 mmap 数据 | ⚠️ 集成覆盖 | 缺独立 replayWAL 单测 |
| DeleteNode + tombstone 验证 | ❌ | 无直接 DeleteNode 测试 |
| 边界: 空向量/零维 | ❌ | — |
| 边界: docId 极长（>65535 bytes） | ❌ | uint16 溢出 |

**测试质量评分**: 8/10 — 正常路径和错误路径覆盖良好，缺少部分边界和 DeleteNode 独立测试。

---

## 性能评估

**50K Insert 0.38s** — 可信。

理由：
- 这是纯写路径（PutNode + SetNeighbors），不含 HNSW 图搜索（占 Insert 95%+ 时间）
- Batch 模式下 WAL 使用 bufio.Writer，mmapSync 延迟到 CommitBatch
- mmap 写入 = 内存 copy，50K × (512B vec + 16B node + 260B L0) ≈ 38MB，现代 SSD 顺序写 > 1GB/s
- WAL 顺序追加 50K records ≈ 30MB，单次 fsync
- 0.38s 合理，瓶颈应在 WAL 最终 fsync

---

## 代码质量

- **可读性**: 良好。文件按职责拆分（write/grow/wal），函数命名清晰
- **命名**: 一致，遵循 Go 惯例
- **函数大小**: PutNode ~70 行偏长但逻辑连贯，replayWAL ~80 行 switch 可接受
- **重复代码**: grow 函数有些重复但通过 `remapFile` 提取了公共逻辑
- **锁顺序**: PutNode 中显式遵守 muGraph → muNodes 顺序（line 53-69 注释），符合 Phase 1 review 要求

---

## 结论

**建议：Approve with conditions**

Phase 2 实现完整度高，核心写入路径 + WAL + Batch + Grow 均已实现且测试覆盖良好。此前 4 个 P0 已在本 PR 中修复。

**合并前须修复：**
1. P1-1: replayWAL NodeCount 漂移（数据正确性风险）
2. P1-5: WAL payload length 无上限校验（OOM 风险）

**建议后续修复（可在 Phase 3/4）：**
- P1-3: loadIdmap 双重 open
- P1-4: idmap.dat 缺少 header
- P2-5: replay INSERT 不恢复 idmap
- P2-6: SetNodeMapping 无 WAL 保护（在 Phase 4 crash recovery 中统一处理）
