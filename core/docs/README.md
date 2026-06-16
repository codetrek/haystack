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
| `vectorstore` | [architecture](vectorstore/architecture.md) · [Phase 1 plan](vectorstore/plans/0001-phase-1-records-segments.md) | 架构完成；Phase 1 计划完成（待执行） |
