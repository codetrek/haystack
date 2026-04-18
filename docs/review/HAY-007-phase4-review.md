# HAY-007 Phase 4 Review — HNSW + MmapStore Integration & E2E

> Reviewer: Claude Code (Opus 4)  
> PR: #57  
> Date: 2026-04-18  
> Files: `docs/tasks/HAY-007-phase4-tasks.md`, `mmap_store_hnsw_test.go` (new, +1071), `mmap_store_bench_test.go` (+357/-0)  
> Verdict: **REQUEST CHANGES** (2 P0, 5 P1)

---

## Summary

Tests-only PR. No production code changes. The PR adds comprehensive integration tests for MmapStore as an HNSW backend, covering CRUD, persistence, WAL replay crash recovery, upper-graph multi-layer verification, recall@10 validation, export integration, and SIFT-128 50K benchmarks. Test design quality is high — real assertions, proper baseline comparisons, and realistic crash simulation.

---

## 1. Design Conformance (vs. docs/design/HAY-007-mmap-store.md Phase 5)

| Phase 5 Requirement | Covered? | Notes |
|---|---|---|
| graph_upper.dat 按需分配验证 | Yes | Task 5: multi-layer, persistence, grow+crash |
| 替换 HNSW 中的 PebbleStore 为 MmapStore | Deferred (Task 7) | Correct — scope declaration explicitly defers production switch |
| E2E 测试 (SIFT benchmark) | Yes | Task 3: 50K SIFT fvecs/ivecs loader, MemStore vs MmapStore |
| Recall 验证 | Yes | Task 4: brute-force ground truth, >0.95 threshold, <0.01 diff |
| 全部现有测试通过 | Not verified | No CI evidence in PR; Go not available in review env |

**Assessment**: Good alignment. The scope split (Task 7 deferral) is explicitly documented and reasonable.

---

## 2. Test Coverage Analysis

### Task 1: HNSW + MmapStore CRUD
| Test | Real assertions? | Verdict |
|---|---|---|
| `TestMmapHNSW_InsertSearch` | 100 inserts, k=10, sorted distance check | Good |
| `TestMmapHNSW_InsertDeleteSearch` | Deleted doc mappings verified via `GetNodeId` | Good |
| `TestMmapHNSW_DeleteReinsert` | Freelist reuse, search correctness post-reinsert | Good |
| `TestMmapHNSW_Upsert` | Upsert same docId, distance ~0 check | Good |

### Task 2: Persistence
| Test | Real assertions? | Verdict |
|---|---|---|
| `TestMmapHNSW_PersistenceReopen` | Bit-exact result comparison pre/post close | Good |
| `TestMmapHNSW_PersistenceReopenContinueInsert` | Node count assertion, search post-reopen | Good |
| `TestMmapHNSW_PersistenceReopenDelete` | Delete after reopen, mapping verification | Good |
| `TestMmapHNSW_WALReplayE2E` | Crash sim (no Close), WAL replay, bit-exact match | Good |

### Task 3: 50K SIFT-128 Benchmark
| Test | Real assertions? | Verdict |
|---|---|---|
| `BenchmarkHNSW_MemStore_50K_Insert` | Reports total_sec, inserts/sec | Good |
| `BenchmarkHNSW_MmapStore_50K_Insert` | 90s hard-fail gate | Good |
| `BenchmarkHNSW_Search_MemStore_vs_MmapStore` | p50/p99 latencies reported | See P1-3 |

### Task 4: Recall@10
| Test | Real assertions? | Verdict |
|---|---|---|
| `TestMmapHNSW_RecallAt10` | Brute-force ground truth, >0.95, <0.01 diff | Good |
| `TestRecallAt10_SIFT` (bench file) | SIFT ground truth, same assertions | Good |

### Task 5: Upper Graph
| Test | Real assertions? | Verdict |
|---|---|---|
| `TestMmapHNSW_UpperGraph_MultiLayer` | Neighbor level invariant, MemStore baseline compare | Good |
| `TestMmapHNSW_UpperGraph_PersistenceReopen` | EP/maxLevel restore, bit-exact results | Good |
| `TestMmapHNSW_UpperGraph_GrowCrashRecovery` | WAL replay, upper slot leak check | Good |

### Task 6: Export Integration
| Test | Real assertions? | Verdict |
|---|---|---|
| `TestMmapHNSW_ExportRecall` | Recall >0.95, bit-exact vs MemStore | Good |
| `TestMmapHNSW_ExportThenInsertDelete` | Insert+delete post-export, node count | Good |

---

## 3. Findings

### P0 — Must Fix

**P0-1: WAL replay crash simulation writes meta but doesn't test partial-meta crash**

`TestMmapHNSW_WALReplayE2E` (line ~490-530) and `TestMmapHNSW_UpperGraph_GrowCrashRecovery` both call `writeMetaHeader()` + `syncAll()` before simulating crash. This means the mmap data files AND meta are fully consistent — the WAL replay is effectively a no-op (all data is already in the mmap files). The test passes vacuously.

A real crash scenario would have WAL entries that are NOT yet reflected in the data files. The current simulation doesn't test WAL replay at all — it tests "reopen with fully-synced data."

**Fix**: Either (a) don't call `syncAll()` — only flush the WAL, so mmap pages may not be persisted; or (b) insert additional records AFTER the syncAll/writeMeta to create genuine un-checkpointed WAL entries; or (c) add a test that explicitly corrupts/truncates a data file region to force WAL replay to reconstruct it.

**P0-2: `TestMmapHNSW_UpperGraph_MultiLayer` comment says 5000 but n=2000**

Line ~541 comment: `"with 5000 nodes, maxLevel should be > 0"` but `n = 2000`. The assertion still likely passes (2000 nodes almost always produces level>0 nodes), but the misleading comment suggests the constant was changed without updating the comment, raising the question of whether test parameters were tuned down to hide a failure.

**Fix**: Update comment to match `n=2000`, or restore `n=5000` per the task spec which says "≥5000 vectors."

### P1 — Should Fix

**P1-1: bench_test.go `TestRecallAt10_SIFT` is in benchmark build tag but is a `Test` function**

`mmap_store_bench_test.go` has `//go:build benchmark` at the top. The function `TestRecallAt10_SIFT` is a regular test, not a benchmark. It will never run in `go test ./...` (no benchmark tag) and will never run via `go test -bench=.` (it's a Test, not Benchmark). This test is dead code in normal CI.

**Fix**: Move `TestRecallAt10_SIFT` to `mmap_store_hnsw_test.go` (no build tag), or change the build constraint.

**P1-2: `recallAtK` in bench file vs `recallAtKMapped` in hnsw_test — duplicate logic**

Two recall-at-K implementations exist: `recallAtK` (bench_test.go:406) uses `SearchResult.ID` directly as index, while `recallAtKMapped` (hnsw_test.go:838) uses a node→baseIdx mapping. The direct-ID version (`recallAtK`) is incorrect when node IDs don't equal base vector indices (e.g., after deletes or freelist reuse), making `TestRecallAt10_SIFT` results unreliable.

**Fix**: Use `recallAtKMapped` consistently, or verify that in the SIFT benchmark node IDs always equal insertion order (and add an assertion for this invariant).

**P1-3: Search benchmark doesn't assert p99 < 5ms**

`BenchmarkHNSW_Search_MemStore_vs_MmapStore` reports p99 but doesn't fail if it exceeds the 5ms target from the design doc. The insert benchmark has a hard 90s gate; the search benchmark should have an equivalent.

**Fix**: Add `if p99 > 5*time.Millisecond { b.Fatalf(...) }` for the MmapStore sub-benchmark.

**P1-4: Crash simulation leaks resources without `t.Cleanup`**

In `TestMmapHNSW_WALReplayE2E` and `TestMmapHNSW_UpperGraph_GrowCrashRecovery`, the manual resource cleanup (`mmapFree`, `file.Close`) can leave leaked file descriptors if any step panics before all cleanup calls execute. Using `t.Cleanup` registered before the crash sim block would be more robust.

**Fix**: Register cleanup via `t.Cleanup(func() { ... })` before opening the store, or accept the risk (test-only code).

**P1-5: `TestMmapHNSW_RecallAt10` uses n=2000 not n=5000 as stated in task spec**

Task spec says "Uses 5000 random 128d vectors" but the test uses `n = 2000`. Recall with 2000 vectors is a weaker signal than with 5000+. The smaller dataset may mask issues that only manifest at scale (e.g., grow-related corruption).

**Fix**: Increase to `n = 5000` per spec, or document why 2000 was chosen.

### P2 — Suggestions

**P2-1: Missing `//go:build !windows` or equivalent for crash simulation tests**

The crash simulation tests use `mmapFree` and direct file handle manipulation that may behave differently on Windows (mmap files can't be deleted while mapped). Consider adding a build tag or `runtime.GOOS` skip.

**P2-2: Benchmark data dependency on external SIFT dataset**

SIFT benchmarks skip silently when data is absent (`b.Skip`). The task spec says "禁止使用合成数据" but there's no CI step to download SIFT data. Consider documenting the download step in a Makefile target or `testdata/sift/README.md`.

**P2-3: `runtime.GC()` in searchBench**

Calling `runtime.GC()` inside the benchmark loop (line ~399) can skew latency measurements if GC runs during timed operations. Move it outside the timed section or remove it.

---

## 4. nocov Usage Audit

All `nocov` annotations are in production code (`mmap_store.go`, `store.go`), not in test files. Each is on error paths that require hardware/OS-level failures:

| Location | Justification | Verdict |
|---|---|---|
| `mmap_store.go:158` WAL replay error | Requires corrupt WAL that passes CRC but fails replay | Acceptable |
| `mmap_store.go:168` post-replay checkpoint | Requires msync/rename to fail | Acceptable |
| `mmap_store.go:288` partial mmap cleanup | Requires partial mmap alloc failure | Acceptable |
| `mmap_store.go:333` closeMmaps helper | Called from Close/error; covered indirectly | Acceptable |
| `store.go:510,552,570` PebbleStore errors | Pebble internal error (not ErrNotFound) | Acceptable |

**Verdict**: nocov usage is compliant — all on genuinely untestable error paths.

---

## 5. Performance Assessment

- 50K insert benchmark has a hard 90s gate — good.
- Search benchmark reports p50/p99 but doesn't enforce the 5ms target — see P1-3.
- SIFT data loaders (`loadSiftFvecs`, `loadSiftIvecs`) are correctly implemented per standard fvecs/ivecs format.
- `runtime.GC()` in search benchmark may introduce noise — see P2-3.

---

## 6. Security

No concerns. Tests-only PR, no user input handling, no network access, no credential handling.

---

## 7. Documentation

- Task breakdown doc (`HAY-007-phase4-tasks.md`) is thorough and well-structured.
- Task 7 scope declaration is explicit and appropriate.
- Comment/code mismatch in P0-2 should be fixed.

---

## Verdict: **REQUEST CHANGES**

### Must Fix Before Merge
1. **P0-1**: WAL replay crash sim tests don't actually test WAL replay (data is fully synced before "crash")
2. **P0-2**: n=2000 vs comment says 5000 — verify intent and fix

### Should Fix
3. **P1-1**: `TestRecallAt10_SIFT` is dead code under `//go:build benchmark`
4. **P1-2**: `recallAtK` may compute incorrect recall when nodeID != insertion index
5. **P1-3**: Search benchmark needs p99 < 5ms hard gate
6. **P1-4**: Crash sim resource cleanup should use `t.Cleanup`
7. **P1-5**: Recall test uses n=2000, spec says 5000
