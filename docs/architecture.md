# Haystack Architecture

This document describes how Haystack is structured today. It is a descriptive
system-design overview for developers working on the codebase — it explains the
major components, the module boundaries, the storage and index subsystems, the
workspace model, and the main data flows. It is descriptive, not a governance
contract: it records how the code currently behaves rather than mandating future
behavior.

Accuracy is established against the source code. Where a claim maps to a
specific place in the tree, the path is named inline.

## Overview

Haystack is a local code-search indexer written in Go. A single binary
(`cmd/haystack`) runs in one of two modes:

- **Server mode** (daemon): scans and indexes local repositories ("workspaces")
  and answers search queries over an HTTP API and an MCP endpoint.
- **Client mode** (CLI): a thin command-line front end that talks to a running
  server over HTTP (TCP or a Unix socket).

The same process picks its mode at startup in `cmd/haystack/main.go`: if it is
launched in daemon mode it calls `server.Run()`; otherwise it dispatches the
CLI subcommand through `client.Run()`.

The repository is a Go **workspace** (`go.work`) containing two modules:

- The **haystack application module** (`github.com/codetrek/haystack`, root
  `go.mod`) — the server, client, indexer/searcher, and haystack-specific core
  packages under `internal/`.
- The **`searchcore` module** (`github.com/codetrek/haystack/searchcore`,
  `searchcore/go.mod`) — a reusable, embeddable full-text search core with no
  dependency on haystack internals.

`go.work` wires both modules together (`use .` and `use ./searchcore`), so the
application module imports `searchcore` packages directly during local
development.

## Components

The major runtime components and their roles:

| Component | Location | Role |
|-----------|----------|------|
| **Client** | `internal/client` | CLI front end. Parses subcommands (`search`, `files`, `symbols`, `workspace`, `server`, `version`) and forwards them to the server over HTTP. |
| **Server** | `internal/server` | Process bootstrap and dependency wiring. `server.go` opens storage, constructs the index/document/collection/workspace/symbol subsystems, and starts the indexer, searcher, and HTTP/MCP server. |
| **HTTP/MCP API** | `internal/server/httpapi`, `internal/server/mcptools` | Request surface. `httpapi` registers the `/api/v1/...` routes and the `/mcp` endpoint; `mcptools` implements the MCP tool handlers. |
| **Indexer** | `internal/server/indexer` | Builds and maintains the index. A pipeline of scanner → parser → writer → symbol parser, each running as its own goroutine stage. |
| **Searcher** | `internal/server/searcher` | Serves queries. Content, file-name (fuzzy), and symbol search over the indexed data. |
| **Core (haystack-specific)** | `internal/core/*` | Haystack's own domain layer: workspace registry, symbol indexing, the storage opener, and the vector index. |
| **searchcore** | `searchcore/*` (separate module) | The reusable full-text engine: inverted index, document store, collection catalog, content query engine, plus supporting `kv`, `queue`, `tokenizer`, and `idtable` packages. |

### The `searchcore` module boundary

`searchcore` was extracted so the full-text search machinery can be reused
independently of haystack. Its layering (see `searchcore/README.md`):

```
collection      registry + lifecycle (named collections of documents)
  documents     per-collection document storage (metadata / words / path)
    invertedindex   low-level posting-list engine (term → doc ids)
engine          content query engine (compile → collect candidates → line match)
────────────────────────────────────────────────────────────────────────────
kv (+ pebblekv) · queue (MPSC) · tokenizer (CJK/camel/snake) · idtable
```

The boundary is one-directional: `searchcore` packages depend only on each
other and on third-party libraries; they never import anything under
`internal/`. The haystack server depends on `searchcore` (not the reverse) and
composes its packages explicitly during startup in
`internal/server/server.go`, which imports `searchcore/collection`,
`searchcore/documents`, `searchcore/idtable`, `searchcore/invertedindex`,
`searchcore/kv`, and `searchcore/queue`. Haystack-specific packages such as
`internal/core/workspace`, `internal/core/symbols`, and the indexer/searcher
also consume `searchcore` types directly.

## Storage and index subsystems

Haystack maintains two distinct index subsystems backed by two distinct storage
engines.

### Full-text / inverted index (Pebble-backed, in `searchcore`)

The full-text path is provided entirely by the `searchcore` module and is
persisted in [Pebble](https://github.com/cockroachdb/pebble):

- `searchcore/kv` defines the `Store` and `Batch` key-value interfaces that
  decouple the search core from any specific engine.
- `searchcore/kv/pebblekv` is the bundled Pebble-backed implementation of those
  interfaces (`pebblekv.Open`).
- `internal/core/storage` is the haystack-side opener. `storage.Open` opens a
  Pebble store via `pebblekv.Open`, manages the on-disk storage version
  (currently `StorageVersion = "1.4"` in `internal/core/storage/storage.go`),
  and cleans up superseded version directories.
- `searchcore/invertedindex` is the low-level posting-list engine mapping terms
  to document ids, with batched async writes and a background keyword-merging
  compactor.
- `searchcore/documents` stores per-collection document metadata, keywords, and
  path information, composing the inverted index so that saving a document also
  indexes it.
- `searchcore/collection` is the catalog of named collections that haystack uses
  as its workspace registry.

The server opens **two** Pebble stores under the configured data path
(`internal/server/server.go`):

- `<data_path>/data` — the document store, collection catalog, id allocator
  (`searchcore/idtable`), workspace records, and the symbol package's own KV
  records.
- `<data_path>/index` — the inverted index. The symbol package shares this same
  inverted-index instance for its keyword posting lists.

> Pebble has **not** been removed from Haystack. It remains the storage engine
> for the entire full-text/inverted-index path, now encapsulated behind
> `searchcore/kv/pebblekv`.

### Vector index (MmapStore-backed, in `internal/core/vectorindex`)

`internal/core/vectorindex` is a self-contained approximate-nearest-neighbor
subsystem:

- `hnsw.go` implements an HNSW (Hierarchical Navigable Small World) graph for
  approximate nearest-neighbor search.
- The graph's nodes are persisted through a `NodeStore` interface (`store.go`).
- `MmapStore` (`mmap_store.go`) is the production `NodeStore` implementation,
  backed by memory-mapped flat files with its own write-ahead log and
  checkpointing. A `MemNodeStore` in-memory implementation also exists for tests.
- The earlier `PebbleNodeStore` implementation has been removed; `MmapStore` is
  its replacement.

The vector index is **not** part of the live server's index/search path today:
no package under `internal/server`, `internal/client`, or the rest of
`internal/core` imports `internal/core/vectorindex`. Within the repository it is
exercised by the `cmd/insert-analysis` tool. It is documented here as a present,
self-contained subsystem rather than as a stage of the running query flow.

## Workspace model

A **workspace** is a local repository directory that Haystack indexes and
searches. The model spans two layers:

- `internal/core/workspace` holds the haystack-side workspace concept: the
  in-memory registry (`init.go`), per-workspace filters and indexing-progress
  state, and lifecycle operations (create / list / sync / delete in
  `manage.go`). It also migrates legacy workspace records (`migrate.go`).
- Persistence is delegated to `searchcore/collection`: each workspace is a
  **collection** in the catalog, keyed by the workspace's absolute path. During
  startup the server migrates any legacy records, constructs the
  `collection.Catalog`, and calls `workspace.Init(cat)` to build the in-memory
  maps from the catalog (`internal/server/server.go`).

Each workspace owns its own inverted-index table(s) and document set, so search
results are scoped to a single workspace. Filtering (include/exclude globs and
`.gitignore` handling) is applied during scanning and querying. Configuration —
the data path, server port/socket, cache size, indexing worker counts, and
search limits — comes from `internal/conf`.

## Data flows

### Indexing a repository

Triggered when a workspace is created or synced (`indexer.Sync` /
`indexer.SyncIfNeeded`). Work moves through the indexer's goroutine stages,
each fed by its predecessor's queue (`internal/server/indexer`):

1. **Scanner** (`scanner.go`) walks the workspace directory, applying
   include/exclude filters and `.gitignore` rules, and enqueues candidate files.
2. **Parser** (`parser.go`) reads each file, tokenizes its content via
   `searchcore/tokenizer`, and produces a `documents.Document` with its
   keywords.
3. **Writer** (`writer.go`) persists each document through the
   `searchcore/documents` store. Saving a document also updates the
   `searchcore/invertedindex` posting lists (writes are batched and serialized
   through the shared `searchcore/queue` MPSC queue).
4. **Symbol parser** (`symbol_parser.go`) extracts code symbols (via an external
   symbol extractor) and records them through `internal/core/symbols`, which
   indexes symbol keywords into its own inverted-index tables.

Document ids are stable, compact ids minted by `searchcore/idtable` from file
paths, so re-indexing a file reuses its id.

### Serving a query

A query arrives at the server through the HTTP API (`/api/v1/search/...`) or the
MCP endpoint (`/mcp`), is dispatched by `internal/server/httpapi`, and is handled
by `internal/server/searcher`:

- **Content search** (`searcher.go`, `SearchContent`) compiles the query string
  with `searchcore/engine` into OR/AND clauses, uses the inverted index to
  collect candidate document ids, then opens each candidate file and confirms
  matches line by line, returning matched lines with optional context. Results
  are scoped to the workspace and ordered with editor-context prioritization.
- **File search** (`SearchFiles`) does fuzzy matching over indexed file paths.
- **Symbol search** (`symbols_searcher.go`) queries the symbol inverted-index
  tables maintained by `internal/core/symbols`.

The client (`internal/client`) issues these queries by POSTing to the server
over HTTP — using a Unix socket when one is configured, otherwise a loopback TCP
port (`internal/client/common.go`) — and renders the response for the terminal.

## Package map

Application module (`internal/`):

- `internal/client` — CLI front end and server communication.
- `internal/server` — process bootstrap and dependency wiring.
- `internal/server/httpapi` — HTTP routes and MCP endpoint setup.
- `internal/server/mcptools` — MCP tool handlers.
- `internal/server/indexer` — scan/parse/write/symbol indexing pipeline.
- `internal/server/searcher` — content, file, and symbol query handling.
- `internal/core/workspace` — workspace registry and lifecycle.
- `internal/core/symbols` — symbol indexing over `searchcore/invertedindex`.
- `internal/core/storage` — Pebble store opener and storage versioning.
- `internal/core/vectorindex` — HNSW vector index with `MmapStore` persistence.
- `internal/conf` — configuration loading and defaults.
- `internal/shared/*`, `internal/utils/*`, `internal/testutil` — runtime,
  shared types, filesystem/git helpers, and test support.

`searchcore` module (`searchcore/`):

- `searchcore/kv` (+ `searchcore/kv/pebblekv`) — KV interfaces and the Pebble
  implementation.
- `searchcore/invertedindex` — posting-list engine.
- `searchcore/documents` — per-collection document store.
- `searchcore/collection` — collection catalog.
- `searchcore/engine` — content query engine.
- `searchcore/queue` — MPSC async write queue.
- `searchcore/tokenizer` — ASCII/CJK/camelCase/snake_case tokenization.
- `searchcore/idtable` — stable document-id allocation.
