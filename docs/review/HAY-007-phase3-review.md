# HAY-007 Phase 3 Review — 崩溃恢复 + Checkpoint

> Reviewer: Claude (strict)  
> PR: #56  
> Date: 2026-04-18  
> Branch: `feat/hay-007-phase3-checkpoint`

---

## Spec 对照

| Spec 要求 | 实现状态 | 备注 |
|-----------|---------|------|
| WAL checkpoint 机制（LSN-based） | ✅ 已实现 | `checkpointLocked()` — msync → writeMeta → WAL Reset |
| meta.bin 原子写（tmp+fsync+rename） | ✅ 已实现 | `writeMetaHeader`: Create tmp → binary.Write → Sync → Close → Rename |
| 崩溃恢复：读 meta → 从 lastLSN+1 replay WAL → CRC 校验 | ✅ 已实现 | `OpenMmapStore` → `replayWAL` → post-replay checkpoint |
| 5 类 crash point 测试 | ✅ 8 类（超出 spec） | 6a–6h 覆盖 WAL write / msync / meta / partial WAL / partial meta / grow / SetNeighbors / DeleteNode |
| kill -9 后重启索引完整 | ✅ 已实现 | `TestKill9Recovery_E2E` — 1000 vectors + neighbors + deletes |
| Checkpoint 在 Close 时触发 | ✅ 已实现 | `Close()` 调用 `checkpointLocked()` |
| Checkpoint 在 N 次操作后触发 | ✅ 已实现 | `maybeCheckpoint()` + `CheckpointInterval` option（默认 1000） |
| WAL 截断安全 | ✅ 已实现 | `WAL.Reset()` — Flush → Truncate(0) → Seek(0) → buf.Reset，LSN 保持单调 |
| CRC32 校验 | ✅ 已实现 | WAL record CRC + idmap entry CRC |

---

## 发现项

### P0 — 无

无阻塞性问题。

### P1 — 需要关注

#### P1-1: `crashAfterMeta` hook 已声明但未测试

`mmap_store.go:80` 声明了 `crashAfterMeta func()`，`checkpointLocked()` 中已 wired（line 391-392），但 **没有任何测试用例实际设置并触发该 hook**。Test 6c (`TestCrashPoint_AfterMeta`) 测试的是 `crashBeforeTruncate`（meta 写完、WAL 未截断），而非 `crashAfterMeta`。

这意味着 "meta 写完但 WAL 截断前崩溃" 和 "msync 后 meta 写完前崩溃" 两个场景都有测试（6b, 6c），但 `crashAfterMeta` 这个 hook 本身是死代码（测试角度）。

**建议**: 要么在某个测试中使用 `crashAfterMeta`，要么删除它。当前 6c 用 `crashBeforeTruncate` 已覆盖了相同的时间窗口（meta 完成后、truncate 前），所以逻辑上没有遗漏，但 hook 是多余的。

#### P1-2: `compactIdmap` 先 Close 旧 file 后赋值新 file 的顺序风险

```go
// mmap_store_write.go:467-468
s.idmapFile.Close()
s.idmapFile = nf
```

如果 `Close()` 返回错误（如 EIO），代码忽略了该错误。虽然 idmap 的 tmp+fsync+rename 保证了数据完整性，但 **Close 错误应该被记录或返回**，尤其在 fsync-on-close 的文件系统上。

#### P1-3: Checkpoint 期间的并发读安全

`checkpointLocked()` 持有 `muWrite`，但 `compactIdmap` 中对 `s.idmapFile` 的替换（close 旧 → 赋值新）不受 `muDoc` 写锁保护。如果有并发的 `SetNodeMapping` 调用在 `compactIdmap` 执行期间写入旧的 `idmapFile`，可能写入已关闭的 fd。

当前由于 HNSW `h.mu` 串行化了所有写操作，这不会在生产中触发。但 `checkpointLocked` 是一个 exported 行为（通过 `Checkpoint()`），如果未来有人在 HNSW 锁外调用，就会有问题。

**建议**: 在 `compactIdmap` 替换 `idmapFile` 时持有 `muDoc` 写锁。

### P2 — 建议

#### P2-1: `TestAutoCheckpoint_TriggeredAtInterval` WAL size 断言不够严格

```go
// mmap_checkpoint_test.go:239-242
if info.Size() == 0 {
    // WAL could be 0 if exactly at threshold ...
}
```

这个 if 分支体为空，既不 fail 也不 skip。如果 WAL 意外为空，测试静默通过。应断言 WAL size > 0（因为 ops 11-15 应在 WAL 中），或至少加个 `t.Log` 说明。

#### P2-2: `simulateCrash` 中 `wal.Sync()` 可能掩盖真实崩溃场景

所有 crash 模拟都先 `s.wal.Sync()`，确保 WAL 数据落盘。真实 kill -9 场景中，如果 WAL 使用了 buffered writer，最后几条记录可能丢失。当前测试保守偏向 "WAL 完整" 场景，缺少 "WAL 尾部丢失" 的测试（6d 部分 WAL 只测了追加垃圾字节，没测 truncated-mid-record）。

实际上 6d 追加了 5 字节垃圾模拟 partial record，这已经覆盖了部分场景。但真正的 buffered write 丢失（WAL 合法记录被截断到一半）没有直接测试。

#### P2-3: `WAL.Reset()` 未 fsync truncate

```go
func (w *WAL) Reset() error {
    w.buf.Flush()
    w.file.Truncate(0)
    w.file.Seek(0, io.SeekStart)
    w.buf.Reset(w.file)
    return nil
}
```

Truncate 后没有 `Sync()`。如果 Reset 后立即崩溃，文件系统可能还保留旧的 WAL 数据。由于 meta.bin 已经记录了新的 checkpoint LSN，replay 时会通过 LSN 过滤跳过旧记录，所以**功能上不受影响**。但如果追求 defense-in-depth，可以加个 `w.file.Sync()`。

#### P2-4: `nocov` 使用合规

PR 中新增代码没有添加新的 `nocov` 标记。已有的 `nocov` 标记（`replayWAL` error path、`cleanup`、`closeMmaps`）均在不可达/极难触发的错误路径上，合规。

#### P2-5: 测试中直接访问内部字段

`TestCrashPoint_DeleteNodeCrash` 和 `TestKill9Recovery_E2E` 直接读取 `s2.nodes[offset]` 来检查 tombstone flags。这使测试与内部布局紧耦合。建议考虑添加一个 `IsDeleted(id)` 方法供测试使用。不阻塞合入。

---

## 性能

- **Checkpoint 开销**: msync 4 个 mmap region + 1 次 meta.bin 原子写 + 1 次 WAL truncate + 1 次 idmap compact（排序 + 顺序写）。对 50K 节点，idmap compact 约 50K × ~30B = 1.5MB 顺序写，< 10ms。
- **默认间隔 1000 ops**: HNSW Insert 产生 ~10 WAL records（1 INSERT + ~8 SET_NEIGHBORS + 1 SET_ENTRY），约每 100 次 Insert 触发一次 checkpoint。合理。
- **Batch mode**: checkpoint 延迟到 CommitBatch，避免 batch 内多次 checkpoint。正确。

---

## 安全

- CRC32 校验覆盖 WAL record 全部字段（LSN + Length + Type + Payload）。✅
- CRC32 校验覆盖 idmap entry。✅
- meta.bin 原子写防止部分写入。✅
- 不完整/CRC 失败的 WAL record 在 replay 时被丢弃（truncate at last valid offset）。✅
- crash hook 字段为 `func()` 类型，nil 检查保护，生产零开销。✅

---

## 结论

**LGTM with minor comments**。P1-1 是死代码（未使用的 hook），P1-2/P1-3 是防御性编程建议，不影响当前正确性。8 类 crash test + kill-9 E2E 验收测试覆盖充分，超出 spec 要求的 5 类。Checkpoint 机制（msync → atomic meta → WAL truncate）顺序正确，crash 在任何步骤之间都不会导致数据丢失。

建议合入后跟进 P1 items。
