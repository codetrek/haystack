# HAY-007 MmapStore Design Review (Copilot)

> Reviewer: GitHub Copilot (simulated)  
> Date: 2026-04-17  
> Verdict: **Conditional Pass** — 3 P0 must fix before implementation

---

## 1. Go 代码骨架 — 数据结构设计

**总体评价：合理，几个 Go 惯用法问题**

### P1: `MmapStore.mu` 单锁保护所有 mmap 区域

设计中 `sync.RWMutex` 同时保护 vectors/nodes/graphL0/graphUpper 四个 mmap 区域。grow 时只有一个区域需要重映射，但当前设计会阻塞所有读。建议每个 mmap 区域独立 RWMutex，grow vectors.dat 时不阻塞 graph 读取。

### P2: MetaHeader 手动 padding 易腐

MetaHeader 手动凑 64 bytes 加 `_ [4]byte` padding，后续加字段容易算错。建议用 `unsafe.Sizeof` 编译期断言，或直接用 `encoding/binary.Size` 验证：

```go
var _ [64 - unsafe.Sizeof(MetaHeader{})]byte // compile-time size check
```

### P2: freelist 未持久化到独立区域

设计说"checkpoint 时持久化到 meta 区域"，但 meta.bin 固定 64 bytes 放不下变长 freelist。需要明确 freelist 持久化位置（独立文件或 meta.bin 扩展）。

---

## 2. mmap 库选型

### P1: edsrzf/mmap-go 已停止维护

`edsrzf/mmap-go` 最后一次有意义的提交在 2020 年。已知问题：
- Windows 上 Unmap 后文件仍被锁定（需要 GC finalize）
- 不支持 `MAP_POPULATE` / `MADV_SEQUENTIAL` 等 madvise hint
- 不支持 partial unmap

**替代方案**：
| 库 | 维护状态 | 特点 |
|---|---|---|
| `golang.org/x/exp/mmap` | 活跃 | 只读，不适用 |
| `grandecola/mmap` | 低活跃 | API 更好 |
| 自己封装 `syscall.Mmap` | — | ~80 行，完全可控 |

**建议**：直接封装 `syscall.Mmap`/`syscall.Munmap`（Linux/macOS）+ `golang.org/x/sys/windows` 三平台。80 行代码，零依赖，完全可控。比依赖一个不维护的库更可靠。

### P0: CGO_ENABLED=0 兼容性未验证

设计文档的开放问题 #4 提到了但未解决。`edsrzf/mmap-go` 在 Linux 上用 `syscall` 包（无 CGo），但 Windows 路径使用 `golang.org/x/sys/windows` 可能有隐式依赖。**必须在三平台 CI 中验证 `CGO_ENABLED=0 go build`**，否则"纯 Go 零 CGo"的约束无法保证。

---

## 3. 文件格式 — 对齐、padding、字节序

### P0: 向量 slot 未对齐到 page boundary 或 cache line

vectors.dat header = 16 bytes，slot 0 起始于 offset 16。对于 128d (512B slot)，16 不是 64 的倍数——跨 cache line 读取会有性能损失。对于 768d (3072B)，3072 不是 4096 的倍数——跨页读取更严重。

**建议**：header 统一填充到 4096 bytes（一个 page），slot 起始 page-aligned。浪费 ~4KB，换取所有向量读 page-aligned。

### P1: 字节序硬编码 LittleEndian

设计隐含 LittleEndian（从 store.go 的 encoding helpers 推断），但文档未显式声明。在 ARM64 big-endian（如某些嵌入式场景）上二进制不兼容。

**建议**：在 meta.bin header 中加一个 byte order mark 字段，或在文档中明确声明"仅支持 LE 平台"。Go 的 `binary.LittleEndian` 在 BE 机器上也能正确工作（软件转换），所以实际上只是个文档问题。

### P2: graph_l0.dat slot size = 260 bytes，未对齐

260 = 4 + 32×8。不是 2 的幂，不是 cache line 对齐。频繁随机读时 cache line split 会有影响。

**建议**：pad 到 264 (8-byte aligned) 或 288 (cache-line friendly)。

---

## 4. WAL 实现

### P0: WAL record 缺少序列号（LSN）

当前 WAL record 格式：`Length | Type | Payload | CRC32`。没有单调递增的序列号。问题：
1. Checkpoint 记录的 `WalCheckpoint uint64` 是文件偏移量还是逻辑序列号？如果是文件偏移，truncate 后偏移失效
2. 无法判断 WAL 中两条记录的先后关系（如果需要 idempotent replay）
3. 无法做 WAL 文件轮转

**建议**：每条 record 加 `uint64 LSN`，MetaHeader 的 WalCheckpoint 存 LSN 而非文件偏移。

### P1: CRC32 只校验 payload，不含 header

当前 CRC32 覆盖 `Payload`，但 `Length` 和 `Type` 字段的翻转不会被检测到。如果磁盘 bit-rot 改了 Length，replay 会读错 payload 边界，可能级联出错。

**建议**：CRC32 覆盖 `Length + Type + Payload` 全部字节。

### P2: WAL checkpoint 时机 "每 N 次操作" 不够

单次 Insert 会产生多条 WAL 记录（INSERT + 多个 SET_NEIGHBORS + 可能的 SET_ENTRY），"1000 次操作"的粒度太粗。建议按 WAL 文件大小（如 16MB）触发 checkpoint。

---

## 5. 内存管理 — mmap 重映射时序

### P1: grow 期间 GetVectorRef 返回的 slice 指向 unmapped 内存

这是最危险的并发问题。场景：

```
goroutine A: ref := GetVectorRef(42)  // 拿到 mmap slice 引用
goroutine A: (still holding ref, computing distance...)
goroutine B: grow() → munmap old → mmap new
goroutine A: access ref[0]  → SIGBUS / SIGSEGV
```

RWMutex 只保证 GetVectorRef **返回时** mmap 有效。一旦 RLock 释放，返回的 slice 就是悬垂指针。

**缓解方案**（任选一）：
1. **取消 GetVectorRef 的零拷贝语义**：总是 copy，但这违背了设计的核心卖点
2. **引用计数**：grow 等待所有 outstanding ref 归还（复杂）
3. **双缓冲**：grow 后保留旧 mmap 直到无引用（需 atomic refcount）
4. **永不 munmap，只 mremap**：Linux 有 `mremap`，但 macOS/Windows 没有

**最务实方案**：因为 HNSW 的 `h.mu` 已经串行化了所有操作，grow 只发生在 Insert 内部（持有 h.mu），而 GetVectorRef 也只在 Insert/Search 内部调用（也持有 h.mu）。所以**如果 HNSW 层保证同一时刻只有一个操作**，这个问题实际不存在。但设计文档应明确记录这个不变量（invariant），否则未来如果 HNSW 去掉全局锁加入并发搜索，MmapStore 会立即崩溃。

### P2: munmap → truncate → mmap 不是原子的

如果在 truncate 和 mmap 之间崩溃，文件已扩展但 meta 未更新。恢复时需要处理"文件大于 meta.Capacity × slotSize + headerSize"的情况。

---

## 6. 测试策略

### P1: 缺少 crash recovery 测试方案

设计只说"模拟断电"但没有具体方案。建议：

**Test Harness 设计**：
```go
// CrashSimulator wraps MmapStore, can inject crash at any WAL write
type CrashSimulator struct {
    *MmapStore
    crashAfterNWrites int  // crash after N WAL writes
    writeCount        int
}

func (c *CrashSimulator) walAppend(...) error {
    c.writeCount++
    if c.writeCount >= c.crashAfterNWrites {
        // Don't flush, don't close — simulate kill -9
        return errSimulatedCrash
    }
    return c.MmapStore.walAppend(...)
}
```

**必须覆盖的 crash 点**：
1. WAL 写了一半（partial record）
2. WAL 写完，mmap 写了一半
3. mmap 写完，meta 未更新
4. Checkpoint 过程中 crash（WAL truncate 前/后）
5. grow 过程中 crash（truncate 后，mmap 前）

**建议加入的测试类型**：
- Property-based testing：随机操作序列 + 随机 crash 点，验证恢复后数据一致性
- Bit-flip testing：WAL 文件随机翻转一个 bit，验证 CRC 检测到
- `TestMain` 中的 `SIGKILL` 子进程测试（真实场景，但难以跨平台）

### P2: 需要 benchmark regression suite

每个 phase 完成后应有对应 benchmark：
- `BenchmarkMmapGetVector` vs `BenchmarkPebbleGetVector`
- `BenchmarkMmapInsert50K` vs `BenchmarkPebbleInsert50K`
- `BenchmarkMmapSearch` with hot/cold cache

---

## 7. 代码量估算 — 1600 行够吗？

**结论：大概率会到 2200–2500 行。**

| 模块 | 设计估算 | 实际预估 | 差异原因 |
|------|---------|---------|---------|
| Phase 1 只读 | 400 | 500 | mmap 封装 + 三平台适配 ~100 行 |
| Phase 2 写+WAL | 500 | 700 | WAL encode/decode + replay 逻辑比预期复杂 |
| Phase 3 Delete | 300 | 350 | freelist 持久化方案未算 |
| Phase 4 崩溃恢复 | 200 | 350 | checkpoint 原子性 + 恢复逻辑 + edge cases |
| Phase 5 上层图 | 200 | 250 | — |
| **测试代码** | 未计 | **500+** | crash recovery 测试、property test、benchmark |
| **总计** | 1600 | **2150+** (不含测试) | |

主要低估区域：
- WAL 的 encode/decode（每种 record type 都需要序列化逻辑，5 种 type × ~30 行 = 150 行）
- idmap.dat 的 compact 逻辑（设计轻描淡写但实际需要完整读写 + 原子替换）
- 错误处理和恢复路径（happy path 简单，error path 代码量翻倍）

---

## 问题汇总

### P0 — Must Fix Before Implementation

| # | 问题 | 建议 |
|---|------|------|
| P0-1 | WAL record 缺少 LSN | 加 `uint64 LSN` 字段 |
| P0-2 | 向量 slot 未 page-aligned | header pad 到 4096 bytes |
| P0-3 | CGO_ENABLED=0 三平台验证 | CI 中加 build constraint 验证，或改用 syscall 自封装 |

### P1 — Should Fix

| # | 问题 | 建议 |
|---|------|------|
| P1-1 | 单锁保护所有 mmap 区域 | 每区域独立 RWMutex |
| P1-2 | GetVectorRef 返回悬垂引用的不变量未文档化 | 明确记录 HNSW 全局锁保证 |
| P1-3 | CRC32 不覆盖 Length+Type | CRC 覆盖全 record |
| P1-4 | 字节序未显式声明 | meta.bin 加 BOM 或文档声明 LE-only |
| P1-5 | crash recovery 无具体测试方案 | 加入 CrashSimulator harness |
| P1-6 | mmap-go 库停止维护 | 自封装 syscall.Mmap ~80 行 |

### P2 — Nice to Have

| # | 问题 | 建议 |
|---|------|------|
| P2-1 | MetaHeader 手动 padding | 编译期 size 断言 |
| P2-2 | freelist 持久化位置未明确 | 独立 freelist.bin |
| P2-3 | graph_l0 slot 260B 未对齐 | pad 到 264 或 288 |
| P2-4 | WAL checkpoint 按次数不按大小 | 按 WAL 文件大小触发 |
| P2-5 | munmap→truncate→mmap 崩溃窗口 | 恢复时校验文件大小 vs capacity |
| P2-6 | 代码量低估 ~40% | 预算调整到 2200–2500 行 |
| P2-7 | 需要 benchmark regression suite | 每 phase 加对应 benchmark |

---

## NodeStore 接口兼容性

对照 `store.go` 中的 `NodeStore` 接口，MmapStore 设计覆盖了所有方法。额外注意：

- `BatchableStore` 接口：设计未提及。MmapStore 的 WAL 天然是批量的（Insert 内的多次 SetNeighbors 共享一个 WAL sequence），但如果 HNSW 层依赖 `BeginBatch`/`CommitBatch` 语义，MmapStore 需要适配或 HNSW 层需要条件调用。**建议检查 HNSW 层是否依赖 BatchableStore。**

- `NextNodeId` 起始值：PebbleNodeStore 从 1 开始，MmapStore 从 0 开始（slot index）。需要对齐，否则迁移会出问题。

---

> 整体设计质量不错，动机清晰，trade-off 分析到位。主要风险在 WAL 正确性和 mmap 生命周期管理。建议先解决 3 个 P0 后开始 Phase 1 实现。
