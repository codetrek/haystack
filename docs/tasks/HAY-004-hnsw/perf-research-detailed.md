# HAY-004: HNSW 向量索引性能深度调研报告

**日期**: 2026-04-15
**环境**: Linux 6.17.0, AMD EPYC 9V74 80-Core, Go 1.24.2
**数据集**: 10K × 128d 随机向量, cosine distance, efConstruction=200, efSearch=128, M=16

---

## 摘要

| 方案 | 10K 插入时间 | 插入 µs/vec | 搜索 p50 | 搜索 p95 | 搜索 p99 | Recall@10 | CGO |
|------|------------|------------|---------|---------|---------|-----------|-----|
| **Haystack (MemStore)** | 48.07s | 3,339 | 967µs | 1,225µs | 1,252µs | 0.884 | ❌ |
| **USearch Go binding** | 4.68s | 468 | 201µs | 407µs | 843µs | 0.894 | ✅ |

**USearch 比我们的实现快 ~7× 插入, ~5× 搜索，recall 略高。**

---

## 1. Haystack 自研实现 (基准)

### TestBenchmarkSearchLatency10K (10K×128d, MemStore, efC=200, efS=128)
```
Insert time:      48.069s (4,807 µs/vec)
Search p50:       967.143µs
Search p95:       1.224983ms
Search p99:       1.251779ms
Recall@10:        0.8840
```

### Go Benchmark (testing.B, 10K iterations)
```
BenchmarkHNSWInsert-4            10000    3,338,735 ns/op   307,054 B/op   1,743 allocs/op
BenchmarkHNSWSearch-4 (1K index) 1000       343,877 ns/op    77,170 B/op     692 allocs/op
BenchmarkHNSWInsertPebble-4      10000   10,391,309 ns/op   356,020 B/op   2,741 allocs/op
BenchmarkHNSWInsertBatchPebble-4 10000    6,457,317 ns/op   389,400 B/op   2,669 allocs/op
```

**分析**: 每次插入 3.3ms + 1,743 次堆分配，说明 HNSW 图遍历中有大量小对象分配。这是纯 Go 实现的主要瓶颈。

---

## 2. USearch Go Binding

### 编译过程
1. `go get github.com/unum-cloud/usearch/golang` — 成功下载
2. Go binding 通过 CGO 链接 `libusearch_c.so`
3. C 库需要从源码编译：
   ```bash
   git clone --depth 1 https://github.com/unum-cloud/usearch.git
   cd usearch && git submodule update --init --recursive
   cmake -B build -DCMAKE_BUILD_TYPE=Release -DUSEARCH_BUILD_LIB_C=ON
   cmake --build build -j$(nproc)
   # 产物: libusearch_c.so
   ```
4. 依赖: cmake, build-essential (gcc/g++), fp16 submodule
5. 编译成功，无额外系统依赖

### 10K×128d Benchmark 结果
```
Insert time:      4.675s (468 µs/vec)
Search p50:       200.624µs
Search p95:       407.086µs
Search p99:       842.755µs
Recall@10:        0.8940
```

### CGO 依赖清单
- `libusearch_c.so` (需自行编译或从 release 下载)
- 编译时: cmake, g++, git (for submodules)
- 运行时: 仅 libusearch_c.so (纯 C++ header-only library 编译产物)
- Go build flags: `CGO_LDFLAGS="-L/usr/local/lib -lusearch_c"`

---

## 3. 其他方案评估

### Bithack/go-hnsw
- **状态**: ❌ 不可用
- 最后更新 2017 年，依赖 `github.com/willf/bitset` 已改名，`go get` 解析失败
- 不值得修复

### viterin/hnsw
- **状态**: ❌ 不存在
- GitHub 上无此仓库

### chromem-go
- **状态**: ⚠️ 不适用
- 这是一个嵌入式向量数据库，但内部使用**暴力搜索**（brute-force），不是 HNSW
- 10K 规模下搜索会很快，但不可扩展

### hnswlib Go binding
- **状态**: 无官方 Go binding
- hnswlib (C++) 是 USearch 的前身/竞品，Go 生态中没有维护良好的 binding

---

## 4. 性能对比总结

### 插入性能 (10K×128d)
```
Haystack MemStore:     48.07s  (3,339 µs/vec)  ████████████████████████████████████ 100%
Haystack Pebble:      103.91s (10,391 µs/vec)  ████████████████████████████████████████████████ 311%
Haystack Pebble Batch: 64.57s  (6,457 µs/vec)  ████████████████████████████████████████ 193%
USearch:                4.68s    (468 µs/vec)   ███ 14%
```

### 搜索延迟
```
                    p50        p95        p99
Haystack 10K:     967µs    1,225µs    1,252µs
USearch 10K:      201µs      407µs      843µs
改善:             4.8×       3.0×       1.5×
```

### 内存分配
- Haystack: 1,743 allocs/insert, 692 allocs/search
- USearch: CGO 调用开销极小，C++ 侧无 Go GC 压力

---

## 5. 结论与推荐

### 推荐: 采用 USearch Go binding 替换自研 HNSW

**理由（全部基于实测数据）**:

1. **插入快 7.1×**: 468µs vs 3,339µs per vector
2. **搜索快 4.8×** (p50): 201µs vs 967µs
3. **Recall 略优**: 0.894 vs 0.884（相同参数下）
4. **零 GC 压力**: C++ 实现不产生 Go 堆分配
5. **编译可行**: CGO 依赖仅 libusearch_c.so，编译流程已验证通过
6. **活跃维护**: USearch 是 Unum Cloud 的核心产品，持续更新

### 风险与代价
- **CGO 依赖**: 增加构建复杂度，需要 libusearch_c.so 随项目分发
- **交叉编译**: CGO 使交叉编译更复杂（但 haystack 已有 pebble 的 CGO 依赖，增量成本低）
- **调试难度**: C++ 侧的问题更难排查

### 下一步
1. 在 haystack 中创建 `USearchStore` 适配层，实现现有 `VectorIndex` 接口
2. 将 libusearch_c.so 加入 CI 构建流程
3. 跑 100K 规模 benchmark 确认大规模下的表现
4. 评估 USearch 的持久化能力（save/load）是否满足需求
