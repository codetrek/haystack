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

## 2. The changes (review-calibrated v4; all ship, F last)

**Goal: the BEST achievable cold-build time** (drain everything reducible off the single worker;
shrink the irreducible apply). Pebble's 61s is a reference only. Review found two FREE single-threaded
wins the v1 plan jumped past (F0, head-fix), and that F is far more dangerous than first specced.

| # | change | WALL effect (review-calibrated; RE-MEASURED per change) | risk |
|---|---|---|---|
| **F0** | build term dict INLINE — kill the `writeTermDict` re-read (§5a) | **−9s spill**, single-threaded, zero concurrency | **none** |
| **B** | per-segment `[minDocid,maxDocid]` forward-read skip | ~6s; bounds forward-read as K grows | low |
| **C-head** | lazy `dels` map + skip per-add `delete` in addPosting (§5) | **−5–8s** off the "19s floor" (it's ~12–14s real), single-threaded | low |
| **A** | merge COMPUTE off-worker, install on-worker (§3) | ~30s off the worker | LOW (single mutator preserved; add input refcounts) |
| **C-rest** | 1-op applyBatch fast path; per-cursor decompress scratch | ~0–3s wall + lower heap | medium |
| **E** | write-path backpressure by in-flight **postings** | ~0 wall; bounds memory | medium |
| **D** | keep zstd for merged | — | none |
| **G** | Open sweeps orphan `seg-*.dat` (make the "GC'd on Open" claim true) | — (correctness/disk hygiene; matters under F) | low |
| **F** | move residual spill encode (sort+snappy) off-worker (§7a) — **last, hardened** | drains the residual ~17s; partly offset by install-fsync + spilling-scan | **HIGH** |

**Realistic landing** after F0+head-fix+B+A+C+E+F ≈ **25–32s** (review-calibrated), well under pebble —
but NOT ~20s: the per-spill MANIFEST fsync stays on the worker (~1s over ~43 spills), F's `spilling`
read path adds cost, and at ~20s the **producer/gob-feed (`tLoad`) may become the co-floor** — acceptance
must confirm the producer is < the post-F worker. Order: free wins (F0, head-fix, B) → A → C/E → G → F.

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

## 4a. (F0) Build the term dict INLINE — kill the `writeTermDict` re-read

The single highest win/risk item, missed by the first draft. `spill`/`mergeSegments` write the `[I]`
data blocks, then `writeTermDict` (segment.go ~185–228) **re-reads and re-decompresses every one of
those blocks** just to extract the keyword strings in ordinal order — strings the writer **already
held** at `addEntry` time (the keyword is `key[5:]`). The re-read exists only to keep memory bounded
to one block; but on the spill path the terms are ALREADY sorted in memory before the addEntry loop,
and the merge emits them in order too. **Change:** accumulate the term-dict region INLINE as each `[I]`
key is added (append the keyword to the current dict chunk in the writer), eliminating the entire
re-read+re-decompress pass. **~9s off spill, single-threaded, zero concurrency risk** — and it shrinks
F's residual target from ~28 to ~17s. Correctness: the dict bytes are byte-identical (same keywords,
same ordinal order); guard with the existing differential + term-id round-trip tests. This is F0
because it must land BEFORE F (F moves a SMALLER encode off-worker once the re-read is gone).

## 5. (C) Cut per-op allocation churn — a MEMORY/GC play, ~0–3s wall (plus the head-fix, real wall)

> **Review-calibrated:** GC is parallel-free (32 cores, build uses ~1.9); GOGC=400 was *worse*. So
> reducing allocation cuts the **heap/GC-cycle count/peak memory**, but moves WALL time only to the
> extent `mallocgc` is on the worker's serial path. Two EXCEPTIONS that DO move wall time (the head-fix
> below + the 1-op fast path); the rest are memory wins. Measure each **AFTER A+B**; keep only real wins.

0. **head-fix (real wall, ~5–8s) — lazy `dels` map + skip the per-add `delete`.** `addPosting`
   (head.go:38–50) allocates a `*postingDelta` with TWO `map[int64]struct{}` per first-seen keyword,
   but on a cold build `dels` is ALWAYS empty (no deletes) → millions of wasted empty-map allocations;
   and every add runs `delete(pd.dels, docid)` (latest-wins) hashing into that empty map pointlessly.
   Fix: allocate `dels` lazily (nil until the first `tombstonePosting`); on the add path, skip the
   `delete` when `dels == nil`. Review estimates ~30–50% of addPosting's 19s is this fat → the "floor"
   is ~12–14s, not 19. Single-threaded, behavior-identical (a nil dels == empty dels).
1. **Skip the per-op `inBatch`/`seen` maps for a 1-op apply** (hot `Update` path is always 1-op): a
   1-op batch can't repeat a docid, so `seen` is always false and `old` always comes from
   `forwardKeywords` — skip both maps. Guard `len(ops)==1`. (Review-verified safe.)
2. **Reuse decompress buffers — `mergeCursor`-scratch ONLY, never a global** (`c.key`/`c.val` alias
   `c.blk`; K cursors' blocks coexist). Measured **1.95 GB** alloc cum. **MUST NOT alias/in-place-sort
   head storage** (§7a M2). **UNSAFE NAIVELY (review): `segWriter.addEntry` retains the cursor's key
   bytes via `blkFirst → blockEntry.firstKey → finish` UNCOPIED, and `advance()` crossing a block
   boundary would overwrite a reused block → corrupt persisted block-index first-key. The differential
   hits-test MISSES this (a too-early `sort.Search` start still finds the key). REQUIRED FIX: copy the
   first-key at capture — `w.blkFirst = append([]byte(nil), key...)` (segment.go:119; one copy per
   block, trivial, also independently hardens the writer) — THEN a per-cursor `c.blk` reuse is safe.**
   Add a dedicated **block-index-integrity test** (after a merge+reopen, every `idx[i].firstKey` ==
   block i's true first record key) — differential + `-race` do NOT cover this class.
3. **Reuse spill/merge ENCODE scratch in `segWriter`** (value/encode scratch ONLY — NOT the key buffer): `encodeDocs` /
   `encodeForward` / `appendUvarint` / `flushDictChunk` allocate a fresh `[]byte` per record — measured
   `encodeDocs` 2.3 GB + `appendUvarint` 1.2 GB + `flushDictChunk` 2.3 GB cum + `encodeForward` 1.1 GB.
   `addEntry` copies into `blkRaw` immediately, so a per-writer scratch is safe (the value is not
   retained after the copy). Encode output scratch is NOT head storage, so it does not violate M2.
4. **(BIGGEST — v6, measured) `mergeSegments` per-keyword `adds`/`dels` map reuse.** merge.go:275–276
   allocates TWO `map[int64]struct{}` **per keyword** across the whole merge → **2.1 GB flat / the merge
   is 44% of alloc + 31% of build CPU**, and the resulting GC (`scanobject` 25%, `findObject` 10%) is
   the top CPU cost. Fix: hoist the two maps out of the per-key loop and `clear()`+reuse them each key
   (the maps are fully consumed — encoded into the output record — before the next key, so reuse is
   safe). **`clear()` BOTH maps UNCONDITIONALLY at the top of every inverted-key iteration — including
   the dropped-key (`keep==false`) path — so a prior key's content never leaks.** Cuts the largest
   single alloc source. Since the merge runs OFF the worker (A), this is an
   **RSS/GC win, not a build-wall win** (the goal here: shrink the ~1 GB build peak RSS, which is the
   one axis where store loses to pebble's 610 MiB).

> **MEASURED (lx, 94.5k docs, post-F): build 45s (BEATS pebble 64s), disk 238 MiB (2.7× < pebble), but
> build peak RSS ~1 GB (pebble 610 MiB) from 30 GB alloc churn → ~25% CPU in GC.** Items 2–4 target the
> churn (merge 44% + encode/decompress scratch) to lower peak RSS. The head `addPosting`/`posting` maps
> (5.1+1.5 GB) are live until spill (can't trivially pool) → out of scope. **Keep only measured wins.**

## 5b. (H) Compact head postings — per-keyword `map[int64]` → ordered ops slice

**Measured (lx, post-F, peak `inuse_space` via `idxbench -peakheap`).** The build's peak LIVE heap
(~467 MB → ~1 GB RSS at GOGC=100) is **HEAD-DOMINATED**: `addPosting` 188 MB (66%) + `Batch.Update`
43 MB (in-flight `op.keywords`) + `posting` 34 MB ≈ **290 MB is the head buffer**. The hog is
`postingDelta.adds map[int64]struct{}` — **ONE Go map per keyword**, ~48–96 B header+bucket overhead
each, paid even for a keyword in a single doc (the long tail). THIS is why store needs ~1 GB build RSS
while pebble (compact skiplist memtable) needs 610 — a representation problem, not a tuning knob
(GOMEMLIMIT=600MiB caps RSS to 607 at +2s build, but only MASKS it). C.2–4 (churn) did NOT move peak
RSS because peak RSS = the live working set, and the head IS the live working set.

**The map's two jobs — a slice loses nothing on either:**
- **dedup-on-insert — REDUNDANT.** The on-disk encode `appendDeltaDocs` (keys.go) already sort+dedups
  each list (`if d == prev { continue }` after `sort`). The map pays ~48 B/keyword to avoid dups the
  spill sort removes anyway.
- **cross add-vs-del latest-wins** (a re-add cancels a pending del, so a docid is in exactly one of
  adds/dels) — the ONLY non-redundant job; moved to a cheap resolve-at-consume.

**Change:** `postingDelta { ops []int64 }`, each op `= docid<<1 | isAdd`, **APPENDED in action order**.
`addPosting`/`tombstonePosting` become an O(1) append — no lookup, no per-keyword map, no dedup.
`h.bytes += 8` per op (now ≈ ACTUAL memory, so CapBytes becomes honest). At spill AND in `Search`/
`GetDocs`, `resolveOps(ops) → (adds, dels)`: **stable-sort by `docid`** (preserves insertion order
within a docid), then the LAST op per docid decides add-vs-del — exactly the map's latest-wins. The
sort is the one `appendDeltaDocs` already performs, so **no new asymptotic cost** (O(N log N) either way).

**`resolveOps` MUST be non-mutating — copy-before-sort.** It works on a scratch copy of `ops`, never
sorting the head's slice in place: (1) the F detached head is READ-ONLY during off-worker encode (§7a
M2); (2) `Search` reads the head under `s.mu.RLock()` concurrently with the worker. Both copy `ops`
(under the RLock for Search; the encode owns the detached head) and resolve on the copy. The resolve
allocations are read-time churn, not live.

**Memory:** ~8 B/op + one slice header per keyword, vs the map's ~48–96 B/entry + the `*postingDelta`'s
two map headers. ~5–6× smaller for the common small-keyword case; a 1-doc long-tail keyword drops from
a whole map to an 8-byte slice. **Expected: head live ~290 → ~60 MB, peak live ~467 → ~200, build RSS
→ ~400 MB (BELOW pebble's 610, no GOMEMLIMIT).** Measure with `-peakheap` and report.

**Scope:** `head.go` (`postingDelta`, `addPosting`/`tombstonePosting`, the `posting()` helper, the
`h.bytes` accounting, spill's per-keyword encode via `resolveOps`) + the readers `search.go`
`Search`/`GetDocs` (`resolveOps` replacing `setToSlice(pd.adds/dels)`). **UNCHANGED:** the forward map
`h.fwd` (separate; `forwardKeywords` never touches `inv`), `liveByTable`, `segMeta.Postings`, and the
on-disk segment format (byte-identical — same encoder, same sorted-dedup'd output).

**Correctness — `resolveOps` must EXACTLY match the map** (a docid ∈ adds iff its LAST op is an add).
Gated by: the differential **hits-identical (2,414,505)** + crash-recovery + merge-robustness suites;
a focused `resolveOps` unit test (add/del/add/del sequences; the cold-build append-only case; duplicate
appends; interleaved docids); and `-race` (Search resolving a copied `ops` under the RLock). Risk is
contained to one pure function + its two call sites.

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
- **Input lifetime (REVIEW CORRECTION — a required ADDITION, not existing).** The spec first claimed
  the merge uses "the existing acquire/release [refcount] path" — it does NOT: `segsByIds` (merge.go)
  returns RAW `*segment` handles with no `refs.Add(1)`, safe today only because the whole merge runs
  in ONE `q.RunFunc` worker task. Off-worker, A MUST add real refcounting: acquire the input segments
  via `acquireSnapshotLocked`-style incref under `s.mu`, hold across the off-worker `mergeSegments`,
  `releaseSnapshot` after the install. (No concurrent retire can happen — spills only append, merges
  are serial — but the refs make it robust against a future second merger / a `CloseAndWait`
  `retireKeepFile` racing the compute.)
- **`maybeMerge` loop interleaving (impl subtlety).** `maybeMerge` loops `mergeOneLevel` until no
  level qualifies; each iteration selects inputs from the CURRENT `s.man.Segments` (only changed at
  install, on the worker). So the loop CONTROL stays on the worker (decide-what-to-merge + install),
  and each iteration's `mergeSegments` COMPUTE hops to the merge goroutine and back. Not a mechanical
  extraction; the breakdown details it.
- **`outId` reservation** stays under `s.mu`. A crash between reserving `outId`+writing the file and
  the install leaves an orphan output file at a reserved id — handled by item **G** (Open sweeps
  orphans; the existing "GC'd on Open" comment is currently false — see §7b).
- **Readers** (Search/forwardKeywords) are unaffected — the segment set they snapshot only changes
  at `installMerge` on the worker, exactly as today.

This must still be proven by a `-race` stress test (concurrent applies + the off-worker merge compute
+ searches) — §9 — but the proof obligation is small: confirm the merge compute never touches
`s.man`/`s.segs` and its inputs stay ref-held.

## 7a. (F) Move RESIDUAL spill encode off the worker — REQUIRED, last (v5: simplified)

After F0 (inline dict, −9s) and the head-fix, the spill's residual encode is **sort ~11 + snappy ~6 ≈
17s** on the worker. F moves that off-worker via a live **head hand-off**: detach the head, encode it
off-worker, install the resulting segment on the worker.

> **v5 — the install-ordering defect (found by the 7B implementation; missed by spec v4 + R1–R4).**
> The read order is `live head → spilling (newest→oldest) → segments`, and segments are ordered by
> their **seal-sequence id** (higher id = newer = wins). v4 reserved a spill's id at **detach** but
> installed it **late**, and async encodes install **out of order**. Two inversions result:
> (1) **spill-vs-spill** — a newer spill (#11) finishes encoding and installs as a segment before an
> older parked spill (#10); the still-parked #10 ranks above seg#11 (spilling is read above all
> segments) and its older data shadows the newer segment → a dropped keyword resurrects. (2)
> **merge-vs-spill** — a merge reserves a higher id than a parked spill but installs older content,
> outranking it. v4's pool/`maxInflightSpills`/ordered-install all tried to patch this and either
> deadlocked or broke the memory bound. **v5 removes the root cause with two changes.**

**Two changes that eliminate the inversions (no ordered-install, no merge deferral, no multi-slot):**

1. **At most ONE in-flight spill** (the detach→install window holds ≤ 1 detached head). With only one
   spill outstanding there is never a same-table spill to install out of order → **(1) is impossible**.

2. **Assign the seg id at INSTALL, not at detach.** Installs run on the single worker, serialized, so
   the id reflects **install order**. The one parked spill — the newest head — installs *after* any
   concurrent merge or earlier work, so it gets the **highest id = correctly newest** → **(2) is
   impossible, with no merge deferral.** The off-worker encode can't know the id, so it writes a
   **temp file** (`seg-tmp-<n>.dat`, `n` from a private counter); install does `id = NextSegId++` then
   `os.Rename(temp, seg-<id>.dat)` (atomic, same dir; an already-open fd survives rename).

**Detach (worker, one `s.mu.Lock()`):** swap `s.head[T]` → fresh, append the old head to `s.spilling`
(now ≤ 1 entry), set `spillInFlight=true`. **No id reserved, no NextSegId bump.** Dispatch the encode
of the old head to a background goroutine writing the temp file. The over-cap check, the `spillInFlight`
read, and the swap/append/set MUST be ONE `s.mu` section, so two applies (or an apply + an install's
re-dispatch) can never both detach → one-in-flight stays race-free. `spillEntry` carries the **temp
counter `n`** (the v4 `outId` field is gone — install assigns the id), not a reserved seg id.

**Reads consult `spilling` (7A, the B1 fix — DONE, committed `85d30aa`):** `forwardKeywords` is the
worker's OWN "read old keyword set" on every edit; after a doc detaches, its forward is in `spilling`,
so a re-post that diffed against an empty `old` would resurrect dropped keywords (silent corruption,
zero concurrency). All four read paths (forwardKeywords, Search, GetDocs, ForwardDocids) resolve
`live head → s.spilling (newest→oldest) → segments`. The single parked head is genuinely the newest
data (detached after every installed segment), so reading it above all segments is correct. Deltas are
COPIED under `s.mu.RLock()` (M1, no refcount); the spilling loop stays in the same RLock window (no
recursive re-lock). Encode is strictly READ-ONLY over the detached head (M2).

**Install (worker `RunFunc`, one `s.mu.Lock()`):** `id = NextSegId++`; rename temp → `seg-<id>.dat`;
append `segMeta`; `publishSnapshotLocked()`; remove the entry from `s.spilling` (**publish before
remove** — the lost direction is forbidden); `spillInFlight=false`; then re-check **ALL tables** for an
over-cap head and dispatch one's detach (over-cap is per-table `h.bytes ≥ CapBytes` but one-in-flight
is store-wide, so a table-B head that filled while a table-A spill was in flight must be found here —
NOT just the just-installed table, or a multi-table workload wedges). **This re-dispatch is
LOAD-BEARING for liveness** (review R1): without it, a head that went over-cap while the spill was in
flight is never detached once the producer re-blocks → permanent wedge; gate it with a
fast-producer/slow-encode test (single- AND multi-table). The dir-fsync in `writeManifestBytes` makes the rename + the new
MANIFEST durable together; a crash before the rename leaves a `seg-tmp-*` orphan (G sweeps it), after
the rename but before the MANIFEST an un-referenced `seg-<id>.dat` (G sweeps it). On install FAILURE,
the entry stays in `s.spilling` (data preserved) and `spillInFlight` stays set; a bounded retry then a
give-up that drops the entry, clears `spillInFlight`/`blockProducer`, and re-dispatches — treating the
lost head as crash-volatile.

**One-in-flight enforcement (no worker block, no deadlock):** in `applyBatch`, on over-cap: if
`spillInFlight`, do NOT detach a second spill — the head simply keeps the data (bounded by producer
backpressure below) until the in-flight spill installs and `installSpill` dispatches it. The worker
**never blocks** waiting for an install: the install is a worker `RunFunc`, so when the producer is
backpressured and the worker runs out of applies, it goes **idle** and naturally picks up the encode
goroutine's install `RunFunc` — it is never parked *waiting* for that install (the deadlock v4's
"worker blocks the detach" had). Single-mutator preserved (install runs on the worker).

**Producer backpressure — a worker-controlled GATE, NOT release-at-install (review R1):** E is
UNCHANGED (tokens still released at applyBatch return — E bounds the QUEUE). The only unbounded-growth
path F adds is `over-cap + spillInFlight` (the worker can't detach a 2nd spill, so the live head keeps
growing). F adds its own gate for exactly that: when the worker hits over-cap while a spill is in
flight, it sets `blockProducer` (under `s.mu`); `Update`/`Commit` evaluate **`for blockProducer {
cond.Wait() }`** (a LOOP, not an `if` — `Broadcast` wakes all parked producers but each install relieves
only one head's worth) **BEFORE `q.AddFunc`** (a blocked producer holds ZERO queue slots — the
property that keeps this deadlock-free). **The `Cond`'s `L` MUST be the lock the worker sets/clears
`blockProducer` under** (`sync.NewCond(&s.mu)` / its write-locker), with the producer checking the flag
while holding it — else a lost wakeup (set-after-check-before-Wait) reintroduces a deadlock.
`installSpill` clears `blockProducer`, detaches the now-over-cap head, and broadcasts. **Release-at-install was REJECTED:** it pins a head's tokens for its whole
residency, and heads that NEVER spill — a partial steady-state head, the `CloseAndWait` flush, a
`DeleteTable` head-drop, a spill-install give-up — would orphan their tokens and shrink the budget to a
deadlock. The gate has none of that: it is set only on over-cap-with-spill-in-flight, cleared at
install (or give-up). Bound: peak un-installed ≈ the one parked head (≤ CapBytes) + the live head
(≤ CapBytes + the applies already enqueued in the depth-100 mpsc queue when the gate engaged — a
harness race-ahead artifact, small for the I/O-bound production producer) ≈ **~2 heads + bounded queue
overshoot** (NOT a hard 2×CapBytes; state it honestly).

**CloseAndWait — the v4 deadlock site, now specified (review R1):** FIRST clear `blockProducer` +
broadcast (so any producer parked at the gate is released and can finish/observe the close — broadcast
BEFORE joining producers, or a producer stuck in `cond.Wait()` can never be quiesced), quiesce
producers, then drain the in-flight encode **OFF the worker** — wait on a `spillDone` channel /
`WaitGroup` from the **caller** goroutine (exactly like `stopMergeLoop`'s `<-mergeDone`), NEVER a
`Wait()` inside a worker `RunFunc` (that deadlocks against the install `RunFunc` — the v4 regression).
Order: let the in-flight spill **install first** (preserves seal order), THEN flush any remaining live
head synchronously, THEN `stopMergeLoop` + teardown.

**Crash:** a detached-but-not-installed head is volatile (lost on crash, like today's unspilled head;
indexer replay recovers it). The temp file is an orphan swept by **G** (extend G + `parseSegFileName`
to also remove `seg-tmp-*`). No cross-reopen double-visibility (`spilling` is in-memory).

**Dropped from v4 (no longer needed):** the bounded encode **pool** + `maxInflightSpills` multi-slot,
the **detach-time id reserve**/NextSegId bump-at-detach, the **ordered-install** state machine, the
**merge-vs-spill deferral**, and the deadlock-prone non-blocking-reserve dispatch. The `spilling`
docid-range skip (old 7C) is now at most a micro-opt over a single parked head — optional.

**Expected:** drains the residual ~17s off-worker when the encode overlaps filling the next head →
worker ≈ addPosting ~12–14s (post head-fix) + per-spill installs + the `spilling` read; producer
backpressure caps the overlap to ~1 head, so the win is bounded by encode-vs-fill rate (measure). Net
build ~25–32s (review-calibrated). Memory ≈ ~2 heads + bounded queue overshoot (NOT a hard 2×CapBytes).


## 7b. (G) Open sweeps orphan segment files

The `merge.go` "GC'd on next Open" comment is currently FALSE — Open opens only MANIFEST-listed
segments and never removes stray `seg-*.dat`. Benign today (orphans are never opened; ids effectively
not mis-reused), but F creates orphans on the common spill-crash path. **Change:** on Open, after
reading the MANIFEST, sweep the dir and `os.Remove` any `seg-*.dat` whose id is not in `man.Segments`.
Low-risk; makes the existing claim true; bounds disk under F. Gate: a crash-leaves-orphan → reopen →
orphan removed test.

---


## 9. Test plan

Per change, TDD; the concurrency ones gate on `-race`.

- **F0:** the inline term-dict bytes are byte-identical to the re-read version — assert via the
  existing term-id round-trip + differential; a unit test compares an inline-built dict region to the
  old re-read path on the same input.
- **head-fix (C.0):** `dels` stays nil on a cold build (no deletes); a delete then re-add still
  resolves correctly (the nil→alloc transition); behavior-identical to the eager-map version.
- **B:** unit — three sealed segments with disjoint ascending docid ranges; a new high docid probes
  0 segments, an in-range docid probes only its segment (`forwardProbeHook` counter). Plus a
  **2-table** case (range is table-agnostic within a segment) and an **`[I]`-present, `[F]`-absent**
  output (empty range still always-skips). Differential stays green.
- **C:** unit per sub-item (applyBatch 1-op fast path == multi-op; mergeCursor per-cursor scratch
  round-trips). `-race`. Measured **after A+B**; keep only worker-serial wins.
- **A:** (1) functional — merges still bound K, hits identical (differential). (2) **`-race` stress** —
  applies+spills on the worker while the merge goroutine runs the off-worker COMPUTE and Searches run;
  no race, hits == serial build, MANIFEST round-trips. (3) the input segments are ref-held across the
  off-worker compute (no teardown-during-read).
- **E:** producer firing more postings than the budget blocks until applies drain (peak in-flight ≤
  budget); a single batch > budget does NOT self-deadlock; `-race`.
- **G:** crash leaves an orphan `seg-*.dat` (write file, skip MANIFEST) → reopen → orphan removed,
  live segments intact.
- **F (the BLOCKER guards — gate hardest):**
  - **B1 silent-corruption (the critical one, ZERO concurrency):** with a small CapBytes, apply doc D,
    force-detach (block its encode via a hook), then **re-post D with a DROPPED keyword on the same
    worker**; assert the dropped keyword is tombstoned (forwardKeywords saw D's old set via `spilling`)
    — i.e. D is NOT searchable under the dropped keyword after install. This is the test that fails if
    forwardKeywords doesn't consult `spilling`. Run it WITHOUT any concurrent goroutine.
  - **B2/B3 atomicity:** `-race` stress (applies + blocked/unblocked encodes + Search) asserting a doc
    is never invisible across the detach→install window (search finds it the whole time — guaranteed by
    publish-before-remove in install).
  - **install-time id / newest-wins:** the one parked spill installs AFTER any concurrent merge and
    gets the highest id (newest); a doc whose dropped keyword was tombstoned in the spill is NOT
    resurrected by an older merge that installed during the parked window. (The old "spilling-skip"
    docid-range test is now an optional micro-opt over a single parked head — de-scoped, not required.)
  - **gate bound + liveness:** a fast producer with the encode artificially slowed parks at the
    `blockProducer` gate (peak `len(spilling)` ≤ 1 — never a 2nd in-flight spill); when the spill
    installs, the over-cap head is re-dispatched and the build CONVERGES (no wedge). Test BOTH single-
    AND multi-table (a table-B over-cap head while a table-A spill is in flight must be re-dispatched).
    `-race` clean (no lost wakeup / no worker-blocks-on-install cycle).
  - **CloseAndWait drain:** with the in-flight encode blocked then released, `CloseAndWait` RETURNS
    within a timeout (off-worker drain, no v4 self-deadlock) and the doc is durable on reopen.
  - **crash:** a crash with a detached head loses it (volatile) and indexer replay recovers it; reopen
    consistent (+ G removes the `seg-tmp-*` orphan).
- **Whole:** existing differential / crash-recovery / merge-robustness suites green; `-race` clean;
  go-cov ≥ 90%; whole-workspace (both modules).

## 10. Acceptance criteria — best achievable build

**Goal: the lowest build time the design allows** (everything reducible leaves the worker; the head
inserts shrink). Pebble's 61s is a reference line only.

- `idxbench -impl=store -batch=1` full lx build: **measured and reported after EACH change** (no
  asserted numbers; Principle 2 — measure on real ext4). Trajectory: 95s → F0 −9 → head-fix −5–8 →
  B −6 → A −30(off-worker) → C/E → F drain residual ~17 → worker ≈ addPosting ~12–14 + ~1s installs.
  Realistic build **~25–32s** (review-calibrated). Bar: "nothing reducible left on the worker."
- **Confirm the producer is not the new floor:** at a ~20s worker, the gob feed + `Update` keyword
  copy + `Commit` (`tLoad` + producer cost) must be < the worker time, else the build floor is the
  producer — measure and report.
- Build CPU profile after F: NEITHER merge NOR spill encode on the worker; the worker is dominated by
  `addPosting` + ms installs + the `spilling`/forward read. GC cycles + peak heap down.
- `hits` identical (2,414,505), `-race` clean, disk unchanged (~240 MiB), search not regressed.
- Memory bounded: peak in-flight postings ≤ E budget; **≤ 1 parked detached head (one-in-flight); peak
  un-installed ≈ ~2 heads + bounded queue overshoot** (NOT a hard `×CapBytes` — postings≠bytes + the
  depth-100 queue; §7a).

## 11. Sequencing & risk

All ship; each independently measured + committed; **re-measure on real ext4 after each** (no asserted
wins). Order — FREE single-threaded wins first, F last:
1. **F0** (inline dict, −9s, zero concurrency) — re-derive via breakdown+TDD.
2. **head-fix / C.0** (lazy dels, −5–8s, single-threaded).
3. **B** (forward range-skip; bump FormatVersion — a stale `[0,0]` default mis-skips).
4. **A** (merge compute off-worker; ADD input refcounts; gate `-race`). Measure.
5. **C.1–3, E** (after A+B; keep real wins; E memory-correctness).
6. **G** (Open orphan sweep — prerequisite-hygiene for F).
7. **F** (highest risk, last) — the head double-buffer + 3 atomic lock sections + `spilling` as a
   first-class tier in **forwardKeywords** (the B1 silent-corruption fix) + spilling-skip + bound.
   Gate hardest on the B1 zero-concurrency corruption test + the `-race` stress before committing.

A and F both keep the single-mutator invariant (compute/encode read-only on detached/immutable data;
installs on the worker). F's new shared state is the `spilling` head list (slice under `s.mu`, NOT a
refcount — readers copy-under-RLock). The first draft's two-writer/manifestMu hazards are gone.



