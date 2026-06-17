# Durability, recovery, and concurrency

> Subsystem page under [architecture.md](architecture.md). Owns the head WAL, the
> atomic manifest, the seal commit order, crash windows, recovery, and the
> concurrency/locking model. The manifest is the **structural source of truth**.

## Two persistent faces

Durability has two faces with complementary jobs, so the hot path stays cheap:

| | writes | when | guarantee |
|---|---|---|---|
| **head WAL** (`records.wal`) | each Put/Delete | every write | append + fsync before the in-memory mutation → a returned write is durable |
| **manifest** (`manifest`) | a snapshot of the whole segment set + index state | each structural change (seal / compact / merge / create / drop / rebuild index) | `tmp + fsync + rename + dir-fsync` atomic rewrite |

The hot path touches only the WAL (append-only); the manifest is rewritten only on
the infrequent structural changes.

## Head WAL

Records are `recPut` (upsert: tombstone the old slot if any, add the new) and
`recDelete`. A put body is prefixed with a 2-byte magic (`{0xF5,0x5A}`) so a
pre-structured-payload or corrupt record is rejected on replay rather than
misparsed. A frame larger than `maxWalPayloadSize` (64 MiB) is rejected at append —
an oversize frame would otherwise look like a torn tail on reopen and silently drop
itself and everything after it. On a failed `Sync`, the log rolls back to the last
durable offset, so a Put whose fsync failed is not silently persisted and
resurrected later.

## Manifest

The manifest is pure metadata — bytes per segment and per `(index, segment)`, so it
scales with the segment *count*, not the record count. On-disk format (version byte
= 4, CRC32-checked):

```
magic(4) | fmtVer(1) | version(8) | headSegId(8) | metric(1)
nDecls(4)  · [ kind(1) propLen(2) prop ]*                       declared attr indexes
nSeg(4)    · [ segId(8) gen(4) vectorCount(8) tombCount(8) ]*   sealed records-segments
nIdx(4)    · [ nameLen(2) name type(1) metric(1) M(4) efC(4) efS(4) ]*   index configs
nIdxSeg(4) · [ nameLen(2) name segId(8) gen(4) state(1) ]*      per-(index,segment) pending|indexed
crc32(4)
```

A segment's records are index-agnostic, so its geometry lives once in the `nSeg`
block; each index's per-segment `{gen, state}` lives in `nIdxSeg`.

## Seal commit order

Seal commits the fast part synchronously and defers the slow part:

1. write the head's `vectors`/`slotdoc`/`tomb`/`payload`(`/attr`) as a sealed
   segment and `fsync` (just dumping data — fast);
2. `alloc.Commit()` — flush the idtable so the `string→docId` mappings carried by the
   WAL are durable in the KV **before** the WAL that carried them is reset (otherwise a
   crash between seal and the next lazy idtable commit would lose every sealed doc's id
   mapping);
3. atomic manifest rewrite (head → a new sealed segment, marked **pending** for every
   index, plus a fresh empty head);
4. reset the head WAL.

The records are durable after step 3; only the N HNSW graphs build later, in the
background (see [indexing.md](indexing.md)).

## Recovery and crash windows

`recover()` runs at `Open`:

1. load and CRC-check the manifest;
2. load `attrDecls`, then mmap each sealed segment;
3. rebuild the global `docId→segId` map from each segment's on-disk `slotdoc.dat`;
4. reopen each index's **indexed** graphs from disk (in the single recover goroutine,
   before any builder is spawned, so the `graphs` map has no concurrent writer);
5. replay `records.wal` to reconstruct the in-memory head and `idToDoc`;
6. resume the **pending** build for every `(index, segment)` the manifest marks pending;
7. **sweep orphans**: delete any `seg-*` dir not referenced by the manifest, and any
   stray `graph-<name>.dat` in a live segment dir for an index the manifest no longer
   carries (a crash after a drop's manifest commit but before its unlink).

Every structural change is "write new files → atomically swap the manifest," so a
crash is always *before* the swap (new files unreferenced → swept) or *after* (new
state live, old files unreferenced → swept). Both converge; there is no in-place
mutation to leave half-applied.

## Concurrency and locking

- **`Store.mu` (`sync.RWMutex`)** guards all store state. `Search` holds `RLock` for
  its **entire** traversal; structural changes (seal, the merge swap, create/drop
  index) take the write `Lock`. Because a search reads vectors zero-copy by aliasing a
  segment mmap, and the merge swap that unmaps a segment holds the write lock, a search
  can never alias a freed mmap (see [indexing.md](indexing.md#performance)).
- **`Store.buildMu`** serializes a background builder's graph install + manifest
  rewrite against other structural rewrites.
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
  segment; manifest fsync per structural change). On Windows, where each
  `FlushFileBuffers` is far more expensive, fsync-heavy tests run much slower. Whether
  the seal/merge paths issue **more** fsyncs than necessary (e.g. batching per-segment
  file syncs, or coalescing the segment-dir and graph-dir fsyncs) is an open question —
  the per-operation fsync count has not yet been audited.
