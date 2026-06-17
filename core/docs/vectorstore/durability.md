# Durability, recovery, and concurrency

> Subsystem page under [architecture.md](architecture.md). Owns the bbolt control
> store, the durable head record, the seal commit order, crash windows, recovery,
> and the concurrency/locking model. The bbolt control store is the **structural
> source of truth**.
>
> **Plane split.** The **control plane** — store metadata (version / head / metric),
> the declared attr-index set, the sealed-segment set, per-`(index, segment)` build
> state, **and the durable head record** — lives in a single embedded bbolt DB
> (`control.db`); see [Control plane on bbolt](#control-plane-on-bbolt). The **data
> plane** (sealed-segment `vectors.dat` + `graph-<name>.dat` and the zero-copy
> `getVectorRef` mmap aliasing) **stays flat mmap** and is never moved into bbolt:
> boxing bulk vectors/graphs in bbolt would destroy the zero-copy hot path and bloat
> the file under long-lived read transactions.
>
> **Migration status.** Increments 1–2 (this page's as-built state) have migrated
> the **manifest** and the **head record** onto bbolt: every structural change AND
> every Put/Delete commits a `control.db` write-txn — there is no longer a
> `records.wal`. The `docId→segId` map (`docseg` bucket) and tombstones (`tomb`
> bucket) are **later increments** — they are still served by the recovery rescan of
> `slotdoc.dat` and `tomb.dat` respectively.

## Two persistent faces

Durability has two faces with complementary jobs, so the hot path stays cheap. Both
now live in the same bbolt control store, but they commit at different granularities:

| | writes | when | guarantee |
|---|---|---|---|
| **head record** (`head` bucket) | each Put/Delete | every write | one `db.Update` commit (copy-on-write page swap, fsync) per write → a returned write is durable |
| **structural state** (segments / indexes / …) | a snapshot of the whole segment set + index state | each structural change (seal / compact / merge / create / drop / rebuild index) | one `db.Update` commit is atomic |

The hot path commits a single small head-row txn per write; the structural buckets
are committed only on the infrequent structural changes. (There is no separate
append-only log file: bbolt's per-commit fsync is the durability point that the old
`records.wal` append+fsync used to provide.)

## Head record

The `head` bucket is the durable form of the in-memory brute head: one row per LIVE
head docId, keyed by `docId` (8 bytes, big-endian). The value is
`idLen(4) · id · norm(4) · vecLen(4) · vec(N·4) · plLen(4) · payload`. Each Put
overwrites the row for its docId (so an upsert is a single keyed Put, not a
tombstone+append); each Delete of a head doc removes the row. A structurally corrupt
value (any length field overrunning the buffer) fails recovery loud rather than
mis-parsing — the bbolt analog of the old WAL CRC / torn-tail rejection.

The string `id` is stored alongside the vector for the same reason the old WAL stored
it: it makes the `id↔docId` mapping crash-safe **independently of idtable's lazily-
committed batch**. On `Open`, `rebuildHead` reads the head bucket in `docId` order
(the monotonic Put insertion order), re-drives the allocator for each `id` to
reconstruct the same `docId`, and rebuilds `idToDoc` — so a crash with no idtable
commit still recovers every head doc's mapping. The in-memory flat head stays for
brute-search speed; it is rebuilt from this bucket at `Open`, never from a log replay.

## Control plane on bbolt

The control plane lives in a single embedded [bbolt](https://pkg.go.dev/go.etcd.io/bbolt)
database, `control.db`, opened beside the flat `seg-*` data dirs. bbolt gives an
ACID, single-file, B+tree KV with copy-on-write page commits, so **one bbolt
write-txn commit = one atomic change** — whether that change is a single head row
(Put/Delete) or a whole structural snapshot (seal/merge). This replaces both the
former hand-rolled manifest's `serialize + CRC32 + tmp + fsync + rename + dir-fsync`
rewrite AND the `records.wal` per-write `append + fsync`: a failed or panicking txn
rolls back fully — there is no half-written state, and there is no separate CRC
because bbolt checksums its own pages.

The control store is opened exclusively (an OS flock with a 5 s timeout), so a
second opener of the same dir fails fast instead of corrupting the file. The bbolt
DB is opened with `NoSync` **off**: control-plane writes must be durable.

Buckets (each is one logical control-plane table):

| bucket | key | value | replaces | wired? |
|---|---|---|---|---|
| `meta` | `version` / `headSegId` / `primaryMetric` | scalar | manifest header | **yes (incr 1)** |
| `attrdecls` | property | kind(1) | manifest `nDecls` block | **yes (incr 1)** |
| `segments` | segId(8, big-endian) | gen(4) · vecCount(8) · tombCount(8) | manifest `nSeg` block | **yes (incr 1)** |
| `indexes` | name | type(1) · metric(1) · M(4) · efC(4) · efS(4) | manifest `nIdx` block | **yes (incr 1)** |
| `indexsegs` | name ‖ segId(8, big-endian) | gen(4) · state(1) | manifest `nIdxSeg` block | **yes (incr 1)** |
| `head` | docId(8, big-endian) | idLen(4) · id · norm(4) · vector · payload | `records.wal` | **yes (incr 2)** |
| `docseg` | docId(8) | segId(8) | recovery rescan of `slotdoc.dat` | *(incr 4/3)* |
| `tomb` | segId(8) ‖ slot(8) | present | `tomb.dat` msync | *(incr 3)* |

Keys that must iterate in numeric order (`segments`, `indexsegs`, `head`) are stored
big-endian so bbolt's byte-ordered cursor yields ascending id; scalar `meta` values,
which have no ordering requirement, are little-endian. A segment's records are
index-agnostic, so its geometry lives once in `segments`; each index's per-segment
`{gen, state}` lives in `indexsegs`. The `docseg` and `tomb` buckets are **created on
open but not yet written** — they are filled by later increments.

`writeManifestLocked` is the single structural commit point. It is a **full
reconciliation**, not an append: in one `db.Update` it (re)writes `meta`, every live
attr-decl, segment, index config, and `(index, segment)` state, and **deletes** any
bucket key no longer backed by live in-memory state. That is what makes a merge —
which replaces N input segments with M outputs — a single atomic commit: the retired
inputs' `segments` + `indexsegs` keys are deleted in the same txn that writes the new
outputs' keys, so the on-disk segment set is never transiently inconsistent. (Seal
does not call it directly — see below.)

**Seal / merge transaction boundary.** Write the flat data-plane files
(`vectors.dat`, `graph-<name>.dat`) and `fsync` them **first**; then **one** bbolt
write-txn commits the metadata change (new `segments`/`indexsegs` rows, retired-key
deletes, bumped `meta.version`); then delete the retired dirs. A crash *before* the
commit leaves the new flat dirs orphaned; a crash *after* leaves the old dirs
orphaned. Either way the orphan sweep simplifies to **"delete `seg-*` dirs not in
the `segments` bucket"** — bbolt's atomic commit is the single swap point that the
manifest rewrite used to be.

Seal commits the fast part synchronously and defers the slow part:

1. write the head's `vectors`/`slotdoc`/`tomb`/`payload`(`/attr`) as a sealed
   segment and `fsync` (just dumping data — fast);
2. `alloc.Commit()` — flush the idtable so the `string→docId` mappings carried by the
   head bucket are durable in the KV **before** the head bucket that carried them is
   cleared (otherwise a crash between seal and the next lazy idtable commit would lose
   every sealed doc's id mapping);
3. **one `control.db` write-txn** (`commitSealLocked`): reconcile the structural
   buckets (head → a new sealed segment, marked **pending** for every index,
   `meta.version` bumped) **AND clear the `head` bucket**, atomically. The head rows
   move out of the head bucket and into the durable sealed segment in the same commit.

The records are durable after step 3; only the N HNSW graphs build later, in the
background (see [indexing.md](indexing.md)). Order is load-bearing: idtable durable
→ seal commit (segment add + head clear, one atomic txn). Because the segment add and
the head clear are a single commit, there is **no manifest-swap / WAL-reset crash
window** between them — the doc lives in exactly one of {old head, new segment}.

## Recovery and crash windows

`recover()` runs at `Open`:

1. `loadControlManifest` — read the whole control plane from `control.db` in one
   read-txn (`meta` + `attrdecls` + `segments` + `indexes` + `indexsegs`). An empty
   `meta` (never committed) means a fresh / Phase-1 store → head-only rebuild from the
   `head` bucket (the fresh-store signal a missing manifest file used to give);
2. reject a metric mismatch (the on-disk vector form is metric-dependent), load
   `attrDecls`, then mmap each sealed segment;
3. rebuild the global `docId→segId` map from each segment's on-disk `slotdoc.dat`
   over **live** slots;
4. reopen each index's **indexed** graphs from disk (in the single recover goroutine,
   before any builder is spawned, so the `graphs` map has no concurrent writer);
5. `rebuildHead` — read the `head` bucket in `docId` order to reconstruct the
   in-memory head and `idToDoc` (re-driving the allocator per row);
6. resume the **pending** build for every `(index, segment)` the control store marks
   pending;
7. **sweep orphans**: delete any `seg-*` dir not referenced by the `segments` bucket,
   any legacy `manifest` / `manifest.tmp` / `records.wal` file left by the pre-bbolt
   control plane, and any stray `graph-<name>.dat` in a live segment dir for an index
   the control store no longer carries (a crash after a drop's commit but before its
   unlink).

Every change — a Put/Delete head row or a structural snapshot — is a single bbolt
commit, so a crash is always *before* the commit (change not applied) or *after*
(change durable). There is no in-place mutation and no separate log to leave
half-applied. In particular, seal's segment-add and head-clear are **one** commit, so
recovery never finds a doc in both the rebuilt head and a sealed segment (the crash
window the former WAL design needed an explicit skip-already-sealed guard for is now
structurally impossible). A structurally corrupt control record (wrong-length value,
or a head value whose length fields overrun) fails recovery loud rather than
mis-reading — the bbolt analog of the old manifest CRC / WAL torn-tail gate.

## Concurrency and locking

- **`Store.mu` (`sync.RWMutex`)** guards all store state. `Search` holds `RLock` for
  its **entire** traversal; structural changes (seal, the merge swap, create/drop
  index) take the write `Lock`. Because a search reads vectors zero-copy by aliasing a
  segment mmap, and the merge swap that unmaps a segment holds the write lock, a search
  can never alias a freed mmap (see [indexing.md](indexing.md#performance)).
- **`Store.buildMu`** serializes a background builder's graph install + control-store
  commit against other structural commits.
- **Quiescence** is tracked by in-flight build/merge counters + a `sync.Cond`, all
  touched only under `s.mu`. This (rather than `sync.WaitGroup`) is what lets a
  completed build safely re-trigger a merge: a WaitGroup's add-after-wait race is
  structurally impossible when every increment, decrement, and wait happens under the
  same lock. A `closing` flag (set in `Close` under `s.mu`) gates new launches so
  teardown never races a launch.
- A **merge defers** until its input segments are indexed in **every** index
  (re-validated under `buildMu+s.mu` at swap time): unmapping an input while a still-
  pending index's builder is mid-read would be a use-after-free, so the merge waits for
  all N builds first.

## Known gaps / do-not-assume

- Directory fsync is a **no-op on Windows** (no directory-fsync syscall); the POSIX
  crash-durability tests that rely on it are gated to non-Windows.
- `WaitForIndex` drains **all** builds, not a single named index (a reserved gap).
- The store is fsync-heavy by design (a bbolt commit per Put/Delete head row;
  per-file + dir fsync per sealed segment; a bbolt commit per structural change). On
  Windows, where each `FlushFileBuffers` is far more expensive, fsync-heavy tests run
  much slower. Whether the seal/merge paths issue **more** fsyncs than necessary (e.g.
  batching per-segment file syncs, or coalescing the segment-dir and graph-dir
  fsyncs), and whether the per-write head commit should be batchable for bulk loads,
  is an open question — the per-operation fsync count has not yet been audited.
