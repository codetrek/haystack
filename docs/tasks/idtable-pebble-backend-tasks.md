# idtable → shared-store restore — Task Breakdown v2

> Status: **Draft — awaiting review (SDD step 4).** Supersedes v1 tasks.
> Spec: `docs/design/idtable-pebble-backend.md` v2 (Approved 2026-06-24).
> Goal: restore the #97-predecessor shared-store idtable; re-wire BOTH consumers; no StorageVersion bump.

### T0 — Clean the branch of the v1 (wrong) commits
Drop `refactor(idtable): pebble backend` (94627f1) and `feat(storage): bump StorageVersion` (f2d26e8). Keep `docs(agents)` + the spec/tasks docs (now v2). Result base: AGENTS.md + v2 docs on top of #103; idtable.go/storage.go back to the #103 state.
**Verify**: `git log` shows no v1 code/storage commit; `storage.go` `StorageVersion == "1.5"`; tree builds.

### T1 — Restore `idtable.go` to the shared-store allocator
Base = `873ef7f~1:core/idtable/idtable.go` (`New(store kv.Store, opts)`, `DefaultKeyType{NextId,Key}` 28/29 configurable, LRU + `store.NewBatch` + ticker). Then:
- **Add** the post-#97 API consumers use: `Lookup()` (non-allocating `store.Get`); `CrashRelease()` with **shared-store semantics** (stop ticker + discard pending + mark closed; do NOT close the caller's store).
- Reconcile #103: the 28/29 consts are `DefaultKeyType*` again (active defaults, not `LegacyKeyType*` reserved).
- No `Open(path)`, no pebblekv/os import, no invented prefixes/cache.
**Verify**: `go build ./core/idtable/`.

### T2 — idtable tests back to `New(store, …)`
Tests construct a real `kv.Store` (pebblekv in a temp dir) and call `New`. Restore the pre-#97 test shapes; keep `Lookup`/`CrashRelease`/`Commit`/ticker/closed-guard tests.
**Verify**: `go test ./core/idtable/` + `go test -race -run 'Close|Commit|Periodic' ./core/idtable/`.

### T3 — Re-wire indexer (`internal/server/server.go`)
`idtable.Open(idtablePath)` → `idtable.New(db, idtable.Options{})` against the shared `data` store (default 28/29). Drop `idtablePath`.
**Verify**: `go build ./internal/server/`; `go test ./internal/server/`.

### T4 — Re-wire vectorstore (`core/vectorstore/store.go` + tests)
Restore `Options.KV kv.Store`, the `idtableKeyTypeNextId=40`/`idtableKeyTypeKey=41` consts, `idtable.New(opts.KV, {40,41})`, and the id→docId reverse-map helper; drop `idtable.Open(opts.Dir/idtable.db)`. Keep the bbolt control plane. Update the ~20 vectorstore test files to pass `opts.KV` (a pebblekv store).
**Verify**: `go test ./core/vectorstore/`; re-check recovery tests under `-race` (R3: `CrashRelease` must not close the caller's store).

### T5 — storage collision registry
`internal/core/storage/storage_test.go`: keep the idtable 28/29 entries; rename the symbol ref `idtable.LegacyKeyType*` → `idtable.DefaultKeyType*` (now active defaults). Re-add the `idtable` import.
**Verify**: `go test ./internal/core/storage/`.

### T6 — Full verification
`go build ./...`; `go vet ./...`; `go test ./core/idtable/... ./core/vectorstore/... ./internal/server/... ./internal/core/storage/...` + idtable consumers (documents/indexer/searcher); go-cov gate on affected packages.

## Acceptance
1. idtable is a shared-store allocator (`New(store, opts)`); **no** `Open(path)`, no separate idtable file created anywhere.
2. indexer uses the shared `data` store @ 28/29; vectorstore uses `opts.KV` @ 40/41.
3. `StorageVersion` is still **1.5** (no bump).
4. `Lookup` kept; `CrashRelease` shared-store semantics (doesn't close caller's store).
5. build + vet + all affected tests green; go-cov gate holds.

## Branch / commit plan
- T0 drops the two v1 code commits (rebase --onto / reset, keep AGENTS.md + v2 docs).
- New commits: `docs(idtable): restore shared-store design (v2 spec/tasks)` / `refactor(idtable): restore shared kv.Store allocator` (T1+T2) / `refactor(vectorstore,server): wire idtable over the shared store` (T3+T4+T5).
- Update PR #104 (force-push) or open fresh — decide at commit time.
