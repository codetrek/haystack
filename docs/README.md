# Haystack Documentation

This is the documentation index for Haystack, a local code-search indexer
written in Go. The docs are descriptive references for developers working on the
codebase: they explain how the system is structured today, the HTTP/MCP API
surface, the configuration system, and the responsibilities of each component.

New to the project? Start with the [root README](../README.md) for build and run
instructions, then read the [architecture overview](architecture.md).

## Architecture

- **[architecture.md](architecture.md)** — system-design overview: the
  server/client split, module boundaries, the storage and index subsystems, the
  workspace model, and the main data flows.

## API reference

The HTTP API and MCP tools exposed by the running server.

- **[Complete API reference](APIs/complete-api-reference.md)** — index of every
  endpoint, organized by category.
- [Search APIs](APIs/search.md) — content, filename, and symbol search.
- [Document management APIs](APIs/document-management.md)
- [Workspace management APIs](APIs/workspace-management.md)
- [Server control APIs](APIs/server-control.md)
- [Summarize API](APIs/summarize.md)
- [MCP integration](APIs/mcp-integration.md) — Model Context Protocol endpoint
  and tools for AI assistants.

## Configuration reference

- **[Configuration overview](config/README.md)** — the configuration system at
  a glance.
- [Configuration reference](config/configuration.md) — every setting, with
  validation rules and examples.
- [Quick reference](config/quick-reference.md) — common configuration patterns.

## Application components (`internal/`)

Haystack-specific packages of the application module.

### Server (`internal/server/`)

- [server](../internal/server/README.md) — process bootstrap and
  dependency-wiring layer for the daemon.
- [httpapi](../internal/server/httpapi/README.md) — HTTP API handlers and
  routing.
- [indexer](../internal/server/indexer/README.md) — workspace scanning and
  index maintenance.
- [searcher](../internal/server/searcher/README.md) — query execution.
- [mcptools](../internal/server/mcptools/README.md) — MCP tool definitions.

### Core (`internal/core/`)

- [storage](../internal/core/storage/README.md) — Pebble-backed key-value store
  opener and ownership.
- [workspace](../internal/core/workspace/README.md) — workspace registry and
  model.
- [symbols](../internal/core/symbols/README.md) — code-symbol indexing and
  storage.
- [vectorindex](../internal/core/vectorindex/README.md) — HNSW
  approximate-nearest-neighbor subsystem.

### Client (`internal/client/`)

- [client](../internal/client/README.md) — the CLI front end that talks to a
  running server over HTTP.

## `searchcore` module

A reusable, embeddable full-text search core with its own `go.mod` and no
dependency on haystack internals.

- **[searchcore](../searchcore/README.md)** — module overview and layering.
- [collection](../searchcore/collection/README.md) — registry and lifecycle of
  named collections.
- [documents](../searchcore/documents/README.md) — per-collection document
  storage and auto-indexing.
- [invertedindex](../searchcore/invertedindex/README.md) — low-level term →
  doc-ids posting-list engine.
- [engine](../searchcore/engine/README.md) — content query engine.
- [tokenizer](../searchcore/tokenizer/README.md) — CJK/camel/snake tokenization.
- [idtable](../searchcore/idtable/README.md) — document-id allocator.
- [kv](../searchcore/kv/README.md) — key-value store interface.
- [queue](../searchcore/queue/README.md) — MPSC async write queue.

## Project tracking

- [Task board](tasks/BOARD.md) — the project task board.
