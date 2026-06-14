# vectorindex performance investigation — detailed timings

Raw measurements gathered while investigating CI/runtime perf. Kept so the
detailed numbers aren't lost when CI instrumentation is reverted.

## 1. Windows CI test step — where the time goes (measured on the runners)

vectorindex-only test step (`go test ./internal/core/vectorindex/...`):

| | Windows | macOS | my Linux box |
|---|---|---|---|
| compile + link (7.3 MB test binary) | 2s | 1s | ~0 (warm) |
| Go build cache restored? | yes (299M) | yes (315M) | — |
| RUN_ALL (pure execution) | 54s | 33s | ~11s |
| RUN — without fault/crash/WAL tests | 41s | 27s | ~11s |
| RUN — only fault/crash/WAL tests | 14s | 7s | 0.026s |

Conclusion: it's test-execution **file I/O**, not compile. The fault-injection
tests (added in #67) cost ~14s on Windows / ~0.026s locally — each spins up and
tears down a real mmap store; only Windows charges heavily for that lifecycle.

## 2. Windows per-test breakdown (fault/crash/WAL subset)

before the msync fix (broad subset total 30s):

| test | Windows before | Windows after fix |
|---|---|---|
| TestKill9Recovery_E2E | 11.27s | 1.59s |
| TestMmapHNSW_WALReplayE2E | 3.16s | 0.24s |
| TestMmapStoreGrowUpperGraph | 2.43s | ~0.1s |
| TestMmapStoreGrowConcurrent | 1.86s | 0.97s |
| TestMmapHNSW_UpperGraph_GrowCrashRecovery | 2.25s | 2.63s (left as-is, compute-bound) |
| subset total | 30s | 21s |

Root cause: non-batch ops doing per-operation `msync` (~10ms each on Windows).
Fix: reduce N / batch the bulk setup so the per-op msync count drops.

## 3. macOS per-test — the SIMD inversion

`TestMmapHNSW_UpperGraph_GrowCrashRecovery` (3000-node HNSW build, compute-bound):

| | macOS (arm64) | Windows (x86) |
|---|---|---|
| time | 6.15s | 2.63s |

macOS is *slower* here despite a faster CPU, because `viterin/vek`'s SIMD is
AVX2/amd64-only; on arm64 it falls back to a scalar dot product. This is the
real-world arm64 distance penalty (not a CI artifact).

## 4. arm64 NEON dot product — measured on the macOS (Apple Silicon) runner

Hand-written NEON kernel (`dot_arm64.s`) vs vek's scalar fallback:

| dim | NEON (`dot`) | vek scalar | speedup |
|---|---|---|---|
| 128 | 47.36 ns/op | 100.3 ns/op | **2.12x** |
| 768 | 262.1 ns/op | 951.4 ns/op | **3.63x** |

HNSW end-to-end on arm64 with the NEON kernel: Insert 1.10 ms/op, Search 101 µs/op.
(amd64 cross-check: `dot` == vek, no regression — 10.84 vs 10.51 ns/op @ dim128.)

### arm64 NEON vs amd64 AVX2 — both runners, 3000x, same benchmark

Dot kernel:

| dim | arm64 NEON `dot` | arm64 scalar (vek) | amd64 AVX2 (vek) |
|---|---|---|---|
| 128 | 47.4 ns | 100.3 ns (2.1× slower) | 9.1 ns |
| 768 | 262 ns | 951 ns (3.6× slower) | 39.5 ns |

HNSW end-to-end:

| | arm64 NEON | amd64 AVX2 | arm64/amd64 |
|---|---|---|---|
| Insert | 1.10 ms | 0.61 ms | 1.80× |
| Search | 101 µs | 91 µs | 1.11× |

Interpretation: NEON removes vek's scalar penalty on arm64 (2.1–3.6× faster dot,
growing with dim). It does NOT make arm64 == amd64 — AVX2 is 256-bit (8 lanes)
vs NEON 128-bit (4 lanes), plus the macOS runner is smaller — so arm64 stays
~1.1–1.8× behind amd64. But Search nearly reaches parity (1.11×), and the dot
hot path is no longer scalar. The remaining headroom on arm64 would come from a
wider/unrolled NEON kernel (multiple accumulators) — deferred; the simple
1-accumulator kernel already captures the bulk of the win.
