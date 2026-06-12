# GitHub Copilot Instructions

## User Authority

The user is the ultimate authority on what changes should be made to their code. Your suggestions must align with their instructions and preferences.

## Project Knowledge: Haystack

### Project Overview
Haystack is a local code-search indexer written in Go. It creates and queries search indexes over local code repositories ("workspaces") so developers can find content, files, and symbols across large codebases. A single binary runs either as a server (daemon that indexes and answers queries) or as a CLI client that talks to that server.

For the full system-design overview, see [`docs/architecture.md`](../docs/architecture.md).

### Module Layout
The repository is a Go workspace (`go.work`) with two modules:

- **haystack application module** (`github.com/codetrek/haystack`, root `go.mod`): the server, client, indexer/searcher, and haystack-specific core packages under `internal/`.
- **`searchcore` module** (`github.com/codetrek/haystack/searchcore`, `searchcore/go.mod`): a reusable, embeddable full-text search core with no dependency on haystack internals. The application module depends on `searchcore`, never the reverse.

`go.work` wires both modules (`use .` and `use ./searchcore`). When building, run `go build ./...` from the repo root **and** `go build ./...` inside `searchcore/` — the root build does not cover the separate module.

### Core Components
- **Client** (`internal/client`): CLI front end. Parses subcommands (`search`, `files`, `symbols`, `workspace`, `server`, `version`) and forwards them to the server over HTTP (Unix socket or loopback TCP).
- **Server** (`internal/server`): process bootstrap and dependency wiring (`server.go`). Opens storage and constructs the index/document/collection/workspace/symbol subsystems, then starts the indexer, searcher, and HTTP/MCP server.
  - **HTTP/MCP API** (`internal/server/httpapi`, `internal/server/mcptools`): registers `/api/v1/...` routes and the `/mcp` endpoint; `mcptools` implements the MCP tool handlers.
  - **Indexer** (`internal/server/indexer`): scanner → parser → writer → symbol-parser pipeline, each stage a goroutine.
  - **Searcher** (`internal/server/searcher`): content, file-name (fuzzy), and symbol search.
- **Core (haystack-specific)** (`internal/core/*`): `workspace` (registry/lifecycle), `symbols` (symbol indexing), `storage` (Pebble opener + versioning), and `vectorindex` (HNSW vector index).
- **searchcore** (`searchcore/*`): the reusable full-text engine — `invertedindex`, `documents`, `collection`, `engine`, plus supporting `kv` (+ `pebblekv`), `queue`, `tokenizer`, and `idtable`.
- **Shared / utilities** (`internal/conf`, `internal/shared/{running,types}`, `internal/utils/{fs,git}`, `internal/testutil`): configuration, runtime, common types, and helpers.

### Storage and Index Subsystems
Two distinct index subsystems backed by two distinct engines:

- **Full-text / inverted index — Pebble-backed, in `searchcore`.** The inverted index (`searchcore/invertedindex`), document store (`searchcore/documents`), and collection catalog (`searchcore/collection`) persist to [Pebble](https://github.com/cockroachdb/pebble) through the `searchcore/kv` interface and its `searchcore/kv/pebblekv` implementation. `internal/core/storage` opens the Pebble stores and manages the on-disk storage version (currently `1.4`). The server opens two Pebble stores: `<data_path>/data` (documents, collections, id allocator, workspace and symbol records) and `<data_path>/index` (the inverted index). **Pebble has not been removed; it is the engine for the entire full-text path, now encapsulated behind `searchcore/kv/pebblekv`.**
- **Vector index — MmapStore-backed, in `internal/core/vectorindex`.** An HNSW graph (`hnsw.go`) whose nodes persist through a `NodeStore` interface. The production implementation is `MmapStore` (`mmap_store.go`), backed by memory-mapped flat files with a write-ahead log and checkpointing. The earlier `PebbleNodeStore` has been removed and replaced by `MmapStore`. This subsystem is self-contained and is **not** wired into the live server's index/search path today; within the repo it is exercised by `cmd/insert-analysis`.

### Workspace Model
A workspace is a local repository directory that Haystack indexes. `internal/core/workspace` holds the in-memory registry, filters, and indexing state; persistence is delegated to `searchcore/collection`, where each workspace is a collection keyed by its absolute path. Each workspace owns its own inverted-index table(s) and document set, so results are scoped per workspace.

### Architecture Patterns
- Go workspace with a clean module boundary: the reusable `searchcore` module never imports haystack internals.
- Client-server architecture; the CLI talks to the daemon over HTTP (Unix socket or TCP).
- Workspace-based, per-collection indexing.
- Async, batched writes serialized through a shared MPSC queue (`searchcore/queue`).
- Git-aware scanning (`.gitignore` plus include/exclude filters).

### Key Technologies
- Go (workspace with two modules).
- Pebble (storage engine for the full-text/inverted-index path, via `searchcore/kv/pebblekv`).
- Memory-mapped files (`MmapStore`) for the HNSW vector index.
- HNSW for approximate nearest-neighbor vector search.
- MCP (Model Context Protocol) integration for agent tooling.

### Project Documentation Structure
- Root `README.md`: project overview, build/run, and a link into the architecture doc.
- `docs/architecture.md`: descriptive system-design overview (the design entry point).
- `docs/APIs/*`, `docs/config/*`: user-facing HTTP/MCP API and configuration reference.
- Per-package `README.md` files document individual components (under `internal/...` and `searchcore/...`); consult the relevant one when working on a component.

This context should help you provide more relevant and aligned suggestions when working with the Haystack codebase.
