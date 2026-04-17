# HAY-007 Task Breakdown Review — Round 2

> Reviewer: Claude Code (Opus 4)
> Date: 2026-04-17
> 对照文档：`docs/design/HAY-007-mmap-store.md` v2
> 上一轮：3 CRITICAL + 5 HIGH，已全部修复

---

## Round 1 修复验证

| ID | 问题 | 修复位置 | 状态 |
|----|------|----------|------|
| C1 | WAL replay 缺失 | Task 2.1 已包含 replay 基础逻辑（顺序扫描+CRC+不完整记录丢弃+回调），Phase 4 只做 Open 集成 | **PASS** |
| C2 | graph_upper.dat 只读路径缺失 | Task 1.3 显式包含 graph_upper.dat 创建/mmap，Task 1.4 增加 upper layer 读取测试 | **PASS** |
| C3 | grow 并发安全间隙 | Task 2.4 采用 atomic.Pointer epoch-based 方案，拆为 2.4a/2.4b 子任务含压力测试 | **PASS** |
| H1 | idmap 与 WAL replay 一致性 | Task 2.1 replay INSERT 时恢复内存 docId→nodeId 映射 | **PASS** |
| H2 | NextNodeId 依赖 freelist | Task 2.5 明确初版只做自增，Phase 3 补充 freelist | **PASS** |
| H3 | 50K benchmark 前置条件 | Task 2.6 明确仅 level-0 路径，Phase 5 Task 5.3 为完整 E2E | **PASS** |
| H4 | Windows mmap truncate | Task 1.1 已知限制段落说明 munmap→truncate→mmap 顺序，Task 2.4 处理 | **PASS** |
| H5 | SetNodeMapping 持久化时机 | Task 2.2 包含 idmap.dat 追加写（append+CRC），Task 3.4 只做 compact+损坏恢复 | **PASS** |

---

## Round 2 新发现

**PASS** — 未发现新的 CRITICAL 或 HIGH 问题。

---

*EOF — 0 CRITICAL, 0 HIGH*
