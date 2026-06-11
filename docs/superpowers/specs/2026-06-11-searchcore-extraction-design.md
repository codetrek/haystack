# searchcore 可复用搜索内核剥离 — 设计文档

- 日期：2026-06-11
- 状态：已确认，待生成实现计划
- 分支：`feat/searchcore-extraction`

## 1. 背景与目标

haystack 的核心搜索能力(倒排索引 + 文档存储 + 内容查询引擎)目前全部位于 `internal/`
之下。Go 的 `internal/` 可见性规则禁止外部模块引用,导致这些能力无法在别的项目里复用。

**目标**：把"搜索内核"剥离成一个**仓库内的独立 Go module** `searchcore`,使其能被其它项目
`go get` 引用;haystack 自身反过来作为该 module 的消费者使用它。剥离后的库要：

- 零依赖 haystack(不 import 任何 haystack 业务包:`conf`/`running`/`workspace`/`indexer` 等);
- 自带可开箱即用的存储后端(Pebble),同时允许消费者替换;
- 对外提供"完整"的索引文档存储体验:`Save` 自动建倒排、`Search` 直接返回匹配文档;
- 保持 haystack 现有磁盘索引数据**零迁移**可读。

## 2. 范围

### 进库(searchcore module)
- `kv`(KV 存储抽象接口)+ `kv/pebblekv`(Pebble 实现)
- `queue`(MPSC 异步写队列 + `Queue` 注入接口)
- `tokenizer`(分词,已自包含)
- `idtable`(docid 分配器:key → 紧凑 int64)
- `invertedindex`(低层 posting-list 引擎,可单独使用)
- `documents`(完整索引文档存储,内部组合 invertedindex)
- `engine`(内容查询引擎 `SimpleContentSearchEngine`)
- `types`(搜索/文档相关的共享类型子集)

### 留在 haystack
- `workspace`(workspace 注册表 + 索引生命周期状态 + 过滤器解析)
- `symbols`(符号解析/搜索;**直接复用** searchcore 的 `invertedindex`)
- `searcher` 服务层(`SearchContent`/`SearchFiles`,文件 I/O、工作区集成)
- `indexer`(扫描/解析/写入流水线;docid 改用 searchcore 的 idtable 实例)
- `conf`/`running`/`httpapi`/`client`/`mcptools` 等 app 层
- `internal/core/pebble`、`internal/core/storage`、`internal/utils/queue` 暂保留(symbols/
  workspace/idtable-adapter 等仍可能引用;最终可视情况收敛,见 §9)

## 3. 关键决策记录

| # | 决策 | 选择 | 依据 |
|---|------|------|------|
| D1 | 目标形态 | **仓库内嵌套 module**(自带 go.mod)+ 根 `go.work` | 单仓库管理、改动可控;别的项目可 `go get .../searchcore/...` |
| D2 | 存储耦合 | **库自定义 `kv` 接口 + 自带 `pebblekv` 实现**;haystack 用薄适配器注入现有 DB | `pebble.DB`/`Batch` 本就是接口,抽象近乎零成本;复用最干净 |
| D3 | API 形态 | **实例式**(`New(...)`),废弃包级单例 | 真正可复用(多实例、无全局态、易测) |
| D4 | 搜索边界 | **搜索内核**(索引 + 存储 + 纯引擎),不含完整搜索服务 | `searcher` 服务层深度耦合 workspace/indexer/conf/fs,剥离=大重构,不在本期 |
| D5 | idtable | **进库**(实例式 docid 分配器) | 它是与倒排配套的稳定 docid 生成器,干净且小;让库开箱可生成 docid |
| D6 | documents↔invertedindex | **组合(B)**:documents 内部持有 idx,invertedindex 仍为独立包 | 对外"完整"体验 + invertedindex 仍可单用(symbols 复用它) |
| D7 | workspace | **整体留 haystack**;库用不透明 collection id(裸 int) | workspace 是 app 级注册表/状态/过滤器,非搜索内核 |
| D8 | key-type 字节 | 做成 **option,默认值 = 当前值** | haystack 磁盘数据零迁移;独立消费者用默认值即可 |

## 4. 依赖分析(剥离前现状)

`go list -deps` + 直接 import 分析结论:

**干净子树(目标进库,几乎零业务耦合):**
```
types          内部依赖：无
queue/pebble   内部依赖：无
storage        内部依赖：pebble（仅 key-type 常量 + Open）
idtable        内部依赖：pebble, storage（GetId：key→int64）
tokenizer      内部依赖：无
invertedindex  内部依赖：pebble, storage, queue
documents      内部依赖：invertedindex, pebble, storage, queue
```

**组合关系**：`documents` 在运行期驱动 `invertedindex`——
`documents.Create → invertedindex.CreateTable`、`SaveNewDocuments/UpdateDocuments →
invertedindex.Update`、`Delete → invertedindex.DeleteTable`。docid 由 haystack 的
`indexer.GetDocumentId(relPath) = idtable.GetId(md5(relPath))` 铸造,作为不透明字符串
传入 documents/invertedindex。

**纯引擎的耦合**：`SimpleContentSearchEngine` 结构体含 `Workspace *workspace.Workspace`
字段,但生产路径仅使用 `Workspace.Id`(给查询定范围)。文件读取在服务层 `SearchContent`
(`io.Reader` 边界),不在引擎内。⇒ 引擎解耦只需把 `*workspace.Workspace` 换成
`collectionID int` + `documents.Store`。

**深耦合(留 haystack)**：`searcher` 直接依赖 `workspace`(12×)、`indexer`(`AddOrSyncFile`/
`RemoveFile`/`Sync`/`RefreshFileIfNeeded`——搜索时主动触发增量索引)、`conf`、`running`、
`os/bufio/filepath`。`workspace` 依赖 `conf`(全局过滤器)、并 Init/组合 documents+
invertedindex+symbols。`symbols` 依赖 `conf` 且**直接使用 invertedindex**。

## 5. 目标架构

### 模块布局
```
searchcore/                         module: github.com/codetrek/haystack/searchcore
├── go.mod
├── kv/
│   ├── kv.go                       Store / Batch 接口（形状 = 现 pebble.DB / pebble.Batch）
│   └── pebblekv/                   Pebble 实现（自带默认后端 + Open）
├── queue/                          MPSC 实现 + Queue 接口（可注入共享实例）
├── tokenizer/                      原样搬迁（已自包含）
├── idtable/                        实例式 docid 分配器；key-type 28/29（可配）
├── invertedindex/                  低层 posting 引擎；key-type 20-22（可配）
├── documents/                      完整索引文档存储；内部组合 invertedindex；key-type 10-13（可配）
├── engine/                         SimpleContentSearchEngine（query 编译 + 候选收集 + 逐行匹配）
└── types/                          搜索/文档类型子集
```

### 分层
```
kv / queue / tokenizer / idtable          ← 叶子基础设施
   └─ invertedindex                       ← 低层 posting 引擎，可单独使用（symbols 复用它）
        └─ documents（内部组合 idx）       ← 复用者主要入口：Save 自动索引、Search 返回文档
             └─ engine                     ← 内容匹配；文件 I/O 留 app 侧
```

### 本地联调
仓库根新增 `go.work`：
```
go 1.23
use .
use ./searchcore
```
haystack 主 module 在 `go.mod` 中 `require github.com/codetrek/haystack/searchcore v0.0.0`,
本地由 go.work 解析,无需发版。

## 6. 公开 API 草图

> 仅示意形态,以实现期细化为准。所有构造器接收注入的 `kv.Store` 与 `queue.Queue`。
> 草图中用 `collectionID` 表意一个不透明的集合标识;按 D7,实现中沿用现有 `workspaceID`
> 参数名以减少 churn(二者同义,均为裸 int)。

```go
// kv —— 存储抽象（形状照搬现有 pebble.DB / Batch，便于薄适配）
package kv
type Store interface {
    Get(key []byte) ([]byte, error)
    Put(key, value []byte) error
    Delete(key []byte) error
    NewBatch(maxBatchSize int32) Batch
    Scan(prefix []byte, cb func(key, value []byte) bool) error
    ScanRange(begin, end []byte, cb func(key, value []byte) bool) error
    IsClosed() bool
}
type Batch interface {
    Put(key, value []byte) error
    Delete(key []byte) error
    DeleteRange(start, end []byte) error
    DeletePrefix(prefix []byte) error
    Commit() error
    Reset()
    Close() error
    Count() int32
}

// queue —— 异步写队列注入接口 + 自带 MPSC 实现
package queue
type Task interface{ Run() error }
type Queue interface {
    Add(task Task)
    RunTask(task Task) error
}
func NewMpsc(name string) *Mpsc // 实现 Queue

// idtable —— docid 分配器（实例式）
package idtable
type Options struct{ KeyTypeNextId, KeyTypeKey byte } // 默认 28 / 29
func New(store kv.Store, opts Options) *Allocator
func (a *Allocator) GetId(key []byte) (string, error)
func (a *Allocator) Close()

// invertedindex —— 低层 posting 引擎（可单用）
package invertedindex
type Options struct{ KeyTypeRow, KeyTypeTable, KeyTypeNextId byte } // 默认 20 / 21 / 22
func New(store kv.Store, q queue.Queue, opts Options) *Index
func (i *Index) CreateTable(desc string) (int, error)
func (i *Index) DeleteTable(tableId int) error
func (i *Index) Update(tableId int, docid string, newKeywords, oldKeywords []string)
func (i *Index) Search(tableId int, query string, limit int, filter func(string) bool) SearchResult
func (i *Index) CloseAndWait()

// documents —— 完整索引文档存储（内部组合 invertedindex）
package documents
type Options struct {
    Keyspace    KeyspaceOptions      // 文档 key-type 默认 10-13
    Index       invertedindex.Options
}
func New(store kv.Store, q queue.Queue, opts Options) *Store   // 内部 New 一个 invertedindex.Index
func (s *Store) CreateCollection(id int, desc string) error    // 含建倒排表
func (s *Store) DeleteCollection(id int) error
func (s *Store) Save(collectionID int, docs []*Document) error // 写文档 + 更新倒排
func (s *Store) Update(collectionID int, docs []*Document) error
func (s *Store) Delete(collectionID int, docID string) error
func (s *Store) GetDocument(collectionID int, docID string, includeWords bool) (*Document, error)
func (s *Store) Search(collectionID int, query string, limit int) SearchResult // 经倒排返回候选文档
func (s *Store) Index() *invertedindex.Index   // 暴露底层 idx，供 symbols 等高级复用
func (s *Store) CloseAndWait()

// engine —— 内容查询引擎（解耦 workspace）
package engine
func New(store *documents.Store, collectionID int, maxWildLen, maxKwDist int, wholeWord bool) *Engine
func (e *Engine) Compile(query string, caseSensitive bool) error
func (e *Engine) CollectDocuments() (*invertedindex.SearchResult, error)
func (e *Engine) IsLineMatch(line string) [][]int
```

## 7. 存储缝设计

1. **接口形状照搬**：`kv.Store`/`kv.Batch` 直接采用现有 `pebble.DB`/`pebble.Batch` 的方法集,
   令 haystack 的现有实现可被薄适配。
2. **自带实现**：`kv/pebblekv` 把现 `internal/core/pebble` 的实现搬入库,提供 `Open(path,
   cacheSize) (kv.Store, error)`,作为独立消费者的默认后端。
3. **key-type 可配**：各包(idtable/invertedindex/documents)的 key-type 字节由 `Options`
   注入,**默认值等于当前值**(文档 10-13 / 倒排 20-22 / idtable 28-29)。`IsKeyType` 助手
   内化进库(各包私有)。
4. **haystack 适配器**(写在 haystack 侧,不进库):
   - `pebbleStoreAdapter`:包 `internal/core/pebble.DB` → 实现 `searchcore/kv.Store`
     (主要处理 `NewBatch` 返回类型 `pebble.Batch` → `kv.Batch` 的适配)。
   - `queueAdapter`:包 `internal/utils/queue.Mpsc` → 实现 `searchcore/queue.Queue`。
5. **共享单库 + 共享写队列**:haystack 仍打开**一个** Pebble DB、一个共享 `DBQueue`,通过
   适配器注入到库的 idtable/invertedindex/documents 实例,key-type 用默认值 ⇒ 与现状
   完全一致(workspace/symbols 等仍与库数据共存于同一 keyspace)。

## 8. haystack 改造

- **`server.go` 装配**:替换包级 `Init` 调用,显式 `New` 出 `idtable.Allocator`、
  `documents.Store`(内含 `invertedindex.Index`)实例并注入适配器 + 共享队列;把实例传递给
  需要的组件(indexer/searcher/symbols/workspace)。
- **调用点 churn(约 20 处)**:从"包级函数"改为"持实例调用"。受影响:`symbols`(改用
  `store.Index()` 暴露的同一个 invertedindex 实例)、`workspace`(实例式后不再 import
  documents/invertedindex/symbols)、`searcher`(引擎改用 `documents.Store`+collectionID)、
  `indexer`(`GetDocumentId` 改用库 `idtable.Allocator` 实例)、`server.go`,以及相应测试。
- **删除**:`internal/core/invertedindex`、`internal/core/documents`、`internal/core/idtable`
  (均已迁库;haystack 改用 searchcore 对应实例 + 适配器)。
- **保留**:`internal/core/pebble`、`internal/core/storage`、`internal/utils/queue`
  (承载适配器 + workspace/symbols 共享 keyspace 常量);最终收敛见 §9。

## 9. 向后兼容

- **磁盘数据零迁移**:key-type 默认值不变 + 编解码逻辑原样迁移 ⇒ 旧索引可直接读。
- **共享 keyspace 常量**:`storage/types.go` 里的全局字节分配(workspace 1-2、doc 10-13、
  倒排 20-22、idtable 28-29、symbols 30-33)需保持全局一致。库内各包用默认值复刻其负责的
  字段;haystack 侧 `storage` 包继续持有 workspace/symbols 的常量。两边数值必须保持同步
  (实现期在两处加注释互指,防止漂移)。
- **存储版本**:`StorageVersion = "1.4"` 不变;本次为纯结构性重构,不改盘上格式。

## 10. 分期计划

每期独立可编译、可测、可单独提 PR;每期结束用旧索引数据回归一次搜索确认 keyspace 兼容。

- **P1 — 骨架**:建 `searchcore` module + `go.work`;搬迁 `kv`(+`pebblekv`)、`queue`、
  `tokenizer`、`types`、`idtable`(实例式)。haystack `indexer.GetDocumentId` 改用库 idtable
  实例 + 适配器。全测试绿。
- **P2 — invertedindex**:实例式化并进库;haystack 适配器接回;symbols 改用注入的 Index 实例。
  全测试绿。
- **P3 — documents**:实例式化并进库,内部组合 invertedindex(D6/B);`server.go` 装配
  `documents.Store`;workspace 不再 import documents/invertedindex。全测试绿。
- **P4 — engine**:`SimpleContentSearchEngine` 解耦 workspace(→ collectionID +
  documents.Store)并进库;searcher 服务层改用库引擎。全测试绿。

## 11. 测试与验证

- 每个包的现有单测随包迁移;把依赖包级单例的测试改写为实例式 setup。
- 两个 module 都跑:`(cd searchcore && go test ./...)` 与根 `go test ./...`。
- 回归:用一份旧版本生成的 `data/`/`index/` 目录启动 haystack,执行若干查询,确认结果与
  迁移前一致(keyspace 兼容验证)。
- `make test` / `make test-safe`(Docker 隔离)保持绿。

## 12. 风险与权衡

- **实例式 churn 较大**:约 20 处调用点 + 测试改写,是本次主要成本(D3 的必然代价)。
  通过分期(P1→P4)逐包推进、每期全绿来控制风险。
- **keyspace 常量双处维护**:库默认值与 haystack `storage` 常量需保持同步;以注释互指 +
  回归测试兜底。
- **adapter 维护**:haystack 需维护 2 个薄适配器;逻辑简单,风险低。
- **symbols 与 documents 共享同一 Index 实例**:需确保 `server.go` 装配时把 documents
  内部的 Index 通过 `store.Index()` 暴露给 symbols,避免两套实例写同一 keyspace。

## 13. 待定 / Open

- **workspace 注册表子包**:当前决策为整体留 haystack(D7)。若后续希望让独立消费者也有
  开箱的多 collection 管理,可把 `workspace/internal`(path↔id + 元数据持久化,仅依赖
  kv)抽成 `searchcore/collections`;过滤器/索引状态/conf 仍留 haystack。本期不做。
- **`internal/core/pebble`/`storage`/`queue` 最终收敛**:初期保留;待 symbols 等也改用库
  设施后,可评估彻底删除 haystack 侧副本、全量改用 `searchcore/kv`+`queue`。本期不强制。
