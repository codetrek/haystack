# HAY-004 HNSW 性能优化方案

> **状态**: Draft
> **创建**: 2026-04-15
> **作者**: 飞马 (架构)
> **关联**: HAY-004

---

## 1. 现状分析

### 1.1 当前性能基线

| 场景 | 耗时 | 单条耗时 |
|------|------|----------|
| 10K×128d insert | 85.7s | ~8.5ms/op |
| Pebble batch (参考) | — | 4.5ms/op |
| 100K×384d insert (预估) | 15-20min | — |

**目标**: 100K×384d insert < 2min（即 ~1.2ms/op）

### 1.2 pprof 热点分布

| 函数 | CPU 占比 | 说明 |
|------|----------|------|
| `selectNeighborsHeuristic` | 46% | 邻居选择，含大量距离计算 |
| `CosineDistance` | 36% | 手写实现，无 SIMD 加速 |
| GC | ~24% | 临时对象分配压力大 |

> 注: 三项有重叠（selectNeighborsHeuristic 内部调用 CosineDistance），总和 >100% 是正常的。

### 1.3 已完成优化

- **GetVector 零拷贝**: 消除 Pebble 读取时的内存拷贝，整体提升 ~33%
- **efConstruction 200→128**: 减少构建时搜索范围，牺牲少量 recall 换取速度

### 1.4 约束条件

- **纯 Go 实现**，不使用 CGo
- 不引入外部 C/C++ 依赖
- 保持 recall@10 ≥ 0.95（不为速度大幅牺牲精度）

---

## 2. 优化方案（按优先级）

### P0: SIMD 距离计算 — viterin/vek

**问题**: 手写的 `CosineDistance` 是标量实现，占 CPU 36%。

**方案**: 引入 [viterin/vek](https://github.com/viterin/vek) 库，用 `vek.CosineSimilarity` 替换手写实现。

**原理**:
- vek 利用 Go 编译器自动向量化 + unsafe 操作实现 SIMD 级别的向量运算
- 纯 Go 代码，无 CGo 依赖，符合约束
- 支持 AVX2/SSE 指令集（通过编译器优化自动生效）

**改动范围**:
1. `go get github.com/viterin/vek`
2. 替换 `CosineDistance(a, b []float32) float32` 的实现体
3. 注意: vek 返回的是 similarity（越大越近），需要转换为 distance（`1 - similarity`）

**预期收益**:
- 距离计算提升 **3-5x**
- 距离计算占总 CPU 36%，整体 Insert 提升约 **30-40%**
- 8.5ms → ~5-6ms/op

**复杂度**: 🟢 低（替换一个函数）

**风险**: 
- 需要验证 vek 的数值精度与手写实现一致
- 需要 benchmark 验证实际加速比

---

### P1: selectNeighborsHeuristic 距离缓存

**问题**: `selectNeighborsHeuristic` 占 CPU 46%，其中大量时间花在重复计算候选节点之间的距离。

**分析**:
- HNSW 的 heuristic 邻居选择需要计算候选节点两两之间的距离
- 同一个节点的距离在 insert 过程中被多次计算（不同层、不同邻居的 pruning）
- hnswlib 参考实现中维护了距离缓存

**方案**: 在单次 insert 操作的上下文中维护距离缓存 map。

**改动范围**:
1. 定义 `distanceCache` 结构（key: `(nodeID_a, nodeID_b)` → value: `float32`）
2. 在 `insert()` 入口创建缓存，通过参数传递到 `selectNeighborsHeuristic`
3. 距离计算前先查缓存，miss 时计算并写入
4. key 做归一化：`min(a,b), max(a,b)` 确保对称

**预期收益**:
- 减少 **50%+** 的距离计算调用
- 整体提升 **20-30%**
- 叠加 P0 后: ~5-6ms → ~3.5-4.5ms/op

**复杂度**: 🟡 中（需要修改函数签名，传递缓存上下文）

**风险**:
- 缓存 map 本身有内存开销，需要控制大小
- 单次 insert 结束后缓存失效，不会无限增长
- 需要 benchmark 验证缓存命中率

---

### P2: GC 压力优化

**问题**: GC 占 CPU ~24%，说明有大量临时对象分配。

**分析**:
- Priority queue 每次 insert 都新建
- 候选列表 `[]Candidate` 动态增长
- `interface{}` 类型导致装箱/拆箱

**方案**: 多管齐下减少 GC 压力。

**改动范围**:

#### 2a. sync.Pool 池化
```go
var pqPool = sync.Pool{
    New: func() interface{} {
        return &PriorityQueue{items: make([]Item, 0, 128)}
    },
}
```
- 池化 priority queue
- 池化候选列表 `[]Candidate`
- 使用后 Reset + Put 回池

#### 2b. 预分配 slice
- 候选列表按 `efConstruction` 预分配容量
- 邻居列表按 `M` 预分配
- 避免 `append` 触发的动态扩容和旧 slice 的 GC

#### 2c. 减少 interface{} 使用
- Priority queue 的 item 使用具体类型替代 `interface{}`
- 避免 float32/int 的装箱开销

**预期收益**:
- GC 占比从 24% 降到 **<10%**
- 整体提升 **15-20%**
- 叠加 P0+P1 后: ~3.5-4.5ms → ~2.5-3.5ms/op

**复杂度**: 🟡 中（多处改动，但每处都不复杂）

**风险**:
- sync.Pool 在高并发下效果好，单线程场景收益可能偏低
- 需要仔细处理 Reset 逻辑，避免数据泄漏

---

### P3: 预计算向量范数

**问题**: `CosineDistance` 每次调用都要计算两个向量的 L2 范数，而同一个向量的范数在不同距离计算中被反复求解。

**方案**: 插入时预计算并持久化存储向量的 L2 范数。

**改动范围**:
1. 向量存储时额外存一个 `float32` 的范数值
2. `CosineDistance` 改为接收预计算范数：`CosineDistanceWithNorm(a, b []float32, normA, normB float32)`
3. 新插入的向量在写入 Pebble 前计算范数
4. 查询向量也预计算范数

**预期收益**:
- 距离计算再快 **30-40%**（省去两次 `sqrt(sum(x²))` 计算）
- 整体提升 **10-15%**（叠加 P0 的 SIMD 后，距离计算占比已下降）
- 叠加 P0+P1+P2 后: ~2.5-3.5ms → ~2-3ms/op

**复杂度**: 🟢 低（存储多一个 float32，改一个函数签名）

**风险**:
- 存储格式变更，需要考虑向后兼容或数据迁移
- 如果使用 vek（P0），需要确认 vek 是否支持传入预计算范数（可能不支持，需要自己包装）

**注意**: 如果 P0 使用了 vek 的 `CosineSimilarity`，vek 内部会自己算范数。此时 P3 的收益取决于能否绕过 vek 用自定义的 norm-aware 距离函数。需要评估是直接用 vek 还是用 vek 的底层向量运算（`vek.Dot`, `vek.Norm`）组合自己的距离函数。

---

### P4: 批量插入优化

**问题**: 当前逐条 insert，每条都走完整的 search → connect → prune 流程。

**方案**: 批量插入时先构建局部子图，再合并到主图。

**改动范围**:
1. `BatchInsert(vectors [][]float32)` 接口
2. 批内向量先互相建图（跳过主图搜索）
3. 批内图构建完成后，找到与主图的连接点
4. 合并两个图，重新 prune 边界节点的邻居

**预期收益**:
- 批量场景提升 **2-3x**
- 叠加 P0-P3 后: ~2-3ms → ~1-1.5ms/op（批量场景）

**复杂度**: 🔴 高

**风险**:
- 实现复杂，容易引入 recall 下降
- 批内子图质量取决于批大小和数据分布
- 需要大量测试验证正确性
- 合并逻辑是全新代码，没有现成参考

---

## 3. 综合预期

### 3.1 累积效果估算

| 阶段 | 单条耗时 | 100K×384d 预估 | 相对基线 |
|------|----------|----------------|----------|
| 基线 | 8.5ms | 15-20min | — |
| +P0 (SIMD) | ~5-6ms | 9-12min | -35% |
| +P1 (距离缓存) | ~3.5-4.5ms | 6-8min | -50% |
| +P2 (GC 优化) | ~2.5-3.5ms | 4-6min | -65% |
| +P3 (范数预计算) | ~2-3ms | 3-5min | -70% |
| +P4 (批量插入) | ~1-1.5ms | 1.5-2.5min | -82% |

> ⚠️ 以上为估算值，384d 向量比 128d 更重（距离计算量 3x），实际数字可能有偏差。
> 384d 的 SIMD 加速效果可能更显著（更长的向量 = SIMD 优势更大）。

### 3.2 达标分析

**目标**: 100K×384d < 2min

- **P0-P3 完成后**: 预估 3-5min，**接近但可能未达标**
- **P0-P4 全部完成后**: 预估 1.5-2.5min，**基本达标或接近**
- 如果仍有差距，可考虑补充措施（见第 4 节）

---

## 4. 补充措施（如 P0-P4 仍不够）

如果 P0-P4 全部完成后仍未达标，可考虑以下降级/激进措施：

### 4a. 降低 efConstruction
- 从 128 进一步降到 64
- 预期: 构建速度提升 ~40%，recall@10 可能从 0.97 降到 0.93-0.95
- 权衡: 需要跑 recall benchmark 确认可接受

### 4b. 向量量化
- float32 → float16（半精度）
- 存储减半，距离计算带宽减半
- 预期: 额外提升 30-50%
- 风险: 精度损失，需要评估对 recall 的影响

### 4c. 并行化 Insert
- 当前 insert 是单线程串行
- HNSW 支持有限并行（不同层的搜索可以并行，但连接需要锁）
- 复杂度高，且 Go 的 goroutine 调度本身有开销

---

## 5. 实施计划

### 建议执行顺序

```
P0 (SIMD) → P3 (范数预计算) → P1 (距离缓存) → P2 (GC) → P4 (批量)
```

**理由**:
- P0 和 P3 改动小、风险低、收益确定 → 先做
- P1 依赖理解当前距离计算的调用模式 → P0 改完后更清晰
- P2 是全局优化，P0-P3 做完后 profiling 数据更准确
- P4 复杂度最高，留到最后，视情况决定是否做

### 每步验收标准

每完成一个优化项，必须：
1. **Benchmark**: 跑 `BenchmarkInsert10K` 对比前后
2. **pprof**: 重新 profile，确认热点变化符合预期
3. **Recall**: 跑 recall@10 测试，确认精度未显著下降
4. **Test**: 所有现有测试通过

---

## 6. 开放问题

1. **vek 与范数预计算的兼容性**: P0 用 vek 后，P3 是否还有收益？需要看 vek 的 API 是否支持传入预计算范数。
2. **384d 的实际 profile**: 当前 profile 基于 128d，384d 的热点分布可能不同（距离计算占比可能更高）。
3. **并发 insert 是否在路线图中**: 如果未来需要并发 insert，P2 的 sync.Pool 和 P1 的缓存设计需要考虑线程安全。
4. **Pebble 的 4.5ms/op 瓶颈**: 即使 HNSW 计算优化到 0，Pebble 写入本身 4.5ms 也是下限。需要确认这是否包含在 8.5ms 中，还是另算的。

---

## 附录

### A. 参考实现
- [hnswlib (C++)](https://github.com/nmslib/hnswlib) — 距离缓存、SIMD 实现参考
- [viterin/vek (Go)](https://github.com/viterin/vek) — 纯 Go SIMD 向量运算
- [usearch (C++/Go bindings)](https://github.com/unum-cloud/usearch) — 高性能 ANN 参考

### B. Benchmark 命令
```bash
# 基础 benchmark
go test -bench=BenchmarkInsert -benchtime=3s -count=5 ./pkg/index/hnsw/

# pprof
go test -bench=BenchmarkInsert -cpuprofile=cpu.prof ./pkg/index/hnsw/
go tool pprof -http=:8080 cpu.prof

# recall 测试
go test -run=TestRecall -v ./pkg/index/hnsw/
```
