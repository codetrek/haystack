# core 设计文档

各模块的架构设计与分期实现计划。

## 结构约定

```
core/docs/
  <module>/
    architecture.md      模块架构设计（决策 + 依据）
    plans/               分期实现计划，每期一份 spec（writing-plans 风格：bite-size TDD 任务）
      <NNNN-phase-name>.md
```

新增模块设计时，建 `core/docs/<module>/` 并放 `architecture.md`，实现计划进 `plans/`，并在下表登记。

## 模块

| 模块 | 文档 | 状态 |
|---|---|---|
| `vectorstore` | [architecture](vectorstore/architecture.md) · plans: [P1](vectorstore/plans/0001-phase-1-records-segments.md) · [P2](vectorstore/plans/0002-phase-2-seal-index-merge.md) · [P4](vectorstore/plans/0003-phase-4-reclamation-merge.md) · [P5](vectorstore/plans/0004-phase-5-payload-filter.md) · [P6](vectorstore/plans/0005-phase-6-multi-index.md) | 架构完成；全部 phase 实现完成（P1 records/段 · P2 封存+异步建图+N路合并+manifest/恢复 · P4 回收/合并 · P5 结构化 payload+过滤 · P6 多命名索引；P3 payload 并入 P1/P5）。在 `feat/vectorstore-phase1` 分支，未合并 |
