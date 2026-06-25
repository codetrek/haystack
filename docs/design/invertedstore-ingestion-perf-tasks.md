# invertedstore — Ingestion-Path Performance: Task Breakdown

> **For agentic workers:** REQUIRED SUB-SKILL: use `superpowers:subagent-driven-development`
> (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking. **AGENTS.md Principle 0 governs:** every task is TDD
> (red → green), each item committed independently, each performance item **re-measured on real
> ext4** (`idxbench`) before its number is reported — no asserted wins.

**Spec:** `docs/design/invertedstore-ingestion-perf-spec.md` (v4, 3-round multi-agent reviewed).

**Goal:** the *best achievable* cold-build wall time for `*invertedstore.Store` — drain everything
reducible off the single mpsc worker and shrink the irreducible apply — measured, not asserted.
Pebble's 61s is a reference line only; realistic landing ~25–32s (review-calibrated).

**Architecture:** all mutations stay on the single mpsc worker (the single-mutator invariant the P9
concurrency model rests on). Two items move *read-only compute* off the worker and *install on* the
worker (A: merge compute; F: spill encode over a detached, immutable head) — never a second MANIFEST
writer. The rest are single-threaded wins (F0 inline dict, head-fix lazy dels) or bounded-memory
guards (E backpressure, F's `maxInflightSpills`).

**Tech stack:** Go (module `./core`, `GOWORK=off go test ./invertedstore/`); `core/cmd/idxbench`
harness; `-race` gates on every concurrency item; `go-cov` TOTAL ≥ 90%.

---

## Sequencing (spec §11) — FREE single-threaded wins first, F last

| Task | Item | Why this order |
|---|---|---|
| 1 | **F0** inline term dict (−9s) | free, zero concurrency; must precede F (F moves the *smaller* residual) |
| 2 | **head-fix / C.0** lazy `dels` (−5–8s) | free, single-threaded |
| 3 | **B** forward docid-range skip (~6s) | free (two int64 in MANIFEST); bump FormatVersion |
| 4 | **A** merge compute off-worker (~30s off-worker) | single mutator preserved; +input refcounts; `-race` |
| 5 | **C.1–C.3 + E** alloc churn + backpressure | measure AFTER A+B; keep only real wins |
| 6 | **G** Open orphan sweep | prerequisite-hygiene for F |
| 7 | **F** residual spill encode off-worker (drain ~17s) | highest risk; head double-buffer + 3 atomic lock sections + `spilling` tier |

Each task ends with an `idxbench` measurement step + a commit. Do **not** start a later task until the
prior task's `-race` (where applicable) and `go-cov` gates are green.

> **Order override (cross-review BLOCKER-2):** implement **E (Task 5's backpressure sub-task) BEFORE
> A (Task 4) and F (Task 7).** A and F add `RunFunc`-driven installs from the merge/encode goroutines
> onto the shared depth-100 mpsc queue; without E's producer backpressure the build feed saturates
> that queue and starves the installs. E bounds the producer first. So the real implementation order
> is: **F0 → head-fix → B → E → A → C.1/C.2/C.3 → G → F.** (The task sections keep their numbers;
> only E moves earlier within the flow.)

---

## File map (what each task touches)

| File | F0 | C.0 | B | A | C | E | G | F |
|---|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|
| `core/invertedstore/segment.go` (segWriter, segment) | ● | | ● | | ● | | | |
| `core/invertedstore/head.go` (headTable, spill) | | ● | ● | | | | | ● |
| `core/invertedstore/manifest.go` (segMeta, FormatVersion) | | | ● | | | | | |
| `core/invertedstore/merge.go` (mergeSegments, installMerge, maybeMerge*) | △ | | ● | ● | ● | | | |
| `core/invertedstore/update.go` (applyBatch) | | | | | ● | ● | | ● |
| `core/invertedstore/store.go` (Store, Open, Options) | | | | | | ● | ● | ● |
| `core/invertedstore/concurrency.go` (snapshot, mergeLoop) | | | | ● | | | | ● |
| `core/invertedstore/reconcile.go` (recomputeLive, forEach…) | | | ● | | | | | |
| `core/invertedstore/dictcache.go` (forwardKeywords) | | | ● | | | | | ● |
| `core/invertedstore/search.go` (Search, GetDocs) | | | | | | | | ● |
| `core/invertedstore/spilling.go` (NEW: spillEntry, read tier) | | | | | | | | ● |
| `core/invertedstore/export_test.go` (test hooks) | ● | ● | ● | ● | ● | ● | ● | ● |

● = production change · △ = deletion only (`writeTermDict` removed) · merge.go's `maybeMerge*` work =
`mergeOneLevel`/`coveringMerge`/`reclaimOrphanTables` KEPT; `maybeMerge`/`maybeCoveringMerge` DELETED
in A; `select{Tiered,Covering}MergePlan`/`runMergePlan`/`segsByIdsLocked` ADDED.

---

## Task 1 — F0: build the term dict INLINE (kill the `writeTermDict` re-read)

**Spec §4a.** `segWriter.finish` calls `writeTermDict` (segment.go:185–229) which re-reads & re-
decompresses every `[I]` data block just to extract keyword strings in ordinal order — strings the
writer already held at `addEntry` time (`key[5:]`). Accumulate the dict region INLINE as each `[I]`
key is added; delete the re-read. Byte-identical output, **−9s spill**, zero concurrency risk.

**Format contract (must stay byte-identical).** The dict region is a sequence of chunks, each
`uvarint(chunkFirst) uvarint(rawLen) uvarint(compLen) comp`, where a chunk's raw bytes are
`(uvarint(len(kw)) kw)*` for `[I]` keys in ascending ordinal order, flushed when raw ≥ `dictChunk`.
The region sits between the last data block and the block index, at footer offset `dictOff`. `[I]`
keys are added before any `[F]` key (`ktInverted` < `ktForward`), so inline accumulation observes
keywords in exact ordinal order — identical to the re-read.

**Files:**
- Modify: `core/invertedstore/segment.go` — `segWriter` struct (lines 52–64), `addEntry`
  (109–128), `finish` (149–178); **delete** `writeTermDict` (180–229).
- Modify: `core/invertedstore/codec.go` — add the `onDecompress` test observer at the top of
  `decompress` (the persistent no-re-read guard; nil in prod).
- Test: `core/invertedstore/segment_inline_dict_test.go` (new).

- [ ] **Step 1 — Write the failing test: a genuine, PERSISTENT behavioral red + byte-identity + round-trip.**

> **Why not a re-read counter (R5 — the workflow caught this):** a hook fired *inside* `writeTermDict`
> is a TAUTOLOGY — once `writeTermDict` is deleted the hook has no call site, so "rereads==0" is true
> by construction and cannot catch a re-introduced re-read. And because F0 is byte-identical, a byte
> oracle passes against BOTH old and new code, so it does not discriminate inline from re-read either.
> The genuine, PERSISTENT discriminator is **"`finish()` decompresses ZERO data blocks"**: the deleted
> `writeTermDict` re-reads + `dataCodec.decompress`es every `[I]` block; the inline build decompresses
> nothing; `openSegment` (called at the end of `finish`) reads only the footer + block index, no
> data-block decompress. Hook `codec.decompress`, count during the `finish()` window, assert 0. This
> fails NOW (old re-read decompresses N blocks) and passes after, AND survives the deletion (any future
> re-read would decompress → caught).

In `codec.go`, add the observer (fired at the top of `func (c *codec) decompress`; nil in prod):

```go
// onDecompress, when non-nil, is invoked at the start of every codec.decompress. Test-only (F0): a
// test counts data-block decompressions DURING finish() — the genuine red→green discriminator (old
// writeTermDict re-reads+decompresses each [I] block; the inline build decompresses none) that a
// byte-identical oracle cannot provide. nil in production (one predictable branch). A test that
// installs it MUST NOT t.Parallel (same constraint as the merge observers).
var onDecompress func()
```

`core/invertedstore/segment_inline_dict_test.go` (new) — the genuine red + an independent byte-identity
oracle + round-trip. The oracle re-derives the expected dict region by scanning the finished segment's
`[I]` blocks (it shares no code with the inline builder), pinning the FORMAT; the decompress-count
test pins the no-re-read BEHAVIOR.

```go
package invertedstore

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

var dictKws = []string{"alpha", "beta", "delta", "gamma", "kappa", "omega", "zeta"}

// writeDictSegment builds a small term-id segment: the 7 sorted [I] keys + one [F] record (which must
// NOT enter the dict). blockTarget 64 forces multiple data blocks so the old re-read decompresses >1.
func writeDictSegment(path string, dictChunk int) *segWriter {
	w := newSegWriter(path, newCodec(codecSnappy), newCodec(codecZstd), 64, 1<<16, 1<<10, true, dictChunk)
	tid := uint32(7)
	for _, kw := range dictKws {
		w.addEntry(invertedKey(tid, kw), encodeInvertedValue([]int64{1}, nil))
	}
	w.addEntry(forwardKey(tid, 1), encodeForward([]uint32{0, 1, 2, 3, 4, 5, 6}))
	return w
}

// rereadDictRegion independently reconstructs the expected term-dict region bytes by scanning the
// segment's own [I] data blocks in order — the SAME bytes finish() must produce inline. Oracle: it
// shares no code with the inline builder under test.
func rereadDictRegion(t *testing.T, s *segment, dictChunk int, dict *codec) []byte {
	t.Helper()
	var region, chunk []byte
	var ord, chunkFirst uint32
	flush := func() {
		if len(chunk) == 0 {
			return
		}
		comp := dict.compress(chunk)
		region = appendUvarint(region, uint64(chunkFirst))
		region = appendUvarint(region, uint64(len(chunk)))
		region = appendUvarint(region, uint64(len(comp)))
		region = append(region, comp...)
		chunk = chunk[:0]
	}
	for i := range s.idx {
		scanBlock(s.blockBytes(i), func(key, _ []byte, _ int64, _ int, _ bool) bool {
			if key[0] != ktInverted {
				return true
			}
			if len(chunk) == 0 {
				chunkFirst = ord
			}
			kw := key[5:]
			chunk = appendUvarint(chunk, uint64(len(kw)))
			chunk = append(chunk, kw...)
			ord++
			if len(chunk) >= dictChunk {
				flush()
			}
			return true
		})
	}
	flush()
	return region
}

// THE GENUINE RED: finish() must decompress zero data blocks (no re-read). Fails before F0 (the
// re-read decompresses every [I] block), passes after, and persists (a re-introduced re-read decompresses).
func TestInlineDict_FinishDecompressesNoDataBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seg-000001.dat")
	w := writeDictSegment(path, 8)
	var decompresses int
	onDecompress = func() { decompresses++ }
	t.Cleanup(func() { onDecompress = nil })
	seg := w.finish(path)
	onDecompress = nil // stop before any read-path decompress
	defer seg.close()
	if decompresses != 0 {
		t.Fatalf("finish() decompressed %d data blocks (re-read path); the inline dict build must decompress 0", decompresses)
	}
}

// Correctness net: the on-disk dict region byte-equals the independent oracle, and every ordinal
// round-trips to its keyword. (Passes against both old + new code — it pins format, not behavior.)
func TestInlineDict_RegionByteIdenticalToReread(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seg-000001.dat")
	dictChunk := 8
	w := writeDictSegment(path, dictChunk)
	seg := w.finish(path)
	defer seg.close()

	want := rereadDictRegion(t, seg, dictChunk, seg.dictCodec)
	got := make([]byte, seg.biOff-seg.dictOff)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	mustReadAt(f, got, seg.dictOff)
	if !bytes.Equal(got, want) {
		t.Fatalf("inline dict region (%d B) != oracle (%d B)", len(got), len(want))
	}
	res := seg.resolveOrds(map[uint32]struct{}{0: {}, 1: {}, 2: {}, 3: {}, 4: {}, 5: {}, 6: {}})
	for i, kw := range dictKws {
		if res[uint32(i)] != kw {
			t.Fatalf("ord %d resolved %q, want %q", i, res[uint32(i)], kw)
		}
	}
}
```

- [ ] **Step 2 — Run; verify the genuine red fails.**

First wire the observer into `codec.go` `decompress` (it stays permanently — it is the persistent
guard): `func (c *codec) decompress(src []byte, rawLen int) []byte { if onDecompress != nil { onDecompress() }; … }`.
Run: `cd core && GOWORK=off go test ./invertedstore/ -run TestInlineDict_FinishDecompressesNoDataBlocks -v`
Expected: **FAIL** at `decompresses != 0` — today `finish` → `writeTermDict` decompresses every `[I]`
data block to re-extract the keywords. (`TestInlineDict_RegionByteIdenticalToReread` passes already —
it is the format net, not the red.)

- [ ] **Step 3 — Implement inline accumulation; delete `writeTermDict`.**

In `segment.go`, add to `segWriter` (after `blkHave bool`):

```go
	// inline term-dict accumulation (F0): built as [I] keys are added, written at finish — no
	// re-read of own blocks. dictRaw is the current chunk; dictRegion is the compressed chunks so far.
	dictRaw        []byte
	dictRegion     []byte
	dictOrd        uint32
	dictChunkFirst uint32
```

In `addEntry`, append after the `if len(w.blkRaw) >= w.blockTarget { w.flushBlock() }` line:

```go
	if w.termid && key[0] == ktInverted {
		if len(w.dictRaw) == 0 {
			w.dictChunkFirst = w.dictOrd
		}
		kw := key[5:] // keyType(1) + tableId(4 BE) then keyword
		w.dictRaw = appendUvarint(w.dictRaw, uint64(len(kw)))
		w.dictRaw = append(w.dictRaw, kw...)
		w.dictOrd++
		if len(w.dictRaw) >= w.dictChunk {
			w.flushDictChunk()
		}
	}
```

Add `flushDictChunk` (mirrors the deleted `writeTermDict`'s `flush`, into `dictRegion`):

```go
// flushDictChunk compresses the current inline dict chunk and appends it to dictRegion (the same
// uvarint(chunkFirst) uvarint(rawLen) uvarint(compLen) comp layout writeTermDict produced).
func (w *segWriter) flushDictChunk() {
	if len(w.dictRaw) == 0 {
		return
	}
	comp := w.dictCodec.compress(w.dictRaw)
	w.dictRegion = appendUvarint(w.dictRegion, uint64(w.dictChunkFirst))
	w.dictRegion = appendUvarint(w.dictRegion, uint64(len(w.dictRaw)))
	w.dictRegion = appendUvarint(w.dictRegion, uint64(len(comp)))
	w.dictRegion = append(w.dictRegion, comp...)
	w.dictRaw = w.dictRaw[:0]
}
```

Rewrite `finish`'s term-dict block (replace lines 152–156) so `dictOff = w.off` is set EVEN for an
empty region (byte-identical to today's forward-only segment, where `writeTermDict` left
`dictOff == biOff`, footer `dictOff` > 0):

```go
	var dictOff int64
	if w.termid {
		w.flushDictChunk() // flush the final partial chunk
		dictOff = w.off    // == biOff when the region is empty (forward-only segment), as before
		w.bw.Write(w.dictRegion)
		w.off += int64(len(w.dictRegion))
	}
```

**Delete** `writeTermDict` (segment.go:180–229) entirely; it has no other caller.

- [ ] **Step 4 — Run the byte-identity + round-trip test; then the full suite.**

Run: `cd core && GOWORK=off go test ./invertedstore/ -run TestInlineDict -v` → **PASS**.
Run: `cd core && GOWORK=off go test ./invertedstore/` → all existing differential / term-id /
merge-robustness / crash-recovery tests green (the dict bytes are unchanged, so every reader path
that resolves ordinals — `forwardKeywords`, merge remap — is unaffected).

- [ ] **Step 5 — Measure on real ext4, then commit.**

Run (idxbench REQUIRES `-impl -tokens -data`, cross-review M1 — `-data` MUST be a real ext4 dir, not
tmpfs, Principle 2):

```
cd core && go build ./cmd/idxbench && \
  ./idxbench -impl=store -batch=1 -tokens=<lx.gob> -data=/workspace/idxbench-store
```

(Every later "`idxbench` as in Task 1 Step 5" carries the SAME `-tokens=<lx.gob> -data=/workspace/...`
flags; vary `-data` per run, add `-buildprofile`/`-memprofile`/`-automerge` where a step calls for
them.) Record the spill time and total build vs the 95s baseline; expect **~−9s** on spill. Append the
measured numbers to the commit body (no asserted number in code).

```bash
git add core/invertedstore/segment.go core/invertedstore/segment_inline_dict_test.go
git commit -m "perf(invertedstore): build term dict inline, drop writeTermDict re-read (F0)"
```

---

## Task 2 — head-fix (C.0): lazy `dels` map + skip the per-add `delete`

**Spec §5.0.** `addPosting` (head.go:38–50) allocates a `*postingDelta` with TWO non-nil
`map[int64]struct{}` per first-seen keyword, but on a cold build `dels` is ALWAYS empty (no deletes)
→ millions of wasted empty-map allocations; and every add runs `delete(pd.dels, docid)` hashing into
that empty map. Allocate both sets lazily; skip the cross-delete when the other set is nil.
**−5–8s** off the addPosting cost, single-threaded, behavior-identical (a nil set == an empty set).

**Invariant preserved:** `h.bytes` accounting is UNCHANGED (`+len(kw)+16` at creation, `+4` per new
docid), so spill cadence / CapBytes crossing / resulting segment bytes are identical — only the
internal allocation changes.

**Files:**
- Modify: `core/invertedstore/head.go` — `addPosting` (38–50), `tombstonePosting` (54–66); add a
  shared `posting` helper.
- Modify: `core/invertedstore/export_test.go` — a `dels == nil` peek accessor.
- Test: `core/invertedstore/head_lazy_dels_test.go` (new).

- [ ] **Step 1 — Write the failing tests.**

The tests inspect a local `headTable` directly (no Store accessor needed).
`core/invertedstore/head_lazy_dels_test.go`:

```go
package invertedstore

import (
	"reflect"
	"testing"
)

func TestHeadFix_DelsLazyOnAddsOnly(t *testing.T) {
	h := newHeadTable()
	h.addPosting("alpha", 1)
	h.addPosting("alpha", 2)
	pd := h.inv["alpha"]
	if pd.dels != nil {
		t.Fatalf("dels allocated on an adds-only keyword; want nil (lazy)")
	}
	if !reflect.DeepEqual(setToSlice(pd.adds), []int64{1, 2}) && len(pd.adds) != 2 {
		t.Fatalf("adds = %v, want {1,2}", pd.adds)
	}
}

// add -> tombstone -> re-add on the same (kw,docid) must collapse to the survivor (PRESENT), exactly
// as the eager-map version did, exercising the nil->alloc transition both ways.
func TestHeadFix_AddDelReaddResolves(t *testing.T) {
	h := newHeadTable()
	h.addPosting("k", 5)       // adds={5}, dels=nil
	h.tombstonePosting("k", 5) // adds={}, dels={5}
	h.addPosting("k", 5)       // adds={5}, dels={}
	pd := h.inv["k"]
	if _, ok := pd.adds[5]; !ok {
		t.Fatalf("docid 5 should be a live add after add/del/re-add")
	}
	if _, ok := pd.dels[5]; ok {
		t.Fatalf("docid 5 should NOT be tombstoned after the final re-add")
	}
	// tombstone-first path allocates adds lazily and stays correct.
	h.tombstonePosting("t", 9) // adds=nil, dels={9}
	if h.inv["t"].adds != nil {
		t.Fatalf("adds allocated on a tombstone-only keyword; want nil (lazy)")
	}
}
```

- [ ] **Step 2 — Run; verify it fails.**

Run: `cd core && GOWORK=off go test ./invertedstore/ -run TestHeadFix -v`
Expected: **FAIL** on `TestHeadFix_DelsLazyOnAddsOnly` — today `addPosting` eagerly allocates `dels`.

- [ ] **Step 3 — Implement lazy sets.**

Replace `addPosting` and `tombstonePosting` (head.go:37–66) with:

```go
// posting returns keyword's postingDelta, creating an empty one (both sets nil/lazy) on first sight
// and charging the same logical byte estimate the eager version did (so spill cadence is unchanged).
func (h *headTable) posting(keyword string) *postingDelta {
	pd := h.inv[keyword]
	if pd == nil {
		pd = &postingDelta{}
		h.inv[keyword] = pd
		h.bytes += int64(len(keyword)) + 16
	}
	return pd
}

// addPosting records that docid is a member of keyword (latest action wins, in-memory dedup). The
// del-set is allocated lazily (nil on a cold build), so the cross-delete is skipped when dels==nil.
func (h *headTable) addPosting(keyword string, docid int64) {
	pd := h.posting(keyword)
	if pd.dels != nil {
		delete(pd.dels, docid) // latest action wins: a re-add cancels a pending tombstone
	}
	if pd.adds == nil {
		pd.adds = make(map[int64]struct{})
	}
	if _, ok := pd.adds[docid]; !ok {
		pd.adds[docid] = struct{}{}
		h.bytes += 4
	}
}

// tombstonePosting records that docid is removed from keyword (latest action wins). Symmetric to
// addPosting: the add-set is consulted only if allocated.
func (h *headTable) tombstonePosting(keyword string, docid int64) {
	pd := h.posting(keyword)
	if pd.adds != nil {
		delete(pd.adds, docid) // latest action wins: a delete cancels a pending add
	}
	if pd.dels == nil {
		pd.dels = make(map[int64]struct{})
	}
	if _, ok := pd.dels[docid]; !ok {
		pd.dels[docid] = struct{}{}
		h.bytes += 4
	}
}
```

Update the `postingDelta` doc comment (head.go:9–16) to note both sets are lazily allocated.

- [ ] **Step 4 — Run the tests; then the full suite.**

Run: `cd core && GOWORK=off go test ./invertedstore/ -run TestHeadFix -v` → **PASS**.
Run: `cd core && GOWORK=off go test ./invertedstore/` → green (spill reads via `setToSlice`, which
already handles a nil map as empty; segment bytes unchanged).

- [ ] **Step 5 — Measure, then commit.**

Run `idxbench` as in Task 1 Step 5; record the addPosting/total delta (expect **−5–8s**). Confirm the
build `hits` are still `2,414,505` (the differential suite already asserts this).

```bash
git add core/invertedstore/head.go core/invertedstore/export_test.go core/invertedstore/head_lazy_dels_test.go
git commit -m "perf(invertedstore): lazily allocate head del-set, skip empty-map delete (C.0)"
```

---

## Task 3 — B: per-segment `[minDocid,maxDocid]` forward-read skip

**Spec §4.** `forwardKeywords` loops every sealed segment calling `lookupForward`, which decompresses
one block per segment. On a cold build of monotonic new docids the lookup always MISSES but still
decompresses → O(docs × segments). Add a persisted `[MinDocid,MaxDocid]` per segment, set from the
EMITTED forward records (live + tombstone); skip a segment whose range can't contain the docid. A new
high docid then probes ZERO segments. ~6s; bounds forward-read as K grows.

**Correctness pillars (from the spec):**
- Range covers BOTH live forwards AND forward-tombstones (else a skipped segment could hide a
  deletion). An empty `[F]` output keeps an **empty range** (`min > max`) that always skips.
- `noteForwardRead` fires on the **first real probe**, not before the loop — a fully-skipped read
  touches no I/O and must not count (same spirit as `len(segs)==0`).
- **Legacy `[0,0]` hazard (spec §11.3):** a manifest written before this change has no range fields →
  JSON unmarshals to `[0,0]`, a VALID-looking range that would mis-skip every docid ≠ 0. Bump
  `FormatVersion` 2→3 and, on Open of a `< 3` manifest, recompute every segment's range from its
  `[F]` records and rewrite at v3 — so a stale `[0,0]` can never reach `forwardKeywords`.

**Files:**
- Modify: `core/invertedstore/manifest.go` — `segMeta` (+`MinDocid`,`MaxDocid`), `newManifest`
  (FormatVersion 2→3).
- Modify: `core/invertedstore/segment.go` — `segment` struct (+`minDocid`,`maxDocid`).
- Modify: `core/invertedstore/head.go` — `spill` sets the range on its segMeta + segment.
- Modify: `core/invertedstore/merge.go` — `mergeSegments` tracks the emitted-forward docid span.
- Modify: `core/invertedstore/dictcache.go` — `forwardKeywords` skip + lazy `noteForwardRead` + probe hook.
- Modify: `core/invertedstore/store.go` — `Store.onForwardProbe`; Open copies the range + legacy upgrade.
- Test: `core/invertedstore/forward_skip_test.go` (new).

- [ ] **Step 1 — segMeta + segment fields + the empty-range helper (compile-only red).**

`manifest.go`, add to `segMeta` after `Postings`:

```go
	// MinDocid/MaxDocid bound the docids of the forward records (live AND tombstone) this segment
	// emitted — the forward-read skip range (spec §4 item B). A read for a docid outside [Min,Max]
	// cannot find a forward record here, so forwardKeywords skips the segment without decompressing a
	// block. An empty forward output is the inverted range Min=MaxInt64 > Max=MinInt64, which always
	// skips. Persisted so Open needs no scan; FormatVersion 3 guarantees the fields are present (a
	// pre-3 manifest is upgraded on Open — a stale [0,0] would mis-skip).
	MinDocid int64 `json:"minDocid"`
	MaxDocid int64 `json:"maxDocid"`
```

Bump `newManifest`: `FormatVersion: 2` → `FormatVersion: 3`.

`segment.go`, add to the `segment` struct (after `path string`):

```go
	minDocid, maxDocid int64 // forward-record docid span (B); set from segMeta on Open / at seal
```

`keys.go` (or segment.go), add the helper:

```go
// emptyDocidRange is the inverted "no forward records" span: min > max, so coversDocid is always
// false and forwardKeywords always skips the segment (spec §4 item B).
func emptyDocidRange() (min, max int64) { return math.MaxInt64, math.MinInt64 }

// coversDocid reports whether a forward record for docid could exist in this segment.
func (s *segment) coversDocid(docid int64) bool { return docid >= s.minDocid && docid <= s.maxDocid }
```

(Add `"math"` to the imports of whichever file hosts `emptyDocidRange`.)

- [ ] **Step 2 — `spill` sets the range.**

In `head.go` `spill`, the forward records are built into `recs` and sorted ascending by docid
(lines 138–145). After the sort, compute the span (covers live + tombstone, which `recs` already
unions) and thread it into the segMeta + the opened segment:

```go
	// B: the forward-read skip range covers every EMITTED forward record (live + tombstone). recs is
	// sorted ascending by docid, so the span is its ends; an empty recs keeps the always-skip range.
	minD, maxD := emptyDocidRange()
	if len(recs) > 0 {
		minD, maxD = recs[0].docid, recs[len(recs)-1].docid
	}
```

Set them on the segment + segMeta where the others are set (lines 160–173):

```go
	seg := w.finish(path)
	seg.id = segId
	seg.minDocid, seg.maxDocid = minD, maxD // B
	seg.refs.Store(1)
	...
	sm := segMeta{
		Id: segId, Level: 0, DataCodec: s.opts.DataCodecL0, DictCodec: s.opts.DictCodec,
		MinTable: tid, MaxTable: tid, Size: size, Postings: postings,
		MinDocid: minD, MaxDocid: maxD, // B
	}
```

- [ ] **Step 3 — `mergeSegments` tracks the emitted-forward docid span.**

In `merge.go` `mergeSegments`, add trackers next to `postings` (line 161):

```go
	outMinDocid, outMaxDocid := emptyDocidRange()
	noteDocid := func(d int64) {
		if d < outMinDocid {
			outMinDocid = d
		}
		if d > outMaxDocid {
			outMaxDocid = d
		}
	}
```

Call `noteDocid(int64(binary.BigEndian.Uint64(min[5:13])))` immediately after EACH forward
`w.addEntry(min, …)` that actually emits — both the tombstone carry-through (line 214) and the live
`encodeForward(out)` (line 245). (A covering merge that drops a forward emits nothing → not noted,
correctly shrinking the range.) Then set the segMeta (lines 316–325):

```go
	sm := segMeta{
		Id: outId, Level: level, DataCodec: dataCodec, DictCodec: s.opts.DictCodec,
		MinTable: minTable, MaxTable: maxTable, Size: size, Postings: postings,
		MinDocid: outMinDocid, MaxDocid: outMaxDocid, // B
	}
```

And set the opened segment's in-memory range before `return` (after `seg := w.finish(path); seg.id = outId`):

```go
	seg.minDocid, seg.maxDocid = outMinDocid, outMaxDocid // B
```

(`installMerge` publishes `res.seg`, which now already carries its range.)

- [ ] **Step 4 — Write the failing probe-count test.**

Add the probe hook to `store.go` `Store` (next to `onForwardRead`):

```go
	// onForwardProbe, if non-nil, fires once per segment forwardKeywords actually PROBES (decompresses
	// a block via lookupForward) — i.e. NOT for a range-skipped segment. Test-only (B): asserts a
	// cold-build read skips every sealed segment. Set/read only on the worker.
	onForwardProbe func()
```

```go
func (s *Store) noteForwardProbe() {
	if s.onForwardProbe != nil {
		s.onForwardProbe()
	}
}
```

Add an installer to `export_test.go`:

```go
// installForwardProbeCounter counts segment forward PROBES (non-skipped lookupForward calls). The
// hook runs on the worker; the atomic keeps it -race clean. Cleared on cleanup.
func (s *Store) installForwardProbeCounter(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	s.onForwardProbe = func() { n.Add(1) }
	t.Cleanup(func() { s.onForwardProbe = nil })
	return &n
}

// forwardKeywordsForTest runs forwardKeywords on the worker (synchronous), so a test can drive the
// "read old keyword set" path directly and observe the probe counter.
func (s *Store) forwardKeywordsForTest(tableId int, docid int64) (words []string, deleted bool) {
	s.q.RunFunc(func() error {
		words, deleted = s.forwardKeywords(tableId, docid)
		return nil
	})
	return
}
```

`core/invertedstore/forward_skip_test.go`:

```go
package invertedstore

import (
	"testing"

	"github.com/codetrek/haystack/core/queue"
)

// newForwardSkipStore mirrors newMergeStore: a started queue + Open + one table (AutoMerge off).
func newForwardSkipStore(t *testing.T, opts Options) (*Store, int) {
	t.Helper()
	q := queue.NewMpsc("fwdskip")
	q.Start()
	s, err := Open(t.TempDir(), q, opts)
	if err != nil {
		t.Fatal(err)
	}
	tid, err := s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}
	return s, tid
}

// Three sealed segments with DISJOINT ascending docid ranges (one table). A docid above all ranges
// probes 0 segments; an in-range docid probes only the covering segment.
func TestForwardSkip_ProbesOnlyCoveringSegment(t *testing.T) {
	s, tid := newForwardSkipStore(t, Options{CapBytes: 1 << 20})
	// Seal three segments: docids [1..3], [10..12], [20..22].
	for _, base := range []int64{1, 10, 20} {
		for d := base; d < base+3; d++ {
			s.applyForTest(tid, d, []string{uniqWord(int(d))})
		}
		s.spillForTest(tid)
	}
	if got := len(s.SegmentsForTest()); got != 3 {
		t.Fatalf("want 3 segments, got %d", got)
	}

	probes := s.installForwardProbeCounter(t)

	// A brand-new high docid (cold-build shape) is above every range → 0 probes.
	probes.Store(0)
	s.forwardKeywordsForTest(tid, 999)
	if n := probes.Load(); n != 0 {
		t.Fatalf("new high docid probed %d segments, want 0 (all range-skipped)", n)
	}

	// An in-range docid (11) probes ONLY the [10..12] segment → exactly 1 probe.
	probes.Store(0)
	words, _ := s.forwardKeywordsForTest(tid, 11)
	if n := probes.Load(); n != 1 {
		t.Fatalf("in-range docid probed %d segments, want 1", n)
	}
	if len(words) != 1 || words[0] != uniqWord(11) {
		t.Fatalf("forward for docid 11 = %v, want [%s]", words, uniqWord(11))
	}
}

// An [I]-present, [F]-absent segment (a head that only added postings via the test stub never sets a
// forward — but for the real path: a spill of only deletes emits forward-tombstones; here assert the
// empty-range case always-skips). Build a segment with NO forward records and confirm it is skipped.
func TestForwardSkip_EmptyForwardRangeAlwaysSkips(t *testing.T) {
	s, tid := newForwardSkipStore(t, Options{CapBytes: 1 << 20})
	// addPosting without setForward → [I] present, [F] absent (exercised via a worker task).
	s.q.RunFunc(func() error {
		s.mu.Lock()
		h := newHeadTable()
		h.addPosting("orphanKw", 7)
		s.head[tid] = h
		s.mu.Unlock()
		return s.spill(tid)
	})
	sm := s.SegmentsForTest()
	if len(sm) != 1 || sm[0].MinDocid <= sm[0].MaxDocid {
		t.Fatalf("forward-absent segment should have an empty (min>max) range, got %+v", sm)
	}
	probes := s.installForwardProbeCounter(t)
	s.forwardKeywordsForTest(tid, 7)
	if n := probes.Load(); n != 0 {
		t.Fatalf("empty-range segment probed %d times, want 0", n)
	}
}
```

- [ ] **Step 5 — Run; verify it fails.**

Run: `cd core && GOWORK=off go test ./invertedstore/ -run TestForwardSkip -v`
Expected: **FAIL** — `forwardKeywords` does not yet skip; it probes all 3 segments (and fires no
probe hook). Both assertions fail.

- [ ] **Step 6 — Implement the skip + lazy `noteForwardRead` in `forwardKeywords`.**

In `dictcache.go` `forwardKeywords`, replace the segment-scan tail (lines 199–235). Remove the
unconditional `s.noteForwardRead()` (line 202) and make it lazy on the first real probe:

```go
	if len(segs) == 0 {
		return nil, false
	}

	tid := uint32(tableId)
	probed := false
	for i := len(segs) - 1; i >= 0; i-- { // newest wins
		seg := segs[i]
		if !seg.coversDocid(docid) {
			continue // B: no forward record for docid can exist in this segment — skip, no I/O
		}
		if !probed {
			s.noteForwardRead() // first segment we actually touch = the first real forward read
			probed = true
		}
		s.noteForwardProbe()
		val, ok := seg.lookupForward(forwardKey(tid, docid))
		if !ok {
			continue
		}
		ords, del := decodeForward(val)
		if del {
			return nil, true
		}
		// ... (the existing resolveOrdsCached + out-building block, unchanged) ...
	}
	return nil, false
```

(Keep the existing `need`/`resolveOrdsCached`/panic-on-unresolvable block verbatim inside the loop.)

- [ ] **Step 7 — Open: copy the range from segMeta; upgrade a pre-v3 manifest.**

In `store.go` `Open`, set the in-memory range when opening each segment (line 167–172 loop):

```go
		seg.minDocid, seg.maxDocid = sm.MinDocid, sm.MaxDocid // B
```

After the segment-open loop, BEFORE `publishSnapshotLocked`, add the legacy upgrade:

```go
	if man.FormatVersion < 3 {
		// Pre-B manifests have no docid range (unmarshals to [0,0], which would mis-skip every docid
		// != 0). Recompute each segment's range from its forward records, then persist at v3 so the
		// stale range can never reach forwardKeywords.
		if err := s.upgradeSegmentRanges(); err != nil {
			return nil, err
		}
	}
```

Add `upgradeSegmentRanges` to `reconcile.go` (it reuses the `[F]` scan machinery; runs single-
threaded on Open, no concurrent readers):

```go
// upgradeSegmentRanges recomputes every live segment's [minDocid,maxDocid] from its forward records
// (live AND tombstone) and rewrites the MANIFEST at FormatVersion 3. One-time legacy migration for
// the forward-skip range (B): a pre-3 manifest lacks the fields, so a stale [0,0] would mis-skip.
// Open-only (no snapshot refcount, no concurrent writers).
func (s *Store) upgradeSegmentRanges() error {
	for i := range s.segs {
		seg := s.segs[i]
		minD, maxD := emptyDocidRange()
		lo := []byte{ktForward}
		hi := prefixUpper(lo)
		seg.scanPrefix(lo, hi, func(key, _ []byte) {
			d := int64(binary.BigEndian.Uint64(key[5:13]))
			if d < minD {
				minD = d
			}
			if d > maxD {
				maxD = d
			}
		})
		seg.minDocid, seg.maxDocid = minD, maxD
		for j := range s.man.Segments {
			if s.man.Segments[j].Id == seg.id {
				s.man.Segments[j].MinDocid, s.man.Segments[j].MaxDocid = minD, maxD
			}
		}
	}
	s.man.FormatVersion = 3
	return writeManifest(s.dir, s.man)
}
```

> Note: `scanPrefix(lo=[ktForward], hi)` walks ALL tables' `[F]` records in the segment (the range is
> table-agnostic, spec §4), so the recomputed span matches what merge/spill emit. Add a focused test
> `TestForwardSkip_LegacyManifestUpgrade`: write a segment + hand-craft a `FormatVersion:2` MANIFEST
> with `MinDocid:0,MaxDocid:0`, Open, assert the range is corrected and `FormatVersion==3`, and a
> read for an in-range docid still resolves.

- [ ] **Step 8 — Run B tests + reconcile existing forward-read assertions + full suite.**

Run: `cd core && GOWORK=off go test ./invertedstore/ -run TestForwardSkip -v` → **PASS**.
Run: `cd core && GOWORK=off go test ./invertedstore/` → green. **Both existing `onForwardRead` tests
stay green AS-IS — do NOT relax them** (cross-review verified): `TestUpdate_ColdBuildNoForwardRead`
(update_test.go:245) has no sealed segments on the counted read (already expects 0), and
`TestUpdate_WarmEditTakesForwardRead` (update_test.go:270) hits the **head** forward (fires
`noteForwardRead` at the head tier, which B does not touch). B only changes the SEGMENT probe path,
so neither needs editing; the only new coverage is the probe-count test above. Differential /
crash-recovery suites must stay green.

- [ ] **Step 9 — Measure, then commit.**

`idxbench` as before; record the forwardKeywords delta (expect **~−6s**) and confirm `hits` unchanged.

```bash
git add core/invertedstore/manifest.go core/invertedstore/segment.go core/invertedstore/head.go \
        core/invertedstore/merge.go core/invertedstore/dictcache.go core/invertedstore/store.go \
        core/invertedstore/reconcile.go core/invertedstore/export_test.go \
        core/invertedstore/forward_skip_test.go
git commit -m "perf(invertedstore): skip forward reads by per-segment docid range (B), FormatVersion 3"
```

---

## Task 4 — A: merge COMPUTE off the worker, install ON the worker

**Spec §3 + §8.** `mergeSegments` (~34s: decompress inputs + zstd-recompress output) mutates ZERO
shared state — it reads refcounted inputs and writes a NEW file at a reserved id. Move ONLY that
compute off the worker; keep `installMerge` (the ms swap) on the worker → exactly one MANIFEST writer,
single-mutator invariant preserved. Add real input refcounts (`segsByIds` returns raw handles today).

**Design — two paths, sharing `mergeSegments`/`installMerge`:**
- **Worker-synchronous (UNCHANGED behavior):** `mergeOneLevel`/`maybeMerge`/`coveringMerge` stay
  worker-synchronous (compute+install both on the worker). They back the test seams
  (`mergeOneLevelForTest`/`coveringMergeForTest` — 8 test files), `reclaimOrphanTables` (Open-time),
  and the Close drain. **Do not change their semantics.** (Refactor only to share a selection helper.)
- **Off-worker (NEW, the hot build path):** `runScheduledMerge` (on the merge goroutine) drives each
  pass as *plan (worker) → compute (off-worker) → install (worker)*. This is the only path that
  changes where `mergeSegments` runs.

**Refcount lifecycle (spec §8 — the required ADDITION):** the plan increfs each input under `s.mu`
(`segsByIdsLocked`); `installMerge` retires them (drops the published ref); `runMergePlan` then
`releaseSnapshot`s the plan's refs AFTER install. So an input file is unlinked only after both the
compute finished AND every in-flight reader released — never mid-read.

**Files:**
- Modify: `core/invertedstore/merge.go` — extract `pickLowestQualifyingLevelLocked`; add
  `segsByIdsLocked`, `mergePlan`, `selectTieredMergePlan`, `selectCoveringMergePlan`, `runMergePlan`,
  `deadFractionLocked`; refactor `mergeOneLevel`/`coveringMerge`/`deadFraction` to use the shared
  helpers (behavior-identical).
- Modify: `core/invertedstore/concurrency.go` — rewrite `runScheduledMerge` to the plan/compute/install
  driver; add the off-worker test hook.
- Test: `core/invertedstore/merge_offworker_test.go` (new).

- [ ] **Step 1 — Faithful refactor: extract the shared selection + dead-fraction helpers (no behavior change).**

`merge.go`. Add the lock-free selection helper and refactor `mergeOneLevel` onto it:

```go
// pickLowestQualifyingLevelLocked returns the lowest level with >= Fanout live segments + its metas
// (oldest->newest), ok=false if none qualifies. Caller holds s.mu (R or W) — no lock taken here.
func (s *Store) pickLowestQualifyingLevelLocked() (level int, metas []segMeta, ok bool) {
	byLevel := map[int][]segMeta{}
	maxL := 0
	for _, sm := range s.man.Segments {
		byLevel[sm.Level] = append(byLevel[sm.Level], sm)
		if sm.Level > maxL {
			maxL = sm.Level
		}
	}
	for l := 0; l <= maxL; l++ {
		if len(byLevel[l]) >= s.opts.Fanout {
			m := byLevel[l]
			sortSegMetasById(m)
			return l, m, true
		}
	}
	return 0, nil, false
}
```

Rewrite `mergeOneLevel` (lines 491–523) to use it — same effect as today:

```go
func (s *Store) mergeOneLevel() (bool, error) {
	s.mu.RLock()
	level, metas, ok := s.pickLowestQualifyingLevelLocked()
	s.mu.RUnlock()
	if !ok {
		return false, nil
	}
	inputIds := map[uint64]bool{}
	for _, m := range metas {
		inputIds[m.Id] = true
	}
	segs := s.segsByIds(inputIds) // raw handles; safe — the whole sync merge is one worker task
	outId := s.nextSegId()
	res := s.mergeSegments(segs, outId, level+1, s.opts.DataCodecMerged, false, nil)
	return true, s.installMerge(inputIds, res)
}
```

Split `deadFraction` (lines 551–572) into a locked core (so the plan can call it while holding `s.mu`):

```go
func (s *Store) deadFraction() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.deadFractionLocked()
}

// deadFractionLocked is deadFraction's body; caller holds s.mu (R or W).
func (s *Store) deadFractionLocked() float64 {
	var written int64
	for _, sm := range s.man.Segments {
		written += sm.Postings
	}
	var live int64
	for t, n := range s.liveByTable {
		if _, ok := s.man.Tables[t]; ok {
			live += n
		}
	}
	if written <= 0 {
		return 0
	}
	d := 1 - float64(live)/float64(written)
	if d < 0 {
		d = 0
	}
	return d
}
```

Run `cd core && GOWORK=off go test ./invertedstore/` now — the full merge/trigger/differential
suite must stay GREEN (pure refactor; this is the regression gate before adding the off-worker path).

- [ ] **Step 2 — Write the failing off-worker test (compute does NOT block the worker; hits identical).**

Add an off-worker compute hook to `merge.go` (package global, nil in prod, like the other observers):

```go
// mergeComputeBlock, when non-nil, is invoked at the START of mergeSegments (the off-worker compute).
// Test-only (A): a test installs one that blocks on a channel, kicks a background merge, and asserts
// the worker still drains an Update while the compute is parked — proving the compute is OFF the
// worker. nil in production. Same no-t.Parallel constraint as the other merge observers.
var mergeComputeBlock func()
```

Call it at the very top of `mergeSegments` (after `curs := …`, before the merge loop):

```go
	if mergeComputeBlock != nil {
		mergeComputeBlock()
	}
```

`core/invertedstore/merge_offworker_test.go`:

```go
package invertedstore

import (
	"testing"
	"time"

	"github.com/codetrek/haystack/core/queue"
)

// With the merge COMPUTE off the worker, a parked compute must NOT block the worker: an Update
// enqueued while mergeSegments is blocked still completes promptly.
func TestMergeOffWorker_ComputeDoesNotBlockWorker(t *testing.T) {
	q := queue.NewMpsc("offworker")
	q.Start()
	s, err := Open(t.TempDir(), q, Options{AutoMerge: true, Fanout: 2, CapBytes: 1 << 12})
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := s.CreateTable("files")

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	mergeComputeBlock = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	t.Cleanup(func() { mergeComputeBlock = nil; close(release) })

	// Seal >= Fanout segments so the background merger fires a tiered pass (compute will park).
	for i := 0; i < 4; i++ {
		s.applyForTest(tbl, int64(1000+i), []string{uniqWord(1000 + i)})
		s.spillForTest(tbl)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("merge compute never started off the worker")
	}

	// The compute is parked. A worker task (RunFunc) MUST still run — proving the compute is off-worker.
	done := make(chan struct{})
	go func() { s.q.RunFunc(func() error { return nil }); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker blocked behind the off-worker merge compute (compute is ON the worker)")
	}
}
```

- [ ] **Step 3 — Run; verify it fails.**

Run: `cd core && GOWORK=off go test ./invertedstore/ -run TestMergeOffWorker_ComputeDoesNotBlockWorker -v`
Expected: **FAIL** (times out at the second select) — today the merge compute runs on the worker
inside `runScheduledMerge`'s single `RunFunc`, so the parked compute blocks the worker.

- [ ] **Step 4 — Add the off-worker plan/compute/install machinery.**

`merge.go`:

```go
// segsByIdsLocked returns the open handles whose ids are in ids, oldest->newest, with a READER REF
// bumped on each (caller MUST releaseSnapshot them). Caller holds s.mu (Lock here — the plan reserves
// outId in the same window). The incref-under-lock closes the load-then-retire race (spec §8).
func (s *Store) segsByIdsLocked(ids map[uint64]bool) []*segment {
	out := make([]*segment, 0, len(ids))
	for _, seg := range s.segs {
		if ids[seg.id] {
			seg.refs.Add(1)
			out = append(out, seg)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].id > out[j].id; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// mergePlan is one off-worker merge pass decided on the worker under s.mu: ref-held inputs, a reserved
// output id, and the mergeSegments parameters. The plan's input refs are released after install.
type mergePlan struct {
	inputIds   map[uint64]bool
	segs       []*segment // ref-held (segsByIdsLocked); released by runMergePlan after install
	outId      uint64
	level      int
	dataCodec  byte
	covering   bool
	liveTables map[int]bool
}

// selectTieredMergePlan picks the lowest qualifying level, increfs its inputs, and reserves outId —
// ALL under one s.mu.Lock (no gap). Returns nil if no level qualifies. MUST run on the worker.
func (s *Store) selectTieredMergePlan() *mergePlan {
	s.mu.Lock()
	defer s.mu.Unlock()
	level, metas, ok := s.pickLowestQualifyingLevelLocked()
	if !ok {
		return nil
	}
	inputIds := map[uint64]bool{}
	for _, m := range metas {
		inputIds[m.Id] = true
	}
	segs := s.segsByIdsLocked(inputIds)
	outId := s.man.NextSegId
	s.man.NextSegId++
	return &mergePlan{inputIds: inputIds, segs: segs, outId: outId, level: level + 1,
		dataCodec: s.opts.DataCodecMerged}
}

// selectCoveringMergePlan decides a covering pass (force, or the dead fraction crosses with >= 2
// segments), increfs ALL live inputs, snapshots liveTables, and reserves outId — under one s.mu.Lock.
// It fires coveringMergeHook here (counter parity with the synchronous coveringMerge). MUST run on the
// worker. Returns nil if nothing to compact.
func (s *Store) selectCoveringMergePlan(force bool) *mergePlan {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.man.Segments) == 0 {
		return nil
	}
	if !force {
		if len(s.man.Segments) < 2 || s.deadFractionLocked() < coveringDeadThreshold {
			return nil
		}
	}
	// NOTE (cross-review): coveringMergeHook fires at INSTALL time in runMergePlan (counting COMPLETED
	// covering merges, parity with the synchronous coveringMerge), NOT here at plan time — a plan can
	// still fail to install, and a test that reads the counter then asserts segment state must not race
	// a not-yet-run install.
	level := 0
	inputIds := map[uint64]bool{}
	for _, sm := range s.man.Segments {
		inputIds[sm.Id] = true
		if sm.Level > level {
			level = sm.Level
		}
	}
	liveTables := map[int]bool{}
	for id := range s.man.Tables {
		liveTables[id] = true
	}
	segs := s.segsByIdsLocked(inputIds)
	outId := s.man.NextSegId
	s.man.NextSegId++
	return &mergePlan{inputIds: inputIds, segs: segs, outId: outId, level: level,
		dataCodec: s.opts.DataCodecMerged, covering: true, liveTables: liveTables}
}

// runMergePlan runs the heavy compute OFF the worker, then installs ON the worker, then releases the
// plan's input refs (so a retired input is torn down only after the compute AND every reader finish).
func (s *Store) runMergePlan(p *mergePlan) {
	res := s.mergeSegments(p.segs, p.outId, p.level, p.dataCodec, p.covering, p.liveTables)
	err := s.q.RunFunc(func() error { return s.installMerge(p.inputIds, res) })
	if err == nil && p.covering && coveringMergeHook != nil {
		coveringMergeHook() // count COMPLETED covering merges (parity); the hook is atomic (-race safe)
	}
	s.releaseSnapshot(p.segs)
}
```

> **Covering-trigger semantics (cross-review MAJOR):** the off-worker `runScheduledMerge` no longer
> calls `maybeMerge`/`maybeCoveringMerge`; `selectCoveringMergePlan(force)` re-implements their
> `nseg<2` + dead-fraction gates and runs at most ONE covering pass per drain — verify the existing
> `installCoveringCounter` / `TestTrigger_*` / `TestMerge_AutoMergeBackgroundFires` assertions still
> hold (they count covering merges; the hook now fires post-install). Update the `export_test.go`
> `installCoveringCounter` comment: the hook may run on the **merge goroutine** (covering path), not
> only the worker — the atomic keeps it `-race` clean.
> **DELETE `maybeMerge` AND `maybeCoveringMerge` (cross-review R2 MAJOR-1):** after this rewrite they
> have NO caller (`runScheduledMerge` was the only one, and `maybeCoveringMerge` was only called by
> `maybeMerge`). Leaving them turns previously-AutoMerge-exercised code into uncovered dead code →
> drops `go-cov` TOTAL below the 90% gate. Remove both funcs; update the stale "runs `maybeMerge`"
> comments in `mergeLoop` (concurrency.go:180) + `store.go`:27. (`mergeOneLevel`/`coveringMerge` STAY —
> the test seams + `reclaimOrphanTables` + Close drain still use them.)
>
> **`liveTables` staleness window (cross-review MAJOR):** `selectCoveringMergePlan` snapshots
> `liveTables` under the lock, but the compute + install run later. A `CreateTable`/`DeleteTable`
> between selection and install changes the catalog. This is benign (a now-deleted table's keys are
> over-retained for one more pass; a now-created table can't be in the already-fixed inputs) — but it
> is a NEW window the synchronous path didn't have. **Add a test:** `DeleteTable` racing an in-flight
> covering compute → the reclaim is still correct + a follow-up pass cleans the deleted table.

`concurrency.go` — rewrite `runScheduledMerge` (lines 203–216):

```go
func (s *Store) runScheduledMerge() {
	req := s.mergeReqSeq.Load()
	force := s.forceCovering.Swap(false)
	// Tiered passes: plan (worker) -> compute (off-worker) -> install (worker), until no level qualifies.
	for {
		var plan *mergePlan
		_ = s.q.RunFunc(func() error { plan = s.selectTieredMergePlan(); return nil })
		if plan == nil {
			break
		}
		s.runMergePlan(plan)
	}
	// One covering pass if forced (DeleteTable) or the dead fraction crosses.
	var cplan *mergePlan
	_ = s.q.RunFunc(func() error { cplan = s.selectCoveringMergePlan(force); return nil })
	if cplan != nil {
		s.runMergePlan(cplan)
	}
	s.mergeAckSeq.Store(req)
}
```

- [ ] **Step 5 — Run the off-worker test; then the `-race` stress + full suite.**

Run: `cd core && GOWORK=off go test ./invertedstore/ -run TestMergeOffWorker -v` → **PASS**.
Run: `cd core && GOWORK=off go test -race ./invertedstore/` → clean. The existing
`TestConcurrency_SearchUpdateMergeRaceClean` + `TestMerge_AutoMergeBackgroundFires` stay green, but
they were written when the merge ran ON the worker — **add a NEW race test** (cross-review MAJOR) that
holds the off-worker compute OPEN via `mergeComputeBlock` and, while it is parked, fires concurrent
`Update`s and `Search`es, asserting `-race` clean + hits identical to a serial reference build + the
input segments are not torn down mid-compute. Also add a **`waitMergeIdle` convergence test** with a
deliberately slow install (`beforeManifestFsync` delay): `waitMergeIdle` must still return only after
the install lands (`mergeAckSeq` is stored after the last `runMergePlan`, which awaits its install
`RunFunc`) — prove it, don't assume it.

- [ ] **Step 6 — Add the ref-held-during-compute assertion.**

Add to `merge_offworker_test.go` a test that, while the compute is parked (reuse `mergeComputeBlock`),
asserts each input segment's `refs.Load() >= 2` (published + plan) — i.e. the inputs are ref-held
across the off-worker compute, so a concurrent retire can't free them mid-read. Use an export_test
accessor `segRefsByIdForTest(id) int64`. After release, assert the merged-away inputs are torn down
(file removed) on reopen the MANIFEST lists only the output.

- [ ] **Step 7 — Measure, then commit.**

`idxbench -impl=store -batch=1` with AutoMerge wired as production does. Capture a build CPU profile
(`-buildprofile`) and confirm `mergeSegments` is **no longer on the worker's** profile (it's on the
merge goroutine). Record the wall delta (~34s leaves the worker; expect build ≈ pebble parity ~55–62s
per spec §3 — A alone does NOT beat pebble; F does). Confirm `hits` unchanged, `-race` clean.

```bash
git add core/invertedstore/merge.go core/invertedstore/concurrency.go core/invertedstore/export_test.go \
        core/invertedstore/merge_offworker_test.go
git commit -m "perf(invertedstore): run merge compute off the worker, install on it (A)"
```

---

## Task 5 — C.1–C.3 (alloc churn) + E (write-path backpressure)

**Spec §5 + §7.** GC is parallel-free here, so reducing allocation mainly cuts heap/GC-cycles/peak —
wall only where `mallocgc` is on the worker's serial path. **Measure each AFTER A+B; keep only real
wins.** C.1 (1-op fast path) DOES move wall; C.2/C.3 are memory plays (conditional). E is a
memory-bound correctness guarantee (~0 wall) — bound in-flight WORK, not task count.

### C.1 — 1-op `applyBatch` fast path (definite)

The hot `Update` path is always 1-op; a 1-op batch can't repeat a docid, so `inBatch`/`seen` are dead
weight and `old` always comes from `forwardKeywords`. Guard `len(ops)==1`.

**Files:** `core/invertedstore/update.go`; test `core/invertedstore/apply_fastpath_test.go` (new).

- [ ] **Step 1 — Failing equivalence test.**

```go
package invertedstore

import "testing"

// A warm 1-op edit (drop a keyword) MUST still diff against the forward and tombstone the dropped
// keyword — the fast path must not skip the diff. (Guards that len(ops)==1 still reads `old`.)
func TestApplyFastPath_WarmEditTombstonesDroppedKeyword(t *testing.T) {
	s, tid := newForwardSkipStore(t, Options{CapBytes: 1 << 20})
	s.Update(tid, 1, []string{"alpha", "beta"})
	s.spillForTest(tid) // seal so the next edit reads the forward from a segment
	s.Update(tid, 1, []string{"alpha"}) // drop "beta"
	s.q.RunFunc(func() error { return nil }) // drain
	// "beta" must no longer resolve to docid 1.
	if got := searchDocidsForTest(t, s, tid, "beta"); len(got) != 0 {
		t.Fatalf("beta still maps to %v after the warm 1-op edit dropped it", got)
	}
	if got := searchDocidsForTest(t, s, tid, "alpha"); len(got) != 1 || got[0] != 1 {
		t.Fatalf("alpha should still map to {1}, got %v", got)
	}
}
```

> **`searchDocidsForTest` is a NEW helper** (it does NOT exist — all three cross-reviewers flagged
> this). Add it to `export_test.go`; it resolves an EXACT keyword via `GetDocs` (membership, not
> prefix) and returns a sorted `[]int64`. It is also used by Task 7B's B1 gate, so it must land here
> (Task 5 precedes Task 7) or be moved to a shared earlier task:
>
> ```go
> // searchDocidsForTest returns the live docids of the EXACT keyword kw in tableId, sorted — a thin
> // []int64 view over GetDocs for membership assertions. (GetDocs, not Search: exact, not prefix.)
> func searchDocidsForTest(t *testing.T, s *Store, tableId int, kw string) []int64 {
> 	t.Helper()
> 	r := s.GetDocs(tableId, kw)
> 	out := make([]int64, 0, len(r.DocIds))
> 	for d := range r.DocIds {
> 		out = append(out, d)
> 	}
> 	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
> 	return out
> }
> ```

- [ ] **Step 2 — Run; verify it passes today (characterization), then refactor under green.**

This case already works (the multi-op loop handles n=1). Run it to confirm GREEN, then refactor to the
fast path and keep it green (a behavior-preserving extraction). In `update.go`, extract the per-op
apply body (the `s.mu.Lock`→head→liveByTable-delta→`s.mu.Unlock`→spill-on-`over` block) into
`applyOneOp(op updateOp, old []string) error` — the spill stays INSIDE, so it returns just the spill
error (NOT `(over bool, err error)`). The `inBatch`/`seen` last-wins bookkeeping is NOT part of
`applyOneOp` — it closes over loop state and stays in the multi-op loop. Split `applyBatch`:

```go
func (s *Store) applyBatch(ops []updateOp) error {
	if len(ops) == 1 {
		if applyFastPathTaken != nil {
			applyFastPathTaken() // test-only (C.1): proves the 1-op fast path is actually taken
		}
		op := ops[0]
		old, _ := s.forwardKeywords(op.tableId, op.docid)
		return s.applyOneOp(op, old)
	}
	// multi-op: the existing inBatch/seen last-wins loop, now calling applyOneOp for the apply body.
	type dk struct {
		t int
		d int64
	}
	inBatch := map[dk][]string{}
	seen := map[dk]bool{}
	for _, op := range ops {
		key := dk{op.tableId, op.docid}
		var old []string
		if seen[key] {
			old = inBatch[key]
		} else {
			old, _ = s.forwardKeywords(op.tableId, op.docid)
		}
		if err := s.applyOneOp(op, old); err != nil {
			return err
		}
		seen[key] = true
		if len(op.keywords) == 0 {
			inBatch[key] = nil
		} else {
			inBatch[key] = op.keywords
		}
	}
	return nil
}
```

`applyOneOp` is the per-op apply block from today's `applyBatch` (update.go:122–174) **MINUS the
loop-local bookkeeping** — i.e. lines 122–141 + 143–163 + 165–174, EXCLUDING `inBatch[key]=nil` (142),
`inBatch[key]=op.keywords` (160), and `seen[key]=true` (164), which stay in the multi-op loop. It
returns the spill error (the spill-on-`over` stays inside it). No behavior change.

**Feature-taken test (cross-review R2 MAJOR-2 — the behavior test above passes even WITHOUT the fast
path, so it does not cover the optimization).** Add `var applyFastPathTaken func()` (segment/update.go,
nil in prod) fired in the `len(ops)==1` branch, and assert it fires for a 1-op apply and does NOT for
a multi-op batch:

```go
func TestApplyFastPath_TakenForOneOpNotMultiOp(t *testing.T) {
	s, tid := newForwardSkipStore(t, Options{CapBytes: 1 << 20})
	var fast int
	applyFastPathTaken = func() { fast++ }
	t.Cleanup(func() { applyFastPathTaken = nil })

	s.Update(tid, 1, []string{"a"}) // 1-op → fast path
	s.q.RunFunc(func() error { return nil })
	if fast != 1 {
		t.Fatalf("1-op apply took the fast path %d times, want 1", fast)
	}
	b := s.NewBatch()
	b.Update(tid, 2, []string{"b"}).Update(tid, 3, []string{"c"}) // 2-op → multi-op loop
	b.Commit()
	s.q.RunFunc(func() error { return nil })
	if fast != 1 {
		t.Fatalf("multi-op batch took the 1-op fast path (fast=%d, want still 1)", fast)
	}
}
```

- [ ] **Step 3 — Run C.1 test + update/differential suites; measure; commit.**

`cd core && GOWORK=off go test ./invertedstore/ -run 'TestApplyFastPath|TestUpdate' -v` → green;
full suite green. `idxbench` — record the applyBatch delta (expect a small but real wall win). Commit:
`perf(invertedstore): 1-op applyBatch fast path (C.1)`.

### C.2 / C.3 — decompress / encode scratch reuse (MEASURE-GATED, conditional)

- [ ] **Step 4 — Measure the residual alloc after A+B; implement ONLY if it moves heap/wall.**

Run `idxbench -memprofile` after A+B+C.1. If `mergeCursor.advance`/`blockBytes` decompression is a
material share of remaining allocs, add a **per-cursor** reusable decompress buffer
(`decompressInto(dst, comp, rawLen)`) — `mergeCursor`-scratch ONLY, never a global (K cursors' blocks
coexist). **Constraint (spec §5.2):** scratch MUST NOT alias or in-place-sort **head** storage — that
interacts with F's read-only-detached-head invariant (Task 7). C.3 spill/encode scratch that touches
head storage is **deferred to ship WITH F** (Task 7), where the read-only constraint is enforced;
Task 5's C.3 is limited to segment/merge scratch that provably aliases nothing live. If a sub-item
shows no heap/wall gain, **drop it** and note the measurement in the commit body — do not keep churn
for a null result. `-race` any kept change. Commit only kept wins.

### E — write-path backpressure by in-flight postings (definite, memory-correctness)

**Files:** `core/invertedstore/store.go` (Options + `Store.budget` + Open), `core/invertedstore/update.go`
(acquire on producer, release via the enqueued closure's defer); test
`core/invertedstore/backpressure_test.go` (new).

- [ ] **Step 5 — Add the posting budget; acquire on the producer, release on apply.**

`store.go` — Options + default:

```go
	// MaxInflightPostings bounds the postings (Σ keyword copies) buffered between the producer and the
	// worker — the memory bound (spec §7, item E). The producer blocks in Update/Commit until the
	// budget frees; applyBatch releases via the enqueued closure's defer. 0 ⇒ default 4 × CapBytes.
	MaxInflightPostings int
```

```go
	if o.MaxInflightPostings <= 0 {
		o.MaxInflightPostings = 4 * o.CapBytes // CapBytes already defaulted above
	}
```

New `core/invertedstore/backpressure.go`:

```go
package invertedstore

import "sync"

// postingBudget is a variable-amount counting semaphore bounding in-flight postings (spec §7, E). The
// producer acquire()s before enqueuing an apply; the apply release()s after running. A request larger
// than the whole budget is capped (acquire/release the same capped amount) so it never self-deadlocks.
type postingBudget struct {
	mu   sync.Mutex
	cond *sync.Cond
	cap  int64
	used int64
}

func newPostingBudget(capacity int64) *postingBudget {
	if capacity <= 0 {
		capacity = 1
	}
	b := &postingBudget{cap: capacity}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// acquire blocks until n (capped at the budget) tokens are free, reserves them, and returns the
// amount actually reserved (which the caller MUST later release exactly). n<=0 reserves nothing.
func (b *postingBudget) acquire(n int64) int64 {
	if n <= 0 {
		return 0
	}
	if n > b.cap {
		n = b.cap
	}
	b.mu.Lock()
	for b.used+n > b.cap {
		b.cond.Wait()
	}
	b.used += n
	b.mu.Unlock()
	return n
}

func (b *postingBudget) release(n int64) {
	if n <= 0 {
		return
	}
	b.mu.Lock()
	b.used -= n
	b.cond.Broadcast()
	b.mu.Unlock()
}
```

Init in `Open`: `s.budget = newPostingBudget(int64(s.opts.MaxInflightPostings))` (add the `budget`
field to `Store`).

`update.go` — acquire on the producer, release via the closure defer (EVERY exit path). `Update`:

```go
func (s *Store) Update(tableId int, docid int64, keywords []string) {
	var kw []string
	if len(keywords) > 0 {
		kw = append([]string(nil), keywords...)
	}
	op := updateOp{tableId: tableId, docid: docid, keywords: kw}
	got := s.budget.acquire(int64(len(kw))) // producer backpressure (spec §7 E)
	s.q.AddFunc(func() error {
		defer s.budget.release(got)
		return s.applyBatch([]updateOp{op})
	})
}
```

`Batch.Commit`:

```go
func (b *Batch) Commit() {
	if len(b.ops) == 0 {
		return
	}
	ops := b.ops
	b.ops = nil
	s := b.s
	var postings int64
	for _, op := range ops {
		postings += int64(len(op.keywords))
	}
	got := s.budget.acquire(postings)
	s.q.AddFunc(func() error {
		defer s.budget.release(got)
		return s.applyBatch(ops)
	})
}
```

- [ ] **Step 6 — Failing backpressure tests.**

`core/invertedstore/backpressure_test.go`: (a) with a tiny `MaxInflightPostings`, a producer firing
more postings than the budget blocks until applies drain — assert peak `budget.used` ≤ cap via a hook,
or assert the producer goroutine does not return until a blocked apply is released. (b) a single
`Update` with more keywords than the whole budget does NOT self-deadlock (it caps + proceeds). (c)
deletes (0 keywords) never block. Gate `-race`. Verify the acquire is on the producer (never inside
`applyBatch`) — a static guard: `applyBatch` must not reference `s.budget`.

- [ ] **Step 7 — Run; implement; `-race`; measure (≈0 wall, bounded peak); commit.**

`cd core && GOWORK=off go test -race ./invertedstore/ -run TestBackpressure -v` → green; full suite +
`-race` green. `idxbench` — confirm build wall is NOT regressed and peak in-flight is bounded. Commit:
`feat(invertedstore): bound in-flight postings with producer backpressure (E)`.

---

## Task 6 — G: Open sweeps orphan segment files

**Spec §7b.** The `merge.go` "GC'd on next Open" comment is currently FALSE — Open opens only
MANIFEST-listed segments and never removes stray `seg-*.dat`. Benign today, but A (off-worker merge)
and especially F create orphans on the reserve-id → crash-before-install path. Make the claim true:
on Open, after reading the MANIFEST, remove any `seg-*.dat` whose id is not in `man.Segments`.

**Files:**
- Modify: `core/invertedstore/store.go` — `sweepOrphanSegments` + call it in `Open`; `parseSegFileName`.
- Test: `core/invertedstore/orphan_sweep_test.go` (new).

- [ ] **Step 1 — Failing test.**

```go
package invertedstore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/codetrek/haystack/core/queue"
)

func TestOrphanSweep_RemovesUnlistedSegmentOnOpen(t *testing.T) {
	dir := t.TempDir()
	q := queue.NewMpsc("orphansweep")
	q.Start()
	s, err := Open(dir, q, Options{})
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := s.CreateTable("files")
	s.applyForTest(tid, 1, []string{"alpha"})
	s.spillForTest(tid) // one LIVE segment, in the MANIFEST
	live := s.SegmentsForTest()
	if len(live) != 1 {
		t.Fatalf("want 1 live segment, got %d", len(live))
	}
	s.CloseAndWait()

	// Simulate a crash-after-reserve orphan: a seg file at an id NOT in the MANIFEST.
	orphan := filepath.Join(dir, segFileName(999999))
	if err := os.WriteFile(orphan, []byte("garbage-not-a-real-segment"), 0o644); err != nil {
		t.Fatal(err)
	}

	q2 := queue.NewMpsc("orphansweep2")
	q2.Start()
	s2, err := Open(dir, q2, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.CloseAndWait()
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan segment was not swept on Open (stat err=%v)", err)
	}
	// The live segment + its data survive.
	if got := s2.SegmentsForTest(); len(got) != 1 || got[0].Id != live[0].Id {
		t.Fatalf("live segment lost after sweep: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, segFileName(live[0].Id))); err != nil {
		t.Fatalf("live segment file removed by sweep: %v", err)
	}
}
```

- [ ] **Step 2 — Run; verify it fails.**

Run: `cd core && GOWORK=off go test ./invertedstore/ -run TestOrphanSweep -v`
Expected: **FAIL** — the orphan still exists after reopen (no sweep yet).

- [ ] **Step 3 — Implement the sweep.**

`store.go` (add `"os"` is already imported; add `"strconv"`, `"strings"`):

```go
// parseSegFileName extracts the seal-sequence id from a "seg-%06d.dat" name; ok=false for any other
// name, so MANIFEST/MANIFEST.tmp and unrelated files are left alone.
func parseSegFileName(name string) (uint64, bool) {
	if !strings.HasPrefix(name, "seg-") || !strings.HasSuffix(name, ".dat") {
		return 0, false
	}
	id, err := strconv.ParseUint(name[len("seg-"):len(name)-len(".dat")], 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// sweepOrphanSegments removes any seg-*.dat in the store dir whose id is NOT live in the MANIFEST
// (item G) — an orphan left when a crash hit between reserving an outId + writing the segment file
// and installing the MANIFEST (off-worker merge A / spill F). Makes the merge.go "GC'd on Open" claim
// true. Open-only (single-threaded, exclusive owner).
func (s *Store) sweepOrphanSegments() error {
	live := make(map[uint64]bool, len(s.man.Segments))
	for _, sm := range s.man.Segments {
		live[sm.Id] = true
	}
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		id, ok := parseSegFileName(e.Name())
		if !ok || live[id] {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, e.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
```

Call it in `Open` right after `s.dictCache = newChunkLRU(...)` and BEFORE the segment-open loop (it
needs only `s.man`; opening only ever touches live, MANIFEST-listed files):

```go
	if err := s.sweepOrphanSegments(); err != nil {
		return nil, err
	}
```

- [ ] **Step 4 — Run; full suite; commit.**

`cd core && GOWORK=off go test ./invertedstore/ -run TestOrphanSweep -v` → PASS; full suite green
(crash-recovery tests still find their live segments — they're all MANIFEST-listed). No measurement
(hygiene). Commit: `fix(invertedstore): sweep orphan segment files on Open (G)`.

---

## Task 7 — F: move the RESIDUAL spill encode off the worker (LAST, hardened)

**Spec §7a.** After F0 + head-fix, the spill's residual encode is sort ~11 + snappy ~6 ≈ 17s on the
worker. Move it off-worker via a **detached head double-buffer** + a `spilling` read tier. Highest
risk: the first draft underspecced three correctness BLOCKERs. Gate hardest on the **B1 zero-
concurrency silent-corruption test** before committing.

> ### ⚠ Spec correction found while breaking this down — RAISE IN CROSS-REVIEW
> Spec §7a M5 says *"the worker BLOCKS the detach when the pool is full."* That **deadlocks**: the
> detach runs inside `applyBatch` **on the worker**; the encode pool drains by calling
> `RunFunc(installSpill)` **back onto the worker**; a blocked worker can't run those installs → the
> pool never frees → the detach never unblocks. **Corrected mechanism (used below): when the pool is
> full, the worker does NOT block — it falls back to a synchronous on-worker spill** (`spillSync`, the
> classic path), which is bounded and deadlock-free. This still caps peak memory at
> `maxInflightSpills × CapBytes` (the `spilling` list never exceeds the bound) and degrades gracefully
> to old behavior exactly when the producer outpaces encoding (the harness artifact, not production).
> Confirm this correction in the task-breakdown cross-review before implementing 7B.

**Three BLOCKERs the design must satisfy (spec §7a):**
- **B1 (silent corruption, ZERO concurrency):** `forwardKeywords` is the worker's OWN "read old
  keyword set" on every edit. After a doc detaches, its forward is in `spilling`; a re-post that diffs
  against an empty `old` writes NO tombstones for dropped keywords → they resurrect. **All four read
  paths must consult the `spilling` tier.**
- **B2/B3 (atomicity):** detach (swap head + publish to `spilling` + reserve/bump outId) is ONE
  `s.mu.Lock()`; install (append segMeta + publish snapshot + remove from `spilling`) is ONE
  `s.mu.Lock()`. So a reader never sees a doc in neither tier.
- **M1/M2 (lifetime + read-only):** readers COPY deltas under `s.mu.RLock()` (no refcount, no pool
  reuse of a detached head); the encode is strictly READ-ONLY over the detached head.

### Task 7A — the `spilling` tier + three-tier reads (the B1 fix), with a test-injected head

Build the read side FIRST, exercised by a test-injected detached head (no async machinery yet), so the
tier plumbing is proven before 7B wires the real detach.

**Files:** `core/invertedstore/spilling.go` (new: types + read helper), `dictcache.go`
(`forwardKeywords`), `search.go` (`Search`, `GetDocs`), `reconcile.go` (`ForwardDocids`),
`store.go` (`Store.spilling` field), `export_test.go` (inject helper); test
`core/invertedstore/spilling_read_test.go` (new).

- [ ] **Step 1 — Types + the read helper + the Store field.**

`spilling.go`:

```go
package invertedstore

// spillEntry is one DETACHED head being encoded off-worker (item F). It is published into s.spilling
// at detach (under s.mu.Lock) and removed at install (under s.mu.Lock). Readers resolve it as a tier
// BETWEEN the live head and the sealed segments, newest -> oldest by detach order. The head is
// READ-ONLY once detached (the encode + readers only read it); it is never pooled/reused while listed.
type spillEntry struct {
	tableId          int
	head             *headTable
	outId            uint64 // the segment id reserved at detach (the file the encode writes)
	minDocid, maxDocid int64 // forward-record docid span (the spilling-head analog of B; Task 7C)
}

// headForwardLookup resolves docid's forward decision in ONE head: found=false ⇒ this head does not
// mention the docid (keep looking older). Words are COPIED so the caller may use them after dropping
// the lock (M1 copy-under-RLock). Caller holds s.mu.RLock.
func headForwardLookup(h *headTable, docid int64) (words []string, deleted, found bool) {
	if h == nil {
		return nil, false, false
	}
	if _, del := h.delForward[docid]; del {
		return nil, true, true
	}
	if w, ok := h.fwd[docid]; ok {
		return append([]string(nil), w...), false, true
	}
	return nil, false, false
}
```

`store.go` — add to `Store` (guarded by `s.mu`):

```go
	// spilling holds heads DETACHED for off-worker encode (item F), newest last. Readers consult it as
	// a tier between the live head and the sealed segments (B1). Published at detach + removed at
	// install, both under s.mu.Lock. Read (copied) under s.mu.RLock. Never refcounted/pooled.
	spilling []*spillEntry
```

`export_test.go` — inject helper (drives the worker so the field is set under the lock):

```go
// injectSpillingHeadForTest detaches tableId's CURRENT head into s.spilling WITHOUT encoding it (the
// head stays readable as a spilling tier), reserving its outId — a test stand-in for 7B's real detach,
// so 7A's read tiers can be tested before the async encode exists. Runs on the worker.
func (s *Store) injectSpillingHeadForTest(tableId int) {
	s.q.RunFunc(func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		h := s.head[tableId]
		if h == nil {
			return nil
		}
		s.head[tableId] = newHeadTable()
		minD, maxD := headForwardRange(h) // Task 7C helper (add a stub returning the full span for 7A)
		outId := s.man.NextSegId
		s.man.NextSegId++
		s.spilling = append(s.spilling, &spillEntry{tableId: tableId, head: h, outId: outId,
			minDocid: minD, maxDocid: maxD})
		return nil
	})
}
```

- [ ] **Step 2 — Failing B1-shaped read test (single path: forwardKeywords).**

`spilling_read_test.go`:

```go
package invertedstore

import "testing"

// A doc whose forward is in the spilling tier (NOT the live head, NOT a segment) must still resolve
// via forwardKeywords — the B1 read. Without the spilling tier this returns (nil,false) and a re-post
// would drop no tombstones (silent corruption).
func TestSpillingTier_ForwardKeywordsReadsDetachedHead(t *testing.T) {
	s, tid := newForwardSkipStore(t, Options{CapBytes: 1 << 20})
	s.applyForTest(tid, 1, []string{"alpha", "beta"})
	s.injectSpillingHeadForTest(tid) // doc 1's forward now lives ONLY in spilling
	if len(s.SegmentsForTest()) != 0 {
		t.Fatalf("inject must not seal a segment")
	}
	got, del := s.forwardKeywordsForTest(tid, 1)
	if del {
		t.Fatal("doc 1 is live, not deleted")
	}
	if len(got) != 2 {
		t.Fatalf("forward for doc 1 = %v, want [alpha beta] (read from the spilling tier)", got)
	}
}
```

- [ ] **Step 3 — Run; fails (forwardKeywords ignores `spilling`). Then implement the tier in `forwardKeywords`.**

In `dictcache.go` `forwardKeywords`, replace the single live-head block (lines 172–184) with a loop
over [live head] then [spilling heads for the table, newest→oldest], all under the one RLock:

```go
	s.mu.RLock()
	if w, del, found := headForwardLookup(s.head[tableId], docid); found {
		s.mu.RUnlock()
		s.noteForwardRead()
		return w, del
	}
	for i := len(s.spilling) - 1; i >= 0; i-- { // newest detached head wins
		e := s.spilling[i]
		if e.tableId != tableId {
			continue
		}
		// Task 7C inserts the docid-range skip here: if docid < e.minDocid || docid > e.maxDocid { continue }
		if w, del, found := headForwardLookup(e.head, docid); found {
			s.mu.RUnlock()
			s.noteForwardRead()
			return w, del
		}
	}
	s.mu.RUnlock()
```

(The rest of `forwardKeywords` — `acquireSnapshot`, the segment loop with B's range-skip — is
unchanged.) **Recursive-RLock caution (R2 MAJOR-2):** the spilling loop MUST stay inside the SAME
RLock as the live-head read and finish before `s.mu.RUnlock()`; `acquireSnapshot` (which re-takes the
RLock) is still called AFTER that `RUnlock`, never nested — a second RLock while a writer is queued
deadlocks `sync.RWMutex`. Do NOT refactor the spilling iteration into a helper that re-locks. (Same
constraint for Search/GetDocs/ForwardDocids: the spilling copy lives in the existing RLock window.)
Run the test → PASS.

- [ ] **Step 4 — Extend the tier to `Search`, `GetDocs`, `ForwardDocids`; one test per path.**

Each path already copies the LIVE head's matching deltas under its RLock, then merges head-first,
segments-next (newest-wins). Insert the `spilling` tier BETWEEN, newest→oldest:

- **`Search` (search.go):** after building `headHits` from the live head, append each matching
  keyword's `setToSlice(pd.adds/dels)` from every `s.spilling[i]` (tableId match), iterating
  `i` from newest→oldest, into the SAME ordered `headHits` (so `merge` sees live-head → spilling
  newest→oldest → segments). All copied under the existing RLock window (before `RUnlock`).
- **`GetDocs` (search.go):** same, for the single exact `key` (copy `pd.adds/dels` from each spilling
  head's `inv[key]`, newest→oldest), merged before the segment loop.
- **`ForwardDocids` (reconcile.go):** after marking the live head's `delForward`/`fwd` into `decided`
  + `headLive`, do the same for each spilling head newest→oldest (a `delForward` marks decided/dead; a
  live `fwd` marks decided + yields), THEN the segment resolver. Copy under the existing RLock.

Concrete `Search` insertion (inside the existing RLock window, AFTER the live-head `headHits` loop
and BEFORE `s.mu.RUnlock()` — `headHits`/`q` are the real search.go locals):

```go
	for i := len(s.spilling) - 1; i >= 0; i-- { // spilling newest -> oldest, between head and segments
		e := s.spilling[i]
		if e.tableId != tableId {
			continue
		}
		for kw, pd := range e.head.inv {
			if !strings.HasPrefix(kw, q) {
				continue
			}
			headHits = append(headHits, headPosting{kw: kw, adds: setToSlice(pd.adds), dels: setToSlice(pd.dels)})
		}
	}
```

`GetDocs` is the same shape for the single exact `key` (merge each spilling head's `inv[key]` via the
existing `merge(adds,dels)` closure, newest→oldest, before the segment loop). Concrete `ForwardDocids`
insertion (reconcile.go) — under the SAME RLock that populates `decided`/`headLive` from the live head,
BEFORE `acquireSnapshotLocked`, collect spilling live docids newest→oldest; yield them AFTER the head's
`headLive`, before `forEachLiveSegmentForward`:

```go
	var spillingLive []int64 // spilling-tier live fwd docids, newest -> oldest; yielded after headLive
	for i := len(s.spilling) - 1; i >= 0; i-- {
		e := s.spilling[i]
		if e.tableId != tableId {
			continue
		}
		for d := range e.head.delForward {
			decided[d] = struct{}{} // a tombstone in a newer-or-equal tier decides the docid dead
		}
		for d := range e.head.fwd {
			if _, dead := decided[d]; dead {
				continue
			}
			decided[d] = struct{}{}
			spillingLive = append(spillingLive, d)
		}
	}
	// ... (after the head's `for _, d := range headLive { fn(d) }` yield, before the segment resolver):
	for _, d := range spillingLive {
		if !fn(d) {
			return
		}
	}
```

Each path gets its own test below.

Tests (`spilling_read_test.go`): for each path, inject a spilling head that DIFFERS from a stale
segment copy and assert newest-wins picks the spilling value: `Search`/`GetDocs` reflect a keyword
added/tombstoned only in the spilling head; `ForwardDocids` yields a doc live only in spilling and
does NOT yield one tombstoned in spilling. Run → PASS. Full suite + `-race` green.

- [ ] **Step 5 — Commit 7A.**

`git commit -m "feat(invertedstore): spilling read tier across all four read paths (F: B1 fix)"`

### Task 7B — detach / encode-off-worker / install, with the deadlock-safe bound

**Files:** `head.go` (split `spill`), `store.go` (Options `MaxInflightSpills`, the pool, Open/Close),
`update.go` (applyBatch over-cap dispatch); test `core/invertedstore/spill_offworker_test.go` (new).

- [ ] **Step 1 — Split `spill` into detach / encode / install.**

Refactor `head.go` `spill` (lines 89–223) into three functions, preserving the byte-identical segment
output (F0's inline dict already removed the re-read):

```go
// detachHeadLocked swaps in a fresh head, publishes the old into s.spilling, and reserves+bumps the
// segment id — ATOMICALLY (caller holds s.mu.Lock). Returns the entry to encode, or nil if the head
// is empty. The atomic swap+publish+reserve is BLOCKER B2/B3: a reader (or the worker's own
// forwardKeywords) must never see the doc in neither the live head nor spilling nor a segment.
func (s *Store) detachHeadLocked(tableId int) *spillEntry {
	h := s.head[tableId]
	if h == nil || (len(h.inv) == 0 && len(h.fwd) == 0 && len(h.delForward) == 0) {
		return nil
	}
	s.head[tableId] = newHeadTable()
	minD, maxD := headForwardRange(h) // Task 7C
	outId := s.man.NextSegId
	s.man.NextSegId++
	e := &spillEntry{tableId: tableId, head: h, outId: outId, minDocid: minD, maxDocid: maxD}
	s.spilling = append(s.spilling, e)
	return e
}

// encodeSpill writes entry.head as one immutable L0 segment at entry.outId and returns the opened
// segment + its segMeta. READ-ONLY over entry.head (BLOCKER M2: no in-place sort, no scratch aliasing
// head storage). This is the old spill body's steps 1–4 + finish, minus the head swap/publish/reset
// (those moved to detach/install). Safe to run OFF the worker (touches only the detached head + a new
// file).
func (s *Store) encodeSpill(e *spillEntry) (*segment, segMeta) { /* ...old spill steps 1–4 + finish... */ }

// installSpillLocked-then-publish appends the segMeta, publishes the snapshot, and removes the entry
// from s.spilling — ATOMICALLY enough that a reader never loses the doc (BLOCKER B3). It mirrors the
// old spill's MANIFEST persist-then-publish (marshal under the lock; fsync OUTSIDE; re-lock to append
// s.segs + publish + remove from spilling). On install FAILURE the entry STAYS in spilling (data
// preserved). MUST run on the worker.
func (s *Store) installSpill(e *spillEntry, seg *segment, sm segMeta) error { /* ... */ }
```

- [ ] **Step 2 — Two drivers: synchronous (worker) and off-worker (pool).**

```go
// spillSync runs the whole spill on the CURRENT worker goroutine (detach + encode + install inline).
// The classic path: used by spillForTest, CloseAndWait, and the deadlock-safe overflow fallback. NO
// RunFunc nesting (it is already on the worker — install runs directly).
func (s *Store) spill(tableId int) error {
	s.mu.Lock()
	e := s.detachHeadLocked(tableId)
	s.mu.Unlock()
	if e == nil {
		return nil
	}
	seg, sm := s.encodeSpill(e)
	if err := s.installSpill(e, seg, sm); err != nil {
		return err
	}
	s.triggerMerge(false)
	return nil
}

// dispatchSpill is the off-worker hot path. DEADLOCK-SAFE ORDER (cross-review BLOCKER-1/-3): reserve
// an encode slot NON-BLOCKINGLY *first*; only then detach; the worker NEVER sends on a channel and
// NEVER blocks. On overflow (no slot) it returns false WITHOUT detaching, so the caller takes the
// fully-synchronous spill. The slot is held from reserve until install completes (bounds detached
// heads to MaxInflightSpills). The encode runs on a fresh goroutine; only THAT goroutine does the
// blocking RunFunc(install) — the worker is never the one waiting, so there is no worker⇄pool cycle.
func (s *Store) dispatchSpill(tableId int) bool {
	select {
	case s.spillSem <- struct{}{}: // reserve a slot (cap = MaxInflightSpills); non-blocking
	default:
		return false // pool full → caller falls back to synchronous spill (no detach, no deadlock)
	}
	s.mu.Lock()
	e := s.detachHeadLocked(tableId)
	s.mu.Unlock()
	if e == nil {
		<-s.spillSem // head empty: release the slot, nothing to encode
		return true
	}
	s.spillWG.Add(1)
	go func() {
		defer s.spillWG.Done()
		seg, sm := s.encodeSpill(e) // OFF the worker (read-only over the detached head)
		// Retry the install on transient MANIFEST-write failure (R2 BLOCKER-2): the entry stays
		// read-correct in s.spilling until it installs, so a failed install must NOT silently strand
		// it (that would leak the head + answer reads from a never-sealed tier forever). Release the
		// slot ONLY after a SUCCESSFUL install; on give-up, KEEP the slot held (bounded backpressure,
		// no leak past the bound). A give-up entry is then CRASH-EQUIVALENT volatile (see below).
		for attempt := 0; attempt < s.opts.MaxInstallRetries; attempt++ {
			if err := s.q.RunFunc(func() error { return s.installSpill(e, seg, sm) }); err == nil {
				<-s.spillSem // success: release the slot
				s.triggerMerge(false)
				return
			}
			time.Sleep(installBackoff)
		}
		// Persistent failure: leave e in s.spilling (read-correct) and HOLD the slot. Further dispatches
		// then take the synchronous fallback, which also surfaces the error up applyBatch → the store.
	}()
	return true
}
```

> **Give-up durability (R3 MAJOR):** a give-up entry is **crash-equivalent volatile** — it only
> happens under PERSISTENT MANIFEST-write/fsync failure (the disk is dying), and on that path the data
> is lost exactly like an unspilled head on a crash (indexer replay recovers it). `CloseAndWait` does
> NOT re-drain `s.spilling` (it flushes only `s.head`); on a HEALTHY disk every in-flight encode
> retry-succeeds and removes itself from `s.spilling` before `spillWG.Wait()` returns, so a clean Close
> IS durable — the clean-Close drain test (7E) runs the healthy (blocked-then-released, install
> SUCCEEDS) path. Do NOT claim CloseAndWait drains a give-up entry; it is reclassified as crash loss.

> **`installSpill` atomicity (R2 MAJOR-1) — pin the publish-then-remove ordering:** under ONE final
> `s.mu.Lock()`, do `s.segs = append(...)` → `publishSnapshotLocked()` → remove `e` from `s.spilling`,
> in THAT order (publish the segment BEFORE removing the spilling tier, mirroring `installMerge`'s
> "publish before retire", merge.go:437–445). The LOST direction — remove-from-spilling before the
> segment is in the published snapshot — leaves a reader seeing the doc in NEITHER tier and MUST be
> forbidden. The MANIFEST marshal+fsync stays split (marshal under the first lock, fsync OUTSIDE, the
> append+publish+remove under this second lock), exactly as today's `spill`. Add a B3 ordering test
> (Task 7E): a reader spinning on the doc's keyword across the install finds it in EVERY snapshot.
> `Options.MaxInstallRetries` (default ~5) + an `installBackoff` const cap the retry loop.

> **Why this is deadlock-free (BLOCKER-1/-2/-3 resolved):** the worker's `dispatchSpill` only does a
> non-blocking `select` send to the semaphore + the cheap detach — it never blocks. The blocking
> `RunFunc(install)` runs on the spawned goroutine; the worker is never parked waiting for that
> goroutine, so the worker keeps draining its queue and the install always lands (latency, not
> deadlock — even when the depth-100 queue is full of producer tasks). The detach happens strictly
> AFTER the slot is secured, so a head is never published to `spilling` with no encoder (BLOCKER-3).
> **`s.spillSem chan struct{}` (buffered `MaxInflightSpills`) replaces the broken `spillCh`/`inflightSpills`
> counter; `s.spillWG sync.WaitGroup` lets Close drain in-flight encodes.**

`applyBatch` (update.go) over-cap dispatch:

```go
	if over {
		if !s.dispatchSpill(op.tableId) {
			if err := s.spill(op.tableId); err != nil { // pool full: deadlock-safe synchronous fallback
				return err
			}
		}
	}
```

`store.go`: add `Options.MaxInflightSpills` (default 3) and `Options.MaxInstallRetries` (default 5) —
both MUST be defaulted in `withDefaults` (a zero `MaxInstallRetries` makes the retry loop `for attempt
< 0` a NO-OP → instant strand):

```go
	if o.MaxInflightSpills <= 0 {
		o.MaxInflightSpills = 3
	}
	if o.MaxInstallRetries <= 0 {
		o.MaxInstallRetries = 5
	}
```

Add `Store.spillSem chan struct{}` (`make(chan struct{}, MaxInflightSpills)` in Open); `Store.spillWG
sync.WaitGroup`; and a package const `const installBackoff = 50 * time.Millisecond`. No long-lived pool
goroutines — each dispatch spawns one (bounded by the semaphore). **`dispatchSpill` (with
`time.Sleep(installBackoff)`) lands in `head.go`, which must add `"time"` to its imports.**
`CloseAndWait`: after the final head flush and BEFORE closing segment fds, `s.spillWG.Wait()` so every
in-flight encode installs (durable on a healthy disk) — see the exact sequence in Task 7D.

> **`spillForTest` stays synchronous** — it already runs `s.spill(tableId)` via `RunFunc`, which now
> uses the inline `spill` (detach+encode+install on the worker). So every existing test that calls
> `spillForTest` then asserts `SegmentsForTest()` keeps passing — the `spilling` tier is empty again by
> the time `spillForTest` returns. The async path is exercised only by the new F tests + `idxbench`.

- [ ] **Step 3 — The CRITICAL B1 zero-concurrency silent-corruption test.**

`spill_offworker_test.go` — the gate. Block the encode via a hook, re-post on the SAME worker, assert
the dropped keyword is tombstoned:

```go
package invertedstore

import (
	"testing"
	"time"

	"github.com/codetrek/haystack/core/queue"
)

// B1: with the encode of a detached head blocked, a re-post of the SAME doc on the worker must diff
// against the doc's keywords IN THE SPILLING TIER (forwardKeywords reads spilling) and tombstone the
// dropped keyword. If forwardKeywords ignored spilling, "beta" would resurrect — ZERO concurrency.
func TestSpillF_B1_RepostAfterDetachTombstonesDropped(t *testing.T) {
	q := queue.NewMpsc("spillf-b1")
	q.Start()
	s, err := Open(t.TempDir(), q, Options{CapBytes: 64, MaxInflightSpills: 2})
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := s.CreateTable("files")

	release := make(chan struct{})
	encoded := make(chan struct{}, 1)
	encodeSpillBlock = func() { select { case encoded <- struct{}{}: default: }; <-release }
	t.Cleanup(func() { encodeSpillBlock = nil; close(release) })

	// Post doc 1 with [alpha,beta]; a tiny CapBytes forces a detach via the async path (encode parks).
	s.Update(tbl, 1, []string{"alpha", "beta"})
	select {
	case <-encoded:
	case <-time.After(5 * time.Second):
		t.Fatal("detached-head encode never started (no async detach happened)")
	}
	// Re-post doc 1 dropping "beta" — on the worker, while the old head is parked in spilling.
	s.Update(tbl, 1, []string{"alpha"})
	s.q.RunFunc(func() error { return nil }) // drain the apply
	close(release)                            // let the encode + install finish
	s.q.RunFunc(func() error { return nil })

	// "beta" must NOT be searchable for doc 1 (it was tombstoned because the re-post saw the spilling set).
	if got := searchDocidsForTest(t, s, tbl, "beta"); len(got) != 0 {
		t.Fatalf("beta resurrected for %v — forwardKeywords did not consult the spilling tier (B1)", got)
	}
}
```

(Add `var encodeSpillBlock func()` fired at the top of `encodeSpill`, nil in prod.)

- [ ] **Step 4 — Run; verify it fails; implement 7B; re-run until the B1 test passes.**

Expected initial FAIL: until `applyBatch` uses `dispatchSpill` AND `forwardKeywords` consults
`spilling` (7A), the re-post diffs against an empty `old`. Implement 7B; the B1 test must go GREEN.
This is the hardest gate — do not proceed until it passes deterministically (run `-count=20`).

- [ ] **Step 5 — Commit 7B.** `feat(invertedstore): detach head + encode spill off the worker (F)`

### Task 7C — spilling-head docid-range skip (so F does not undo B)

- [ ] **Step 1 — `headForwardRange` + the skip + test.**

`spilling.go`:

```go
// headForwardRange is the docid span of a head's forward records (live fwd + delForward) — the
// spilling-head analog of segMeta's [MinDocid,MaxDocid] (B). An empty head ⇒ the always-skip range.
func headForwardRange(h *headTable) (min, max int64) {
	min, max = emptyDocidRange()
	note := func(d int64) {
		if d < min { min = d }
		if d > max { max = d }
	}
	for d := range h.fwd {
		note(d)
	}
	for d := range h.delForward {
		note(d)
	}
	return
}
```

Wire the skip in `forwardKeywords`'s spilling loop (the comment placeholder from 7A Step 3):
`if docid < e.minDocid || docid > e.maxDocid { continue }`. (Search/GetDocs are prefix/keyword reads,
not single-docid — no range skip there.) Replace the 7A `injectSpillingHeadForTest` stub's
full-span with the real `headForwardRange`.

Test: inject two spilling heads with disjoint docid ranges; a `forwardKeywordsForTest` for a docid in
one range must not scan the other head. **The existing `onForwardProbe` hook observes only SEGMENT
probes — add a distinct `onSpillingProbe func()` fired in `forwardKeywords`' spilling loop (just
before `headForwardLookup`, after the range check passes) + an `installSpillingProbeCounter` test
seam** (do NOT reuse the non-existent `forwardProbeHook`). Confirms F keeps B's O(1)-on-cold-build
property on the head axis. Commit:
`perf(invertedstore): docid-range skip for spilling heads (F, keeps B)`.

### Task 7D — Close drain + crash/orphan consistency

- [ ] **Step 1 — Drain in-flight spills at Close; crash test.**

**`CloseAndWait` drain — EXACT ordering (cross-review R2 BLOCKER-1: `spillWG.Wait()` on the worker
deadlocks against the in-flight install `RunFunc`).** The Wait MUST run on the Close CALLER goroutine
while the worker is still draining `m.q`, never inside a worker `RunFunc` task. Sequence:

```go
func (s *Store) CloseAndWait() {
	s.q.RunFunc(func() error { /* final head flush: spill every non-empty head SYNCHRONOUSLY */ })
	s.spillWG.Wait() // CALLER goroutine: worker still alive + draining, so each in-flight install
	                 // RunFunc lands and every dispatch goroutine reaches Done(). NEVER on the worker.
	s.stopMergeLoop() // safe now: encodes done; a triggerMerge raised during the drain is caught by drainMerge
	// ... existing: lock, publish emptySnapshot, retireKeepFile each segment ...
}
```

A dispatch goroutine raises `triggerMerge(false)` after its install + before `Done()`, so a merge may
be signaled during the drain; `stopMergeLoop` AFTER `Wait()` (worker still alive) catches it via
`drainMerge`. **Add a Close-drain test (Task 7E):** dispatch a spill, block its encode via
`encodeSpillBlock`, call `CloseAndWait` from a goroutine, release the encode, assert `CloseAndWait`
RETURNS within a timeout AND the doc is durable on reopen — the 7D crash test does NOT exercise the
clean-Close drain.

On a CRASH (no clean Close), a detached-but-not-installed head is volatile (lost, like today's
unspilled head — indexer replay recovers it) and its reserved-id file is an orphan swept by **G**
(Task 6). **Extend `dropHeadCloseSegmentsForTest` (cross-review): the crash stub must abandon in-flight
encode goroutines without hanging** — it must NOT `spillWG.Wait()` (that would wait out the very
encodes the crash is meant to lose); drop the head map, stop the merge loop, retireKeepFile the
segments, and let any in-flight encode goroutine finish into the torn-down store harmlessly (its
`RunFunc(install)` returns once the queue stops; assert no panic on a stopped queue). Test: dispatch a
spill, block the install, simulate crash → reopen → the doc is absent (volatile) AND no orphan
`seg-*.dat` remains (G swept it) AND the store is consistent (differential vs a re-applied reference).
Commit: `fix(invertedstore): drain in-flight spills on Close; F crash-consistency`.

### Task 7E — full `-race` atomicity stress + acceptance measure

- [ ] **Step 1 — B2/B3 atomicity + bound + ordering, all under `-race`.**

`spill_offworker_test.go` add: (B2/B3) concurrent `Update`s + `Search`es while encodes are
blocked/unblocked, asserting a doc is NEVER invisible across the detach→install window (a Search for
its keyword finds it the whole time) and ids never collide. (bound) a fast producer with the encode
artificially slowed never exceeds `MaxInflightSpills` detached heads (peak `len(s.spilling)` ≤ bound
via an export_test accessor; the rest take the synchronous fallback) — and never deadlocks. **(install-
failure bound — R2 BLOCKER-2)** a forced-install-failure (`beforeManifestFsync` errors N times) must
keep `len(s.spilling)` bounded (the slot is held, not leaked) and the entry stays read-correct; once
the failure clears, it installs. **(queue saturation — R2 BLOCKER-2/MAJOR)** a variant that floods the
depth-100 mpsc queue with producer `AddFunc`s WHILE an off-worker encode's install `RunFunc` is
pending must still drain (no wedge). **(B3 publish-then-remove — R2 MAJOR-1)** a reader spinning on a
doc's keyword across the detach→install handoff finds it in EVERY snapshot. **(clean-Close drain — R2
BLOCKER-1)** `CloseAndWait` with a blocked-then-released encode RETURNS within a timeout + the doc is
durable on reopen. (ordering) two in-flight spills install in detach order. Run
`go test -race ./invertedstore/ -run TestSpillF -count=10` → clean.

- [ ] **Step 2 — Whole-suite gates + read-regression + acceptance measure; commit.**

`cd core && GOWORK=off go test -race ./invertedstore/` clean; `go-cov` TOTAL ≥ 90%; whole-workspace
(`make coverage` root AND `cd core && go-cov` — both gate, per the go-cov gotcha). `idxbench` final
build: capture a CPU profile and confirm **NEITHER merge NOR spill encode is on the worker**; the
worker is `addPosting` (~12–14s post head-fix) + ms installs + the `spilling`/forward reads. **Confirm
the producer (`tLoad` + `Update` keyword copy + `Commit`) is < the worker time** (spec §10 — else the
producer is the new floor). **READ-REGRESSION + DISK (cross-review R2 MAJOR-3 — F's three-tier read
adds per-read work):** add a Go benchmark (`BenchmarkSearch`/`BenchmarkForwardKeywords` over a built
index with N sealed segments) run BEFORE F (capture a baseline ns/op) and AFTER F; assert no material
regression on the steady-state read path (spilling empty in steady state ⇒ the tier loop is a cheap
`len(s.spilling)==0` skip). Record on-disk size (`du -sb` the store dir) after F vs the ~240 MiB
baseline. Record the final build (~25–32s target, measured). Commit:
`perf(invertedstore): F complete — residual spill encode off the worker`.

---

## Acceptance criteria (spec §10) — checked after F

- [ ] `idxbench -impl=store -batch=1` full lx build measured + reported after EACH task (no asserted
  numbers in code). Trajectory: 95s → F0 −9 → head-fix −5–8 → B −6 → A −30 (off-worker) → C/E → F
  drain residual ~17 → worker ≈ addPosting ~12–14 + ~1s installs. Realistic **~25–32s** (measured).
  Bar: "nothing reducible left on the worker."
- [ ] **Producer is not the new floor:** at a ~20s worker, `tLoad` + `Update` keyword copy + `Commit`
  < the worker time — measured and reported.
- [ ] Build CPU profile after F: NEITHER merge NOR spill encode on the worker; worker dominated by
  `addPosting` + ms installs + the `spilling`/forward read. GC cycles + peak heap down.
- [ ] `hits` identical (**2,414,505**); `-race` clean; disk unchanged (~240 MiB); search not regressed.
- [ ] Memory bounded: peak in-flight postings ≤ E budget; detached heads ≤ `MaxInflightSpills ×
  CapBytes`.

## Cross-cutting reminders (apply to EVERY task)

- **Build/test:** `cd core && GOWORK=off go test ./invertedstore/` (and `-race` on the concurrency
  items). `idxbench` measurements on **real ext4 (`/workspace`), never tmpfs** (Principle 2). gopls
  "undefined" diagnostics are go.work false-positives — trust the `GOWORK=off` compile.
- **Coverage:** `go-cov` TOTAL ≥ 90%; run BOTH the root `make coverage` AND `cd core && go-cov`
  (separate CI gates; the `cmd/idxbench` harness is untracked and won't reach CI).
- **Commits:** one per item (F in sub-commits 7A–7E). End every commit body with the measured number
  (or "hygiene/no-perf" for G). Commit/push only when the user asks.
- **Never** `git stash`/`clean` in this shared worktree; if a task needs a clean tree for a benchmark,
  use `git worktree add --detach <ref>`.

## Self-review (writing-plans)

- **Spec coverage:** F0 (§4a)→T1; head-fix/C.0 (§5.0)→T2; B (§4)→T3; A (§3,§8)→T4; C.1/C.2/C.3 (§5)
  + E (§7)→T5; D (§6)→decision, no task (correct); G (§7b)→T6; F (§7a) + its 3 BLOCKERs + bound→T7A–E.
  All §2 rows covered.
- **Sequencing:** matches spec §11 (F0 → head-fix → B → A → C/E → G → F); each item independently
  measured + committed; F last + gated hardest on B1.
- **Found during breakdown (raise in cross-review):** the spec §7a M5 *"worker blocks the detach"*
  deadlocks against worker-side install; Task 7B uses the deadlock-safe synchronous-fallback bound.
- **Type consistency:** `spillEntry`, `headForwardLookup`, `headForwardRange`, `detachHeadLocked`,
  `encodeSpill`, `installSpill`, `dispatchSpill`, `spill` (sync), `mergePlan`, `segsByIdsLocked`,
  `pickLowestQualifyingLevelLocked`, `deadFractionLocked`, `selectTieredMergePlan`/
  `selectCoveringMergePlan`/`runMergePlan`, `postingBudget`, `sweepOrphanSegments`/`parseSegFileName`,
  `coversDocid`/`emptyDocidRange` — names used consistently across tasks. Test harness uses the real
  `queue.NewMpsc(name).Start()` + `Open(dir, q, Options{})` + `CreateTable` pattern (matched to
  `store_test.go`/`merge_test.go`), NOT an invented `openTestStore(Options)`.

## Cross-review resolutions (R1 — 3 independent reviewers: spec/TDD, concurrency, code-accuracy)

**BLOCKERs (all fixed inline above):**
- **`searchDocidsForTest` did not exist** (all 3 reviewers) — used by the Task 5 C.1 test AND the
  Task 7B B1 corruption gate. Now defined concretely in Task 5 Step 1 via `GetDocs` (exact keyword),
  landed before its first use.
- **F0 fake red** (reviewer 1) — Task 1 now drives a GENUINE red via a `finishDictReread` counter
  (`> 0` before inlining, `0` after) + the byte-identity oracle + round-trip as the safety net.
- **F deadlock fix was itself a deadlock** (reviewer 2, BLOCKER-1/-3) — `dispatchSpill` rewritten:
  reserve a `spillSem` slot NON-BLOCKINGLY *before* detach; overflow → synchronous `spill` (no channel
  send by the worker, no detach-before-slot). Per-dispatch goroutine does the blocking install RunFunc.
- **idxbench commands missing required `-tokens`/`-data`** (reviewer 3) — canonical command fixed;
  note added that all later invocations carry them (real ext4 `-data`).

**MAJORs (fixed/documented inline above):**
- **Queue-saturation + RunFunc-install** (reviewer 2, BLOCKER-2) — E sequenced BEFORE A and F (order
  override in §Sequencing); a queue-saturation `-race` stress added to Task 7E. The worker never waits
  on the goroutine, so it is latency, not deadlock.
- **Covering-hook parity + `liveTables` staleness** (reviewers 2/3) — hook moved to fire post-install
  in `runMergePlan` (counts COMPLETED covering merges); staleness window documented + a DeleteTable-
  racing-covering test required (Task 4).
- **Off-worker race coverage + `waitMergeIdle` fence** (reviewers 1/2) — Task 4 Step 5 now requires a
  NEW race test (concurrent Update/Search while the compute is held open) + a slow-install
  `waitMergeIdle` convergence test, not a re-run of pre-A tests.
- **Task 3 Step 8 onForwardRead audit** (reviewers 1/3) — replaced the vague "audit and update" with
  the named verdict: both existing tests stay green AS-IS; do not relax them.
- **Task 7A Step 4 prose-only tier wiring** (reviewers 1/3) — a concrete `Search` insertion snippet
  added; GetDocs/ForwardDocids shapes specified; one test per path required.
- **Crash stub vs F pool** (reviewer 2) — Task 7D now specifies `dropHeadCloseSegmentsForTest` must
  abandon in-flight encode goroutines without `spillWG.Wait()` (else it waits out the lost encodes).

**MINORs (resolved here):**
- **Task 1 line refs** — `finish` is segment.go:149–178, `writeTermDict` is 180–229 (the intro's
  "~185–228" is the stale cite); the inline change replaces finish's term-dict block (152–156).
- **Task 5 C.1 is a regression guard, not a feature test** (reviewer 1) — add a hook/assertion that
  the 1-op FAST PATH is actually taken for `len(ops)==1` (e.g. the `inBatch`/`seen` maps are not
  allocated), so the optimization itself is covered, not just its behavior.
- **`injectSpillingHeadForTest` burns a NextSegId** (reviewers 1/2) — intentional; the stubbed entry
  is never installed (no file written), so G's sweep finds no orphan for it and id-gaps are benign.
  Note this in the helper so id-ordering assertions tolerate the gap.
- **F M2 read-only invariant** (reviewer 2) — add an explicit invariant note + a `-race` assertion:
  once a head is detached, nothing mutates its `inv`/`fwd`/`delForward` maps or their slices (safe
  because `op.keywords` is defensively copied at `Update`/`Batch.Update`); the off-worker `encodeSpill`
  + readers only read it. This also bounds the C.2/C.3 deferral (Task 5) — its `-race` must cover the
  detached-head encode path.
- **Acceptance: search-not-regressed + disk size** (reviewer 1) — add a step that measures `Search`/
  `forwardKeywords` latency and on-disk size after F (the three-tier read adds work to every read).
- **Task 2 `headDelsNilForTest` accessor is unused** (reviewer 3) — the shown tests use a local
  `headTable` directly; DROP the unused Store accessor (or have a test use it) to avoid dead code.

**Unchanged-and-confirmed:** A's refcount lifecycle is balanced on all paths (reviewer 2 traced
success + both failure rollbacks); F0 byte-identity, B's docid-range compute, the `min[5:13]` docid
extraction, the `upgradeSegmentRanges` placement, and the bulk of line/signature refs all check out
against the real source (reviewer 3).



## Cross-review resolutions (R2 — re-review of the R1 fixes; 3 reviewers)

The R1 fixes were re-reviewed (per AGENTS.md Principle 0: re-review until clean). R2 confirmed all R1
BLOCKER fixes compile + are correct, but found that the deadlock fix MIGRATED the cycle into Close,
plus new dead-code/test-gap issues. All BLOCKERs + MAJORs below are fixed inline above.

**BLOCKERs (fixed):**
- **`CloseAndWait` deadlock** (concurrency reviewer) — `spillWG.Wait()` on the worker would deadlock
  against the in-flight install `RunFunc`. Task 7D now pins the EXACT sequence: flush (worker) →
  `spillWG.Wait()` on the CALLER goroutine (worker still draining) → `stopMergeLoop` → teardown; + a
  clean-Close drain test in 7E.
- **Off-worker install-failure strands the spilling entry forever** (concurrency reviewer) — leak +
  read-path corruption. Task 7B's dispatch goroutine now RETRIES the install (bounded
  `MaxInstallRetries`), releases the slot ONLY on success, and HOLDS the slot on give-up (backpressure,
  no leak); + an install-failure-bound test in 7E.

**MAJORs (fixed):**
- **`installSpill` publish-then-remove ordering** (concurrency reviewer) — pinned: one `s.mu.Lock`,
  append segs → publish → remove-from-spilling, in that order (the LOST direction forbidden); + a B3
  spinning-reader test in 7E.
- **`maybeMerge`/`maybeCoveringMerge` become dead code after A → go-cov < 90%** (spec reviewer) —
  Task 4 now explicitly DELETES both (gates re-implemented in `selectTiered/CoveringMergePlan`) + fixes
  the stale comments.
- **C.1 fast-path-taken untested** (spec reviewer) — added `applyFastPathTaken` hook +
  `TestApplyFastPath_TakenForOneOpNotMultiOp` (fires for n=1, not for multi-op).
- **No search-regression / disk measurement step** (spec reviewer) — Task 7E Step 2 now benchmarks
  Search/forwardKeywords before/after F + records `du -sb` disk vs the ~240 MiB baseline.
- **forwardKeywords recursive-RLock** (concurrency reviewer) — caution added: the spilling loop stays
  in the same RLock; `acquireSnapshot` (re-locks) only AFTER `RUnlock`, never nested.

**MINORs (fixed inline / explicit instruction):**
- Task 7C referenced the non-existent `forwardProbeHook` — corrected to a NEW `onSpillingProbe`
  observer (the existing `onForwardProbe` sees only segment probes).
- Task 7A ForwardDocids tier wiring — concrete code added (was prose; the most error-prone path).
- Task 2's unused `headDelsNilForTest` accessor — removed (tests inspect a local `headTable`).
- **`searchDocidsForTest` needs `"sort"` added to `export_test.go`'s import block** (currently
  `strconv`/`sync/atomic`/`testing`) — do it when landing the helper (Task 5 Step 1).
- **F0 test:** drop the unused `"encoding/binary"` import + its `_ = binary.BigEndian` guard line
  (dead weight; the test doesn't need `binary`).
- **B1 test:** add `if len(s.spilling) == 0 { t.Fatal("async detach didn't happen") }` right after
  `<-encoded`, so a future head-accounting drift that silently takes the SYNC fallback fails loud
  instead of passing for the wrong reason.
- **`encodeSpillBlock` sharp edge:** the hook fires in `encodeSpill`, which the SYNC `spill` also
  calls — any test leaving it non-nil while a `CloseAndWait`/`spillForTest` runs will block. The hook
  MUST be cleared/released before any synchronous spill (the B1 test's `t.Cleanup` already does).
- **File-map table** — add `search.go` (Task F) and `spilling.go` (new, Task F) rows; the per-task
  "Files:" lists are already correct.

**Confirmed-correct (no change):** the `dispatchSpill` slot/WG balance on all three exits; the B1
gate deterministically forces the async path at `CapBytes:64` (head bytes ≈65 ≥ 64 on op 1); the
covering-hook has no double-count (disjoint sync vs off-worker paths); A's refcount lifecycle balances
on success + both failure rollbacks; F0's genuine red; all R1 idxbench/helper/snippet identifiers.

## Cross-review resolutions (R3 — re-review of the R2 fixes)

R2's fixes introduced new code, so they were re-reviewed (2 reviewers). All R2 concurrency fixes
(CloseAndWait off-worker Wait, publish-then-remove ordering, maybeMerge deletion, recursive-RLock,
three-tier newest-wins) were CONFIRMED-CORRECT. Two MAJORs in the R2 deltas, fixed inline:
- **C.1 `applyOneOp` signature contradiction** — prose said `(over bool, err error)` but both call
  sites need `error`-only, and "lines 122–174 verbatim" wrongly included the `inBatch`/`seen` loop
  bookkeeping. Corrected to `applyOneOp(...) error` (spill inside) with the bookkeeping explicitly left
  in the multi-op loop.
- **Give-up durability claim false** — `CloseAndWait` flushes only `s.head`, never `s.spilling`, so a
  persistently-failing install's stranded entry is NOT "drained by Close". Reclassified as
  crash-equivalent volatile loss (disk-failure-only; healthy disk retry-succeeds before `Wait()`
  returns, so clean Close IS durable). Removed the false claim.

MINORs fixed: `MaxInflightSpills`/`MaxInstallRetries` now have concrete `withDefaults` (a zero
`MaxInstallRetries` would no-op the retry → instant strand); `installBackoff` const value + the `"time"`
import in `head.go` pinned; the file-map `maybeMerge*` footnote corrected.

**Confirmed-correct (no change):** the retry-loop slot/WG balance (success releases + Done; give-up
holds slot + Done, bounded ≤ MaxInflightSpills heads); the CloseAndWait sequence is deadlock-free
(worker alive + draining during the caller-side Wait; queue stopped by the caller only after Close
returns); `installSpill` pointer-identity removal under the same lock as both installs (serialized);
`maybeMerge`/`maybeCoveringMerge` have exactly one caller each and `deadFraction` stays live via
`DeadFractionForTest`; `TestApplyFastPath_TakenForOneOpNotMultiOp` + the chainable `NewBatch().Update().Update().Commit()`
compile; the ForwardDocids tier code matches reconcile.go's real structure.

## Next SDD stage

The breakdown has been through four multi-agent cross-review rounds. **R4 is CLEAN — two independent
reviewers each returned "zero Blocking/Major, ready to implement."** The review loop has converged
(R1 found a spec deadlock + missing helper + fake red; R2 found the deadlock fix migrated the cycle
into Close + a stranding leak + dead-code cov breaks; R3 found a signature contradiction + a false
durability claim; R4 confirmed all corrections are consistent and compile). Per AGENTS.md Principle 0
this satisfies stage 4 (cross-review until clean). It is ready for **stage 5 — TDD implementation**,
order **F0 → head-fix → B → E → A → C.1/C.2/C.3 → G → F**; each item red→green, `-race` on the
concurrency items, measured on real ext4, committed independently. F lands last and is gated hardest
on the B1 corruption test + the `-race` atomicity/bound/queue-saturation/Close-drain stress.
