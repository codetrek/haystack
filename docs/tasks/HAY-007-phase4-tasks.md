# HAY-007 Phase 4: HNSW Integration + E2E

> 对应设计文档 Phase 5（上层图 + 完整集成）  
> 前置：Phase 1-3 已完成（只读/写入/WAL/Checkpoint/Crash Recovery）

## 前置确认（已验证）

- [x] MmapStore 完整实现 NodeStore 接口（14 方法）
- [x] MmapStore 完整实现 BatchableStore 接口（4 方法）
- [x] GetVectorRef 已实现（初版 copy，同 GetVector）
- [x] HNSW `NewHNSWIndex(store NodeStore, ...)` 可直接传入 MmapStore

---

## Task 1: HNSW + MmapStore 基本集成测试

**文件**: `mmap_store_hnsw_test.go`

- [x] 1.1 小规模 Insert → Search 测试（100 vectors, 128d）
  - OpenMmapStore → NewHNSWIndex → Insert 100 docs → Search top-10 → 验证结果非空且有序
  - 参考 `hnsw_integration_test.go:49-68` 的 MemStore 模式
- [x] 1.2 Insert → Delete → Search 测试
  - Insert 50 docs → Delete 10 → Search → 验证已删除 doc 不在结果中
- [x] 1.3 Insert → Search → Delete → Re-insert → Search
  - 验证 freelist slot 复用后搜索仍然正确
- [x] 1.4 Upsert 测试
  - Insert doc → Upsert 同 docId 新向量 → Search → 验证返回新向量的结果

## Task 2: 持久化验证测试

**文件**: `mmap_store_hnsw_test.go`

- [x] 2.1 写入 → Close → 重新打开 → Search
  - Insert 200 docs → Close store → OpenMmapStore 同目录 → NewHNSWIndex → Search → recall 与关闭前一致
- [x] 2.2 写入 → Close → 重新打开 → 继续 Insert → Search
  - 验证持久化后可增量追加
- [x] 2.3 写入 → Close → 重新打开 → Delete → Search
  - 验证持久化后可正确删除
- [x] 2.4 WAL replay → HNSW Search 完整 E2E
  - Insert N docs → 模拟 crash（不调用 Close/Checkpoint）→ 重新 OpenMmapStore（触发 WAL replay）→ NewHNSWIndex → Search → 验证 recall 与 crash 前一致
  - 覆盖场景：crash 发生在 Insert 中途 / Delete 中途 / Checkpoint 中途

## Task 3: 50K SIFT-128 E2E Benchmark

**文件**: `mmap_store_bench_test.go`

- [x] 3.1 实现 SIFT-128 数据加载工具
  - 使用 SIFT-128 数据集（ftp://ftp.irisa.fr/local/texmex/corpus/sift.tar.gz）
  - 实现 fvecs/ivecs 格式解析，取前 50K base vectors + 100 query vectors + ground truth
  - 禁止使用合成数据：SIFT-128 提供真实分布和标准 ground truth，确保 recall 可比
- [x] 3.2 MemStore 50K Insert benchmark（baseline）
  - `BenchmarkHNSW_MemStore_50K_Insert`
- [x] 3.3 MmapStore 50K Insert benchmark
  - `BenchmarkHNSW_MmapStore_50K_Insert`
  - 目标 < 90s
- [x] 3.4 Search benchmark（MemStore vs MmapStore）
  - 1000 次随机 Search top-10，记录 p50/p99 延迟
  - MmapStore 目标 p99 < 5ms

## Task 4: Recall@10 验证

**文件**: `mmap_store_bench_test.go` 或 `mmap_store_hnsw_test.go`

- [x] 4.1 实现 recall@K 计算工具
  - brute-force KNN 作为 ground truth → 与 HNSW 结果对比
- [x] 4.2 MemStore recall@10 验证（baseline）
  - 50K 128d 数据，100 query，recall@10 > 0.95
- [x] 4.3 MmapStore recall@10 验证
  - 同数据同 query，recall@10 > 0.95
  - 验证 MmapStore recall 与 MemStore 一致（差异 < 0.01）

## Task 5: graph_upper.dat 上层图验证

**文件**: `mmap_store_hnsw_test.go`

- [x] 5.1 多层节点生成与上层图读写验证
  - 使用 efConstruction=200 + 固定 seed，Insert 足够数据（≥5000 vectors）确保生成 level>0 节点
  - 验证 graph_upper.dat 中上层图邻居列表的正确性：读出 level>0 节点的邻居，与 MemStore 基线对比
- [x] 5.2 上层图持久化 reopen 验证
  - 写入含多层节点的 HNSW → Close → Reopen → Search → 验证 recall 与关闭前一致
  - 确认 entry point 和 maxLevel 正确恢复
- [x] 5.3 上层图 grow 与 crash recovery
  - 触发 graph_upper.dat grow（大量 Insert 使上层图扩容）→ 在 grow 过程中模拟 crash
  - Replay WAL → 验证上层图结构完整，Search 正确
  - 验证 upper slot 分配在 crash 后无泄漏

## Task 6: MemStore → MmapStore 导出集成验证

**文件**: `mmap_store_hnsw_test.go`

- [x] 6.1 MemStore 构建 HNSW → 导出 → MmapStore 打开 → Search
  - 验证 ExportToMmapStore 后 recall 无损
- [x] 6.2 导出后继续 Insert/Delete
  - 验证导出的 MmapStore 可正常增删

## Task 7: 生产集成声明

> **本 Phase 范围限定为验证测试。** MmapStore 作为 HNSW 可选 backend 的生产集成（在 hnsw.go 或配置层暴露 MmapStore 选项、提供 `WithMmapBackend()` 等 API）将在独立 issue 中跟进。
> 本 Phase 的验收标准确认 MmapStore 通过 NodeStore 接口与 HNSW 完全兼容后，即可开始生产集成工作。

---

## 验收标准

| 指标 | 目标 |
|------|------|
| 50K×128d MmapStore Insert | < 90s |
| Search p99 | < 5ms |
| Recall@10 (SIFT-like) | > 0.95 |
| MmapStore vs MemStore recall 差异 | < 0.01 |
| 持久化 reopen 后 search 正确 | pass |
| WAL replay 后 HNSW search 正确 | pass |
| graph_upper.dat 上层图读写正确 | pass |
| 上层图 grow crash recovery | pass |
| 所有现有测试 | pass |
| `CGO_ENABLED=0` 编译 | pass |
| CI 三平台验证（Linux amd64, macOS arm64, Windows amd64） | 全部 pass |

---

## 完成状态

Phase 4 所有验证测试任务（Task 1-6）已完成并通过。

**已验证能力:**
- MmapStore 通过 NodeStore 接口与 HNSW 完全兼容（Insert/Search/Delete/Upsert）
- 持久化 close→reopen 搜索结果一致
- WAL crash recovery 后搜索正确
- Recall@10 > 0.95（MemStore vs MmapStore 差异 < 0.01）
- graph_upper.dat 多层节点正确生成、持久化、grow+crash recovery
- MemStore→MmapStore 导出无损 recall，支持后续 Insert/Delete

**生产集成（Task 7 scope）:** 本 Phase 范围限定为验证测试。MmapStore 作为 HNSW backend 的生产 API 暴露（`WithMmapBackend()` 等）将在独立 issue 中跟进。
