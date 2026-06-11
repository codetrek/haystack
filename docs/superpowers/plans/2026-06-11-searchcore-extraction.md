# searchcore 剥离 — 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 haystack 的搜索内核(倒排索引/文档存储/collection 注册表/分词/docid 分配/纯查询引擎)剥离为仓库内独立 Go module `searchcore`,可被其它项目引用;haystack 反过来作为消费者使用它。

**Architecture:** 三层组合 `collection → documents → invertedindex`,底座 `kv`(接口)+`kv/pebblekv`(实现)+`queue`+`tokenizer`+`idtable`。全实例式 API。haystack 全量改用 `searchcore/kv`+`queue`(无适配器)。详见 spec `docs/superpowers/specs/2026-06-11-searchcore-extraction-design.md`(决策 D1–D12)。

**Tech Stack:** Go 1.23 多 module(go.work)、cockroachdb/pebble、go.work 本地联调。

---

## 关于本计划的粒度说明

这是一次**机械性为主的大型重构**(移动现有包 + 改 import + 实例式化),不是写全新功能。因此:
- 迁移类任务的「测试」步骤 = **跑被迁移包的现有测试 + `go build ./...`**(现有测试套件即安全网),而非写新失败测试。
- **P1 在本文件中完整展开到可执行步骤**(下方 Task 1.x)。**P2–P5 给出任务级路线图**;每期开工前,由对应阶段把它展开为自己的细化计划(later phase 的确切代码依赖前一期落地的 API 形状,提前写死会失真)。
- 依赖与可并行点已在每期标注,供 team 调度。

## 阶段路线图与依赖

```
P1 骨架+基础设施收敛 ──► P2 invertedindex ──► P3 documents ──► P4 collection ──► P5 engine
   (kv/queue/pebblekv/                (进库,                (进库,组合idx)    (注册表进库,        (解耦workspace,
    tokenizer/types/idtable,           symbols 共用)                            workspace薄包装)     searcher改用)
    ~35处 rename sweep)
```
- **严格串行**:每期依赖前一期。每期结束:两个 module `go build ./...` + `go test ./...` 全绿,可单独 PR。
- **可并行(期内)**:见各期「并行」标注。

## searchcore 目标 module 布局
```
searchcore/
├── go.mod                module github.com/codetrek/haystack/searchcore
├── kv/kv.go              Store / Batch 接口（= 现 pebble.DB / pebble.Batch，一字不差）
├── kv/pebblekv/          Pebble 实现（package pebblekv；Open→kv.Store）
├── queue/                MPSC + Queue 接口（package queue，原样搬）
├── tokenizer/            分词（原样搬）
├── types/                搜索/文档类型子集
├── idtable/              docid 分配器（实例式）
├── invertedindex/        低层 posting 引擎（实例式）         [P2]
├── documents/            文档存储（按 collectionID 寻址，组合 idx）[P3]
├── collection/           注册表+生命周期（组合 documents）    [P4]
└── engine/               SimpleContentSearchEngine            [P5]
```

---

# P1 — 骨架 + 基础设施收敛

**产出:** searchcore module 立起来;`pebble`→`kv`(+`pebblekv`)、`queue`→`searchcore/queue`、
`tokenizer`/`types` 进库;haystack 全量改名改用并删除两份副本;`storage` 瘦身;`idtable` 实例式进库。
P1 结束后整个仓库编译/测试绿,且 `internal/core/pebble`、`internal/utils/queue`、`internal/core/idtable` 已删除。

**并行:** Task 1.2(tokenizer)、1.3(types)、1.4(kv/pebblekv)、1.5(queue)互相独立可并行;
1.6(rename sweep)依赖 1.4+1.5;1.7(idtable)依赖 1.4+1.6。

---

### Task 1.1: 立 module 骨架 + go.work

**Files:**
- Create: `searchcore/go.mod`
- Create: `searchcore/doc.go`(占位,使空 module 可 build)
- Create: `go.work`(仓库根)
- Modify: `go.mod`(haystack 根:加 require)

- [ ] **Step 1: 建 searchcore/go.mod**

```
module github.com/codetrek/haystack/searchcore

go 1.23.0
```

- [ ] **Step 2: 占位包,让空 module 可编译**

`searchcore/doc.go`:
```go
// Package searchcore is a reusable search-core library extracted from haystack.
package searchcore
```

- [ ] **Step 3: 建仓库根 go.work**

`go.work`:
```
go 1.23.0

use .
use ./searchcore
```

- [ ] **Step 4: haystack 根 go.mod 加 require(由 go.work 本地解析)**

在 `go.mod` 的 require 块加:
```
require github.com/codetrek/haystack/searchcore v0.0.0-00010101000000-000000000000
```

- [ ] **Step 5: 验证两 module 都 build**

Run: `go build ./... && (cd searchcore && go build ./...)`
Expected: 无错误(haystack 尚未 import searchcore,空 module 编译通过)

- [ ] **Step 6: Commit**

```bash
git add go.work go.mod searchcore/go.mod searchcore/doc.go
git commit -m "build(searchcore): scaffold nested module + go.work (P1.1)"
```

---

### Task 1.2: 搬 tokenizer 进库 〔并行〕

**Files:**
- Move: `internal/core/invertedindex/tokenizer/` → `searchcore/tokenizer/`

- [ ] **Step 1: git mv 整个 tokenizer 目录**

```bash
git mv internal/core/invertedindex/tokenizer searchcore/tokenizer
```

- [ ] **Step 2: 确认 tokenizer 无 haystack 内部依赖**

Run: `cd searchcore && go list -deps ./tokenizer/ | grep codetrek || echo "无内部依赖(预期)"`
Expected: 仅 `.../searchcore/tokenizer` 自身

- [ ] **Step 3: 改 haystack 侧 tokenizer importer 的 import 路径**

找出引用者并改路径 `internal/core/invertedindex/tokenizer` → `github.com/codetrek/haystack/searchcore/tokenizer`:
```bash
grep -rl "internal/core/invertedindex/tokenizer" --include="*.go" internal/
```
对每个文件,把 import 行改为 `"github.com/codetrek/haystack/searchcore/tokenizer"`(包名 `tokenizer` 不变,调用点无需改)。

- [ ] **Step 4: 验证 build + tokenizer 测试**

Run: `(cd searchcore && go test ./tokenizer/...) && go build ./...`
Expected: tokenizer 测试 PASS;haystack build OK

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor(searchcore): move tokenizer into module (P1.2)"
```

---

### Task 1.3: 搬 types 子集进库 〔并行〕

**Files:**
- Create: `searchcore/types/`(从 `internal/shared/types` 拣出搜索/文档相关类型)
- Modify: 引用这些类型的 haystack 文件

- [ ] **Step 1: 甄别需进库的类型**

Run: `ls internal/shared/types/ && grep -nE "^type " internal/shared/types/*.go`
判定:`document.go`(Document/相关)、`search.go`(SearchResult/SearchLimit/SearchContentRequest 等)中**被 searchcore 包用到**的类型进库;`workspace.go`/app 专属类型留 haystack。
> 注:仅迁移 searchcore 各包真正需要的类型;haystack 专属(如 Editor、workspace 设置)留 `internal/shared/types`。具体清单在执行时按 `documents`/`invertedindex`/`engine` 的实际引用确定。

- [ ] **Step 2: 移动选定类型到 `searchcore/types/`(package types)**

把选定 type 定义移入 `searchcore/types/`,保持字段/JSON tag 不变(序列化兼容)。

- [ ] **Step 3: haystack 侧改引用**

haystack 仍需这些类型处,改 import 为 `searchcore/types`;若 haystack 有别名/再导出需求,在 `internal/shared/types` 做 type alias(`type Document = scoretypes.Document`)以减少改动面。

- [ ] **Step 4: 验证**

Run: `(cd searchcore && go build ./types/...) && go build ./... && go test ./internal/shared/... `
Expected: 全 OK

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor(searchcore): move shared search/document types into module (P1.3)"
```

---

### Task 1.4: 移 pebble → kv + kv/pebblekv 〔并行〕

**Files:**
- Create: `searchcore/kv/kv.go`(Store/Batch 接口)
- Move: `internal/core/pebble/*` → `searchcore/kv/pebblekv/*`(package pebblekv)

- [ ] **Step 1: 定义 kv 接口(= 现 pebble.DB / pebble.Batch)**

`searchcore/kv/kv.go`:
```go
package kv

type Store interface {
	Get(key []byte) ([]byte, error)
	Put(key, value []byte) error
	Delete(key []byte) error
	NewBatch(maxBatchSize int32) Batch
	Scan(prefix []byte, cb func(key, value []byte) bool) error
	ScanRange(begin, end []byte, cb func(key, value []byte) bool) error
	GetIncrementalId(key []byte) (int, error)
	ScheduleCompact()
	Close() error
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
```

- [ ] **Step 2: 移动 pebble 实现到 pebblekv**

```bash
git mv internal/core/pebble searchcore/kv/pebblekv
```
改 `searchcore/kv/pebblekv/*.go` 的 `package pebble` → `package pebblekv`。

- [ ] **Step 3: pebblekv 实现 kv 接口**

- 删掉 pebblekv 内原来的 `DB`/`Batch` 接口定义(已移到 kv 包);
- `PebbleDB`/`PebbleBatch` 改为实现 `kv.Store`/`kv.Batch`:`NewBatch` 返回类型改 `kv.Batch`;
- `OpenDB` 改名 `Open`,返回 `(kv.Store, error)`;
- import `github.com/codetrek/haystack/searchcore/kv`。

- [ ] **Step 4: 验证 pebblekv 测试**

Run: `cd searchcore && go test ./kv/...`
Expected: PASS(原 pebble 测试随之迁移)

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor(searchcore): kv.Store/Batch interfaces + pebblekv impl (P1.4)"
```

---

### Task 1.5: 移 queue → searchcore/queue 〔并行〕

**Files:**
- Move: `internal/utils/queue/` → `searchcore/queue/`
- Modify: `searchcore/queue/mpsc.go`(加 Queue 接口)

- [ ] **Step 1: git mv queue**

```bash
git mv internal/utils/queue searchcore/queue
```
(package 名 `queue` 不变)

- [ ] **Step 2: 加 Queue 注入接口,*Mpsc 实现之**

在 `searchcore/queue/mpsc.go` 增:
```go
// Queue 是异步任务队列的注入接口；*Mpsc 实现之。
type Queue interface {
	Add(task Task)
	RunTask(task Task) error
}
```
(`*Mpsc` 已有 `Add`/`RunTask`,自动满足)

- [ ] **Step 3: 验证 queue 测试**

Run: `cd searchcore && go test ./queue/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "refactor(searchcore): move queue into module + Queue interface (P1.5)"
```

---

### Task 1.6: rename sweep + 删副本 + storage 瘦身 〔依赖 1.4,1.5〕

**Files(改 import / 类型名):** 所有原导入 `internal/core/pebble` 的文件(21,含测试)、所有原导入
`internal/utils/queue` 的文件(14,含测试)。
**Files(删除):** `internal/core/pebble`(已 git mv 走)、`internal/utils/queue`(已 git mv 走)——确认无残留。
**Files(瘦身):** `internal/core/storage/storage.go`、`internal/core/storage/types.go`。

- [ ] **Step 1: pebble importer 全量改名**

对每个仍在 haystack 的文件(documents/idtable/invertedindex/symbols/workspace/server/testutil/storage):
- import `.../internal/core/pebble` → `.../searchcore/kv`(用 pebblekv.Open 处额外 import `.../searchcore/kv/pebblekv`);
- 类型 `pebble.DB` → `kv.Store`、`pebble.Batch` → `kv.Batch`、`pebble.PebbleDB`/`PebbleBatch`(若被引用)→ 经 pebblekv;
- `pebble.OpenDB(...)` → `pebblekv.Open(...)`(仅 `internal/core/storage/storage.go`)。

精确文件清单:
```
internal/core/documents/{batch_write,document_internal,storage}.go
internal/core/idtable/idtable.go
internal/core/invertedindex/{batch_write,invertedindex_internal,keywords_merger,storage}.go
internal/core/symbols/{function,storage}.go
internal/core/workspace/init.go
internal/core/workspace/internal/storage.go
internal/server/server.go
internal/testutil/testutil.go
internal/core/storage/storage.go
+ 上述包的 _test.go 中引用 pebble 处
```

- [ ] **Step 2: queue importer 全量改名**

文件:`internal/core/{documents,invertedindex,symbols}/storage.go`、`internal/server/server.go`、
`internal/testutil/testutil.go`(+ 相关 _test.go)。仅改 import 路径
`.../internal/utils/queue` → `.../searchcore/queue`(包名 `queue` 不变,类型 `*queue.Mpsc` 等不变)。

- [ ] **Step 3: storage 瘦身**

- `internal/core/storage/storage.go`:`Open` 内部 `pebble.OpenDB` → `pebblekv.Open`,返回 `kv.Store`;
  版本目录清理逻辑保留。
- `internal/core/storage/types.go`:保留全部 key-type 常量与 `IsKeyType`**暂不动**(本期 symbols 等仍 import 它;
  各库包用自己的默认值复刻,P2/P3/P4 各包进库时再切到库内常量)。
  > 说明:`storage` 最终只留 symbols 用的 30-33 + Open;但 doc/inverted/idtable/workspace 常量在它们各自
  > 进库前仍由此处提供,避免 P1 一次性大改。P1 仅保证编译通过。

- [ ] **Step 4: 确认旧目录已删除**

Run: `test ! -d internal/core/pebble && test ! -d internal/utils/queue && echo "副本已移除"`
Expected: 副本已移除

- [ ] **Step 5: 验证全仓库 build + 相关测试**

Run: `go build ./... && (cd searchcore && go build ./...) && go test ./internal/core/... ./internal/server/... -count=1`
Expected: 全 PASS

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "refactor: haystack 全量改用 searchcore/kv+queue, 删 pebble/queue 副本 (P1.6)"
```

---

### Task 1.7: idtable 实例式进库 〔依赖 1.4,1.6〕

**Files:**
- Move: `internal/core/idtable/` → `searchcore/idtable/`
- Modify: `searchcore/idtable/idtable.go`(包级单例 → `Allocator` 实例 + Options)
- Modify: `internal/server/server.go`、`internal/server/indexer/utils.go`(用注入的 Allocator)

- [ ] **Step 1: git mv idtable**

```bash
git mv internal/core/idtable searchcore/idtable
```

- [ ] **Step 2: 单例 → 实例式**

把包级全局(`db`/`lru`/`nextId`/`mu`/`batch`)收进 `type Allocator struct{...}`;
`Init(database)` → `New(store kv.Store, opts Options) *Allocator`;`GetId`/`Close` 改为方法;
key-type 由 `Options{KeyTypeNextId, KeyTypeKey byte}` 注入(默认 28/29);`storage.KeyTypeIdTable*`
常量改为库内本地常量(默认值不变)。

```go
package idtable

type Options struct {
	KeyTypeNextId byte // 默认 28
	KeyTypeKey    byte // 默认 29
	CacheCapacity int
	CommitInterval time.Duration
}

type Allocator struct { /* 原包级状态收进此处 */ }

func New(store kv.Store, opts Options) *Allocator { /* ... */ }
func (a *Allocator) GetId(key []byte) (string, error) { /* 原 GetId 逻辑,operate on a.* */ }
func (a *Allocator) Close() { /* ... */ }
```

- [ ] **Step 3: 迁移并改写 idtable 测试为实例式**

把 `idtable_test.go`/`lru_cache_test.go` 随包移动,setup 改为 `New(testStore, idtable.Options{})`。

- [ ] **Step 4: haystack 接回**

- `server.go`:`idtable.Init(db)` → `idAlloc := idtable.New(db, idtable.Options{})`;`idtable.Close()` → `idAlloc.Close()`;
  把 `idAlloc` 传给 indexer。
- `internal/server/indexer/`:`GetDocumentId` 改为方法/持 `*idtable.Allocator`(经 indexer.Run 注入),
  `idtable.GetId(...)` → `idAlloc.GetId(...)`。

- [ ] **Step 5: 验证**

Run: `(cd searchcore && go test ./idtable/...) && go build ./... && go test ./internal/server/indexer/... -count=1`
Expected: 全 PASS

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "refactor(searchcore): idtable instance-based Allocator into module (P1.7)"
```

---

### Task 1.8: P1 收尾验证

- [ ] **Step 1: 两 module 全量测试**

Run: `go build ./... && go test ./... -count=1 && (cd searchcore && go test ./... -count=1)`
Expected: 全 PASS

- [ ] **Step 2: 旧索引回归(keyspace 兼容)**

用一份既有 `data/`/`index/` 目录启动 server 跑几个查询(或现有 E2E 测试),确认结果不变。
> 若无现成 fixture,记录为待 QA 验证项,不阻塞。

- [ ] **Step 3: P1 阶段提交点 / 准备 PR**

```bash
git log --oneline 46a0e0b..HEAD
```

---

# P2 — invertedindex 实例式进库 〔依赖 P1〕

**任务级路线(开工前展开为细化步骤):**
- **2.1** `git mv internal/core/invertedindex searchcore/invertedindex`;包级单例 → `type Index struct`,
  `Init(db, mpsc)` → `New(store kv.Store, q queue.Queue, opts Options) *Index`;`Search/Update/CreateTable/
  DeleteTable/CloseAndWait` 改为方法;`db.GetIncrementalId`(tableId)、`db.ScheduleCompact` 经 `kv.Store`。
- **2.2** key-type(20-22)改库内 Options 默认常量(替代 `storage.KeyTypeInverted*`);`IsKeyType` 用 `searchcore/kv` 版本。
- **2.3** 迁移并实例式化 invertedindex 全部测试(含并发/coverage/crash 等)。
- **2.4** haystack 接回:`server.go` 建 `idx := invertedindex.New(...)`;`symbols` 改用注入的同一个 `idx`
  (替代它自建/包级调用);`documents`(此期仍在 internal)改为持注入 idx。
- **验证:** 两 module `go build/test` 全绿;symbols 测试绿;旧倒排数据可读。
- **并行:** 2.3(测试迁移)可与 2.4(haystack 接回)部分并行,但都依赖 2.1/2.2。

# P3 — documents 实例式进库(组合 idx) 〔依赖 P2〕

**任务级路线:**
- **3.1** `git mv internal/core/documents searchcore/documents`;包级单例 → `type Store struct`,
  `Init(db, q)` → `New(store kv.Store, q queue.Queue, idx *invertedindex.Index, opts Options) *Store`;
  `SaveNewDocuments/UpdateDocuments/DeleteDocument/GetDocument/CountByWorkspace/...` 改方法,参数带 collectionID。
- **3.2** 把「Save→更新倒排」收敛为 documents 内部**窄 seam**(现仅对接注入的 `idx`;为 §12 向量留扩展点)。
- **3.3** key-type(10-13)改库内 Options 默认常量。
- **3.4** 迁移并实例式化 documents 测试。
- **3.5** haystack 接回:`server.go` 装配 `documents.New(store, q, idx, ...)`;workspace/searcher/indexer 改用 Store 实例。
- **验证:** 全绿;旧文档数据可读。

# P4 — collection 注册表进库 〔依赖 P3〕

**任务级路线:**
- **4.1** 新建 `searchcore/collection`:`Catalog`(组合 `documents.Store`)+ `Collection` handle + `Record`
  (核心字段 ID/Name/Desc/CreatedAt/LastAccessed/LastFullSync + `Extra []byte`);id 经 `store.GetIncrementalId(key-1)`。
  逻辑取自 `internal/core/workspace/internal`(注册表)+ documents 的 workspace 记录。
- **4.2** key-type(1-2)库内 Options 默认常量。
- **4.3** **读时 shim**:旧 workspace JSON(过滤器内联)→ 新 `Record`(filters→Extra),首次写回新格式。
- **4.4** haystack `workspace` 退化为薄包装:持 `collection.Collection`;过滤器(读 conf)+ 运行期索引状态留下;
  **移除 `CountByWorkspaceFunc` hack**(直接调 collection/documents Count)。
- **4.5** 删除 `internal/core/workspace/internal`;`server.go` 装配 `collection.New(...)`。
- **验证:** 全绿;旧 workspace 列表/过滤器经 shim 正确还原。

# P5 — engine 解耦进库 〔依赖 P4〕

**任务级路线:**
- **5.1** 新建 `searchcore/engine`:把 `simple_content_search_engine.go` 的 `SimpleContentSearchEngine`
  迁入;`*workspace.Workspace` 字段 → `*collection.Collection`(或 collectionID + documents 视图);
  `MaxWildcardLength/MaxKeywordDistance` 由构造参数注入(替代 conf)。
- **5.2** `Compile/CollectDocuments/IsLineMatch` 改用 collection/documents 的查询能力。
- **5.3** 迁移引擎相关测试(searcher 中纯引擎部分)。
- **5.4** haystack `searcher` 服务层(`SearchContent`/`SearchFiles`,文件 I/O、workspace 集成)**留 haystack**,
  改用库 `engine`;`storage` 此时应只剩 symbols 30-33 + Open(确认瘦身到位)。
- **验证:** 全绿;搜索结果与迁移前一致。

---

## 收尾(P5 后)
- 清理重复的旧分支 `feat/searchcore-extraction`(主工作区那份 spec 原件)。
- `searchcore` 写一份最小 README + 独立消费者用法示例(`pebblekv.Open` + `collection.New` 全自建路径)。
- 更新 `docs/tasks/BOARD.md`。

## 自检(Self-Review)结果
- **Spec 覆盖:** D1-D12 均有对应任务(D1→1.1;D2/D12→1.4/1.6;D3 贯穿各期实例式化;D4 范围=不含 searcher 服务层(P5.4 明确留下);D5→1.7;D6→3.2;D7→P4;D8→各期 key-type Options;D9→P4 三层;D10→3.2 seam+§12;D11→2.4/3.5 共享 idx 注入)。
- **占位符:** P1 步骤含确切命令/代码/文件;P2–P5 为**显式任务级路线图**(已注明开工前展开),非隐藏占位。
- **类型一致:** `kv.Store`/`kv.Batch`、`queue.Queue`、`idtable.Allocator`、`invertedindex.Index`、
  `documents.Store`、`collection.Catalog/Collection/Record`、`engine` 命名跨任务一致。
