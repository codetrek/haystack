# Vector Store 引擎架构（Segment / LSM 形）

> **模块**: `core/vectorstore`（规划中）
> **状态**: 架构设计完成；数值参数（maxSegSize / fanout K / 各阈值）留实现期实测确定。
> **日期**: 2026-06-16
> 经多轮 brainstorm + 实测（见 §5）收敛；早期探索稿见仓库根 `docs/drafts/`（非 tracked）。

---

## 0. 定位

在 `core` 内新增引擎 `core/vectorstore`，与 `invertedindex`/`documents`/`engine` 平级——「倒排索引的向量版」。记录为 `(id, vector, payload)`，支持带元数据过滤的 top-k 近邻检索，持久化、可崩溃恢复。

与 s2 的关键差异：索引不再是**一张 growing 大图 + brute-tail**，而是**一组不可变 sealed segment + 一个可变 brute head**（LSM 形）。这统一了"写不阻塞"、"删除+空间回收"、"崩溃安全"、"扩展性"四件事，且回避了 s2 最难证的 per-vector watermark 不变量。**依据见 §5 实测。**

---

## 1. 目标

- **同一向量集多索引**：向量存一份，其上并存 N 个索引（不同参数/度量）。
- **索引可插拔**：HNSW 首实现，IVF-PQ 留口。
- **可重建**：任一索引可丢弃后从 records 重建，零丢失。
- **写不阻塞**：写入落 head 即返回，建图在后台封存时做。
- **空间可回收**：删除 = tombstone；回收 = 段内 compaction / 段间 merge（重写新段 + 原子换）。

---

## 2. 三层 + segment 核心

```
records   唯一真相源（唯一必须 crash-safe）—— 按段切分
   │  每个 records-段拥有: { 段内 slot → 向量(metric自然形)+norm + payload + slot→docId } + tombstone 位图
   ▼  + 全局 docId→segId
index×N   每个 metric/参数一个 VectorIndex；它在【每个 records-段】上各建一张图(index-段)
builder   后台：把 head 封存(对其建图)；跑 compaction(段内重写)/merge(段间合并)
```

- **records 按段切分**：每个 records-段拥有自己那片 ~100k 向量+payload+删除位图，是源真；向量恰好存一份（在某个段里）。
- **index 不另存向量**：每个索引在每个 records-段上只建一张图(index-段)，按段内 slot 引用本段向量。
- **段是物理分块单位**：compact(段内重写)/merge(相关段)的影响被隔离，不波及全局。

---

## 3. records 存储形 · 近全精度唯一副本（已定，承自 s2 §3.4）

**原则：向量只存一份（在 records），形式即「主 metric 的自然存储形」；索引不另存全向量，只持有派生表示；需要 raw 时按需重建。**

- **cosine 存 `unit + |v|`（保留 #69 预归一化）**：`cos_dist = 1 − dot(unit_a,unit_b)`（无除法、最快）+ 把分量缩到 O(1) 的数值调理。
- **为什么不存 raw（实测裁决）**：试过 records 存 raw、距离 `1−dot/(na·nb)` —— insert/search **回退 ~11%** + 极小向量(≲1e-20) float32 下溢正确性回退。raw 唯一多买的"逐位精确"无人需要。（详见 memory `cosine-raw-storage-perf-measured`；PR #78 已 measure 后关闭。）
- **raw 按需重建**：非 cosine 索引 / `Get` 读回 / IVF-PQ 训练-重排，从 `unit·norm` 重建——实测 L2 相对误差 **~1e-7**（float32 噪声级）。`unit+norm` 即一份近全精度副本。
- **非 cosine 主导部署**：records 直接存 raw（dot/euclid 的自然形）。「自然形」随主 metric 而定。
- **派生表示按索引而异**：HNSW ≈ 零（只有图）；IVF-PQ = PQ 码 + 倒排 + 码本。

---

## 4. Segment 架构（核心，已定骨架）

### 4.1 一个 segment = 自洽的一块（封存后不可变；唯一可变是删除位图）

```
records-段/
  vectors:     段内 slot → 向量(metric自然形)+norm + payload   （本段拥有,源真）
  slot→docId:  段内 slot → docId 数组（盘上源真;翻译搜索结果 + 重建映射）
  deletes:     段内 tombstone 位图
  index-段(每索引一份): 段内 slot 上的 HNSW（或未来 IVF-PQ）
  attr:        value → 段内 bitmap（属性过滤，见 §6）
```

### 4.2 写入 —— 单个可变 brute head

- 写落进 **head 段**（WAL 保护）；head **纯暴力检索**，不建图。
- head 满（达 `maxSegSize`）→ **封存**：后台对它建 HNSW；建好发布为 sealed 段、起新 head。
- 依据（§5）：建图固有地贵（连 C++ 单线程都 ~0.3-1ms/vec），给"年轻、马上要被重建"的 head 建图是浪费，且会让每次写 ~1ms。head 暴力 → 写 O(1)。

### 4.3 删除 / 更新 —— O(1)，不动图

- 删：经全局 `docId→(segId, 段内 slot)` 找到持有段，**置 tombstone 位**。
- 更：旧的 tombstone + 新的写进 head。

### 4.4 搜索 —— N 路合并

```
for 每个 sealed 段:  段.index.search(q, k)  → 过 该段 tombstone + attr 过滤
head:               暴力(live + 过滤)
合并所有腿 → 全局 top-k → 取 docId + payload
```
- 每条腿产出对 q 的精确距离（同度量）→ 直接进同一个 size-k 堆。
- 段数由 merge 策略压住，N 路合并成本有界。

### 4.5 回收 / compaction —— 写新段 + 原子换 manifest（平凡崩溃安全）

- **段内 compact**：某段 tombstone 占比 > `compactThreshold`（如 30-40%）→ 重写该段（去死、重建图）→ 原子换。
- **段间 merge**：size-tiered 合并小段，压住段数。
- 两者都 = 写新段文件(fsync) + 原子改 manifest + 删旧。**封存段不可变 + 原子换 → 回收和崩溃安全一起包了：无 per-vector watermark、无原地复用、无 generation。**

### 4.6 id 模型（两层；compaction 影响隔离在段内）

```
对外 id (string) ──idtable──▶ docId (int64, 稳定) ──全局──▶ segId ──段内──▶ slot
   用户身份                    逻辑身份,跨 compaction/merge 不变    物理位置,compact 会变
```

- **docId 稳定**：外部一切引用用它，跨 compaction/merge 不变。
- **两层映射**（而非扁平 `docId→(segId,slot)`）：
  - **全局 `docId → segId`**：谁在哪个段（每 doc 一个段号，小）。
  - **段内 `docId ↔ slot`**：段里的物理位置，属于该段、随段重写。
- **关键 — compaction 的 slot 重排被隔离在段内**：段内 compact 保持 **segId 不变**，只重写该段的 `slot→docId` 与图，**全局 `docId→segId` 完全不动**。只有 **merge**（段间合并成新 segId）才动全局 map，且只动被合并段的那些 doc。
- **盘上源真 vs 派生**：每段在盘上存 `slot→docId` 数组（源真）；全局 `docId→segId` 与段内 `docId→slot` 都是**派生、常驻内存、可重建**（开库扫各段 `slot→docId` + 重放 head WAL）。崩溃安全靠 manifest 原子换（compact 中途崩 → manifest 仍指旧段 → 旧映射有效，半成品新段被 GC）。

| 操作 | 动作 |
|---|---|
| Put(docId,v) | append 到 head → `(head, slotK)`；全局 `map[docId]=head`；段内 `slotK→docId` |
| Delete(docId) | 全局 map→segId → 该段内经 `docId↔slot` 置 tombstone |
| Update(docId,v) | append 到 head + tombstone 旧段旧 slot + 全局 map 改指 head |
| Search | 各段图返回段内 slot → 段内 `slot→docId` → 合并 → 取 payload |
| **Compact(seg)** | 重写该段(slot 重排)+ 重建该段图 → 原子换 manifest。**全局 map 不动**(segId 不变) |
| **Merge(segs)** | 合并成新 segId → 全局 map 里被合并的 doc 改指新 segId |

### 4.7 多索引 × segment（C，已定）

- **段边界全索引共享**：records 只切一次；所有索引按**同一套 records-段边界**各建图。一个 records-段被 N 个索引共享（N 张图），向量仍只存一份。
- **一个 VectorIndex(metric) = M 张 index-段图**（M=sealed 段数）+ 对**共享 head** 的暴力（用它自己的 metric：cosine→1-dot，dot→dot，同一份 head 向量算各自距离）。
- **`(索引, records-段)` 状态 = indexed(有图) | pending(无图→暴力)**。这**统一了 head 与新索引**：head = 对所有索引都 pending 的段；新建索引 = 它对所有段都 pending。同一个 brute-fallback 机制，**无需 per-vector watermark**。

| 操作 | 代价 |
|---|---|
| 封存 head | 对 head 建 **N 张图**（每索引一张），后台、可并行 |
| Compact(段) / Merge(段) | 重写段 + 重建该段的 **N 张图** |
| `CreateVectorIndex` | 该索引对所有现存段标 pending → **立即可查(全暴力)** → builder 逐段建图收敛（§15.2 ⑧「每加一个索引=一次全量构建」，有界、可并行） |
| `DropVectorIndex` | 删该索引在所有段的图文件；records / 其它索引 / payload 不动 |

v1 简化：所有索引覆盖所有向量（每段都有每索引的图）；将来某索引只覆盖子集 → 它在无关段上无 index-段即可。

### 4.8 manifest + 崩溃安全（D，已定）

sealed 段不可变 → 无 legacy 的 in-place / zombie 问题。两个持久面分工：

| | 写什么 | 何时 | 保证 |
|---|---|---|---|
| **head WAL** | 每次 Put/Delete/Update | 每写 | append + fsync → Put 返回即 durable |
| **manifest** | 当前段集合快照 | 结构变更（封存/compact/merge/create/drop index） | tmp+rename+dir-fsync 原子重写 |

→ 每写只碰 head WAL（快）；manifest 只在结构变（不频繁）时重写。

**manifest = 纯元数据**（~32B/records-段 + ~16B/(索引,段)；只随段数 M=向量数÷maxSegSize 走 → KB 级到亿、~MB 到十亿）：
```
version(单调)
records-段: segId → {gen, 向量数, tombstone 数}     （文件按 seg-<id>-<gen>/ 约定推，不存路径）
index-段:   (indexName, segId) → {gen, 状态: indexed | pending}
head segId;  索引配置 name → VectorIndexConfig
```
→ **option (a) 单文件整体原子重写**（结构变更按封存频率、每次几 KB+一次 fsync、亚毫秒）。option (b) log 结构（RocksDB MANIFEST）留到十亿级 + 封存极频繁再说。

**封存 = 快的立即 durable、慢的后台**：① 写 head 向量为 sealed records-段文件（fsync，快，就是 dump 数据）→ ② manifest 原子换（head→新 sealed 段，所有索引标 **pending** + 新空 head）→ ③ 截断旧 head WAL。N 张图由 builder 后台建，每建好改 pending→indexed。**records-段立即耐久，只有图是慢的后台部分。**

**恢复**：载 manifest → sealed 段直接 mmap + 重放 head WAL 重建 head 内存态 → 重建派生映射（全局 `docId→segId` 扫各段 `slot→docId` + head；段内 `docId↔slot`）→ pending 图续建。

**封存/compact/merge 中途崩**：全是"写新文件 → 原子换 manifest"。崩在换前 → 新文件无人引用，启动扫掉孤儿（不在 manifest 的文件）；崩在换后 → 新态生效、旧文件成孤儿被扫。两边一致——审计加固的 meta.bin 原子换模式（#76）推广到 manifest。

### 4.9 段参数 + 合并策略（已定取向，数值留实现期实测调）

**关键前提**：**向量段无查询相关 key range → k-NN 必搜所有段 → 读放大 = 总段数，无 range 剪枝。** 故合并用 size-tiered + 段数封顶，**不用** RocksDB 的 leveled（其 range 剪枝对向量白搭）。

**段参数：**
- **maxSegSize 自适应**：目标 head-brute ≤ ~3ms（可配延迟预算）。由 `brute ≈ N·dim·0.3ns` 反推 `maxSegSize ≈ budget_ns/(0.3·dim)` → ~3ms 时 ≈ `10M/dim`（≈78k@128 / 39k@256 / 26k@384），夹在 [~16k, ~128k]。

**合并：一套机器（写新段 + 原子换 manifest），两个驱动喂它：**

| 驱动 | 触发 | 挑哪些段 | 目的 |
|---|---|---|---|
| **删除驱动**（churn 主力） | 段 live 占比 < `mergeFloor`(~50%) | 瘪段们，bin-pack live 文档进 ~maxSegSize 桶 | 回收 tombstone + 压段数 + 段恢复满 |
| **增长驱动** | 一层攒够 `K`(~8-10) 个段 | 同层段，size-tiered 合上一层 | 库变大时压总段数 |

- merge = 对合并的 live 向量**重建 HNSW × N 索引**（图不能平凡合并；实现期可选"保留最大输入图、其余 insert 进去"换质量省时间）。瘪段 merge 比满段便宜（live 少）。
- **单段 compact = "merge 1 个" 的退化情形**（高 tombstone 段无合并伙伴时才用）。
- **封顶 maxMergedSize**（~1M）避免顶层 merge 膨胀；目标活段数 ~几十（保 N 路 search 便宜）。
- **scale 天花板**：超 ~千万级，"搜所有段" + "巨段 merge" 同时变痛 → IVF 粗量化剪枝候选段 / sharding 的领域，v1 留口不做。

**参数表（全可调，measure-dont-assert 定值）：** `maxSegSize`(自适应) · fanout `K`~8-10 · `maxMergedSize`~1M · `compactThreshold`~35% · `mergeFloor`~50% · 目标段数 ~几十。

---

## 5. 实测依据（2026-06-16；详见 memory `hnsw-build-brute-timings-2026-06`）

随机向量、cosine、M=16、efC=200、单线程；AMD EPYC 7763。

**HNSW 建图（µs/向量）：**

| dim | N | Go(Mmap) | Go(Mem) | hnswlib(1线程) | Go/C++ |
|---|---|---|---|---|---|
| 128 | 100k | 1308 | 1696 | 398 | 3.3× |
| 256 | 100k | 1682 | 2116 | 707 | 2.4× |
| 384 | 100k | 2063 | 2552 | 955 | 2.2× |

**Brute（1-dot,unit;ms/query,k=10）：**

| dim | 50k | 100k |
|---|---|---|
| 128 | 1.4 | 3.1 |
| 256 | 3.0 | 6.3 |
| 384 | 5.4 | 11.6 |

**结论（每条都是 measure 推翻臆测）：**
1. **建图固有地贵** —— 连优化 C++ 单线程都 0.3-1ms/vec；并行只 ~2.5-4.6×（图竞争）。→ **只在后台封存时建图**。
2. **Go 比 C++ 只慢 ~2.2-3.4×**（非 10-100×），高维收窄到 2.2×；可接受。
3. **I/O 不是瓶颈** —— 扁平数组 MmapStore 比内存 map MemNodeStore 还快 ~25%；那 2-3× 是真·语言/实现开销（锁+接口分派+GC+边界检查），去内存救不了。
4. **brute ≈ N×dim×0.3ns** —— ms 级、线性。

**推论 → 段参数：**
- **head = 纯暴力**（已定）。
- **`maxSegSize` 自适应**（已定，§4.9）：要 head brute ≤ ~3ms → ≈ `10M/dim`（~78k@128 / ~39k@256 / ~26k@384）。
- **封存/compaction 稀着来**（建图贵）；compaction 阈值设高。
- 封存吞吐若成问题：**并行建图**(~3×) 是实现期优化，不挡架构。

---

## 6. payload + 过滤（段化，已定 — E）

- **payload 每段自带**：payload 是 records-段文件的一部分，和向量同段；`Get(id)` → `docId→segId` → 从该段读。无全局 payload 区；段 compact/merge 时随段重写、自然回收。
- **属性索引每段一份**：每个 records-段为每个声明字段存 `value → 段内 slot bitmap`(roaring)。
- **过滤下推 + 每段自适应**：`Search(filter)` 时每段独立 eval → 段内 member 位图 `S_seg`；`|S_seg| ≤ T` → 段内 brute-S（精确），`|S_seg| > T` → 该段图∩S（filter-during-traversal）；各段结果合并。阈值 T 按**段内** `|S_seg|` 判。
- **tombstone**：member 位图 AND 段 live 位 → 删除不漏出。
- **生命周期**：`CreateAttrIndex(prop,kind)` 对每段扫 payload 建位图（便宜，纯扫一遍）；compact/merge 时 attr 位图随段重写 → §15.2 ⑩「删除不清属性位」自然解决。payload 模型 = 方案1（结构化 + 声明可过滤字段；非声明字段仍存供返回、不索引）。

---

## 7. 公开 API + `VectorIndex` 接口

**写入 / 读回（落 head 即返回）：**
```go
func (c *Collection) Put(id string, v []float32, payload Payload) error
func (c *Collection) Get(id string) (v []float32, payload Payload, found bool, err error)
func (c *Collection) Delete(id string) error
// 对外 id = string（经 idtable→docId，见 §4.6）
```

**向量索引管理（collection 级）：**
```go
type VectorIndexConfig struct {
    Type           string // "hnsw"(v1); "ivfpq"(future)
    Metric         Metric // Cosine | DotProduct | Euclidean
    M, EfConstruction, EfSearch int
}
func (c *Collection) CreateVectorIndex(name string, cfg VectorIndexConfig) error // 异步:标全段 pending,立即可查(暴力兜底)
func (c *Collection) DropVectorIndex(name string) error                          // 删图文件;records 不动
func (c *Collection) RebuildVectorIndex(name string) error                       // 从 records 全重建(改参/IVF-PQ 重训)
func (c *Collection) ListVectorIndexes() []VectorIndexInfo
func (c *Collection) WaitForIndex(name string) error      // 等建齐(测试/强一致)
func (c *Collection) IndexLag(name string) IndexLagInfo   // 还有几段/几向量 pending
func (c *Collection) Search(index string, q []float32, k int, filter Predicate) (SearchResponse, error)
```

**属性索引（过滤，§6）：** `CreateAttrIndex(property string, kind AttrKind) error` / `DropAttrIndex(property string) error`，`kind ∈ {Keyword, Numeric}`。

**`VectorIndex` 内部接口**（承自 s2 §9，为 IVF-PQ 收紧）：`AddBatch` / `Delete` / `Search(q,k,member) 返回精确距离` / `Reset` / `Build(sampler)`。**调度单位 = "一个索引在某个 records-段上的图"**（每段一个实例，由 builder 建/重建）；上层对一个索引的 N 路合并跨它的各段图 + head 暴力（§4.4/§4.7）。

---

## 8. 分期（F）

> §0–§7 已全部拍定并折入上文。分期：每期独立可上线、CI 绿；各自 spec→plan→实现。

1. **records 分段层 + brute 搜索** —— 分段 records（向量(metric自然形)+norm + payload + slot→docId + tombstone）+ idtable + Put/Get/Delete；单段(=head)纯暴力搜索，同步、无封存。纯重构起步，对外可用。
2. **封存 + 异步建图 + N 路合并 + manifest/恢复** —— head→sealed（后台建 HNSW）；search 合并 sealed 图 + head brute；manifest + head WAL + 崩溃恢复（§4.8）。段化核心。
3. **payload** —— 每段 payload，`Get` 返回（§6）。
4. **回收** —— 删除驱动 merge（瘪段重打包）+ 增长驱动 size-tiered + 单段 compact（§4.9）。交付空间回收 = 解 VEC-015。
5. **过滤** —— 每段 attr 索引 + 自适应 brute-S/图∩S + tombstone 下推（§6）。
6. **多索引** —— `CreateVectorIndex`/`Drop`/`Rebuild` + 每段 N 图 + pending/indexed 调度（§4.7/§7）。

### 已决一览（折入上文）
- A 段生命周期 §4.1–4.5 · B id 模型 §4.6 · C 多索引×段 §4.7 · D manifest §4.8 · E payload+过滤 §6 · 段参数+合并 §4.9 · 实测依据 §5 · API §7。
- records 存储形 §3（cosine unit+norm，raw 按需重建 ~1e-7；既有 cosine 库即 records 形、迁移无损）。
- §15.2 静默漏结果 / brute-tail：段化由"sealed/pending 段状态 + head 暴力"天然处理，**无 per-vector watermark**。

### 关联现状
- `core/vectorindex` 隐患已在 #76/#77 修复合并（输入校验/崩溃恢复/tombstone-zombie/mmap grow/cosine norm/EP reseat/faulted-read）。VEC-015(free-list) 归属本设计 §4.9 回收机制，不在 legacy 解。

### 留口（v1 不做）
- IVF-PQ（接口 §7 已留）；sealed-segment 之外的索引形态；Tier-3 布尔过滤（OR/NOT/嵌套）；千万级以上的 IVF 候选段剪枝 / sharding；网络 / 分布式 / GPU。
