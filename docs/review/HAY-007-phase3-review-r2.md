# HAY-007 Phase 3 — Code Review R2 (Post P1 Fixes)

**PR**: #56  
**Branch**: `feat/hay-007-phase3-checkpoint`  
**Reviewer**: Pegasus (Claude)  
**Date**: 2026-04-18  
**Verdict**: **APPROVE**

---

## Checklist Summary

| # | Area | Verdict | Notes |
|---|------|---------|-------|
| 1 | Design (Checkpoint + Recovery) | PASS | msync → writeMeta → WAL Reset → compactIdmap ordering correct |
| 2 | Test Coverage (8 crash + crashAfterMeta) | PASS | All 8 crash scenarios + dedicated crashAfterMeta test |
| 3 | Error Handling | PASS | P1 fix: idmapFile.Close() error handled |
| 4 | Code Quality | PASS | Clean, well-structured |
| 5 | Performance | PASS | No unnecessary allocations in hot path |
| 6 | Security | PASS | No injection vectors; crash hooks unexported + nil-guarded |
| 7 | Documentation | PASS | Comments adequate |

---

## P1 Fix Verification

### 1. crashAfterMeta hook has test? **YES**

`mmap_checkpoint_test.go:614` — `TestCrashPoint_AfterMeta` explicitly sets `s.crashAfterMeta` and panics after `writeMetaHeader` but before WAL truncate. Correctly split from the old `TestCrashPoint_BeforeTruncate` (now at line 659) which tests `crashBeforeTruncate`. Both hooks are exercised independently.

### 2. compactIdmap Close error handling? **YES**

`mmap_store_write.go:468` — `s.idmapFile.Close()` error is checked. On failure, `nf` (new file handle) is closed to prevent leak, and the error is returned wrapped. Correct.

### 3. muDoc.Lock during idmapFile replacement? **YES**

`mmap_store_write.go:467-474` — `s.muDoc.Lock()` is acquired before `s.idmapFile.Close()` + `s.idmapFile = nf` and released after. This prevents a concurrent `SetNodeMapping` from writing to the stale fd. The earlier read phase correctly uses `s.muDoc.RLock()` (line 420).

---

## Detailed Review

### Design Correctness

**Checkpoint ordering** (`checkpointLocked`):
1. `syncAll()` — msync all 4 mmap regions
2. `writeMetaHeader` — persist `WalCheckpointLSN`
3. `wal.Reset()` — truncate WAL (LSN preserved in memory)
4. `compactIdmap()` — rewrite idmap.dat atomically via tmp+rename

This is the correct crash-safe ordering. If crash occurs:
- After step 1 but before 2: WAL still has records, they replay idempotently (mmap already has the data, meta still has old LSN)
- After step 2 but before 3: WAL has stale records, LSN filter in `Replay` skips them (LSN <= WalCheckpointLSN)
- After step 3 but before 4: idmap is stale but still valid (append-only, compaction is optimization only)

**Post-replay checkpoint** (`mmap_store.go:167-174`): After WAL replay during Open, if `wal.LSN() > meta.WalCheckpointLSN`, a checkpoint is triggered. This persists recovered state and prevents re-replay on next Open. Error handling properly cleans up all resources.

**Auto-checkpoint** (`maybeCheckpoint`): Increments ops counter, triggers checkpoint when threshold reached. Skipped in batch mode (deferred to `CommitBatch`). `CommitBatch` checks threshold after sync.

**Close** delegates to `checkpointLocked()` — clean, no duplication of msync/writeMeta logic.

### Test Coverage

| Test | Crash Point | Hook | Verified |
|------|------------|------|----------|
| 6a `TestCrashPoint_AfterWALWrite` | After WAL append, before mmap write | `crashAfterWALWrite` | WAL replay recovers node |
| 6b `TestCrashPoint_AfterMsync` | After msync, before meta write | `crashAfterMsync` | WAL replays all data |
| 6c `TestCrashPoint_AfterMeta` | After meta write, before WAL truncate | `crashAfterMeta` | LSN filter skips stale WAL records |
| 6c-b `TestCrashPoint_BeforeTruncate` | Before WAL truncate | `crashBeforeTruncate` | Same as 6c, different hook |
| 6d `TestCrashPoint_PartialWALRecord` | Partial bytes appended | Manual corruption | Good records recovered, junk ignored |
| 6e `TestCrashPoint_PartialMeta` | meta.bin truncated to 32 bytes | Manual corruption | Open returns error |
| 6f `TestCrashPoint_GrowMidWrite` | 1030 nodes (triggers grow), crash | simulateCrash | Replay re-grows and recovers |
| 6g `TestCrashPoint_SetNeighborsCrash` | After SetNeighbors WAL | simulateCrash | Neighbors recovered |
| 6h `TestCrashPoint_DeleteNodeCrash` | After DeleteNode WAL | simulateCrash | Tombstone recovered |

Additional:
- `TestKill9Recovery_E2E` — 1000 vectors + neighbors + deletes, skip Close, full verification
- `TestCheckpoint_MetaAndWALTruncated` — basic checkpoint behavior
- `TestCheckpoint_ContinueWriteAndReplay` — write after checkpoint, verify replay
- `TestCheckpoint_LSNMonotonic` — LSN increases after Reset
- `TestAutoCheckpoint_*` (3 tests) — interval trigger, below threshold, batch mode
- `TestCrashRecovery_BasicReplay` / `AfterCheckpoint` — integration recovery tests

### Potential Issues (All P2 — Non-blocking)

**P2-1: `compactIdmap` tmp file not cleaned on rename failure**  
`mmap_store_write.go:458` — if `os.Rename(tmp, path)` fails, the `.tmp` file is left on disk. Minor: rename rarely fails on same filesystem.

**P2-2: `Close()` calls `checkpointLocked()` without holding `muWrite`**  
`mmap_store.go:189` — `Close()` does not acquire `muWrite`. If a concurrent write is in progress when Close is called, there's a potential race. This is acceptable if the contract is "no concurrent operations during Close" (standard Go pattern), but worth documenting.

**P2-3: `rebuildNodeCount` on every replay**  
`mmap_store.go:505-507` — After replay, `rebuildNodeCount` scans all slots. For large indexes this could be slow. Acceptable for correctness-first approach; can optimize later with WAL-based count tracking.

**P2-4: Double sync in CommitBatch + auto-checkpoint path**  
When `CommitBatch` syncs (line 322) and then triggers `checkpointLocked` (line 327), `syncAll()` runs again inside checkpoint. Minor performance cost, not a correctness issue.

---

## Verdict

**APPROVE** — All P1 fixes verified. Design is crash-safe with correct ordering. Test coverage is comprehensive across all 8 crash scenarios plus the new `crashAfterMeta` hook. No P0 or P1 issues remaining. P2 items are non-blocking and can be addressed in future iterations.
