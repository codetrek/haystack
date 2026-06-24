# idtable: restore the shared-store implementation (over pebble) — Spec v2

> Status: **Approved (2026-06-24, SDD step 2). SUPERSEDES the v1 separate-pebble design.**
> Date: 2026-06-24
> Author: Claude (Opus 4.8)

## 0. Correction (why v2)

v1 **redesigned** idtable as its own pebble store at a path, with invented `0x00`/`0x01`
key prefixes. That is wrong on the core design point: **idtable is a thin allocator
OVER a shared `kv.Store`**, namespaced by reserved single-byte prefixes, so it
**coexists with other data in one pebble instance**. v2 restores the #97-predecessor
implementation (`873ef7f~1:core/idtable/idtable.go`). PR #104 (v1) will be closed/redone.

## 1. Problem & approach

idtable has been standalone bbolt since #97 → a separate ~1GB file at 8M entries.
**Restoring the pre-#97 design puts idtable's rows back into the existing shared pebble
store** (already pebble, dense) — the separate bbolt file simply disappears. No new
pebble instance, no invented prefixes, no `Open(path)`.

## 2. Design (restore #97-predecessor)

- **API**: `New(store kv.Store, opts Options) (*Allocator, error)` — takes a shared store,
  does NOT open or own it (caller owns Close of the store).
- **Prefixes** (configurable, namespacing within the shared store):
  `Options.KeyTypeNextId` (default **28**), `Options.KeyTypeKey` (default **29**);
  byte `0` = "use default". nextId at `{28}` (decimal string), key→id at `{29}+key`
  (8-byte big-endian). Restore the `DefaultKeyType{NextId,Key}` names (#103 had renamed
  them `LegacyKeyType*` "reserved" — they're active again).
- **Mechanics**: LRU + `store.NewBatch(BatchSize)` + commit ticker; `Commit`/tick/Close
  flush the batch (durability follows the store's own write opts — pebble Sync/NoSync).
- **Keep the post-#97 additions consumers use**:
  - `Lookup()` (non-allocating `store.Get`) — vectorstore uses it on the read path.
  - `CrashRelease()` (tests only) — shared-store semantics: stop the ticker + discard
    pending + mark closed; **does NOT close the store** (the caller owns it).
- **Drop from v1**: `Open(path)`, stale-file removal, the own pebblekv instance,
  `0x00/0x01` prefixes, `pebbleCacheSize`, the NoSync-specific Commit doc.

## 3. Re-wire BOTH consumers

- **indexer** (`internal/server/server.go`): `idtable.New(db, idtable.Options{})` against
  the shared `data` pebble store with the default 28/29 prefixes. (Drop `Open(path)`.)
- **vectorstore** (`core/vectorstore/store.go`): restore `Options.KV kv.Store`, the
  `idtableKeyTypeNextId=40`/`idtableKeyTypeKey=41` consts, `idtable.New(opts.KV, {40,41})`,
  and the id→docId reverse-map helper. Its caller provides a pebble store. Keep its bbolt
  control plane unchanged. (Vectorstore is not yet wired into prod, so this mostly touches
  its own tests.)

## 4. Affected files

- `core/idtable/idtable.go` — restore shared-store impl + add `Lookup`/`CrashRelease`.
- `core/idtable/*_test.go` — back to `New(store, …)` (tests provide a `kv.Store`).
- `core/vectorstore/store.go` + its ~20 test files — provide `opts.KV`.
- `internal/server/server.go` — indexer wiring.
- `internal/core/storage/storage_test.go` — 28/29 stay in the collision registry (now
  *active*, not just reserved) → rename ref `LegacyKeyType*`→`DefaultKeyType*`.
- **Revert v1**: undo the `idtable.go` separate-pebble rewrite + the v1 idtable test edits.

## 5. Keep / drop from v1

- **StorageVersion stays 1.5** (review decision — NO bump); the v1 `feat(storage)` 1.6
  commit is dropped. (Caveat for the record: on a deployment that went through #97,
  idtable's 28/29 keyspace in the shared store is stale, so restoring it restarts docids
  and could mismatch the old invertedindex/documents — not force-reindexed here, per the
  decision. The orphan bbolt `idtable.db` is left for deploy cleanup.)
- **KEEP**: AGENTS.md Principle 0/1 + the spec/tasks process artifacts.
- **DROP**: the v1 `refactor(idtable)` separate-pebble code + the `feat(storage)` bump.

## 6. Risks

- **R1** vectorstore re-gains a pebble-store dependency (`opts.KV`), reversing #97's
  "pebble-free vectorstore". User accepted (restore both).
- **R2** large diff: effectively reverts #97's idtable extraction across idtable +
  vectorstore + ~20 test files, adapted to current main (#96 int64 docids, #103).
- **R3** `CrashRelease` shared-store semantics (must NOT close the caller's store) — verify
  vectorstore recovery tests still pass.

## 7. Verification

- `go test ./core/idtable/...` (+ `-race`) green; tests drive a real `kv.Store`.
- `core/vectorstore` green with `opts.KV` provided.
- `internal/server` / `internal/core/storage` green; `go build ./...` + `go vet ./...` clean.
- No standalone `idtable.db` created; idtable rows live under 28/29 (shared store) / 40/41
  (vectorstore KV).

## 8. Review decision (2026-06-24, Approved)

- **StorageVersion stays 1.5** — no bump; drop the v1 `feat(storage)` commit.
- Keep AGENTS.md governance + spec/tasks; drop the v1 `refactor(idtable)` code; redo
  idtable + vectorstore for the shared-store design.
- Rest of v2 approved → proceed to task breakdown (SDD step 3).
