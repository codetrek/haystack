# HAY-007 Phase 1 Code Review — Spec vs Implementation

> Reviewer: Claude Code  
> Date: 2026-04-17  
> Scope: `git diff origin/main...HEAD` vs `docs/design/HAY-007-mmap-store.md` Phase 1

---

## Checklist: Phase 1 Requirements

| # | Spec Requirement | Status | Notes |
|---|-----------------|--------|-------|
| 1 | vectors.dat mmap 读取 | PASS | |
| 2 | nodes.dat mmap 读取 | PASS | |
| 3 | graph_l0.dat mmap 读取 | PASS | |
| 4 | 自封装 syscall.Mmap (Linux/macOS/Windows) | PASS | `mmap_unix.go` + `mmap_windows.go` + `mmap.go` |
| 5 | GetVector | PASS | |
| 6 | GetVectorRef (初版=copy) | PASS | 文档注释正确说明 Phase 5 零拷贝计划 |
| 7 | GetNeighbors (L0 + upper) | PASS | |
| 8 | GetNorm | PASS | |
| 9 | 从 PebbleStore 导出数据到 mmap 文件格式 | **见 HIGH-1** | |
| 10 | meta.bin 原子写 (tmp→fsync→rename) | PASS | |
| 11 | 三平台 `CGO_ENABLED=0` 编译 | 未验证 | CI 层面事项，代码层面无 CGo 依赖 |

---

## Findings

### HIGH-1: 导出来源偏离 spec — MemStore 而非 PebbleStore

**Spec 原文**: "从 PebbleStore 导出数据到 mmap 文件格式"  
**实际实现**: `exportMemStoreToMmap()` — 从 MemNodeStore 导出，无任何 PebbleStore 相关代码。

**影响**: Phase 1 验收条件中的数据迁移路径与 spec 不一致。如果实际使用场景需要从 PebbleStore 迁移，则该功能缺失。如果 MemStore 导出是有意替代（例如 PebbleStore 已废弃），则 spec 应同步更新。

**建议**: 确认意图 — 是 spec 需要更新，还是需要补充 PebbleStore 导出实现。

### HIGH-2: MetaHeader 字段顺序与 spec 不一致

**Spec 定义**:
```
EntryPoint    uint64    // offset 位于 EntryLevel 之前
EntryLevel    uint32
NextNodeId    uint64
```

**实际代码** (`mmap_format.go:26-39`):
```go
// uint32 group first, then uint64 group
MaxLevel   uint32
EntryLevel uint32   // ← 移到了 uint32 组
NodeCount  uint64
TotalSlots uint64
EntryPoint uint64   // ← 移到了 uint64 组
NextNodeId uint64
```

**影响**: 字段重新排列导致二进制布局与 spec 文档不一致。虽然代码通过 `var _ [64]byte = [unsafe.Sizeof(MetaHeader{})]byte{}` 保证了 64 字节大小，且代码内部自洽（读写都用同一个 struct），但：
- 其他语言/工具按 spec 解析 meta.bin 将得到错误数据
- spec 文档和代码不同步，后续开发者会困惑

**建议**: 二选一 — (a) 更新 spec 文档匹配代码布局，并注明 padding 优化原因；(b) 恢复 spec 顺序并使用 explicit padding 字段保持 64 字节。推荐 (a)，代码的排列确实更优。

---

## Summary

| Severity | Count |
|----------|-------|
| CRITICAL | 0 |
| HIGH | 2 |

Phase 1 的核心只读路径实现完整且正确（GetVector / GetVectorRef / GetNeighbors / GetNorm / GetEntryPoint / GetNodeLevel）。mmap 封装跨平台实现到位，文件格式与测试覆盖充分。两个 HIGH 问题均为 spec-代码同步问题，不影响运行时正确性。
