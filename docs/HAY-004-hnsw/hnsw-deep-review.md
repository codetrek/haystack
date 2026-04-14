# HAY-004 HNSW 深度审查报告

> 作者：Architect (via Copilot) | 日期：2026-04-06
> 审查范围：`origin/user/jianjun/hnsw` 分支，hnsw + hnsw2 两个包

## 结论

**当前实现是原型/早期 MVP 阶段，核心算法有 3 个致命缺陷，功能上基本不可用。**

---

## 🔴 P0 - 致命缺陷（系统完全无法正常工作）

### 1. 持久化完全失效
- **文件**: `hnsw/node_data.go:4-9`, `hnsw/storage.go:106-125`
- **问题**: `nodeData` 所有字段未导出（小写），`encoding/gob` 无法编码未导出字段，静默忽略
- **影响**: 每次 SaveNode 写入空数据，重启后图完全为空
- **修复**: 字段改为导出，或实现自定义序列化

### 2. 搜索提前终止 Bug
- **文件**: `hnsw/graph_pebble.go:454`
- **问题**: results 是最小堆但当最大堆用，`current.dist > (*results)[0].dist` 意为"候选比最近结果还远就停"
- **影响**: 搜索在找到第 k 个结果后立即停止，recall 接近 0
- **修复**: results 应改为最大堆

### 3. 插入算法严重偏离论文
- **文件**: `hnsw/graph_pebble.go:113-167`
- **问题**: 完全跳过论文 INSERT 的第 1 步（高层贪心导航），直接从新节点 level 开始
- **影响**: 高层导航被跳过，搜索起点不是最优

---

## 🟠 P1 - 严重缺陷（算法正确性问题）

### 4. Entry Point 不更新
- **文件**: `hnsw/graph_pebble.go:86-168`
- **问题**: 新节点层级高于 entry point 时不更新 entry point
- **影响**: entry point 停留在低层节点，丧失层级跳跃优势

### 5. randomLevel() 概率分布不符合论文
- **文件**: `hnsw/graph_pebble.go:527-549`
- **问题**: 用几何分布 Ml=0.25 + maxLevel 截断，论文是 `floor(-ln(uniform) * mL)`，mL=1/ln(M)≈0.36
- **影响**: 层级分布偏低，高层节点过少

### 6. 没有 M0=2*M 的零层特殊处理
- **文件**: `hnsw/graph_pebble.go:553-653`
- **问题**: 所有层使用相同 M，论文要求零层 M0=2*M
- **影响**: 零层连接密度不足，recall 降低

### 7. selectDiverseNeighbors 不是论文 Algorithm 4
- **文件**: `hnsw/graph_pebble.go:773-825`
- **问题**: 用 maximin 多样性选择代替论文的启发式选择，且有 O(n²) I/O

### 8. Search 返回结果可能 panic
- **文件**: `hnsw/graph_pebble.go:388`
- **问题**: `results[:k]` 没有边界检查，节点数 < k 时 panic

### 9. Entry Point 删除后不一致
- **文件**: `hnsw/graph_pebble.go:329-345`
- **问题**: 删除 entry point 时写入 id=0 而非删除 key，重启后指向不存在的节点

---

## 🟡 P2 - 中等问题（并发安全与性能）

### 10. Storage 缓存 TOCTOU 竞争
- **文件**: `hnsw/storage.go:128-157`

### 11. 缓存存储值拷贝导致数据不一致
- **文件**: `hnsw/lru_cache.go`, `hnsw/storage.go`

### 12. BufferedStorage 无并发保护
- **文件**: `hnsw/cached_storage.go`

### 13. pruneNeighbors 与 ensureBidirectionalLinks 可能死循环
- **文件**: `hnsw/graph_pebble.go:553-709`

---

## 🔵 P3 - 次要问题

### 14. hnsw2 包未完成
- 只有空壳，核心图算法未实现

### 15. 持久化格式无版本号和校验

### 16. 距离函数比较用 reflect

---

## 测试覆盖评估

| 覆盖的场景 | 缺失的场景 |
|------------|------------|
| 基本 Add/Delete/Search | ❌ 无 recall@K 测试 |
| 邻居双向性验证 | ❌ 无并发插入/搜索测试 |
| LRU 缓存基本功能 | ❌ 无大规模 recall 基准测试 |
| 层级分布统计 | ❌ 无持久化后重启测试 |
| 高维数据基本测试 | ❌ 无 edge case（k > 图大小等）|

---

## 建议

P0 缺陷叠加意味着当前实现功能上不可用。建议：
1. 参考论文原文重新实现核心 INSERT 和 SEARCH-LAYER
2. 或考虑使用成熟的第三方库（如 hnswlib 的 Go binding）
3. 如果自研，建议从 P0 开始逐一修复，每步加 recall@K 回归测试
