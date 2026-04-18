# HAY-007 Phase 2 Full Review — Round 3

**PR**: #55 `feat(vectorindex): HAY-007 Phase 2 - Write paths, WAL, Batch, Grow`
**Date**: 2026-04-18
**Reviewer**: Claude Opus 4 (code review)
**Verdict**: **REQUEST CHANGES**

---

## Targeted Check: scanLSN + maxWalPayloadSize

**Status: FIXED** (commit `3637ad3`)

`scanLSN()` now checks `if length > maxWalPayloadSize { break }` before allocating payload buffer, matching the same guard in `replayWAL()`. This closes the OOM vector on corrupted WAL during open.

---

## 7-Point Checklist

### 1. Design Compliance — PARTIAL PASS

| Requirement | Status |
|---|---|
| WAL with LSN + CRC32 | PASS |
| Batch mode (deferred sync) | PASS |
| File grow (2x expansion) | PASS |
| 50K insert < 90s benchmark | PASS (0.38s reported) |
| Lock ordering: muGraph -> muNodes -> muVec | **FAIL** — PutNode acquires muVec.RLock (line 41) before muGraph.Lock (line 55) |
| Crash recovery via WAL replay | PARTIAL — replay works but metadata updates unprotected |

### 2. Test Coverage — PARTIAL PASS

**Present**: WAL roundtrip (5 record types), WAL corruption/truncation recovery, write roundtrip, batch nesting, grow preservation, concurrent grow+read, 50K benchmark, node mapping persistence, NextNodeId persistence.

**Missing**:
- No crash recovery end-to-end test (kill mid-write, reopen, verify)
- No concurrent write stress test (would expose metadata races)
- No concurrent Append+Replay WAL test
- No grow failure / error path test

### 3. Error Handling — PARTIAL PASS

- WAL CRC mismatch -> truncate at last valid record: good
- WAL payload > 64MiB -> reject: good (both scanLSN and replayWAL)
- Close() collects first error but continues cleanup: acceptable
- **Gap**: grow failure (mmapAlloc fails after Truncate) leaves file truncated with no mmap — unrecoverable without rollback
- **Gap**: replayWAL callback failure leaves metadata inconsistent

### 4. Code Quality — PARTIAL PASS

- Clean file decomposition (wal, write, grow, bench — each focused)
- Consistent encoding (little-endian everywhere)
- Good use of bufio for WAL I/O

**Issues**:

| # | Severity | File:Line | Issue |
|---|---|---|---|
| C1 | CRITICAL | mmap_store_write.go:41-61 | **Lock ordering violation**: muVec.RLock before muGraph.Lock — deadlock risk with concurrent grow |
| C2 | CRITICAL | mmap_store_write.go:79-86,193-196,259-260,341-342 | **s.meta unprotected**: PutNode, SetEntryPoint, DeleteNode, NextNodeId all mutate s.meta without any lock |
| C3 | CRITICAL | mmap_store_write.go:30-49 | **Capacity stale after ensure**: ensureVecCapacity called outside muVec; grow unmaps old region before write acquires RLock -> potential SIGSEGV |
| C4 | HIGH | mmap_store_write.go:215-222 | **idmapFile.Write outside muDoc** -> concurrent SetNodeMapping interleaves file writes |
| C5 | HIGH | mmap_wal.go:195-258 | **Replay concurrent with Append**: Replay seeks to 0 and resets bufio while Append may be writing. In practice only called during Open (single-threaded), but API doesn't enforce this |
| C6 | MEDIUM | mmap_windows.go:40-43 | **munmapPlatform crash on empty slice**: &data[0] panics when len(data)==0 (Unix side is safe) |
| C7 | MEDIUM | mmap_wal.go:78-92 | **scanLSN allocates per-record**: header/payload/crc buffers allocated in every loop iteration — GC pressure on large WAL |

### 5. Performance — PASS

- 50K x 128d in 0.38s (batch mode) — well within 90s target
- 2x file growth amortizes resize cost
- WAL buffered writer reduces syscalls
- rebuildNodeCount is O(N) on open but acceptable for current scale

### 6. Security — PASS (with notes)

- maxWalPayloadSize (64 MiB) prevents OOM on malicious WAL — good
- CRC32 is not cryptographic; acceptable for integrity, not tamper-proofing
- docId encoded as uint16 length — silently truncates at 65535 chars (document or validate)

### 7. Documentation — PASS

- Design doc (HAY-007-mmap-store.md) is thorough and matches implementation intent
- Commit messages are clear and well-structured
- Lock ordering documented in design but should be added as code comments

---

## Required Changes Before Merge

### Must Fix (3 critical)

1. **C1 — Lock ordering**: Reorder PutNode to acquire muGraph.Lock before muVec.RLock
2. **C2 — Metadata protection**: Add a mutex (or use existing locks) to protect all s.meta mutations
3. **C3 — Capacity re-check under lock**: After ensureVecCapacity, re-verify capacity under muVec.RLock before writing

### Should Fix (2 high)

4. **C4 — idmapFile write race**: Extend muDoc lock to cover idmapFile.Write
5. **C6 — Windows munmap empty check**: Add `if len(data) == 0 { return nil }` guard

### Nice to Have

6. C5 — Document that Replay must only be called during single-threaded Open
7. C7 — Reuse buffers in scanLSN (perf, not correctness)
8. Add crash recovery integration test
9. Add concurrent PutNode stress test with `-race`
10. Add lock ordering comment in code (e.g., top of mmap_store.go)

---

## Verdict

**REQUEST CHANGES** — 3 critical concurrency issues (lock ordering violation, unprotected metadata, stale capacity after grow) create deadlock and data corruption risk under concurrent load. The scanLSN fix (original review target) is confirmed correct. Estimated fix effort: 2-3 hours.
