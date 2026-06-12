# `internal/core/storage`

The haystack-side opener for the Pebble-backed key-value store and the owner of
the on-disk storage version. See the system-level
[architecture overview](../../../docs/architecture.md) for how this package fits
into the wider design.

## Responsibility

This package is a thin opener/versioning layer over the `searchcore` KV
abstraction. It does not implement storage itself — it composes
`searchcore/kv/pebblekv` and adds two haystack concerns on top:

1. **Versioned data directories.** The current schema version is
   `StorageVersion = "1.4"` (`storage.go`). The store is opened under
   `<storagePath>/<StorageVersion>` so that incompatible on-disk formats live in
   separate directories.
2. **Stale-version cleanup.** On open, a background goroutine removes superseded
   version directories (`"index"`, `"1.0"`, `"1.1"`, `"1.2"`, `"1.3"`) under the
   storage path.

## Key API

- `Open(storagePath string, cacheSize int64) (kv.Store, error)` — creates the
  storage directory, writes a `version` marker file containing `StorageVersion`,
  opens the Pebble store via `pebblekv.Open` under the versioned subdirectory
  (defaulting `cacheSize` to 8 MiB when non-positive), and kicks off the
  asynchronous `cleanup` of old directories. It returns a `searchcore/kv.Store`.

- `StorageVersion` — the current on-disk schema version constant.

## Key-type registry

`types.go` is the single place that declares haystack-specific KV key-type
bytes. It only owns the **symbol** key types, because the workspace,
document, inverted-index, and id-table key types are now owned by their
respective `searchcore` sub-packages (`collection`, `documents`,
`invertedindex`, `idtable`) and are intentionally not redeclared here to keep a
single source of truth (a collision canary lives in `storage_test.go`):

| Constant | Byte | Meaning |
|----------|------|---------|
| `KeyTypeSymbol` | 30 | symbol inverted-index id |
| `KeyTypeSymbolDocFunctions` | 31 | per-document function records (`"df:"`) |
| `KeyTypeSymbolWords` | 33 | symbol-words inverted-index id |

`IsKeyType(key string, keyType byte) bool` reports whether a key begins with a
given key-type byte.

## Relationships

- **Depends on** `searchcore/kv` (the `Store` interface) and
  `searchcore/kv/pebblekv` (the bundled Pebble implementation). Pebble remains
  the engine for the entire full-text / inverted-index path; this package is
  where haystack opens it.
- **Consumed by** `internal/server` (`server.go` calls `storage.Open` for the
  `<data_path>/data` and `<data_path>/index` stores) and by
  `internal/core/symbols` (`codec.go` references the symbol key-type constants
  from `types.go`).
- This package does **not** open or manage the vector index; that subsystem
  (`internal/core/vectorindex`) is backed by its own mmap store, not Pebble.
