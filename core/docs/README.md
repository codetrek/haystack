# core 设计文档

各模块的架构设计文档：描述**真实系统**（as-built）的结构、状态归属、核心流程与已知缺口。
遵循 bf `project-design-document-standard`：大一点的子系统用**入口页(reading map) + 聚焦子系统页**分层，
不堆成一个百科全书，也不把目录清单当架构。

## 结构约定

```
core/docs/
  <module>/
    architecture.md      入口页：系统边界、子系统归属、状态权威、核心流程概览、reading map
    <subsystem>.md       聚焦页：各自独立演进的边界（按需拆分）
```

`architecture.md` 跟随代码保持准确；它是描述性文档，不是决策日志、方案讨论或分期计划。

## 模块

| 模块 | 入口 | 子系统页 |
|---|---|---|
| `vectorstore` | [architecture](vectorstore/architecture.md) | [storage](vectorstore/storage.md) · [indexing](vectorstore/indexing.md) · [durability](vectorstore/durability.md) · [reclamation](vectorstore/reclamation.md) |
