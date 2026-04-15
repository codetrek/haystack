# HNSW 性能基准调研报告

> 2026-04-15 | HAY-004 | 飞马 subagent

## 背景

我们自研 Go HNSW（haystack 仓库），当前性能：
- **10K × 128d 插入**：85 秒（~117 QPS）
- **单条插入延迟**：p50 1.45ms，p95 3.49ms
- **Recall@10**：0.884
- **瓶颈**：`selectNeighborsHeuristic` 占 47% CPU，距离计算无 SIMD，Pebble 持久化

---

## 1. 业界 HNSW 性能基准

### hnswlib (C++, nmslib)
| 规模 | 维度 | 插入 QPS (单线程) | 搜索 QPS | Recall |
|------|------|-------------------|----------|--------|
| 10K | 128d | ~50,000–80,000 | ~100,000+ | >0.99 |
| 1M | 128d | ~30,000–50,000 | ~50,000–80,000 | >0.95 |

- 纯内存，C++ SIMD 优化（SSE/AVX）
- M=16, efConstruction=200 默认参数
- **10K×128d 插入约 0.1–0.2 秒**（vs 我们的 85 秒，差距 **400–800x**）

### USearch (C/C++, unum-cloud)
在 AWS c7g.metal (64 核 Graviton 3) 上，f32×256d：
| efConstruction | 插入 QPS | 搜索 QPS | Recall@1 |
|----------------|----------|----------|----------|
| 128 | 75,640 | 131,654 | 99.3% |
| 64 | 128,644 | 228,422 | 97.2% |

- 支持 f16/i8 量化，i8 可达 115K insert QPS / 274K search QPS
- **有官方 Go binding**（CGO），接口简洁
- 支持持久化（mmap）

### Faiss (Meta, C++)
- HNSW 实现默认 M=32, efConstruction=40
- 1M×128d 插入 ~30-60 秒（单线程），搜索 QPS 数万级
- 重量级库，主要面向 Python/C++，Go 绑定不成熟

### Go 生态
| 库 | 特点 | 性能 |
|----|------|------|
| **coder/hnsw** | 纯 Go，用 viterin/vek 做距离计算 | 轻量但无 SIMD，纯内存 |
| **viterin/hnsw** | 纯 Go | 类似 coder/hnsw |
| **chromem-go** | 嵌入式向量 DB，暴力搜索 | 无 ANN 索引，小规模可用 |

纯 Go HNSW 实现普遍比 C++ 慢 **10–50x**，主要因为：
- Go 编译器不做 SIMD 自动向量化
- GC 压力（大量小对象/slice）
- 无法内联热路径的汇编优化

---

## 2. 我们自研实现慢的原因分析

### 2.1 Go vs C++ 向量运算差距
- C++ 编译器自动向量化 + 手写 SIMD intrinsics，128d float32 距离计算可用 AVX-256 一次处理 8 个 float
- Go 标准库无 SIMD，128d 距离计算比 C++ 慢 **5–15x**
- viterin/vek 有部分 SIMD 支持但覆盖有限

### 2.2 selectNeighborsHeuristic 占 47% CPU — **偏高但不异常**
- hnswlib 中该函数也是热路径，但通常占 20–30%
- 47% 偏高，可能原因：
  - 候选集过大（efConstruction 设太高？）
  - 内部排序/堆操作 Go 实现效率低
  - 每次调用重新分配 slice
- **优化方向**：复用 buffer、减小 efConstruction、优化堆实现

### 2.3 无 SIMD 的影响
- 距离计算是 HNSW 最内层循环，每次插入调用数百到数千次
- SIMD 对 128d float32 可加速 **4–8x**（AVX-256）到 **8–16x**（AVX-512）
- 这单项就能让整体插入快 **3–5x**

### 2.4 Pebble 持久化 overhead
- 每次插入涉及 Pebble KV 读写，序列化/反序列化开销
- 纯内存 HNSW（hnswlib）没有这个成本
- 估算持久化带来 **2–5x** 额外开销（取决于 batch 策略）
- 但即使去掉持久化，纯 Go 内存实现仍会比 C++ 慢 10–30x

### 综合估算
| 因素 | 影响倍数 |
|------|----------|
| Go vs C++ (无 SIMD) | 10–15x |
| Pebble 持久化 | 2–5x |
| 算法实现细节（堆/alloc） | 2–3x |
| **总计** | **~40–200x** |

我们实际差距 400–800x，说明还有算法层面的低效（可能是 efConstruction 过高或邻居选择逻辑未优化）。

---

## 3. 替代方案评估

### 方案 A：CGO 绑定 hnswlib
- **性能**：接近原生 C++，10K 插入 < 1 秒
- **成本**：CGO 调用开销小（~100ns/call），但构建复杂度增加
- **风险**：交叉编译困难，调试不便，内存管理跨语言
- **评估**：✅ 可行但维护成本中等

### 方案 B：USearch Go binding（推荐 ✅）
- **性能**：与 hnswlib 持平或更优，有 SIMD + 量化支持
- **Go binding**：官方维护，`github.com/unum-cloud/usearch/golang`
- **特性**：
  - 嵌入式，无需外部服务
  - 支持 mmap 持久化（比 Pebble 更高效）
  - 支持 f16/i8 量化减少内存
  - 活跃维护，MIT 协议
- **成本**：CGO 依赖，但官方 binding 质量高
- **评估**：⭐ **最佳选择** — 性能接近最优，嵌入式，官方 Go 支持

### 方案 C：外部服务（Qdrant/Milvus）
- **不适合**：haystack 要求嵌入式、低内存、单进程
- 引入网络延迟 + 运维复杂度
- **评估**：❌ 不符合需求

### 方案 D：继续优化自研
- 可做的优化：
  - 加 SIMD（Go 汇编或用 viterin/vek 增强版）
  - 优化 selectNeighborsHeuristic（复用 buffer、调参数）
  - Batch 写入减少 Pebble 开销
  - 降低 efConstruction
- **预期收益**：可能提升 5–10x，但仍比 C++ 慢 10–30x
- **评估**：⚠️ 投入大、天花板低

### 方案 E：其他嵌入式方案
| 方案 | 说明 |
|------|------|
| sqlite-vec | SQLite 扩展，暴力搜索，小规模可用 |
| vectorlite | SQLite + hnswlib，有 Go 绑定 |
| Bleve | Go 全文搜索，向量搜索能力弱 |

---

## 4. 结论与推荐

### 推荐：**方案 B — 用 USearch Go binding 替换自研 HNSW**

**理由**：
1. **性能差距不可弥补**：纯 Go 实现的天花板（即使全优化）仍比 C++ 慢 10–30x，无法满足需求
2. **USearch 完美匹配需求**：嵌入式、低内存（支持量化）、有 mmap 持久化、官方 Go binding
3. **投入产出比最高**：替换成本远低于持续优化自研实现
4. **性能预期**：10K×128d 插入应在 **0.1–0.5 秒**（当前 85 秒），提升 **170–850x**

**实施建议**：
1. 先做 PoC：用 USearch Go binding 跑 10K×128d 基准，验证实际性能
2. 适配接口：将现有 HNSW 接口抽象为 interface，方便切换后端
3. 持久化迁移：用 USearch 的 mmap 替换 Pebble 存储
4. 保留 Pebble：仅用于其他 KV 数据，不存向量索引

**备选**：如果 USearch Go binding 有坑（CGO 问题等），退而选方案 A（CGO 绑定 hnswlib）。
