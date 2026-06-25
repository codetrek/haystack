# invertedstore — Ingestion-Path Performance (Spec)

Status: **proposal / for review**. Scope: cut the store's cold-build wall time (measured 95s on
the linux corpus, ~1.5× pebble's 61s) by attacking the ingestion path, NOT the already-fixed
covering-merge trigger. Five changes (A–E) from the profiling session; D is a decision, the rest
are code.

Harness: `core/cmd/idxbench` (drives `*invertedstore.Store` and pebble-`*invertedindex` through the
same `invertedindex.Indexer` seam, real ext4). Prior fix: `invertedstore-covering-trigger-fix-spec.md`.

---

## 1. Where the 95s goes (measured, per-doc full lx — the profiling basis)

The whole build runs on the **single mpsc worker** (worker = 92s of the 95s critical path). Worker
work, all serialized:

| block | worker time | what |
|---|---|---|
| `mergeSegments` | ~34s | tiered-merge zstd re-compression — runs **on the worker** via `runScheduledMerge → q.RunFunc` |
| `spill` | ~28s | encodeDocs sort 11 + writeTermDict re-read 9 + flushBlock snappy 6 |
| `addPosting` | ~19s | head map inserts (mostly map-growth → mallocgc) |
| `forwardKeywords` | ~8s | per-doc "read old keyword set" — scans **every** segment (no skip), `lookupForward` decompresses one block/segment |

Underneath: ~1 GB/s allocation (≈107 GB total) → **1838 GC cycles** (heap pinned ~88 MB). On 32
cores GC runs parallel-free (program uses only ~1.9 cores), so GC does NOT steal the worker — the
worker's ~92s is real serial work, inflated by per-op allocation. Disabling the forward scan saved
only ~10s (it allocates a lot but is GC'd in parallel), confirming **the lever is serialization +
per-op work, not GC tuning** (GOGC=400 made it WORSE: heap ballooned to 7.3 GB).

pebble (61s) wins because: compaction runs on **background threads** (off the ingest critical
path); no zstd during ingest (L0 is cheap); bloom filters make forward reads O(1)-ish.

**Anomaly resolved:** batched (1000/batch) was SLOWER (115s, 2.8 GB peak) purely because the mpsc
queue is a bounded channel of **depth 100** — 100 × 1000-op batches = 100k docs' keyword copies
buffered in flight. A harness artifact (the gob feed races ahead; production documents.Store is
I/O-bounded), but it exposes that the write-path backpressure bounds **task count, not work/memory**
(item E).

---

## 2. The changes (review-calibrated; D keep, F optional)

| # | change | est. WALL win (review-calibrated) | risk |
|---|---|---|---|
| **A** | merge COMPUTE off-worker, install on-worker (§3) | ~30s → build **~55–62s ≈ pebble parity** | LOW (single mutator preserved) |
| **B** | per-segment `[minDocid,maxDocid]` forward-read skip | **~6s** (not 8); + bounds forward-read as K grows (couples with A) | low |
| **C** | cut per-op allocation churn | **~0–3s wall** — a MEMORY/GC-cycle play, not wall (GC is parallel-free; GOGC=400 was worse) | medium |
| **D** | **keep zstd** for merged (off the critical path after A) | — | none |
| **E** | write-path backpressure by in-flight **postings/bytes** | ~0 wall; bounds memory; fixes the batched blowup | medium |
| **F** | (optional) move spill ENCODE off-worker (§7a) | the real lever PAST parity (spill ~28s is the post-A floor) | higher |

**Honest target:** A+B+C ≈ **match pebble (~55–65s)**, not a clear win. The untouched serial spill
(~28s) is the floor; only F beats pebble. Every number is re-measured on real ext4 after each lands.

---

## 3. (A) Merge COMPUTE off the worker, install ON the worker — single-mutator preserved

> **v2 (post-review).** The first draft moved the WHOLE merge (compute + install) onto a dedicated
> goroutine → two concurrent writers of the MANIFEST/segment set. Review found that breaks the
> single-mutator invariant in four places (spill, installMerge, **CreateTable, DeleteTable** all
> write the MANIFEST; the last two write it under `s.mu` on the worker and would race the merge
> goroutine's rename + invert the lock order → deadlock/torn MANIFEST). It also bought almost
> nothing extra, because the install is already milliseconds. **v2 splits the merge:**

**The expensive part is `mergeSegments` (~34s: decompress inputs + zstd-recompress output), and it
mutates ZERO shared state** — it only reads refcounted input segments and writes a brand-new output
segment file at a pre-reserved id. The cheap part is `installMerge` (~ms: swap `s.man`/`s.segs`,
publish snapshot, retire inputs, write MANIFEST).

**Change:** the merge goroutine runs `mergeSegments` (the 34s) **off the worker**, then hands the
resulting `mergeResult` back to the worker via `s.q.RunFunc(func() { installMerge(...) })`. So:
- `mergeSegments` overlaps applies/spills on a free core (the build uses only ~1.9 of 32 cores).
- `installMerge` stays on the **single worker** → exactly ONE MANIFEST writer and ONE `s.man`/
  `s.segs` mutator, unchanged. **No `manifestMu`, no lock-order rework, no CreateTable/DeleteTable
  change, no two-writer race.** The P9 invariant the whole design rests on is preserved verbatim.

**Restructure (`runScheduledMerge` / `maybeMerge` / `mergeOneLevel` / `coveringMerge`):** today they
call `mergeSegments` then `installMerge` back-to-back inside one `q.RunFunc` (so the 34s runs on the
worker). Split: reserve `outId` (still under `s.mu`), run `mergeSegments` in the merge goroutine
(no lock held — it only reads refcounted inputs + writes a new file), then `s.q.RunFunc(installMerge)`
for the swap. The merge goroutine must hold reader refcounts on its inputs across `mergeSegments`
(acquire a snapshot of the input ids) so a (future) concurrent merge or a retire can't free them
mid-read — today merges are serial in the one goroutine and only a merge retires, so the existing
single-goroutine serialization already guarantees this; keep merges strictly serial in the goroutine.

**Quiescence:** `waitMergeIdle` already fences on `mergeAckSeq` reaching the sampled `mergeReqSeq`;
since the install still runs via `q.RunFunc` on the worker, the existing `RunFunc`-fence semantics
are preserved (the install — the state change — is still a worker task). `CloseAndWait`/`stopMergeLoop`
likewise unchanged: the merge goroutine's final drain still installs via the worker `RunFunc`, fenced
by `<-mergeDone`.

**Honest expected win (review-calibrated, NOT asserted):** `mergeSegments` ~34s leaves the worker;
the worker's remaining serial floor is spill ~28 + addPosting ~19 + forwardKeywords ~8 ≈ **~55s**.
So A lands the build around **55–62s ≈ pebble parity**, NOT a guaranteed win — and deferred merges
merge MORE data if the goroutine falls behind the producer (cf. the AutoMerge-off 107s). The serial
**spill ~28s is the post-A floor**; beating pebble needs item **F (§7a)**, not A alone.

---

## 4. (B) Forward-read docid-range skip

`forwardKeywords` (applyBatch's "read old keyword set") loops every sealed segment calling
`lookupForward` → decompresses one block/segment to look for the docid. On a cold build of all-new
docids the lookup always MISSES but still decompresses a block in every segment → O(docs × segments).

**Change:** add `MinDocid,MaxDocid int64` to `segMeta` (and the in-memory `segment`), set from the
spilled/merged forward records' docid span. `forwardKeywords` skips a segment when `docid <
seg.minDocid || docid > seg.maxDocid` — no forward record can exist there. Monotonic cold-build
docids ⇒ a new doc is above every sealed range ⇒ probes ZERO segments. An existing doc probes only
the segment(s) whose range covers it.

- Range covers EMITTED forward records (live + tombstone); an empty output keeps an empty range
  (`min > max`) that always skips.
- `noteForwardRead` moves to fire on the FIRST real probe (a fully-skipped read touches no I/O, so
  it must not count as a forward read — same spirit as the existing `len(segs)==0` fast path).
- Correctness: skipping a segment that provably has no record for the docid cannot change the
  resolved keyword set — guarded by the existing differential test + a probe-count unit test.

This is a *range* check, not a bloom filter: it is exact for the cold-build (disjoint ascending
ranges) and for any docid outside all ranges; for an overlapping/edit workload it conservatively
probes every segment whose range spans the docid (correct, just less optimal). Bloom is a possible
follow-up; range is enough for the build win and is free (two int64 in the MANIFEST).

## 5. (C) Cut per-op allocation churn — a MEMORY/GC play, ~0–3s wall

> **Review-calibrated:** GC is parallel-free (32 cores, build uses ~1.9); GOGC=400 was *worse*. So
> reducing allocation cuts the **heap/GC-cycle count/peak memory**, but moves WALL time only to the
> extent `mallocgc` is on the worker's serial path (partly — addPosting's map growth). Expect **~0–3s
> wall, not 10s.** Measure each sub-item **AFTER A+B land** (C.1's decompress is mostly removed by A
> (merge off-worker) + B (forward skip), so don't double-count). Keep only sub-items that move the
> *worker serial* time; ship the rest as memory wins or drop them (AGENTS.md P1 — real wins only).

1. **Skip the per-op `inBatch`/`seen` maps for a single-op apply** (the highest-value sub-item — hits
   the hot `Update` path, which is always 1-op). `applyBatch` allocates two maps/call to track
   in-batch repeats; a 1-op batch can't repeat a docid, so `seen` is always false and `old` always
   comes from `forwardKeywords` — a fast path that skips both maps is behavior-identical. Guard
   `len(ops)==1`. (Review-verified safe.)
2. **Reuse decompress buffers — `mergeCursor`-scratch ONLY, never a global.** `c.key`/`c.val` are
   slices INTO `c.blk`, and a k-way merge holds K cursors' blocks live simultaneously, so a shared
   global buffer would alias them — **must be per-cursor scratch**, reused across `advance` (the prior
   block's bytes are consumed before the next advance — review-verified). External-value buffers
   likewise. (Most of this is removed by A+B; measure what remains on the worker.)
3. **Reuse the spill/encode scratch** (`setToSlice`/`encodeDocs` temporaries, consumed immediately on
   the single-threaded worker) where provably not retained.

## 6. (D) Keep zstd for merged segments — DECISION

With A moving the merge COMPUTE off-worker (§3), the zstd re-compression cost is **off the apply
critical path**. zstd's disk win (−25% vs snappy, measured) is worth keeping. No change.

## 7. (E) Write-path backpressure by in-flight work

The mpsc queue blocks at **100 tasks** regardless of task size, so 100 large batches buffer 100k docs
(2.8 GB). Bound **in-flight work**, not task count.

**Design (E1):** the Store holds an in-flight budget as a buffered token channel. `Update`/
`Batch.Commit` ACQUIRE tokens **on the producer goroutine, BEFORE `q.AddFunc`** (blocking when the
budget is exhausted — natural backpressure); `applyBatch` (on the worker) RELEASES them via a
top-of-function `defer` so EVERY exit path (incl. the mid-batch spill error return) releases exactly
what was acquired. Review-mandated constraints:
- **Budget by `Σ len(op.keywords)` (postings), NOT op-count** — docs vary wildly in keyword count, and
  the OOM vector is keyword copies (postings/bytes), not tasks. Budget ≈ a few × CapBytes worth.
- **The acquire MUST be on the producer, never inside `applyBatch`** — `applyBatch` runs on the sole
  consumer worker; acquiring there would self-deadlock (the worker waiting for itself to drain).
- **Single batch larger than the budget**: cap acquisition at `min(postings, budget)` (or split), or
  it self-deadlocks waiting for tokens that can't free until the batch is enqueued+applied.

**Alternative (E2):** the Store's own apply channel + dedicated apply goroutine (decoupled from the
shared mpsc); the channel capacity is the bound. Cleaner but restructures worker ownership + the
integration. Deferred.

E is a **memory-bound correctness** guarantee (~0 wall win); production documents.Store is I/O-bounded
so it rarely binds, but the bound should exist. Sequence E after A (it doesn't help build wall).


---

## 8. (A) correctness — single mutator preserved (no two-writer proof needed)

Because v2 keeps `installMerge` on the single worker (§3), the four-MANIFEST-writer / lock-order /
`manifestMu` problems of the first draft **do not arise** — there is still exactly one writer of
`s.man`/`s.segs`/MANIFEST (the worker), and CreateTable/DeleteTable/spill/installMerge all run on it,
serialized as today. The only new concurrency is the **read-only** merge compute on its own goroutine:

- `mergeSegments` runs off-worker but **mutates nothing shared** — it reads its input segments
  (held via reader refcounts, like Search) and writes a NEW output file at a reserved `outId`. So it
  cannot race the worker's `s.man`/`s.segs`/MANIFEST mutations (it touches none of them).
- **Input lifetime:** the merge goroutine acquires reader refcounts on its input segments for the
  duration of `mergeSegments` (the existing acquire/release path). Only a merge retires a segment,
  and merges are strictly serial in the one goroutine, so no input can be retired mid-compute. A
  concurrent spill only APPENDS new segments — it never retires an input. ✓
- **`outId` reservation** stays under `s.mu` (as today); a spill bumping `NextSegId` concurrently is
  already `s.mu`-guarded. A crash between reserving `outId`+writing the file and the worker's install
  leaves an orphan output file at a reserved id — the EXISTING single-writer crash case, GC'd on Open
  (merge.go documents it); unchanged by A.
- **Readers** (Search/forwardKeywords) are unaffected — the segment set they snapshot only changes
  at `installMerge` on the worker, exactly as today.

This must still be proven by a `-race` stress test (concurrent applies + the off-worker merge compute
+ searches) — §9 — but the proof obligation is small: confirm the merge compute never touches
`s.man`/`s.segs` and its inputs stay ref-held.

## 7a. (F) Beat pebble: move spill ENCODE off the worker (optional, the real lever past parity)

Review's honest floor: after A, the worker's serial **spill ~28s** (encodeDocs sort 11 + writeTermDict
re-read 9 + snappy 6) is untouched and is ~46% of pebble's whole build. A+B+C only reach pebble
PARITY. To actually beat pebble, apply the SAME safe pattern to spill: build the sealed segment BYTES
(sort terms, encode postings, compress blocks, build the term-dict region) **on a helper goroutine**,
then do the cheap install (append `s.man`/`s.segs`, publish, write MANIFEST) on the worker. The head
must be SNAPSHOT/detached at spill time (copy the maps out, or double-buffer the head) so the worker
can keep applying into a fresh head while the old head's bytes encode off-worker. This is more
involved than A (the head hand-off needs care) and is scoped as a SEPARATE, measured follow-up — only
pursue if parity isn't enough. Without F, the honest target is "match pebble," not "beat it."


## 9. Test plan

Per change, TDD; the concurrency ones gate on `-race`.

- **B:** unit — three sealed segments with disjoint ascending docid ranges; a new high docid probes
  0 segments, an in-range docid probes only its segment (`forwardProbeHook` counter). Plus a
  **2-table** case (the range is table-agnostic within a segment — pin it so nobody "optimizes" it
  into per-table ranges) and a **covering output with `[I]` records but NO `[F]` records** (empty
  range still always-skips). Differential test stays green.
- **C:** unit per sub-item proving behavior identical (applyBatch 1-op fast path == multi-op on the
  same input; mergeCursor per-cursor scratch round-trips a k-way merge unchanged). `-race`. Each
  sub-item measured **after A+B**; keep only worker-serial wins.
- **A:** (1) functional — build with AutoMerge on, merges still bound K, hits identical
  (differential). (2) **`-race` stress** — applies+spills on the worker while the merge goroutine
  runs the off-worker COMPUTE and M goroutines Search; assert no race, hits == a serial build,
  MANIFEST round-trips on reopen. (3) a focused assertion/invariant that `mergeSegments` (off-worker)
  touches **no** `s.man`/`s.segs` and holds reader refs on its inputs — the small proof obligation
  §8 leaves. Crash-consistency is the EXISTING single-writer case (orphan output GC'd on Open).
- **E:** unit — a producer firing more postings than the budget blocks until applies drain (peak
  in-flight postings ≤ budget); a single batch > budget does NOT self-deadlock; `-race`.
- **Whole:** existing differential / crash-recovery / merge-robustness suites green; `-race` clean;
  go-cov ≥ 90%; whole-workspace (both modules).

## 10. Acceptance criteria (honest)

- `idxbench -impl=store -batch=1` full lx build: **measured and reported after each change** (no
  asserted numbers). Realistic landing after A+B+C ≈ **match pebble (~55–65s)**, NOT a guaranteed win.
  A clear win over pebble's 61s requires **F** (spill-encode off-worker, §7a). State which target is
  being pursued.
- Build CPU profile: the merge COMPUTE no longer on the apply-worker critical path; forward-read
  decompression (B) down; GC cycle count + peak heap down (C).
- `hits` identical (2,414,505), `-race` clean, disk unchanged (~240 MiB), search not regressed.
- Memory bounded under a fast producer (E): peak in-flight postings ≤ budget; batched 2.8 GB blowup
  gone.

## 11. Sequencing & risk

Order (each independently measured + committed; re-measure on real ext4 after each — no asserted wins):
1. **B** (low risk, clean) — already prototyped; re-validate vs this spec + add the 2-table /
   forward-absent tests; bump/confirm FormatVersion (a stale `[0,0]` default would mis-skip).
2. **A** (now LOW risk in the compute-off-worker form, §3) — the dominant lever; gate on the `-race`
   stress test. Measure: does it actually reach ~55–62s?
3. **C** (measure AFTER A+B; keep only worker-serial wins — likely just the 1-op fast path).
4. **E** (memory-correctness; postings budget; after A).
5. **F** (optional) — only if the user wants to beat pebble, not just match it.

A no longer touches the single-mutator invariant (the compute is read-only; the install stays on the
worker), so the first draft's two-writer hazards (four MANIFEST writers, lock-order, manifestMu) are
all gone — that was the key review outcome.


