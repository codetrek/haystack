# Durability, recovery, and concurrency

> Subsystem page under [architecture.md](architecture.md). Owns the head WAL, the
> bbolt control store, the seal commit order, crash windows, recovery, and the
> concurrency/locking model. The bbolt control store is the **structural source of
> truth**.
>
> **Plane split.** The **control plane** — store metadata (version / head / metric),
> the declared attr-index set, the sealed-segment set, and per-`(index, segment)`
> build state — lives in a single embedded bbolt DB (`control.db`); see
> [Control plane on bbolt](#control-plane-on-bbolt). The **data plane**
> (sealed-segment `vectors.dat` + `graph-<name>.dat` and the zero-copy
> `getVectorRef` mmap aliasing) **stays flat mmap** and is never moved into bbolt:
> boxing bulk vectors/graphs in bbolt would destroy the zero-copy hot path and bloat
> the file under long-lived read transactions.
>
> **Migration status.** Increment 1 (this page's as-built state) has migrated the
> **manifest** onto bbolt: every structural change commits a `control.db` write-txn
> instead of rewriting a hand-rolled manifest file. The head record durability
> (`head` bucket), the `docId→segId` map (`docseg` bucket), and tombstones (`tomb`
> bucket) are **later increments** — they are still served by the head WAL,
> recovery rescan of `slotdoc.dat`, and `tomb.dat` respectively.

## Two persistent faces

Durability has two faces with complementary jobs, so the hot path stays cheap:

| | writes | when | guarantee |
|---|---|---|---|
| **head WAL** (`records.wal`) | each Put/Delete | every write | append + fsync before the in-memory mutation → a returned write is durable |
| **control store** (`control.db`) | a snapshot of the whole segment set + index state | each structural change (seal / compact / merge / create / drop / rebuild index) | one bbolt `db.Update` commit (copy-on-write page swap, fsync) is atomic |

The hot path touches only the WAL (append-only); the control store is committed only
on the infrequent structural changes.

## Head WAL

Records are `recPut` (upsert: tombstone the old slot if any, add the new) and
`recDelete`. A put body is prefixed with a 2-byte magic (`{0xF5,0x5A}`) so a
pre-structured-payload or corrupt record is rejected on replay rather than
misparsed. A frame larger than `maxWalPayloadSize` (64 MiB) is rejected at append —
an oversize frame would otherwise look like a torn tail on reopen and silently drop
itself and everything after it. On a failed `Sync`, the log rolls back to the last
durable offset, so a Put whose fsync failed is not silently persisted and
resurrected later.

## Control plane on bbolt

The control plane lives in a single embedded [bbolt](https://pkg.go.dev/go.etcd.io/bbolt)
database, `control.db`, opened beside the flat `seg-*` data dirs. bbolt gives an
ACID, single-file, B+tree KV with copy-on-write page commits, so **one bbolt
write-txn commit = one atomic structural change**. This replaces the former
hand-rolled manifest's `serialize + CRC32 + tmp + fsync + rename + dir-fsync`
rewrite with a plain `db.Update`: a failed or panicking txn rolls back fully —
there is no half-written control state, and there is no separate CRC because bbolt
checksums its own pages.

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
| `head` | docId(8) | vector · norm · payload | `records.wal` | *(incr 2)* |
| `docseg` | docId(8) | segId(8) | recovery rescan of `slotdoc.dat` | *(incr 4/3)* |
| `tomb` | segId(8) ‖ slot(8) | present | `tomb.dat` msync | *(incr 3)* |

Keys that must iterate in numeric order (`segments`, `indexsegs`) are stored
big-endian so bbolt's byte-ordered cursor yields ascending segId; scalar `meta`
values, which have no ordering requirement, are little-endian. A segment's records
are index-agnostic, so its geometry lives once in `segments`; each index's
per-segment `{gen, state}` lives in `indexsegs`. The `head`, `docseg`, and `tomb`
buckets are **created on open but not yet written** — they are filled by later
increments.

`writeManifestLocked` is the single commit point. It is a **full reconciliation**,
not an append: in one `db.Update` it (re)writes `meta`, every live attr-decl,
segment, index config, and `(index, segment)` state, and **deletes** any bucket key
no longer backed by live in-memory state. That is what makes a merge — which
replaces N input segments with M outputs — a single atomic commit: the retired
inputs' `segments` + `indexsegs` keys are deleted in the same txn that writes the
new outputs' keys, so the on-disk segment set is never transiently inconsistent.

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
   WAL are durable in the KV **before** the WAL that carried them is reset (otherwise a
   crash between seal and the next lazy idtable commit would lose every sealed doc's id
   mapping);
3. **one `control.db` write-txn** (head → a new sealed segment, marked **pending** for
   every index, `meta.version` bumped);
4. reset the head WAL.

The records are durable after step 3; only the N HNSW graphs build later, in the
background (see [indexing.md](indexing.md)). Order is load-bearing: idtable durable
→ control commit → WAL truncated.

## Recovery and crash windows

`recover()` runs at `Open`:

1. `loadControlManifest` — read the whole control plane from `control.db` in one
   read-txn (`meta` + `attrdecls` + `segments` + `indexes` + `indexsegs`). An empty
   `meta` (never committed) means a fresh / Phase-1 store → head-only WAL replay (the
   fresh-store signal a missing manifest file used to give);
2. reject a metric mismatch (the on-disk vector form is metric-dependent), load
   `attrDecls`, then mmap each sealed segment;
3. rebuild the global `docId→segId` map from each segment's on-disk `slotdoc.dat`
   over **live** slots;
4. reopen each index's **indexed** graphs from disk (in the single recover goroutine,
   before any builder is spawned, so the `graphs` map has no concurrent writer);
5. replay `records.wal` to reconstruct the in-memory head and `idToDoc`;
6. resume the **pending** build for every `(index, segment)` the control store marks
   pending;
7. **sweep orphans**: delete any `seg-*` dir not referenced by the `segments` bucket,
   any legacy `manifest` / `manifest.tmp` file left by the pre-bbolt control plane,
   and any stray `graph-<name>.dat` in a live segment dir for an index the control
   store no longer carries (a crash after a drop's commit but before its unlink).

Every structural change is "write new files → atomically commit the control store,"
so a crash is always *before* the commit (new files unreferenced → swept) or *after*
(new state live, old files unreferenced → swept). Both converge; there is no in-place
mutation to leave half-applied. A structurally corrupt control record (wrong-length
value) fails recovery loud rather than mis-reading — the bbolt analog of the old
manifest CRC gate.

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
- The store is fsync-heavy by design (WAL per write; per-file + dir fsync per sealed
  segment; a bbolt commit per structural change). On Windows, where each
  `FlushFileBuffers` is far more expensive, fsync-heavy tests run much slower. Whether
  the seal/merge paths issue **more** fsyncs than necessary (e.g. batching per-segment
  file syncs, or coalescing the segment-dir and graph-dir fsyncs) is an open question —
  the per-operation fsync count has not yet been audited.
