# Haystack Task Board

## Done ✅
| ID | 任务 | Owner | PR | 完成日期 |
|----|------|-------|----|---------|
| HAY-001 | 覆盖度提升 90% | Dev | — | 2026-04-04 |
| HAY-002 | 多语言索引（gse + 停用词） | Dev | #35/#37/#38/#39/#40 | 2026-04-06 |
| HAY-003 | 文件统计不准（状态管理重构） | Dev | #31/#32/#33/#34 | 2026-04-05 |
| HAY-004a | HNSW 设计定稿 | 飞马 | — | 2026-04-14 |
| HAY-004b | NodeStore + Pebble 持久化 | Dev | #42 | 2026-04-14 |
| HAY-004c | HNSW 算法核心（内存 mock） | Dev | #42 | 2026-04-14 |
| HAY-004d | Pebble 替换 mock | Dev | #44 | 2026-04-14 |
| HAY-004e | 并发安全 | Dev | #46 | 2026-04-14 |
| HAY-004f | 性能验证 & E2E（参数化 benchmark + SIFT 真实数据验证） | Dev | #49 | 2026-04-17 |
| HAY-004f-qa | QA 验收：HNSW 性能 & E2E | QA | #50 | 2026-04-17 |
| HAY-004g | HNSW Insert upsert 修补（重复 docId 孤儿节点） | Dev | #51 | 2026-04-17 |
| HAY-006 | 补充 upsert 测试场景 | Dev | #52 | 2026-04-17 |
| HAY-008 | searchcore 剥离（搜索内核独立 module，P1-P5） | Dev | PR #62 | 2026-06-12 |

## In Progress 🔵
| ID | 任务 | Owner | Branch | 说明 |
|----|------|-------|--------|------|
| HAY-007 | mmap flat file 存储引擎 | Dev | PR #57 | Phase 4 完成（测试/E2E/集成），等飞马 review |

## Backlog 📋
（空）

## 设计文档
- HAY-008 searchcore 剥离：见 PR #62（持久设计文档待补,详见 §docs 重整）
