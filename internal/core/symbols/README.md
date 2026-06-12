# `internal/core/symbols`

Symbol (code-identifier) indexing and storage for haystack. This package records
the functions/symbols extracted from each indexed document and maintains the
inverted-index tables that back symbol search. See the
[architecture overview](../../../docs/architecture.md) for how symbol indexing
fits into the indexing pipeline and query flow.

## Responsibility

For each workspace, this package maintains two per-workspace inverted-index
tables and a per-document record of the symbols found in that document:

- a **symbol table** keyed on full symbol names, and
- a **symbol-words table** keyed on tokenized/case-split pieces of symbol names,

plus a **doc-functions record** that stores, per document, the symbol names and
the line numbers at which they appear. Symbol keyword posting lists live in the
shared `searchcore/invertedindex` instance; the per-document records and table
metadata live in the KV store.

## Lifecycle and injection (`storage.go`)

The package holds process-global handles, injected once at server startup:

- `Init(database kv.Store, q *queue.Mpsc, idx *invertedindex.Index) error` wires
  in the KV store, the shared MPSC write queue, and the inverted index.
- `CloseAndWait()` drains the queue (via a `queue.NopeTask`) and clears the
  handles.
- `Shards = 8` is a package constant.

All mutating operations (`AddFunctions`, `DeleteDocument`, `Delete`) run inside
`mpsc.RunFunc`, so writes are serialized through the same MPSC queue used by the
document/inverted-index path. Each operation guards against a closed database or
a disabled symbol feature (`conf.Get().Symbols.EnableFeature`) as applicable.

## Key types and flows

- `SymbolUniversalTable` (`database.go`) — metadata for one inverted-index table:
  `WorkspaceId`, `InvertedId` (the id of the backing `invertedindex` table),
  `Desc`, `CreateAt`. Persisted as JSON in the KV store.
- `Function` / `DocFunction` (`function.go`) — a symbol name + line, and the set
  of functions belonging to a document (`ID`, `RelPath`, `Functions`).

Table management (`database.go`):

- `Create(workspaceId, desc)` creates the two backing inverted-index tables
  (via `idxInst.CreateTable`) and writes their `SymbolUniversalTable` metadata.
- `Delete(workspaceId)` deletes both inverted-index tables and prefix-deletes the
  workspace's doc-functions records.
- `GetSymbolTable` / `GetSymbolWordsTable` fetch the metadata for the two tables.

Document indexing (`function.go`):

- `AddFunctions(workspaceId, []DocFunction)` — for each document, reads the
  previously indexed function names, computes the new set, updates both
  inverted-index tables via `idxInst.Update(invertedId, docId, new, old)`
  (full names in the symbol table; lowercased tokens from
  `tokenizer.TokenizeForIndex` in the symbol-words table), then writes the
  packed doc-functions record.
- `GetDocFunctions(workspaceId, docId)` — reads back the packed record.
- `DeleteDocument(workspaceId, docId)` — removes a document's symbols from the
  index and deletes its doc-functions record.
- `SplitCamelCase` / `splitCamelCasePart` split identifiers on `::`, camelCase,
  digit boundaries, and underscores.

### On-disk encoding (`codec.go`)

- Doc-functions records are encoded as
  `symbol#line,line|symbol#line,...` and stored under
  `EncodeDocFunctionsKey(workspaceId, docId)` (`%c%d|%s`).
- Table metadata is keyed by `EncodeSymbolTableKey` / `EncodeSymbolWordsTableKey`
  and stored as JSON.
- The key-type bytes (`KeyTypeSymbolTable`, `KeyTypeSymbolDocFunctions`,
  `KeyTypeSymbolWordsTable`) are aliases of the constants declared in
  `internal/core/storage`.

## Relationships

- **Depends on** `searchcore/kv` (KV store), `searchcore/invertedindex`
  (posting-list tables), `searchcore/queue` (MPSC serialization),
  `searchcore/tokenizer`, `internal/core/storage` (key-type constants), and
  `internal/conf` (feature flag).
- **Consumed by** `internal/server` (`server.go` calls `symbols.Init`),
  `internal/server/indexer/symbol_parser.go` (calls `AddFunctions` /
  `DeleteDocument` after extracting symbols), `internal/server/searcher`
  (`symbols_searcher.go` queries the symbol tables), and
  `internal/core/workspace` (`Create`/`Delete` create/delete the per-workspace
  symbol tables).
- The shared inverted-index instance used here is the same `<data_path>/index`
  Pebble-backed index used for full-text search (opened by the server); the
  symbol package's own KV records live in the `<data_path>/data` store.
