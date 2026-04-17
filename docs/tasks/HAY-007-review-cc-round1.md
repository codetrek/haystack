# HAY-007 Task Breakdown Review — Round 1

> Reviewer: Claude Code (Opus 4)
> Date: 2026-04-17
> 对照文档：`docs/design/HAY-007-mmap-store.md` v2

---

## CRITICAL

### C1: Phase 2 缺少 WAL replay 实现任务

**设计文档 §4.5** 明确定义了恢复流程：从 `WalCheckpointLSN + 1` 开始 replay，逐条 CRC 校验，不完整记录丢弃。但 Phase 2 的 Task 2.1 只覆盖了 WAL append/CRC/LSN，**没有独立任务覆盖 replay 逻辑**。replay 被推迟到 Phase 4（Task 4.2），但 Phase 2 验证标准写道"WAL replay 正确"——矛盾。

**风险**：Phase 2 结束时 WAL 只能写不能读，无法验证写入正确性；Phase 4 才发现 WAL 格式有缺陷需要返工。

**建议**：将 WAL replay 基础逻辑（顺序扫描 + CRC 校验 + 回调）放入 Task 2.1，Phase 4 只做 Open 流程集成和 crash-point 注入。

---

### C2: Phase 1 缺少 graph_upper.dat 只读路径

**设计文档 §4.1** 定义了 `graph_upper.dat` 文件格式和 `GetNeighbors(id, layer>0)` 读路径。Task 1.4 列出了 `GetNeighbors(id, layer)` 并提到 "layer>0 读 graphUpper"，但 **Phase 1 没有任何任务创建/mmap graph_upper.dat 文件**。Task 1.3 的 `Open` 只说"初始化各文件"但未明确包含 graph_upper.dat。Task 5.1 才做 "graph_upper.dat 按需分配"。

**风险**：Phase 1 的 `GetNeighbors(id, layer>0)` 实现无法测试，Task 1.4 验证不完整。Phase 5 集成时才发现 upper graph 读路径有 bug。

**建议**：Task 1.3 显式列出 graph_upper.dat 的 Open/mmap，Task 1.4 增加 upper layer 读取测试。Task 5.1 聚焦"按需分配写入"而非读。

---

### C3: grow 与并发读的安全间隙未拆任务验证

**设计文档 §4.4** 描述 grow 流程：munmap → ftruncate → 重新 mmap。在 munmap 和重新 mmap 之间存在窗口，此时持有 RLock 的并发 reader 会访问已释放的 mmap 区域 → **SIGBUS/SIGSEGV**。

Task 2.4 验证写道"并发读不崩溃"，但没有独立任务设计并发安全方案。设计文档的 RWMutex 方案本身有缺陷：RLock holder 在 grow Lock() 等待期间仍在读旧 mmap。

**风险**：这是 mmap store 最高风险的技术点。仅靠 Task 2.4 一个 ~100 行任务无法同时实现 grow 逻辑 + 解决并发安全 + 验证正确性。

**建议**：(1) 拆出独立 Task 分析并发安全方案（如 double-buffer/epoch-based 切换），(2) Task 2.4 拆分为 grow 实现 + grow 并发压力测试两个子任务。

---

## HIGH

### H1: idmap.dat 持久化与 WAL 的一致性依赖未明确

Task 3.4 实现 idmap.dat 持久化，Task 2.1 实现 WAL。但 **INSERT WAL 记录包含 docId**（设计文档 §4.1 WAL 类型），WAL replay 时需要恢复 idmap。两个任务分属不同 Phase，没有明确依赖关系。

**风险**：Phase 2 WAL replay INSERT 时无法恢复 docId→nodeId 映射（idmap 还没持久化），crash 恢复后 ID 映射丢失。

**建议**：Task 2.1 WAL replay 必须包含 INSERT 对应的内存 map 恢复逻辑（即使 idmap.dat compact 推迟到 Phase 3）。在 Task 2.1 依赖中显式标注。

---

### H2: Task 2.5 NextNodeId 依赖 freelist，但 freelist 在 Phase 3

Task 2.5 实现 `NextNodeId()`，设计文档 §4.2 写道 "freelist 有则 pop，否则 meta.NextNodeId++"。但 freelist 在 Phase 3 Task 3.1/3.2 才实现。

**风险**：不是功能 bug（Phase 2 没 Delete 所以 freelist 为空），但 Task 2.5 的代码必须预留 freelist 接口，否则 Phase 3 需要重写 NextNodeId。

**建议**：Task 2.5 说明中注明"初版只做 meta.NextNodeId++，Phase 3 补充 freelist 逻辑"，避免预留过度设计但也避免返工。

---

### H3: Phase 2 验收标准 "50K Insert < 90s" 缺少前置条件

Task 2.6 做 50K Insert benchmark，但 Phase 2 尚无 graph_upper.dat 写入（Phase 5 Task 5.1）。如果 HNSW Insert 需要写 upper layer neighbors，Phase 2 的 50K benchmark 会 panic 或 silently skip upper layers。

**风险**：Phase 2 benchmark 结果不反映真实性能；Phase 5 集成后性能退化超出 90s 目标。

**建议**：Task 2.6 明确说明 benchmark scope（仅 level-0，或 mock upper store），Phase 5 Task 5.3 才是完整 E2E benchmark。

---

### H4: Windows mmap truncate 问题（开放问题 #3）无任务覆盖

设计文档 §8 开放问题 #3 明确标注 "Windows 下 mmap 文件无法 truncate——需要先 munmap 再 truncate。grow 流程需测试"。但 Task 2.4（grow）和 Task 1.1（mmap 封装）均未提及 Windows 特殊处理。

**风险**：Phase 2 grow 实现在 Windows CI 上 panic，阻塞三平台交付。

**建议**：Task 1.1 增加 Windows grow 兼容性设计（munmap→truncate→mmap 顺序），Task 2.4 验证增加 Windows CI 显式测试。

---

### H5: SetNodeMapping 持久化时机不明

设计文档 §4.2 接口映射表：`SetNodeMapping` = "更新内存 map + 追加 idmap.dat（带 CRC）"。但 Task Breakdown 中 **Phase 2 没有任何任务覆盖 SetNodeMapping 的 idmap.dat 追加写**。Task 2.2 只列了 PutNode/SetNeighbors/SetNorm/SetNodeMapping，但 idmap.dat 写入在 Task 3.4。

**风险**：Phase 2 SetNodeMapping 只更新内存 map 不写盘，crash 后 ID 映射全丢。与 WAL replay 依赖同一问题（见 H1），但影响范围更大——正常 Close→Open 也丢映射。

**建议**：Task 2.2 或 Task 2.5 中增加 idmap.dat 追加写的基础实现（仅 append + CRC），Task 3.4 只做 compact + 损坏恢复。

---

*EOF — 3 CRITICAL, 5 HIGH*
