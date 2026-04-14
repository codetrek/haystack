# Haystack Task Board

| ID | 任务 | 状态 | Owner | PR |
|----|------|------|-------|----|
| HAY-001 | 覆盖度提升 90% | done | — | — |
| HAY-002 | 多语言索引（gse + 停用词） | done | — | #35/#37/#38/#39/#40 |
| HAY-003 | 文件统计不准（状态管理重构） | done | — | #31/#32/#33/#34 |
| HAY-004 | HNSW 向量搜索引擎 | blocked | Architect | — |

## HAY-004 详情

> 状态：blocked — 3 个 P0 致命缺陷，等建军定方向（修 vs 重写）

| 子任务 | 描述 | Owner | 状态 | 依赖 |
|--------|------|-------|------|------|
| HAY-004a | 设计定稿 + 决策点 | Architect | blocked | 等建军 |
| HAY-004b | NodeStore + Pebble 持久化层 | Dev | backlog | 等 004a |
| HAY-004c | HNSW 算法核心 | Dev | backlog | 等 004b |
| HAY-004d | LRU 分层缓存 | Dev | backlog | 等 004b |
| HAY-004e | Haystack 集成 | Dev | backlog | 等 004c, 004d |
| HAY-004f | 性能验证 + E2E | QA | backlog | 等 004e |

### 等建军决策

1. Embedding 来源：本地模型 vs API？（影响维度 384 vs 1536）
2. 删除策略：标记删除 + 定期重建 vs 即时重连？
3. 是否需要过滤搜索（按文件类型等）？
