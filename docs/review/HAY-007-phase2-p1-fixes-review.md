# HAY-007 Phase 2 P1 Fixes Review

PR: codetrek/haystack#55
Reviewer: Pegasus
Date: 2026-04-18

---

## P1-1: replayWAL NodeCount 盲增/盲减

**结论: 已修复**

| 检查项 | 状态 | 说明 |
|--------|------|------|
| `rebuildNodeCount` 函数存在 | OK | `mmap_store.go:493` |
| replay 后调用 | OK | `mmap_store.go:487`，在 `replayWAL()` 末尾、return 前调用 |
| 扫描逻辑正确 | OK | 遍历 `[0, TotalSlots)` 所有 slot，检查 `nodeFlagDeleted` 位，排除 tombstone 后计数 |
| 测试覆盖 | **缺失** | 无专门测试验证重复 replay 后 NodeCount 幂等性 |

**代码质量**: 实现简洁正确。`rebuildNodeCount` 无条件覆盖 `meta.NodeCount`，无论 replay 期间累加了什么值，最终都以扫描结果为准——这是正确的幂等策略。

**遗留问题**: replay 循环体中仍有 `s.meta.NodeCount++` 和 `s.meta.NodeCount--`（`mmap_store.go:468, 483`），虽然被 `rebuildNodeCount` 覆盖不会造成 bug，但属于死代码。建议后续清理。严重程度: LOW。

---

## P1-5: WAL payload length 无上限校验

**结论: 已修复**

| 检查项 | 状态 | 说明 |
|--------|------|------|
| 常量定义 | OK | `maxWalPayloadSize = 64 << 20` (64 MiB), `mmap_wal.go:33` |
| replay 时校验 | OK | `mmap_wal.go:213`: `if length > maxWalPayloadSize` 在 `make([]byte, length)` 之前 |
| 超限处理 | OK | 返回 `fmt.Errorf` 错误，终止 replay。不截断、不跳过——正确选择 |
| 测试覆盖 | **缺失** | 无测试构造 length > 64MiB 的损坏 WAL 验证拒绝行为 |

**代码质量**: 防御位置正确（allocate 前检查），阈值合理（64 MiB 远超正常 record），错误信息包含实际值和上限便于诊断。

**无新问题引入。**

---

## 总结

| 修复 | 状态 | 测试 |
|------|------|------|
| P1-1 rebuildNodeCount | 已修复，逻辑正确 | 缺专项测试 |
| P1-5 WAL payload cap | 已修复，逻辑正确 | 缺专项测试 |

两个 P1 修复实现正确，无新 bug 引入。测试覆盖不足但不阻塞合入。

## 结论: APPROVE

附带建议:
1. (LOW) 清理 replay 循环中的死代码 `NodeCount++/--`
2. (LOW) 补充 `rebuildNodeCount` 幂等性测试和 WAL oversized payload 拒绝测试
