# invertedstore — Design

Status: **proposal / for review** (design phase). Replaces the pebble-backed
`core/invertedindex` for full-text keyword search with a self-managed, segment-based
store. The numbers in this document come from the `sortbench` spike measured on the real
linux corpus and real disk (see §11); per [AGENTS.md](../../AGENTS.md) Principle 3 the
spike's on-disk format is exactly what this design specifies. The one exception is the WAL
cost in §9, measured in an **earlier WAL-enabled spike iteration** (the `-wal` flag has since
been removed) and labeled as such there.

---

## 1. Motivation

`core/invertedindex` stores posting rows in pebble (an LSM). For the **build** workload
this is a poor fit, and the deployment target — the **user's own machine**, possibly with
very little RAM, indexing a corpus that can be **far larger than the linux kernel** —
makes two costs unacceptable:

- **Memory is unbounded by design.** pebble's flush buffer (`pendingWrites`) accumulates
  docids for every live keyword between flushes; it is sized by the flush window, not by a
  memory budget. Under `GOMEMLIMIT=256MiB` indexing the linux kernel, pebble blows up to
  **8m6s** (GC thrash) and still cannot hold the budget.
- **Build is slow.** A keyword in N docs is split into ~N/200 tick'd rows during ingest,
  each pushed through WAL fsync + memtable + L0 + compaction, then re-merged by the keyword
  merger — two stacked merge layers, every posting written many times.

Prior exploration ruled out the alternatives **by measurement** (see memory cards):
bbolt (read-fast but build/disk regressions) and bluge (`blugelabs/bluge`, a BM25 engine;
search **9.5–41× slower** for our boolean-membership workload, disk and RAM worse). The win
is a **purpose-built store** for exactly this workload.

### Priorities (user-stated, in order)

1. **Build speed** — must be much faster than pebble.
2. **Low, bounded memory** — must fit a small budget regardless of corpus size.
3. **Disk** — smaller on disk is better (ranks above search).
4. **Search speed** — may be **moderately** slower than pebble.

### Goal

A pebble-free, self-managed segment store — like `core/idtable` and `core/vectorstore` —
that builds fast at bounded memory, keeps the index small on disk, and keeps search fast
enough via a background merge. **De-pebble** the full-text index.

---

## 2. Design in one paragraph

**Write-once sorted runs + tiered background merge.** Writes accumulate in a
**byte-capped** in-memory head buffer (per table: the inverted deltas
`keyword → {added docids, tombstoned (keyword,docid)}` plus the forward map
`docid → keywords`). When the head hits its byte cap it is sorted and **spilled as one
immutable sorted segment** (an L0 segment); the head resets. Each posting is written
**exactly once** on the build path — **no WAL** (the index is re-derivable from source),
**no merge on the build path**, **no segment ever rewritten** (updates and deletes are
appends — a delete is a tombstone). A **background** tiered merger consolidates segments (a
level with ≥ `fanout` segments → one next-level segment), **reconciling each `(keyword,docid)`
newest-wins** and reclaiming superseded/tombstoned postings where co-located (the rest at a covering
merge), so the live segment count — and thus search latency — stays bounded as the index grows. The forward map is stored compactly as **segment-local term-ids** (§8). Search
snapshots the head + all live segments and unions postings (newest-wins). Memory is bounded
by the head cap, independent of corpus or vocabulary size.

This is the same **segment + head + manifest + merge** shape that `vectorstore` already
settled on, specialized for keyword postings.

---

## 3. Validated results

Measured on `sortbench` over the linux corpus (**94,559 docs / 41.4M postings / 10.6M
terms**), ext4 real disk, the **whole** index (inverted **+ forward**, the forward map
always on per AGENTS.md §3); hit parity **2,414,505** everywhere. Recommended configuration:
L0 segments `snappy`, background-merged segments `zstd`, forward stored as term-ids with a
`zstd` term-dict region (4 KiB chunks, 32 MiB chunk cache).

| metric | pebble | invertedstore (term-id) |
| --- | --- | --- |
| foreground build (writeSegments) | 96.5 s (whole build) | **~22 s** |
| background merge | — | ~25 s |
| disk | 363 MiB | **241 MiB** |
| search | 2228 µs/q | **~1180 µs/q** |
| peak memory | 1804 MiB | **~220 MiB** |

**invertedstore beats pebble on disk, search AND memory**, and its foreground build is ~4×
faster (pebble cannot background its merge). Disk breaks down (post-merge) as: blocks (keys +
inline small values) 137.8 + large forward values 24.5 + large inverted values 3.3 +
**term-dict region 74.7** + block index 0.4 MiB.

**Memory is the hard constraint and it holds.** Under `GOMEMLIMIT=256MiB` pebble blows up to
**8m6s** (GC thrash) and still can't fit; invertedstore's memory is bounded by the head cap (`CapBytes`)
(the knob), independent of corpus size — at the default cap it peaks ~220 MiB; a smaller cap
fits a smaller budget (more segments, slightly slower search).

**Forward scheme — term-id vs strings (measured).** Storing the forward map as keyword
**strings** costs **319 MiB** total; storing it as segment-local **term-ids** (§8) costs
**241 MiB — 25% smaller**, because a shared per-segment term dictionary captures the
cross-document redundancy (the same term in thousands of docs' word lists) that the block
codec's window cannot reach. Search is **identical** for the two (search never reads the
forward map). The cost term-id pays is on the incremental-update read path, quantified in §8.

**Long-term/incremental behavior proven.** With a tiny cap forcing 176 spills (a long-lived
growing index), no-merge degrades (176 segments) while the tiered merger holds the live
segment count to single digits — search stays bounded. The `CapBytes` and `Fanout` knobs trade
memory ↔ merge-work ↔ search.

> **Format caveats on these numbers (Principle 3 honesty).** The spike keys carry **no tableId**; the
> production format adds a fixed 4-byte tableId per key (§5). Docids: the spike's forward *key* is
> already 8 bytes, but its posting/ordinal **deltas are computed in int32 space** — byte-identical to the
> production int64 deltas for all ids < 2³¹ (true at this corpus), with a full-int64-range re-measure
> owed. The per-key tableId adds a small, not-yet-measured overhead to blocks + term-dict (a re-measure
> with tableId is owed). The "~25 s background merge" is **separately measured** work the design intends
> to run off the foreground path; the spike runs the merge synchronously, so it is not yet proven that a
> user waits only the ~22 s foreground (concurrent merge is a build-then-measure item, §11).

---

## 4. Public API

Reads are **thread-safe, direct (snapshot)**; writes are **thread-safe, async** — each
enqueues an apply task on the mpsc queue, so callers never need to be "on the worker" (an
improvement over `invertedindex`'s contract). The constructor is pebble-free (a path, like
`idtable.Open`).

```go
package invertedstore

func Open(path string, q queue.Queue, opts Options) (*Store, error)

// Reads — concurrent, lock-free over a segment snapshot (+ RLock head).
func (s *Store) Search(tableId int, query string, limit int, filterKeyword func(string) bool) SearchResult
func (s *Store) GetDocs(tableId int, key string) SearchResult

// Writes — thread-safe, asynchronous (enqueue an apply task). `keywords` is the doc's
// CURRENT full keyword set; empty ⇒ delete the doc. NO oldKeywords: the store diffs against
// its own forward map (§8), so it can't drift from a stale caller arg. Update is exactly a
// single-item Batch.
func (s *Store) Update(tableId int, docid int64, keywords []string)
func (s *Store) NewBatch() *Batch

// Table ops return values ⇒ synchronous (block on the worker via RunTask).
func (s *Store) CreateTable(description string) (int, error)
func (s *Store) DeleteTable(tableId int) error
func (s *Store) CloseAndWait()

// Batch amortizes N updates into ONE apply task (94,559 tasks → ~185). It is the bulk
// ingest path; Update is the n=1 convenience.
type Batch struct{ /* ... */ }
func (b *Batch) Update(tableId int, docid int64, keywords []string) *Batch
func (b *Batch) Commit()

type SearchResult struct {
	DocIds     map[int64]struct{} `json:"docIds"`
	WildDocIds map[int64]struct{} `json:"wildDocIds,omitempty"`
}
type TableInfo struct{ Id int; CreatedAt time.Time; Description string }

type Options struct {
	CapBytes        int    // head byte cap (the memory knob); default 16 MiB
	Fanout          int    // tiered-merge fanout; default 4
	DataCodecL0     Codec  // default snappy
	DataCodecMerged Codec  // default zstd (bounded: concurrency 1, 128 KiB window)
	DictCodec       Codec  // term-dict region codec; default zstd
	DictChunkBytes  int    // term-dict chunk size; default 4096
	ChunkCacheBytes int    // Store-level dict-chunk LRU budget; default 32 MiB
	InlineThreshold int    // value ≤ this is inline, else external; default 1 KiB
}
```

`docid` is **int64** (idtable ids); postings are delta-varint of the unsigned bit pattern
(identical scheme to `invertedindex/codec.go`).

**Compatibility.** `Search`/`GetDocs` and the table ops match `invertedindex` exactly.
`Update` **changes**: it drops `oldKeywords` (the store owns the forward map) and is
async/thread-safe (no "must run on the worker"). This is a deliberate, smaller, safer
signature — the migration at the call site (`documents.Store`) is mechanical and lets that
store **drop its doc-words machinery**.

**Drop-in seam.** Consumers depend on an `Indexer` interface both implementations satisfy
(`Search`/`GetDocs`/`Update`/`NewBatch`/`CreateTable`/`DeleteTable`/`CloseAndWait`);
`invertedindex` satisfies it with a trivial adapter. `SearchResult` must be a shared/aliased
type across the two packages — resolved in build step 8 (§12).

---

## 5. On-disk layout

Rooted under the storage version dir (see §10), self-managed (no pebble):

```
<storagePath>/<version>/invertedstore/
  MANIFEST          # live segment set + table catalog + checkpoint (atomically replaced)
  seg-000123.dat    # immutable segment files (id = monotonic seal sequence)
  seg-000124.dat
  ...
```

### Two key-types in one sorted keyspace

A segment is a single sorted run holding **both** the inverted and the forward maps,
separated by a **key-type prefix byte**, so all the segment/spill/merge machinery is shared.
`tableId` is a **fixed-width 4-byte big-endian** value immediately after the type byte (so
keys sort by `(keyType, tableId, …)` and a Search/GetDocs prefix is unambiguous — a
variable-width tableId would mis-sort `[I]2foo` vs `[I]10foo`):

```
key           := keyType(1) tableId(4 BE) ( keyword | docid )      # docid = 8 BE int64
[I] = 0x01 → invertedValue     # inverted: sorted by (tableId, keyword)
[F] = 0x02 → forwardValue       # forward:  sorted by (tableId, docid)

invertedValue := uvarint(addsByteLen) delta-varint(added docids) delta-varint(tombstoned docids)
                 # the del-list runs to end-of-value; addsByteLen splits the two regions
forwardValue  := uvarint(nKw) delta-varint(sorted term-ids)   # the doc's keywords as term-ids, §8
                 # nKw == 0 (a single 0x00 byte) is the FORWARD TOMBSTONE (doc deleted). A live doc
                 # has nKw >= 1, so it can NEVER alias the tombstone — even one whose only term-id is
                 # ordinal 0 encodes as 0x01 0x00 (nKw=1, then the ordinal). The nKw prefix also lets
                 # merge carry a tombstone through verbatim (decode 0 ords → remap nothing → re-emit nKw=0).
```

> **`[I]` = 0x01 sorts BEFORE `[F]` = 0x02.** This ordering is load-bearing: a streaming
> k-way merge must emit all inverted keys (and assign the merged segment's term-ids) before
> it writes any forward record that references those term-ids (§6, §8).

- **Inverted** drives prefix Search (contiguous by keyword). Its value carries this segment's
  **adds and per-keyword tombstones** for the keyword — there is **no doc-level tombstone** and
  **no roaring**; removal of docid D from keyword K is the pair `(K,D)` in the del-list,
  resolved newest-wins at read (§6) and at merge (§6, §8).
- **Forward** lets `Update` read a doc's old keywords to diff. invertedstore **owns it** so the
  inverted index stays self-consistent (it never trusts a caller-supplied `oldKeywords`). It is
  a **latest-wins point-lookup by docid**. The value is segment-local **term-ids** (§8); a
  **delete writes an explicit forward-tombstone** (the `nKw=0` form) so the newest-wins scan
  returns "empty" rather than letting an older non-empty record win.

> **Postings encoding — delta-varint, NOT roaring (measured).** ~10.6M terms at ~4
> postings/term ⇒ posting lists are overwhelmingly **tiny/sparse**, roaring's worst case.
> Isolated A/B (codec=none, full linux): delta-varint **653 µs / 337 MiB / 11.4 s** vs roaring
> **3809 µs (5.8× slower) / 534 MiB (1.6× bigger) / 17.1 s**. roaring would only win for
> few-huge-dense lists or heavy boolean intersection — not this prefix-union workload.

### Segment layout — SSTable: data blocks of packed records + a term-dict region

A segment is a sequence of **data blocks**, then (for term-id forward) a **term-dict region**,
then the block index and a footer.

**A data block packs N records `(key,value)` and is compressed AS ONE UNIT** (~32 KiB of
records before compression), so the per-block codec overhead is amortized across many tiny
values — critical here, where most posting values are 1–6 bytes. A value is stored **inline**
in its record when small; only a **large** value (> `inlineThreshold`, default 1 KiB) is
written **externally** as ≤64 KiB chunks with the record holding a pointer — so a single large
value cannot bloat a block and memory stays bounded.

```
segment :=
  ( externalValue* , block )*     # large values are written just before the block that
                                  # references them; small values are inside the block
  termDictRegion?                 # present iff this segment uses term-id forward (§8)
  blockIndex
  footer

block         := uvarint(rawLen) uvarint(compLen) dataCodec( record* )   # ~32 KiB raw, ONE compress
record        := uvarint(klen) key  flag
                 flag==0 inline:   uvarint(vlen) value
                 flag==1 external: uvarint(offset) uvarint(compLen)
externalValue := chunk* ;  chunk := uvarint(rawLen) uvarint(compLen) dataCodec(bytes)   # ≤64 KiB raw

# term-dict region: the [I] keyword strings in ORDINAL order (no postings), a 2nd compact copy
# so an Update's term-id -> string resolve reads ONE region instead of the scattered inverted
# blocks. Compressed in small (default 4 KiB raw) chunks under a SEPARATE dictCodec; each chunk
# headed by its firstOrd so a single ordinal binary-searches to its chunk.
termDictRegion := dictChunk*
dictChunk      := uvarint(firstOrd) uvarint(rawLen) uvarint(compLen) dictCodec( (uvarint(klen) keyword)* )

blockIndex := uvarint(numBlocks) ( uvarint(fkLen) firstKey uvarint(blockOffset) )*   # in memory on open
footer     := blockIndexOffset(8 BE) termDictOffset(8 BE) dataCodecId(1) dictCodecId(1) magic(7)  # 25 bytes
```

> **Two codec ids in the footer.** The data blocks and the term-dict region use *different*
> codecs (data L0=snappy/merged=zstd; dict=zstd, §7), so each segment persists BOTH ids and the
> reader picks them up on Open — a reader must never have to guess a region's codec. (The spike's
> footer is 24 bytes with one codecId and a process-global dict codec; persisting `dictCodecId`
> is the production fix.) `docid` on disk is a fixed **8-byte big-endian int64** in the `[F]` key,
> and posting deltas are uvarints of full uint64-space gaps (the spike measured int32 — byte-
> identical within the int32 range at this corpus; a full-int64 re-measure is owed).

- **key** = `(keyType, tableId, keyword|docid)` (`[I]`/`[F]`); **value** = `invertedValue` or
  `forwardValue`.
- Blocks bound memory: a block decompresses to ~32 KiB; an external value is read one ≤64 KiB
  chunk at a time; a dict chunk is ~4 KiB. **No single document or keyword produces an unbounded
  block or read buffer**, whatever the input.
- The **term-dict region** is a deliberate ~1× redundant copy of the term strings (already
  present in the `[I]` keys) laid out for ordinal access; it is built at seal time by re-reading
  the segment's own just-written inverted blocks one at a time (so merge stays bounded-memory),
  and its on-disk cost and the resolution tradeoff are quantified in §8.

### MANIFEST

A small **versioned** file (length-prefixed binary or versioned JSON — a format byte first):
storage version, the live segment set `{id, level, dataCodec, dictCodec, tableRange, size}`, and the
**table catalog** (`TableInfo` per tableId + next-table-id, replacing pebble's table rows). It carries
**no recovery watermark** — recovery is indexer-driven (§9), so the store need only be crash-consistent.
Replaced atomically (write `MANIFEST.tmp`, fsync, rename) on every seal/merge/table change — the only
fsync'd metadata.

> **In v1 every term-id segment — L0 spill and merged — carries the term-dict region.** The §3
> disk numbers include the L0 dict regions. Storing forward as strings on L0 (and converting to
> term-id only at the bottom merge) is the **v2 hybrid** (§13), not v1.

---

## 6. Write & read paths

### Write path

All public writes are **thread-safe and asynchronous**: each enqueues an apply task via
`q.AddFunc` (mpsc) — no "must be on the worker" contract. **`Update` is a single-item
`Batch`**; `Batch.Commit` amortizes N ops into one task. The apply task runs on the single
worker, so writers are serialized with no locks; `Search` reads concurrently via a snapshot.
`CreateTable`/`DeleteTable` return values so they are synchronous (`q.RunTask`); don't call
them from within a worker task.

- **Head buffer** (worker-owned; RWMutex for reader access): per `tableId`, the inverted deltas
  `keyword → {added docids, tombstoned (keyword,docid)}` **plus the forward entries**
  `docid → keywords`. A running **byte estimate** drives spill. v1 **dedups docids in memory** when
  appending to a keyword's list (a docid already present is a no-op) so repeated edits of the same
  doc within one window don't inflate the head; the spike does NOT do this in-memory dedup (it
  appends unconditionally and lets the spill-time `encodeDocs` sort+dedup collapse them), so the
  in-memory-dedup memory benefit is a v1 addition to measure, not a spike-measured number.
- `Update(tableId, docid, keywords)`: read the doc's old keywords from the forward map (head, then
  segments — latest-wins; for term-id this is the ord→string resolve of §8), diff, and apply (the
  diff differs for term-id, see §8). Set `forward[docid]=keywords`. `keywords` empty ⇒ **delete**:
  tombstone docid in all its old keywords **and write a forward-tombstone record** (`nKw=0`, §5) —
  *not* merely dropping the entry, since over append-only segments a dropped record would let an
  older non-empty forward record win and resurrect the doc. **No segment is ever rewritten** —
  every op is an append. Cold build: all docs are new ⇒ the forward read misses ⇒ write-only.
- **Spill**: when the byte estimate ≥ `CapBytes`, sort the head's keys, write **one L0 segment**
  (snappy data blocks, §7), fsync once, install a new MANIFEST version, reset the head. The segment
  is written as **[sorted inverted records] ++ [forward records by docid]** — a single sort of the
  term dict yields both the sorted inverted order and (for term-id) each keyword's ordinal; forward
  records are already in docid order and `[I] < [F]`, so no second full sort. The byte estimate is a
  **logical** size (matching the spike: `len(keyword)+16` per new keyword, `+4` per posting, and
  `8 + len(keywords)*4` per forward entry), not physical file bytes. `CapBytes` is **the** low-memory
  control.
- **Batch**: `NewBatch()` accumulates `(tableId, docid, keywords)` ops in memory; `Commit` enqueues
  **one** apply task that applies them in order on the worker (a repeated docid → last op wins). A
  spill triggered mid-Batch is fine — the head and segment set are worker-owned and the apply runs
  to completion before the next task. This collapses ~94,559 cold-build tasks into ~185.
- **Background merger** (own goroutine, off the critical path): tiered policy — a level with ≥
  `Fanout` segments is streaming k-way merged into one next-level segment. The merge **reconciles
  each `(keyword, docid)` to its newest action** across the inputs (merged oldest→newest, the latest
  add-or-tombstone wins) — so an add superseded by a later tombstone (or vice-versa) collapses to the
  survivor, fixing add→del→add. It **cannot drop a keyword key** (the term-id remap append-index *is*
  the source ordinal, §8), so a fully-tombstoned keyword persists as a small del-only record;
  tombstones whose matching add is co-located are reclaimed, others survive until a **covering merge**.
  For term-id the merge also **remaps ordinals** and rebuilds the term-dict region (§8); all of this is
  bounded-memory. Then the MANIFEST is atomically swapped and inputs deleted (deferred until no reader
  references them, §6 concurrency).
- **Covering merge (the reclamation forcing function).** Incidental tiered fanout alone does NOT bound
  tombstone / fully-tombstoned-key / cross-window-duplicate / dead-tableId growth — a doc edited forever
  or a dropped table sitting at the bottom level may never be re-merged. A **covering merge** is a full
  compaction of the bottom level together with everything above it (for one tableId, or globally) that
  reclaims all of the above. Default trigger: fire it when the bottom level's **dead fraction**
  (tombstoned + superseded postings ÷ live) crosses a threshold (default ~25%), checked after each tiered
  merge; `DeleteTable` **schedules one explicitly** (otherwise a dropped table at the bottom would never
  be reclaimed). This policy is **specified here but not yet validated** — it does not exist in the spike,
  so the "bounded growth" guarantee is a build-then-measure item (§11), not a measured one.

### Read path (search)

A keyword's current postings can be spread across the head + several immutable segments (writes
only append). `Search`/`GetDocs`:

1. **Snapshot** the live segment set (atomic version pointer) + RLock the head — no queue, no
   blocking against writers.
2. **Prune, don't full-scan**: skip any segment whose key range / block index can't contain the
   `[I]` prefix; within a candidate, binary-search the block index and `ReadAt`+decompress only the
   overlapping **blocks** — one block decompress yields **many** key+value pairs at once (keys and
   their inline small values are co-located). Cost = a few block reads per live segment.
3. **Newest-wins merge**: scan the head first, then segments **newest→oldest**; the first mention
   of a given `(keyword, docid)` — an add *or* a tombstone — decides it, older mentions ignored.
   Accumulate surviving docids (gen-stamped set), apply `filterKeyword`/`limit`/`WildDocIds`.

Search **never reads the forward map**, so the term-id encoding does not affect it. This is LSM
read-amplification, and **pebble pays the identical cost** internally; our K segments ≈ pebble's
sstables/levels, K bounded by the background merge. Measured search **~1180 µs — faster than
pebble's 2228 µs**.

### Forward read (term-id → strings)

`Update`'s diff needs the doc's old keyword **strings**. The forward record gives **term-ids**;
resolving them to strings reads the winning segment's **term-dict region** (§8). A **Store-level
chunk cache** (default 32 MiB LRU of decompressed dict chunks) keeps the hot chunks (common terms,
recently-edited files) resident so the resolve is cheap under real editing locality; memory stays
bounded by the LRU budget. Resolution cost and its tradeoffs are in §8.

### Concurrency model

Single-writer, many-reader (the spike is single-threaded and zero-lock — this is a build-then-measure
specification):

- **Writes** run only on the mpsc worker (one goroutine), so the head and the live segment set have a
  single mutator and need no write-write locking. The head keeps only the **latest action per
  `(keyword, docid)`** (a later tombstone cancels a pending add and vice-versa) so a spilled value
  never holds both for a docid.
- **The live segment set** is published via an `atomic.Pointer[segmentSnapshot]`; the worker swaps in a
  new snapshot on seal/merge/table change. `Search`/`GetDocs` load the pointer once (a consistent
  snapshot) and never block writers.
- **The head** is guarded by an `RWMutex`: the worker `Lock`s only for the brief mutation of head maps;
  readers `RLock` to scan it. Spilling resets the head under the write lock.
- **Deferred segment deletion**: a merge swaps the MANIFEST to drop input segments, but a reader may be
  mid-scan on one. Each snapshot holds segment handles by **refcount** (or epoch); a merged-away
  segment's file is unlinked only once its refcount hits zero (POSIX keeps an open fd valid across
  unlink, so in-flight reads finish safely). The **chunk LRU** is keyed by `(segmentId, chunkIdx)` with
  its own mutex, and entries for a merged-away segment are purged on the MANIFEST swap; it is read on the
  Update (forward) path only — Search never touches it.
- `CreateTable`/`DeleteTable` return values, so they run synchronously via `q.RunTask` (don't call from
  within a worker task). Because segments are immutable there is **no synchronous prefix delete**:
  `DeleteTable` drops the catalog entry (and bumps a per-table epoch); `Search`/`GetDocs` return empty
  for an absent tableId immediately, and the dead table's `[I]`/`[F]` keys are reclaimed when a covering
  merge drops keys whose tableId is no longer in the catalog — **which `DeleteTable` schedules**, so the
  bytes are reclaimed even if the table's segments sit at the bottom level with no further writes.

---

## 7. Compression — per-level + a zstd term-dict region

Priorities are **build > memory > disk > search** (disk ranks above search). A data block (§5)
packs many records and is compressed as one unit behind a codec seam.

**Decision: per-level — L0 spills `snappy`, background-merged segments `zstd`; the term-dict
region is `zstd`.** L0 is on the foreground build path so it stays snappy-fast; the bulk of the
data ends up in background-merged bottom segments, which get zstd's better ratio off the critical
path. zstd **must** be bounded (`WithEncoderConcurrency(1)` + 128 KiB window; the default spins up
GOMAXPROCS encoders → 766 MiB observed).

| codec layout (full linux, cap=16, tiered, forward on) | foreground | merge | disk | search |
| --- | --- | --- | --- | --- |
| snappy everywhere | ~20 s | ~23 s | 432 MiB | ~1150 µs |
| **per-level (snappy L0 + zstd merged)** | ~22 s | ~25 s | **241 MiB** | ~1180 µs |

Per-level wins under the priority order: disk drops markedly for a modest search cost (both beat
pebble's 2228 µs), the foreground stays snappy-fast, merge is background.

**Term-dict region codec = zstd, chunk = 4 KiB (recommended production defaults).** The dict region is
accessed by *scattered single ordinals*, so the choice trades disk vs resolve speed (§8): zstd packs the
region ~30% smaller than snappy (74.7 vs 105.9 MiB) and, under real editing locality, reads **nearly as
fast** as snappy (the chunk LRU absorbs most of the per-call cost; in the worst-case spread read zstd is
~11% slower — 1211 vs 1080 µs — but in the realistic code-edit scenario the two are within noise).
Smaller dict chunks make a single resolve decompress less wasted data, at a slightly larger region;
4 KiB is the measured sweet spot for the LRU+locality case. These are the recommended `Options` defaults
(`dictChunk=4096`, `dictCodec=zstd`, `chunkCacheBytes=32 MiB`); note the spike's *flag* defaults differ
(`-dictchunk=32768`, `-chunklru=0`), so the headline numbers require the explicit flag set in §11.

Orthogonal future win: keyword **prefix compression** within a block (shared-prefix-len + suffix)
before the codec runs.

---

## 8. Forward map: segment-local term-ids

The forward map (`docid → keywords`) is the single largest part of the index when stored as
strings — the relocated doc-word lists. Storing it as **segment-local term-ids** shrinks the whole
index **25%** (319 → 241 MiB). This section is the design and the measured tradeoff in full,
because term-id is the one place that costs something elsewhere (the update read path).

### Encoding

A keyword's **term-id is its position (ordinal) in the segment's own sorted inverted term dict**.
The dict is exactly the `[I]` keys, already sorted; the spill/merge sort produces the ordinals for
free. A forward value is then `delta-varint(sorted ordinals)` — structurally identical to a posting
list, 1–3 bytes per keyword instead of a full string.

- **Why segment-local (not a global keyword→id allocator).** A global allocator (a keyword
  `idtable`) was **rejected**: the per-keyword id lookup on the cold-build hot path (41.4M lookups /
  10.6M new) fights priority #1 (build speed). Segment-local ordinals are assigned with **zero** extra
  hot-path work.
- **Consequence: ordinals are per-segment.** The same keyword has a different ordinal in each
  segment, so two things are required — a merge **remap**, and an **ordinal→string** path for reads.

### Merge remap (ord → ord, bounded memory)

When segments merge, the merged segment has a new sorted term dict, so every forward value must be
re-pointed. Because `[I]` sorts before `[F]`, a single streaming pass works:

1. As the k-way merge emits each merged inverted key, it assigns the next output ordinal and appends
   it to `remap[srcSeg]` for each source that contributed the key (the append index *is* that key's
   source ordinal in that segment). So `remap[srcSeg][srcOrd] = outputOrd`, built incrementally.
2. When forward records are emitted (after all inverted keys), each value's ordinals are remapped
   `srcOrd → outputOrd` via the integer arrays — **no string round-trip**, so merge memory is
   Σ(source term counts) ints (**≈42 MB at the bottom merge — an estimate from the term count, not a
   spike-measured figure; T6 asserts the bound**), not a string map.

The remapped forward correctly tracks newest-wins across the merge. Correctness is gated in the
spike by a sampled forward round-trip (decode → resolve → compare to ground truth): **401/401 OK**
after spill + merge.

### Resolution (term-id → string) and the term-dict region

`Update`'s diff needs old keyword **strings**. A doc's keywords scatter across the whole alphabetical
term dict, so resolving its ordinals from the postings-diluted inverted blocks is expensive. Instead
each segment carries a compact **term-dict region** (§5): the keyword strings in ordinal order, zstd
in 4 KiB chunks, each chunk headed by its `firstOrd`. Resolution binary-searches `firstOrd` → chunk,
decompresses it (or hits the Store-level **chunk LRU**, default 32 MiB), and slices out the string.
The region is the ~1× redundancy that buys cheap ordinal access; it is rebuilt at seal/merge time by
re-reading the segment's own inverted blocks (bounded memory).

### The disk ⇄ update-read Pareto (measured)

Resolution is either dict-resident (fast, more memory) or scattered (bounded memory, slower). With
the chunk-index + LRU it is **bounded memory at every point (~220 MiB)**, and disk-saving trades
against update-read speed via the dict chunk size and codec:

| forward scheme | cold disk | vs string | update read /doc (spread, worst case) |
| --- | --- | --- | --- |
| string forward | 319 MiB | — | 462–566 µs |
| term-id, zstd dict 4 KiB | **241 MiB** | **−25%** | ~1200 µs (bounded ~220 MiB) |
| term-id, snappy dict 4 KiB | 272 MiB | −15% | ~1080 µs |

The **spread** read (2000 distinct docs scattered across the corpus) is the worst case. The
**realistic** case — a small working set of files re-edited (interactive code editing) — is far
kinder, because after a file's first edit its forward lives in a small recent segment (small dict)
and its chunks stay hot in the LRU:

| code-edit scenario (16 files × 128 re-edits) | string | term-id (zstd dict 4K, 32M LRU) |
| --- | --- | --- |
| forward read /edit | ~520 µs | ~660 µs (1.26×, sub-ms — imperceptible) |
| disk after edits | 313 MiB | **238 MiB (−25%)** |
| search | identical (~1.1 ms, 1.8× faster than pebble) | |
| peak memory | ~230 MiB | ~220 MiB (bounded) |
| correctness | ok | edit + forward round-trip verified |

### Update strategy: full re-post (cheap in practice)

term-id cannot do a string-style **delta** update: a doc's new forward references its *full* current
keyword set, which must all be `[I]` keys in the segment that holds the forward, so on edit the doc
is **fully re-posted** (every current keyword re-added in the new segment) plus per-keyword
tombstones for removed keywords. The real residual cost is small: **one full re-post per doc per
spill window** (vs string's changed-only). Those re-posts then coalesce — at spill `encodeDocs`
sort+dedups a docid that repeats within a window, and a **covering merge** dedups duplicate adds
across segments — so they are bounded to that per-window re-post and **do not accumulate on disk**:
measured cold disk stays −25% and edit wall-time is unaffected. (Across windows a *partial* tiered
merge may not yet co-locate a frequently-edited doc's segments, so some duplicate adds sit on disk
between merges until a covering merge collapses them.) v1 can also dedup in the head in memory (§6)
to shrink the per-window cost further — a v1 addition to measure, not a spike number.

### Why this is the chosen scheme

Across the two real update patterns: **interactive edits** go through the Update path where the read
penalty is ~1.3× and sub-ms (locality), and the write coalesces; **mass changes** (whole-tree) are a
**rebuild** through the cold-build path, where term-id is *best* (faster build, less memory, −25%
disk). The unrealistic "spread incremental update of thousands of files" — the only case term-id
reads slowly — is one you would do as a rebuild instead. So term-id wins or ties on every priority
axis (build, memory, disk, search) at a sub-millisecond, locality-absorbed update cost.

---

## 9. Durability & crash recovery

**No WAL** — the index is fully re-derivable from source files (the indexer tokenizes them).

> **WAL cost (measured in an earlier WAL-enabled spike iteration; the `-wal` flag has since been
> removed, so the current spike has no WAL path).** Full linux, real disk, group-commit fsync:
> **batch=512 docs → +~1 s on a ~22 s build (~5%)**; batch=128 → +4 s (~19%); batch=1 (per-doc) →
> **7m10s (~20×)**. Affordable at a sane batch but not worth it: it only saves re-tokenizing the
> **unspilled head** on crash (≤ one cap, ~16 MiB / 1–3 s), crashes are rare, and it doubles write
> volume. "No WAL" rests on the re-derivable-from-source argument; keep WAL as an *optional* knob.

- A **sealed segment** is durable once its file is fsync'd and the MANIFEST naming it is atomically
  replaced (write-tmp + fsync + rename).
- The **head buffer is volatile** — lost on crash.
- **Recovery is indexer-driven; the store only guarantees crash-consistency** (sealed segments durable,
  head volatile). The store does NOT keep a recovery watermark — a store-internal apply counter is
  incomparable to per-doc source state and has no natural producer. Instead, on Open the **indexer**
  reconciles its source view against the store using its **own** durable cursor (the change-tracking it
  already needs for incremental indexing): it re-`Update`s every doc whose source mtime/version is newer
  than that cursor — **including low-id docs** edited just before the crash — and it **reconciles
  deletions** (a docid in the store's forward map but absent from source is re-`Update`-d with empty
  keywords = delete). This is safe because **`Update` is idempotent in result**: re-`Update`-ing an
  already-sealed doc with the same keywords yields no net change (a redundant newer forward + re-post a
  covering merge dedups), so the indexer may over-replay without corrupting the index. The store exposes
  a hook to enumerate `forward` docids (or a `Reconcile` callback) so the indexer can drive the deletion
  pass.
- Merge is crash-safe: the new segment is fully written + fsync'd before the MANIFEST swap; inputs are
  deleted only after. A crash mid-merge leaves the inputs live and the orphan output unreferenced
  (GC'd on next Open).

---

## 10. Migration

Breaking on-disk change (new format, pebble dropped) → **reindex on upgrade**, the established
mechanism (`internal/core/storage`): bump `StorageVersion`, build into the new
`<version>/invertedstore/` dir, add the old version to the cleanup list so the stale pebble
inverted-index data **and** `documents.Store`'s doc-words (now owned here) are removed. No live
migration — a reindex from source is simpler and the index is derived anyway.

---

## 11. Validation & the spike

Everything above is backed by `core/cmd/sortbench` (spike branch
`worktree-spike+sortruns-invertedindex`): a `pebble` baseline + a `sortruns` mode (byte-capped head →
spill → tiered merge → segment search) over the kept token dump `/workspace/blugespike/lx.gob`. The
forward map is **always on**. The §3 headline numbers are the run:
`sortruns -cap=16 -merge=tiered -fanout=4 -codec=snappy -mergecodec=zstd -termid -dictcodec=zstd
-dictchunk=4096 -chunklru=32` (plus `-updates`/`-editfiles -editrounds` for the §8 update tables).
The spike validates the **on-disk format, build path, memory bound, long-term merge, search,
compression, posting encoding, the term-id forward (encoding, merge remap, resolution, the disk⇄read
Pareto), and the incremental + code-edit update paths** — all measured on the real corpus and real disk.

NOT yet exercised in the spike (so NOT numbers we report, per AGENTS.md §3), and each owes a spike case
or a re-measure before the matching build step is "done":

- **MANIFEST format + crash recovery** (the indexer-driven recovery of §9).
- **tableId multi-tenancy** — keys carry no tableId in the spike; re-measure disk with it (§3 caveat).
- **int64 docids** — the spike is int32 (byte-identical at this corpus); re-measure for the full range.
- **Concurrent** background merge (the spike merges synchronously inside spill).
- **Merge value reconciliation** for `add → del → add` on one `(keyword,docid)` (the spike concatenates
  adds+dels — correct only because its edit workload adds globally-unique words; needs the §6 reconcile +
  a test).
- **Delete** (`Update` with empty keywords → forward-tombstone + re-read returns empty).
- **In-memory head dedup** (§6) — the spike appends unconditionally; measure the peak-memory effect.
- The **WAL** path (removed from the current spike; §9 numbers are from an earlier iteration).

---

## 12. Build order (v1 = full scope)

1. **Segment format** — block writer/reader (inline-small/external-large values, block index, footer
   with **both `dataCodecId` and `dictCodecId`**), the **term-dict region** writer/reader, the two
   key-types with a **fixed 4-byte tableId** and **8-byte int64 docid**, codec seam; unit/golden tests
   against `invertedindex`'s delta-varint values, incl. the `invertedValue` (addsByteLen + adds + dels)
   and forward-tombstone (`nKw=0`) encodings.
2. **Head buffer + spill + MANIFEST + table catalog** (versioned MANIFEST encoding, no recovery
   watermark — recovery is indexer-driven, §9); `Open`/`Close`, `CreateTable`/`DeleteTable`. The head
   keeps the latest action per `(keyword,docid)` and **dedups docids in memory**.
3. **Forward map (term-id)** — write segment-local ordinals, the ordinal→string resolution path
   (term-dict region + **Store-level chunk LRU**, keyed by `(segmentId,chunkIdx)`), latest-wins point
   lookup **incl. the forward-tombstone**; so `Update` can diff.
4. **Search/GetDocs** over head + segments (prefix scan by `(tableId,keyword)`, newest-wins union,
   `filterKeyword`, `limit`, `WildDocIds`, tombstone resolution).
5. **Update/Batch** (async apply; term-id full re-post + per-keyword tombstones + forward write;
   **delete = forward-tombstone + tombstone all old keywords**; Batch = one apply task, last-op-wins).
6. **Background tiered merger** — streaming k-way merge with **per-`(keyword,docid)` newest-wins
   reconciliation** (fixes add→del→add; cannot drop keys — preserves the remap invariant), **ord→ord
   remap + term-dict rebuild**, crash-safe MANIFEST swap, deferred file reclamation by reader refcount.
7. **Compression** — snappy/zstd data codecs + the zstd term-dict region behind the codec seam (§7),
   each persisted in the footer.
8. **Concurrency** — `atomic.Pointer` segment snapshot, head `RWMutex`, MANIFEST-swap-then-deferred-
   delete with reader refcount/epoch, chunk-LRU mutex + purge-on-swap (§6 concurrency).
9. **`Indexer` interface + server wiring** (+ shared `SearchResult`); `documents.Store` drops doc-words
   and calls `Update` without oldKeywords; **StorageVersion** bump + cleanup + reindex-on-upgrade
   (indexer-driven recovery, §9).
10. **Differential + correctness tests** vs `invertedindex` (identical hit sets) + add→del→add, delete,
    crash-recovery cases + the memory-capped build benchmark + the code-edit update benchmark as
    regression guards.

---

## 13. Open questions / deferred

- **Hybrid forward (deferred).** Storing fresh/L0 forward as **strings** and converting to term-id only
  at the bottom merge would give delta updates with no re-post and no resolve on recent docs. Measured
  unnecessary for v1 (the re-post coalesces in memory and the resolve is sub-ms under locality), but it
  is the obvious v2 lever if a long-session merge stress test ever shows the re-post hurting.
- **Doc-version watermark (alternative, unmeasured).** Drop the forward entirely (`docid → latest-seq`,
  Update = bump seq + re-post, search filters stale postings). Cheapest build+update read; cost moves to
  a search-time seq filter + a docid→seq map. Worth a measurement only if term-id's update read ever
  proves too costly in production.
- **Block-size sweep** on the current format (16/32/64 KiB) — 32 KiB is the SSTable-conventional default;
  re-measure before fixing.
- **`WildDocIds` / suffix-tokenizer** parity: confirm no coupling beyond prefix semantics.
- **Merge scheduling**: idle detection / rate-limit so the merger doesn't contend with foreground
  indexing or search; chunk-LRU contention under concurrent search + update.

