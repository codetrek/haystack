# invertedstore — Task Breakdown (v1)

Decomposition of the [design](invertedstore-design.md) build order (§12) into concrete,
dependency-ordered tasks. Each task lists: **Dep** (blocking tasks), **Spec** (design §),
**Deliverable**, **Acceptance** (tests/checks that close it). "Owed re-measure" items from the
design's §3/§11 caveats are called out where they attach. This is a working plan, not an
as-built doc.

Legend for size: S ≈ ½ day, M ≈ 1–2 days, L ≈ 3–5 days.

---

## T1 — Segment format: writer/reader  · Dep: none · Spec §5 · Size L

The immutable on-disk segment: data blocks (inline-small / external-large values), term-dict
region, block index, 25-byte footer.

- **Deliverable**: `segWriter` (addEntry → packed blocks; external-value chunking; term-dict
  region built by re-reading own blocks; `finish` writes blockIndex + footer with **both**
  `dataCodecId` and `dictCodecId`) and `segment` reader (`openSegment`, block read+decompress,
  external-value read, term-dict chunk read, `scanPrefix`). Includes a **minimal block-codec seam
  (snappy + the persisted `dataCodecId`)** so T2 can spill before T7 adds zstd / per-level / dict-codec.
- **Encodings** (must match the spec byte-for-byte): key = `keyType(1) tableId(4 BE) (keyword |
  docid 8 BE)`; `invertedValue = uvarint(addsByteLen) deltaVarint(adds) deltaVarint(dels)`;
  `forwardValue = uvarint(nKw) deltaVarint(term-ids)` (tombstone = nKw 0); `dictChunk = uvarint(firstOrd)
  uvarint(rawLen) uvarint(compLen) dictCodec(strings)`.
- **Acceptance**: round-trip unit tests for every record kind (inline/external, `[I]`/`[F]`,
  forward-tombstone); golden test that a hand-built segment's bytes match a fixed fixture;
  decode of `invertedindex`-produced delta-varint values is bit-identical; footer parses both
  codec ids; fuzz: random records → write → read → equal.

## T2 — Head buffer + spill + MANIFEST + table catalog  · Dep: T1 · Spec §5,§6 · Size L

The in-memory write side and durable metadata.

- **Deliverable**: head buffer (`map[tableId] → {inv adds, del tombstones, forward}`), keeping the
  **latest action per `(keyword,docid)`** and **in-memory docid dedup**; logical byte estimate
  (`len(kw)+16` per new kw, `+4`/posting, `8+len(kw)*4`/forward) driving spill at `CapBytes`; spill
  writes one L0 segment ([sorted inverted] ++ [forward by docid], single term-dict sort) + fsync +
  MANIFEST swap; versioned MANIFEST encode/decode (segment set with per-segment `dataCodec`/`dictCodec`
  + table catalog; **no recovery watermark** — recovery is indexer-driven, §9/T10); `Open`/`Close`,
  `CreateTable`/`DeleteTable`. **DeleteTable** drops the catalog entry and bumps a per-table epoch;
  Search/GetDocs return empty for an absent/old tableId without rewriting any segment, and the dead
  table's `[I]`/`[F]` keys are reclaimed when a covering merge (T6) drops keys for tableIds not in the
  catalog — **`DeleteTable` schedules that covering merge** so the bytes go even if the table sits at the
  bottom level (segments are immutable — no synchronous DeletePrefix).
- **Owed re-measure**: in-memory dedup peak-memory effect (§11).
- **Acceptance**: spill→reopen yields the same segment set; CapBytes actually bounds head bytes
  (assert peak); CreateTable persists across reopen; **after DeleteTable, Search/GetDocs on that tableId
  return empty across head + segments**, and a covering merge reclaims its bytes; MANIFEST is the only
  fsync'd metadata; a torn `MANIFEST.tmp` is ignored on Open.

## T3 — Forward map (term-id) + resolution  · Dep: T1,T2 · Spec §8 · Size L

Segment-local ordinals and the ordinal→string path.

- **Deliverable**: assign ordinals at spill (free from the term-dict sort); encode `forwardValue` as
  `uvarint(nKw)` + delta-varint ordinals (nKw=0 = tombstone); resolution = ord→chunk binary search on
  `firstOrd` + decompress, behind a **Store-level chunk LRU** keyed by `(segmentId, chunkIdx)` (mutex,
  byte budget `ChunkCacheBytes`, purge entries of merged-away segments). Latest-wins forward point
  lookup that reads the **head's pending forward first, then segments newest→oldest** (so a doc edited
  twice within one spill window diffs against its current keywords, not a stale sealed copy), honoring
  the nKw=0 tombstone.
- **Acceptance**: forward round-trip (decode→resolve→strings) equals the input keyword set for a
  sampled corpus (the spike's `verifyForward`, port it); a single-keyword doc whose ordinal is 0 reads
  back present (not mistaken for a tombstone); LRU never exceeds budget; resolve of a deleted doc
  returns empty.

## T4 — Search / GetDocs  · Dep: T1,T2 · Spec §4,§6 · Size M

- **Deliverable**: **Search** = prefix scan by `(tableId, keyword)` over head + segment snapshot,
  newest-wins union across segments (first add/tombstone per `(kw,docid)` decides), `filterKeyword` /
  `limit`, tombstone resolution; preserve the `WildDocIds` field for compatibility (the store does not
  populate it — caller-populated per `SearchResult`). **GetDocs** = **exact-key** match (no
  lowercasing/limit/filter), kept separate from Search so a fixed-width-tableId prefix can't leak (e.g.
  `GetDocs("a")` must not match keyword `"a"+suffix`).
- **Acceptance**: differential test — identical hit set vs `invertedindex` (the spike's 2,414,505
  parity); a tombstoned doc is absent; **add→del→add resolved at READ across un-merged L0 segments
  (no merge): a doc tombstoned in an older segment and re-added in a newer one is PRESENT; the symmetric
  add-then-tombstone-in-newer is absent**; `GetDocs("a")` does not return `"a"+suffix` docs (the
  `TestGetDocs_NoPipePrefixLeak` guard); limit/filter honored.

---

## T5 — Update / Batch (apply path)  · Dep: T3,T4 · Spec §6,§8 · Size M

- **Deliverable**: async `Update` via `q.AddFunc` = single-item Batch; `Batch.Commit` = one apply
  task, ops applied in order (repeated docid → last wins). Diff old (forward read — **head pending then
  segments**, T3) vs new → term-id **full re-post** (every current keyword) + per-keyword tombstones for
  removed; `forward[docid]=new`. **Delete** (empty keywords) = write forward-tombstone (nKw=0) + tombstone
  docid in all old keywords.
- **Acceptance**: after a batch of edits, Search reflects adds and removals; **delete→re-read returns
  empty** (no resurrection from an older segment); re-`Update` of a doc supersedes its prior keywords;
  a docid repeated in one Batch resolves to the last op.

## T6 — Background tiered merger  · Dep: T1–T5, T7 · Spec §6,§8 · Size L

The heart of the long-term correctness + bounded-K story.

- **Deliverable**: tiered policy (level with ≥ `Fanout` segments → one next-level segment); streaming
  k-way merge with **per-`(keyword,docid)` newest-wins reconciliation** (merge inputs oldest→newest,
  latest add/tombstone wins — **fixes add→del→add**); **cannot drop keyword keys** (the remap append
  index is the source ordinal) so fully-tombstoned keys persist as del-only records; **ord→ord remap +
  term-dict rebuild** (T3's machinery — directly consumed here, the transitive dep is load-bearing);
  a **covering-merge trigger** = full compaction of the bottom level + everything above, fired when the
  bottom level's **dead fraction** (tombstoned+superseded ÷ live) crosses a threshold (default ~25%) OR
  scheduled by `DeleteTable` — NOT incidental tiered fanout; it reclaims dangling tombstones,
  fully-tombstoned keys, cross-window duplicate adds, and dead-tableId keys, bounding the growth §8
  relies on. Crash-safe MANIFEST swap. Pre-T8 (no concurrent readers) inputs are deleted immediately on
  swap; T8 adds refcount-deferred deletion.
- **Acceptance**: **add→del→add then force a merge → resolves PRESENT** (the case the spike's
  unique-word workload never hit); **a forward-tombstone (nKw=0) survives a merge spanning the delete +
  an older non-empty forward record → the doc still reads empty**; forward round-trip still 401/401 after
  merge; the covering merge reclaims add/tombstone pairs and a long edit run's fully-tombstoned keys +
  dangling tombstones do NOT grow without bound; bounded merge memory (assert remap arrays ≈ Σ source
  term counts, not a string map); long-cap=4 run holds live-K to single digits with search bounded.

## T7 — Compression seam  · Dep: T1 · Spec §7 · Size S

- **Deliverable**: snappy + bounded-zstd (`concurrency=1`, 128 KiB window) data codecs behind the
  block seam; per-level (L0 snappy / merged zstd); dict region uses `DictCodec` (default zstd, 4 KiB
  chunks); all codec ids persisted in the footer and honored on Open for mixed segments.
- **Acceptance**: a zstd-merged + snappy-L0 + zstd-dict index opens and reads correctly (codecs read
  from each footer, not assumed); zstd encoder memory bounded (assert peak). *(The post-merge `disk ≈
  241 MiB` figure is a whole-pipeline measurement — it needs spill/term-dict/tiered-zstd-merge, so it is
  asserted in T11, not here.)*

## T8 — Concurrency  · Dep: T2,T4,T6 · Spec §6 (Concurrency) · Size M

- **Deliverable**: `atomic.Pointer[snapshot]` for the live segment set; head `RWMutex` (worker Locks
  to mutate/spill, readers RLock); MANIFEST-swap-then-deferred-delete with a **reader refcount/epoch**
  (unlink a merged-away file only at refcount 0); chunk-LRU mutex + purge-on-swap; table ops via
  `RunTask`.
- **Acceptance**: race detector clean under concurrent Search + Update + merge; a reader mid-scan on a
  segment being merged away completes (no use-after-unlink); no Search blocks on a writer.
- **Owed re-measure**: confirm the foreground (~22 s) is what the user waits with merge truly
  backgrounded (§3 caveat); chunk-LRU contention under concurrency (§13).

## T9 — Indexer interface + server wiring + migration  · Dep: T4,T5 · Spec §4,§10 · Size M

- **Deliverable**: `Indexer` interface both stores satisfy (+ shared/aliased `SearchResult`); trivial
  `invertedindex` adapter; `documents.Store` drops its doc-words machinery and calls `Update` without
  `oldKeywords`; **StorageVersion** bump + add old version to cleanup + reindex-on-upgrade.
- **Acceptance**: server builds and serves search on invertedstore behind the interface; upgrade path
  reindexes from source and removes the stale pebble + doc-words data; `documents` has no doc-words.

## T10 — Crash recovery  · Dep: T2,T5,T9 · Spec §9 · Size M

- **Deliverable**: **indexer-driven** recovery (the store keeps NO watermark; it only guarantees
  crash-consistency). On Open the indexer, from its own durable change cursor, re-`Update`s every doc
  whose source mtime/version is newer than that cursor (**incl. low ids**) and **reconciles deletions**
  (a docid in the store's forward map but absent from source → delete) via the store's `forward`-docid
  enumeration hook. Safe because `Update` is idempotent in result. Orphan-output cleanup on Open.
- **Acceptance**: kill -9 mid-build → reopen → reindex → identical hit set; an **edit to a low-id doc**
  lost in the volatile head is re-applied (no stale postings); a **delete** lost at crash is re-applied
  (no resurrection); re-`Update`-ing already-sealed docs leaves the hit set unchanged (idempotent); a
  crash mid-merge leaves inputs live + orphan output GC'd.

## T11 — Differential + correctness + perf regression tests  · Dep: T1–T10 · Spec §11 · Size M

- **Deliverable**: differential vs `invertedindex` (identical hits); targeted cases for add→del→add,
  delete, crash recovery, tableId multi-tenancy isolation; memory-capped (`GOMEMLIMIT`) build benchmark
  and the code-edit update benchmark as CI regression guards.
- **Owed re-measures** to fold in here (design caveats): **tableId-in-key** disk overhead, **int64**
  full-range, in-memory-dedup memory, backgrounded-merge foreground time.

---

## Dependency graph

Edges (`A → B` = B depends on A):

```
T1 → T2, T3, T4, T7
T2 → T3, T4
T3 → T5      T4 → T5
T3,T4,T5 → T6    T7 → T6     (T6 directly consumes T3's term-dict/remap; zstd merge needs T7's codec seam)
T6 → T8      T2,T4 → T8
T4,T5 → T9   T2,T5,T9 → T10
T1..T10 → T11
```

Critical path: **T1 → T2 → T3 → T5 → T6 → T8 → T11**. T7 parallels early; T9/T10 (interface +
recovery) parallel T6/T8 once T5 lands. T1–T7 reproduce the spike-validated behavior in production
shape; T8–T10 are the build-then-measure pieces the spike never exercised (concurrency, recovery,
migration); T11 backs it with the correctness cases the spike's narrow workload missed.

## Owed re-measures (rolled up from design §3/§11 caveats)

1. Disk with the **per-key tableId** (spike has none) — T1/T11.
2. **int64** full-range (spike is int32, byte-identical at this corpus) — T1/T11.
3. **In-memory head dedup** peak-memory effect (spike appends unconditionally) — T2/T11.
4. **Backgrounded** merge — confirm foreground wait ≈ 22 s (spike merges synchronously) — T8.
5. **WAL** path numbers (removed from current spike; §9 from an earlier iteration) — only if WAL ships.

