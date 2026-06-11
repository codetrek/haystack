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
- 对外提供"完整"的、**多 collection** 的索引文档存储体验:`Create(name)` 建集合、`Save` 自动
  建倒排、`Search` 直接返回匹配文档;
- 保持 haystack 现有磁盘索引数据(文档/倒排,重建代价大的部分)**字节兼容**。

## 2. 范围

### 进库(searchcore module)
- `kv`(KV 存储抽象接口)+ `kv/pebblekv`(Pebble 实现)
- `queue`(MPSC 异步写队列 + `Queue` 注入接口)
- `tokenizer`(分词,已自包含)
- `idtable`(docid 分配器:key → 紧凑 int64)
- `invertedindex`(低层 posting-list 引擎,可单独使用)
- `documents`(文档存储,按 collectionID 寻址,组合注入的 invertedindex)
- `collection`(collection 注册表 + 生命周期,组合 documents —— **库的完整入口**)
- `engine`(内容查询引擎 `SimpleContentSearchEngine`)
- `types`(搜索/文档相关的共享类型子集)

### 留在 haystack
- `workspace`(**改薄为包装器**:包一个库 collection + 过滤器解析 + 运行期索引状态)
- `symbols`(符号解析/搜索;**直接复用注入的** searchcore `invertedindex.Index`)
- `searcher` 服务层(`SearchContent`/`SearchFiles`,文件 I/O、工作区集成)
- `indexer`(扫描/解析/写入流水线;docid 改用 searchcore 的 idtable 实例)
- `conf`/`running`/`httpapi`/`client`/`mcptools` 等 app 层
- `internal/core/pebble`、`internal/core/storage`、`internal/utils/queue`(承载适配器 +
  symbols 共享 keyspace 常量;最终收敛见 §9)

## 3. 关键决策记录

| # | 决策 | 选择 | 依据 |
|---|------|------|------|
| D1 | 目标形态 | **仓库内嵌套 module**(自带 go.mod)+ 根 `go.work` | 单仓库管理、改动可控;别的项目可 `go get .../searchcore/...` |
| D2 | 存储耦合 | **库自定义 `kv` 接口 + 自带 `pebblekv` 实现**;haystack 用薄适配器注入现有 DB | `pebble.DB`/`Batch` 本就是接口,抽象近乎零成本;复用最干净 |
| D3 | API 形态 | **实例式**(`New(...)`),废弃包级单例 | 真正可复用(多实例、无全局态、易测) |
| D4 | 搜索边界 | **搜索内核**(索引 + 存储 + 纯引擎),不含完整搜索服务 | `searcher` 服务层深度耦合 workspace/indexer/conf/fs,剥离=大重构,不在本期 |
| D5 | idtable | **进库**(实例式 docid 分配器) | 与倒排配套的稳定 docid 生成器,干净且小;让库开箱可生成 docid |
| D6 | documents↔invertedindex | **组合(B)**:documents 内部持有 idx,invertedindex 仍为独立包 | 对外"完整"体验 + invertedindex 仍可单用(symbols 复用它) |
| D7 | workspace | **抽象为库的 `collection`**;haystack workspace 退化为薄包装器 | workspace 注册表本质通用(id 分配 + name↔id + 元数据);过滤器/索引状态留 haystack |
| D8 | key-type 字节 | 做成 **option,默认值 = 当前值** | 文档/倒排数据零迁移;独立消费者用默认值即可 |
| D9 | 组织形式 | **三层组合 `collection → documents → invertedindex`**,每层可单用、组合下一层 | 单一职责、可分层复用;由 B 决策自然上长一层 |
| D10 | index 抽象 | **现在只做 invertedindex**;留前向兼容缝,vectorindex 作为未来兄弟包(见 §12) | 向量索引真实需求未定(HAY-007 进行中),现在定接口大概率错;设计出缝、不建实现 |
| D11 | index 实例 | **全进程共用同一个 `invertedindex.Index` 实例**,在装配根创建并注入 | tableId 由全局计数器(key-22)分配,多实例会冲突;documents 与 symbols 必须共享同一个 |

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

**workspace 注册表(`workspace/internal`)本质通用**：
`GetNextId()` = 自增 id 分配(key-1);`Save(id, json)`/`Get(id)`/`ScanAll()`/`Delete(id)`
= `id → 序列化记录` 持久化(key-2);path→id 查找 = `ScanAll` 后比对记录里的 path。
现状把 haystack 专属的过滤器字段混进了那条序列化记录。

**纯引擎的耦合**：`SimpleContentSearchEngine` 含 `Workspace *workspace.Workspace` 字段,
但生产路径仅用 `Workspace.Id`;文件读取在服务层(`io.Reader` 边界)。⇒ 引擎解耦只需把
`*workspace.Workspace` 换成 collectionID + documents 视图。

**深耦合(留 haystack)**：`searcher` 直接依赖 `workspace`(12×)、`indexer`(搜索时主动触发
增量索引)、`conf`、`running`、文件系统。`symbols` 依赖 `conf` 且**直接使用 invertedindex**。

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
├── documents/                      文档存储（按 collectionID 寻址）；组合注入的 idx；key-type 10-13（可配）
├── collection/                     注册表 + 生命周期；组合 documents；key-type 1/2（可配）
├── engine/                         SimpleContentSearchEngine（query 编译 + 候选收集 + 逐行匹配）
└── types/                          搜索/文档类型子集
```

### 三层组合(D9)
```
collection   注册表 + 生命周期（name↔id、Create/Get/List/Delete）;组合 ↓
  documents  文档存储（metadata/words/path，按 collectionID 寻址）;Save 自动索引;组合 ↓
    invertedindex   低层 posting 引擎（可单用;symbols 复用它）

底座: kv / queue / tokenizer / idtable
旁路: engine  调 collection/documents 的 Search 收候选 + 逐行匹配（文件 I/O 留 app）
```
- 每层都能单独用:只要 posting 引擎 → 用 `invertedindex`;只要文档存储不要注册表 →
  用 `documents`(自带 collectionID);要完整体验 → 用 `collection`。
- **index 实例共享(D11)**:`invertedindex.Index` 在装配根创建并**注入**进 documents/
  collection,同时注入给 haystack 的 symbols;库另提供"全自建"便捷构造器给独立消费者。

### 本地联调
仓库根新增 `go.work`：
```
go 1.23
use .
use ./searchcore
```
haystack 主 module `require github.com/codetrek/haystack/searchcore v0.0.0`,本地由 go.work
解析,无需发版。

## 6. 公开 API 草图

> 仅示意形态,以实现期细化为准。所有构造器接收注入的 `kv.Store` 与 `queue.Queue`。

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
type Batch interface { Put(k, v []byte) error; Delete(k []byte) error
    DeleteRange(s, e []byte) error; DeletePrefix(p []byte) error
    Commit() error; Reset(); Close() error; Count() int32 }

// queue —— 异步写队列注入接口 + 自带 MPSC 实现
package queue
type Task interface{ Run() error }
type Queue interface { Add(task Task); RunTask(task Task) error }
func NewMpsc(name string) *Mpsc // 实现 Queue

// idtable —— docid 分配器（实例式）
package idtable
func New(store kv.Store, opts Options) *Allocator  // Options.KeyType* 默认 28/29
func (a *Allocator) GetId(key []byte) (string, error)

// invertedindex —— 低层 posting 引擎（可单用，进程内单实例共享）
package invertedindex
func New(store kv.Store, q queue.Queue, opts Options) *Index // Options 默认 key 20/21/22
func (i *Index) CreateTable(desc string) (int, error)
func (i *Index) DeleteTable(tableId int) error
func (i *Index) Update(tableId int, docid string, newKw, oldKw []string)
func (i *Index) Search(tableId int, query string, limit int, filter func(string) bool) SearchResult
func (i *Index) CloseAndWait()

// documents —— 文档存储（按 collectionID 寻址，组合注入的 idx）
package documents
func New(store kv.Store, q queue.Queue, idx *invertedindex.Index, opts Options) *Store
func (s *Store) Save(collectionID int, docs []*Document) error    // 写文档 + 经内部 seam 更新索引
func (s *Store) Update(collectionID int, docs []*Document) error
func (s *Store) Delete(collectionID int, docID string) error
func (s *Store) GetDocument(collectionID int, docID string, includeWords bool) (*Document, error)
func (s *Store) Count(collectionID int) int
func (s *Store) Search(collectionID int, query string, limit int) SearchResult
// 内部把“通知索引更新”收敛为窄 seam，便于将来增加第二种索引（见 §12），现仅 invertedindex。

// collection —— 注册表 + 生命周期（组合 documents），库的完整入口
package collection
func New(store kv.Store, q queue.Queue, docs *documents.Store, opts Options) *Catalog
func (c *Catalog) Create(name string) (*Collection, error)   // 自分配 id（key-1 计数器）+ 建表
func (c *Catalog) Get(id int) (*Collection, error)
func (c *Catalog) GetByName(name string) (*Collection, error)
func (c *Catalog) List() []*Collection
func (c *Catalog) Delete(id int) error

// Collection 是绑定到某 id 的轻量 handle（底层共用 documents/index，按 id 定范围）
type Record struct { ID int; Name string; Desc string
    CreatedAt, LastAccessed, LastFullSync time.Time
    Extra []byte // 消费者私有扩展位（haystack 把过滤器配置塞这里）
}
func (c *Collection) Save(docs []*Document) error
func (c *Collection) Search(query string, limit int) SearchResult
func (c *Collection) Meta() *Record

// engine —— 内容查询引擎（解耦 workspace）
package engine
func New(c *collection.Collection, maxWildLen, maxKwDist int, wholeWord bool) *Engine
func (e *Engine) Compile(query string, caseSensitive bool) error
func (e *Engine) CollectDocuments() (*invertedindex.SearchResult, error)
func (e *Engine) IsLineMatch(line string) [][]int
```

## 7. 存储缝设计

1. **接口形状照搬**：`kv.Store`/`kv.Batch` 采用现有 `pebble.DB`/`pebble.Batch` 方法集,
   令 haystack 现有实现可被薄适配。
2. **自带实现**：`kv/pebblekv` 把现 `internal/core/pebble` 搬入库,提供
   `Open(path, cacheSize) (kv.Store, error)`,作为独立消费者默认后端。
3. **key-type 可配**：各包(idtable/invertedindex/documents/collection)的 key-type 字节
   由 `Options` 注入,**默认值=当前值**(collection 1-2 / 文档 10-13 / 倒排 20-22 /
   idtable 28-29)。`IsKeyType` 助手内化进库。
4. **id 自分配**：collection 的 id 不依赖 KV 的 `GetIncrementalId`(避免污染 `kv.Store`),
   而像 idtable 一样自持计数器(读 key-1 当前值→自增→持久化),**从现有计数器续号**。
5. **haystack 适配器**(写在 haystack 侧,不进库):
   - `pebbleStoreAdapter`:`internal/core/pebble.DB` → `kv.Store`(主要适配 `NewBatch`
     返回类型 `pebble.Batch` → `kv.Batch`)。
   - `queueAdapter`:`internal/utils/queue.Mpsc` → `queue.Queue`。
6. **共享单库 + 共享写队列 + 共享 Index**:haystack 仍打开一个 Pebble DB、一个共享
   `DBQueue`,创建一个 `invertedindex.Index`,通过适配器/注入构建 idtable/documents/
   collection 实例,并把同一个 Index 注入 symbols。key-type 用默认值 ⇒ 与现状一致。

## 8. haystack 改造

- **`server.go` 装配**:替换包级 `Init`,显式 `New` 出
  `idtable.Allocator` → `invertedindex.Index` → `documents.Store` → `collection.Catalog`,
  注入适配器 + 共享队列;把 Index 也注入 symbols,把 Catalog/Store 传给 indexer/searcher/
  workspace。
- **`workspace` 退化为薄包装器**:内部持一个 `collection.Collection`;
  - `Path` → collection name;`CreatedAt/LastAccessed/LastFullSync` → collection 元数据;
  - `Filters`/`UseGlobalFilters` → 序列化进 collection 记录的 `Extra`;`GetFilters()` 仍读
    `conf` 做合并(留 haystack);
  - 运行期索引状态(Scanning/Done/进度,非持久化)留在 workspace;
  - **移除 `CountByWorkspaceFunc` 回调 hack**(原为绕循环依赖),改为直接调
    `collection`/`documents` 的 Count。
- **`symbols`**:改用注入的同一个 `invertedindex.Index`(经 server.go 注入),其余不变。
- **`searcher`/`engine`**:引擎改用 `collection.Collection`(替代 `*workspace.Workspace`)。
- **`indexer`**:`GetDocumentId` 改用库 `idtable.Allocator` 实例。
- **删除**:`internal/core/invertedindex`、`internal/core/documents`、`internal/core/idtable`、
  `internal/core/workspace/internal`(注册表已迁库;其余 workspace 保留为薄包装)。
- **保留**:`internal/core/pebble`、`internal/core/storage`、`internal/utils/queue`
  (承载适配器 + workspace/symbols 共享 keyspace 常量);最终收敛见 §9。

## 9. 向后兼容

- **文档/倒排数据零迁移**:key-type 默认值不变 + 编解码逻辑原样迁移 ⇒ 旧的文档(key 10-13)
  与倒排(key 20-22)、idtable(key 28-29)数据直接可读,**无需重建索引**。
- **collection 注册表(key-1/2)轻量迁移**:库 collection 记录的 JSON schema 与现有 workspace
  JSON 不同(过滤器从内联改到 `Extra`)。在 `collection` 里加**读时 shim**:遇到旧格式记录,
  把内联的 path/times/filters 映射到新 `Record`(filters→Extra),首次写回新格式。id 计数器
  (key-1)续号读取现有值。⇒ **用户无感,无需重建索引**。
- **共享 keyspace 常量同步**:`storage/types.go` 的全局字节分配(workspace 1-2、doc 10-13、
  倒排 20-22、idtable 28-29、symbols 30-33)需保持全局一致。库内各包用默认值复刻其字段;
  haystack `storage` 包继续持有 symbols 的常量。两处加注释互指防漂移。
- **存储版本**:`StorageVersion = "1.4"` 不变(纯结构性重构,不改盘上格式;注册表 shim 在
  应用层平滑处理)。

## 10. 分期计划

每期独立可编译、可测、可单独 PR;每期结束用旧索引数据回归一次搜索确认兼容。

- **P1 — 骨架**:建 module + `go.work`;搬迁 `kv`(+`pebblekv`)、`queue`、`tokenizer`、
  `types`、`idtable`(实例式)。haystack `indexer.GetDocumentId` 改用库 idtable 实例 + 适配器。
- **P2 — invertedindex**:实例式化进库;haystack 适配回接;symbols 改用注入的 Index 实例。
- **P3 — documents**:实例式化进库,组合注入的 idx(D6);`server.go` 装配 `documents.Store`。
- **P4 — collection**:注册表进库(D7/D9),含读时 shim;haystack `workspace` 改薄包装、移除
  `CountByWorkspaceFunc`;删除 `workspace/internal`。
- **P5 — engine**:`SimpleContentSearchEngine` 解耦 workspace(→ collection.Collection)进库;
  searcher 服务层改用库引擎。

## 11. 测试与验证

- 各包现有单测随包迁移;依赖包级单例的测试改写为实例式 setup。
- 两个 module 都跑:`(cd searchcore && go test ./...)` 与根 `go test ./...`。
- 回归:用旧版本生成的 `data/`/`index/` 目录启动 haystack,执行若干查询 + 校验 workspace
  列表/过滤器(验证注册表 shim),确认结果与迁移前一致。
- `make test` / `make test-safe`(Docker 隔离)保持绿。

## 12. 前向兼容:未来的向量索引(扩展点,本期不做)

未来 collection 下的文档需要同时建倒排 + 向量(语义检索)。本期**只做 invertedindex**,但
按 D10 把缝留好,使加入向量索引是**加法**而非重写:

- **三层包结构即骨架**:`vectorindex`(HNSW,haystack 已在建)将来作为 `invertedindex` 在
  `index` 层的**兄弟包**进库。
- **documents 的索引更新收敛为窄内部 seam**:目前该 seam 只对接一个 `invertedindex.Index`;
  将来扩展为对接多个索引(倒排 + 向量),`documents.Save/Update/Delete` 调用面不变。
- **现在明确不做**:不定义大一统可插拔 index 接口、不引入 vectorindex 依赖、不加索引类型
  配置、不设计 embedding 来源与混合排序。理由:向量索引真实需求未定(HAY-007 进行中),
  过早抽象大概率抽错。届时按真实需求扩展 seam。

## 13. 待定 / Open

- **`internal/core/pebble`/`storage`/`queue` 最终收敛**:初期保留;待 symbols 等也全面改用
  库设施后,可评估彻底删除 haystack 侧副本、全量改用 `searchcore/kv`+`queue`。本期不强制。
- **collection 记录核心字段范围**:`LastFullSync` 等偏"索引同步"语义的字段是放 collection
  核心元数据还是 `Extra`,实现期定;倾向核心(对任何索引消费者通用)。
