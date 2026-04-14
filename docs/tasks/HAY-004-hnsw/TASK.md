# HAY-004 HNSW 向量搜索引擎

## 状态
- Status: design
- Owner: Architect（负责方案设计）
- Created: 2026-04-06

## 背景
建军在 2026-04-04 提出：引入 HNSW 数据库用于向量搜索。

## 需求
- Go 原生 HNSW 实现
- 内存占用低
- 数据持久化到磁盘（Pebble）
- 用时动态加载

## 目标
- 设计生产可用的 HNSW + Pebble 持久化方案
- 明确性能目标和验收标准

## 进展
- haystack-hnsw 项目不存在，确认为从零设计
- 完成架构评估：haystack 现有存储层（PebbleDB）可直接复用
- 完成技术调研：Go HNSW 生态、持久化策略、性能预估
- **设计文档初稿完成**（`design.md`）
  - 推荐方案：基于 coder/hnsw 算法 + Pebble 持久化改造
  - 4 阶段实施路径
  - 性能目标：搜索 < 20ms, 插入 < 5ms

## Next Step
等建军 review 设计文档并拍板。

## 讨论点（待建军决策）
1. Embedding 来源：本地模型 vs API？影响维度（384 vs 1536）
2. 删除策略：标记删除 + 定期重建 vs 即时重连？
3. 是否需要过滤搜索（按文件类型等）？

## 依赖
- 无
