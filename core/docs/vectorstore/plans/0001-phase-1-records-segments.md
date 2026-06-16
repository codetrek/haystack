# Phase 1 Implementation Plan — `core/vectorstore` (segmented records layer + brute search)

> **Scope contract (architecture.md §8.1):** a NEW package `core/vectorstore` providing a *segmented records layer* (vectors in metric-natural form + norm + payload + slot→docId array + tombstone bitmap) + a *single head segment* with *pure brute-force, synchronous* search, fronted by `idtable` for string↔docId, crash-recoverable via a WAL. NO HNSW graph, NO sealing, NO merge/compaction, NO attribute filtering, NO multi-index. The existing `core/vectorindex` package STAYS UNTOUCHED.

---

## Goal

Ship a new, crash-safe `core/vectorstore` package whose `Store` supports `Put(id,vec,payload)` / `Get(id)` / `Delete(id)` / `Search(q,k)` against a single in-memory head segment backed by a write-ahead log, reusing the metric-natural storage form and WAL/fs primitives proven in `core/vectorindex`. **Each `Put`/`Delete` is durable on return** (architecture §4.8), including the string→docId mapping, and the head is rebuilt exactly on reopen by replaying the WAL — even after an unclean crash.

## Architecture

A `Store` owns one in-memory `segment` (the "head") plus an `idtable.Allocator` (string id → stable int64 docId, backed by a `kv.Store`). The **WAL is the single crash-safe source of truth for both the records and the id↔docId mapping**: each `Put` records the string id *and* its docId in the WAL payload and fsyncs before mutating any in-memory state. `Search` prepares the query once, brute-scans every live (non-tombstoned) slot computing `metric.distance`, and returns the top-k via a size-k max-heap; `Get` restores the raw vector from the stored norm. On open, the WAL is replayed in order to rebuild the head segment, re-drive the allocator (reconstructing the exact same monotonic docIds the original run assigned), and rebuild the derived `docId→slot` and `id→docId` maps.

## Tech Stack

- **Language:** Go. Module `github.com/codetrek/haystack/core` (`go 1.23.0`, no toolchain pin); the workspace/root pins toolchain `1.24.2` (`go.work`, root `go.mod`).
- **New package:** `github.com/codetrek/haystack/core/vectorstore` (under the existing `core` module — already covered by `go.work use ./core`, **no go.work edit needed**).
- **Reused deps:** `github.com/viterin/vek/vek32` (SIMD dot/norm/distance), `github.com/codetrek/haystack/core/idtable` (string→docId), `github.com/codetrek/haystack/core/kv` + `core/kv/pebblekv` (KV store for idtable; tests use a temp pebble dir, `pebblekv.Open(path string, cacheSize int64)`).
- **Test/quality gates:** `go test ./...`; the coverage gate is `go-cov` v0.1.2. **The gate's per-function (80) / package (85) / total (90) CRITICAL floors are enforced ONLY when go-cov runs in CI mode** (`CI=true` env, or the `-ci` flag). Locally we run `go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.2 -ci` so the local check matches CI exactly; a bare invocation returns 0 even on a sub-threshold module and gives a false green. Repo-wide `gofmt -l` (App job's `check-fmt.sh`, which DOES walk `core/`).

---

## CI / build wiring (read before starting — corrects a common misconception)

`/workspace/haystack/.github/workflows/ci.yml` has two jobs:

- **Core (ubuntu / macos / windows):** all steps run with `working-directory: core` (the `core` module). On ubuntu it runs the **go-cov gate**; on macos/windows it runs `go test -v ./...`. **This is the ONLY job that compiles, tests, and coverage-gates `core/vectorstore`.** Neither leg runs `-race`, so do not claim CI proves race-freedom (we still run `-race` locally as hygiene).
- **App (ubuntu):** runs from the **repo root** module (`github.com/codetrek/haystack`): `check-fmt.sh` (gofmt over `find . -name '*.go'`, which **does** include `core/`), then root `go build ./...` and `test_and_coverage.sh`. Root `./...` walks only the **root** module's packages — it does **NOT** compile `core/vectorstore` (a different module). So **the App job enforces only repo-wide gofmt on the new files; all functional + coverage gating is the Core job.**

Consequences baked into the tasks below:
- Run build/vet/test/go-cov with `cd /workspace/haystack/core && ...` (Core module).
- Run `gofmt -l` from repo root over the new dir (App parity).
- No `ci.yml` edit and no `go.work` edit are required; the package is auto-discovered by the Core job's `go build ./...`, `go test ./...`, and the `./...` go-cov glob.

---

## Key decisions (made up-front so tasks have no ambiguity)

1. **Metric machinery is DUPLICATED into `core/vectorstore`, not imported.** `Metric.prepare/restore/norm/distance` are unexported methods on `vectorindex.Metric` and cannot be called cross-package; the prompt forbids touching `vectorindex`, so we copy the metric files (preserving the `//go:build` arch split and the "call `vek32.Dot`/`vek32.Distance` directly" coverage trick). `vectorstore.Metric` is a distinct type. **We copy only what Phase 1 uses** — `storesNormalized()` is dropped from the copy because nothing in Phase 1 calls it and an uncovered 0%-function would sink the per-function go-cov floor.

2. **Head segment is IN-MEMORY in Phase 1; durability is the WAL.** Per §4.8 "records-段立即耐久 … 恢复 = 重放 head WAL 重建 head 内存态". We do NOT build the mmap segment file in Phase 1 (that is the §8.1 Phase-2 "封存→sealed records-段文件" deliverable). The head's source of truth on crash is the WAL; in-memory state is rebuilt by replay. Phase 1 is mmap-free and trivially cross-OS.

3. **Tombstone = packed bitmap** (architecture §4.1 "deletes: 段内 tombstone 位图"), not a per-slot flag byte.

4. **`Search` returns `[]SearchResult{DocID int64, Distance float32}`** (docId-land, matching §4.4/§7 and `vectorindex.SearchResult`). idtable has no reverse map. **Known Phase-1 limitation:** results are in docId space; mapping docId→string is the caller's responsibility because idtable has no reverse map. If string-id results are a Phase-1 acceptance requirement, that is a *scope addition* — confirm with the spec owner before shipping. (Documented in `doc.go` and Done-criteria.)

5. **Payload IS in Phase 1.** Architecture §8 lists payload under both Phase 1 ("payload + slot→docId") and a standalone Phase 3 line; the §8.1 scope string and this task's authoritative IN-scope list both enumerate payload, so it ships now. The §8 standalone "payload" phase is subsumed because §8.1 folds payload into the records layer (noted in `doc.go` so a later reader does not think Phase 3 regressed). Payload is an opaque `[]byte` stored alongside the slot.

6. **Delete = tombstone only** (no space reclaim — deferred to Phase 4). `Get`/`Search` skip tombstoned slots. The id↔docId mapping persists (no reverse-delete); re-`Put` of the same string reuses the same docId. **`Delete`/`Get`/`Search` of an unknown id never allocate a docId** — they consult the in-memory `idToDoc` map (rebuilt from the WAL), so reads/deletes of never-seen ids are pure no-ops with zero KV writes. Only `Put` allocates.

7. **Upsert is crash-atomic.** `Put` of an existing id WAL-appends ONE combined record carrying the string id, docId, the old slot to tombstone (−1 if new), and the new stored vector+norm+payload. The record is fsynced before any in-memory mutation, so a crash either loses the whole `Put` or applies it whole on replay.

8. **Two-level id (§4.6) is honored; the global level is intentionally degenerate in Phase 1.** §4.6 defines a two-level model: a **global `docId→segId`** map plus a **per-segment `docId↔slot`** map. In Phase 1 there is exactly one segment (the head), so the global level degenerates to the constant `{every docId → head}` and is **intentionally NOT materialized** (it is the identity). `segment.docToSlot` IS the per-segment level. Phase 2 introduces the real global map when sealing produces >1 segment. This closes §4.6 traceability without adding out-of-scope code.

9. **The WAL is the single crash-safe source of truth for the id↔docId mapping** (corrects the durability gap). `idtable.Allocator.GetId` only writes to an in-memory batch committed every 5s or on `Close` — so on an unclean crash the WAL would reference a docId whose string mapping was never persisted, orphaning the record. Fix: **store the string id in every `recPut`/`recDelete` payload.** On replay, drive `alloc.GetId(id)` for each record in WAL order; because allocation is strictly monotonic (`nextId` starts at 1 and `++`s per new key) and the WAL preserves insertion order, replay reconstructs the **exact same docIds** the original run assigned, and re-populates the allocator (made durable on the next `Close`/commit). The collection also keeps an in-memory `idToDoc` map rebuilt during replay so reads never re-allocate. This keeps idtable's allocator authoritative for `nextId` **and** makes `Put`-return durable per §4.8, with **no idtable API change**.

10. **Public facade is named `Store`, not `Collection`.** `core/collection` already exports a `Collection` type (the document-collection catalog entry); a second, semantically-different exported `Collection` in the same module is a readability hazard for Phase 2 wiring (where `collection/catalog` will likely own a vectorstore handle). The facade is `vectorstore.Store`, matching the package name.

---

## File Structure

Every file is under `/workspace/haystack/core/vectorstore/`. Each has exactly one responsibility.

| File | Responsibility |
|---|---|
| `metric.go` | `Metric` type + `Cosine/DotProduct/Euclidean` consts; `String/norm/prepare/restore` — copy of `vectorindex/metric.go` minus `storesNormalized` (unused in Phase 1). Storage form §3. |
| `metric_distance_amd64.go` | `(Metric).distance` for amd64 (`//go:build !arm64`) via `vek32.Dot`/`vek32.Distance` (copy, build tag). |
| `metric_distance_arm64.go` | `(Metric).distance` for arm64 (`//go:build arm64`) via NEON `dot()` (copy, build tag). |
| `dot_arm64.go` / `dot_arm64.s` | NEON `dot()` kernel for arm64 (copy of `vectorindex/dot_arm64.{go,s}`). arm64-only. |
| `validate.go` | `validateVector(v, dim, metric)` input guard — **reimplemented as a free function** mirroring the cosine checks in `HNSWIndex.validateVector` (hnsw.go:131; there is no free function to lift). |
| `bitmap.go` | `bitmap` packed tombstone bitset: `set/get/count`. |
| `result.go` | `SearchResult{DocID int64; Distance float32}` + size-k max-heap `topK`. |
| `osfile.go` | `osFile` interface + the single injectable `fsOpenFile` the WAL needs (fault-injection seam). Trimmed copy — drops `fsCreate/fsOpen/fsRename/fsRemove/fsyncDir`, none used in Phase 1. |
| `wal.go` | The append-only CRC WAL (`WAL` struct, `OpenWAL/scanLSN/Append/Sync/Reset/Close/Replay`) ported from `vectorindex/mmap_wal.go`'s framing, plus the records `recType` enum and `putRecord`/`deleteRecord` encode/decode. Unused HNSW WAL methods (`Flush/LSN/SeedLSN`) and record types are dropped. |
| `segment.go` | `segment`: in-memory head — per-slot stored vector + norm + payload + `slotDoc []int64` + `tomb bitmap` + `docToSlot map[int64]int`. Methods: `append`, `tombstone`, `slotOfDoc`, `read`, `eachLive`. |
| `store.go` | `Store` facade: `Open`, `Close`, `Metric`, `Put`, `Get`, `Delete`, `Search`. Owns idtable + segment + WAL + `idToDoc`; drives replay on open. |
| `doc.go` | Package overview + Phase-1 scope/limitations. |
| `metric_test.go` | `prepare/restore/norm/distance/String` round-trip (cosine + dot + euclidean + zero vector). |
| `validate_test.go` | `validateVector` accept/reject cases. |
| `bitmap_test.go` | bitmap set/get/grow/count. |
| `result_test.go` | `topK` ordering / capacity. |
| `wal_test.go` | record encode/decode round-trip + WAL append/replay/reset + fault-injection (via `osFile` seam). |
| `segment_test.go` | segment append/tombstone/slotOfDoc/read/eachLive. |
| `store_test.go` | Put/Get/Delete/Search happy + error paths, upsert-replace, Get-aliasing safety, clean-reopen, **unclean-crash replay**, cosine norm-reject, error-branch coverage. |
| `helpers_test.go` | Test helpers: `approxEqual`, `requireNoError`, `faultFile` + `withOpenFileFault`, `newTestKV`, `openTestStore`, `bruteForceKNN` oracle. |

---

## TDD tasks

Each task is 2–5 minutes. Every code step shows complete code. Run all commands from `/workspace/haystack`. The per-package test command is:

```
cd /workspace/haystack/core && go test ./vectorstore/...
```

> **Per-commit green policy:** PRs here are squash-merged, so intermediate commits need not all pass go-cov; however each task is structured so the *whole package is `go test`-green at every commit*, and the package is **go-cov-green from Task 15 onward**. Production code is never committed without a test that covers it in the same commit (no write-then-delete smoke tests).

Make a branch first (you are on `main`):

```
git checkout -b feat/vectorstore-phase1
```

---

### Task 1 — Create package + copy metric machinery (storage form §3)

**Create:** `core/vectorstore/metric.go`, `metric_distance_amd64.go`, `metric_distance_arm64.go`, `dot_arm64.go`, `dot_arm64.s`
**Test:** `core/vectorstore/metric_test.go`

**(1) Write the FAILING test** — `core/vectorstore/metric_test.go`:

```go
package vectorstore

import "testing"

func approxEqual(a, b, eps float32) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= eps
}

func TestMetric_PrepareRestore_Cosine(t *testing.T) {
	m := Cosine
	v := []float32{3, 4} // norm 5
	stored, norm := m.prepare(v)
	if !approxEqual(norm, 5, 1e-5) {
		t.Fatalf("norm = %v, want 5", norm)
	}
	if !approxEqual(stored[0], 0.6, 1e-6) || !approxEqual(stored[1], 0.8, 1e-6) {
		t.Fatalf("stored = %v, want unit [0.6 0.8]", stored)
	}
	got := m.restore(stored, norm)
	if !approxEqual(got[0], 3, 1e-4) || !approxEqual(got[1], 4, 1e-4) {
		t.Fatalf("restore = %v, want [3 4]", got)
	}
	if v[0] != 3 || v[1] != 4 {
		t.Fatalf("prepare mutated input: %v", v)
	}
}

func TestMetric_PrepareRestore_Raw(t *testing.T) {
	for _, m := range []Metric{DotProduct, Euclidean} {
		stored, norm := m.prepare([]float32{1, 2})
		if norm != 0 {
			t.Fatalf("%v: norm = %v, want 0", m, norm)
		}
		got := m.restore(stored, norm)
		if got[0] != 1 || got[1] != 2 {
			t.Fatalf("%v: restore = %v, want [1 2]", m, got)
		}
	}
}

func TestMetric_ZeroVector(t *testing.T) {
	stored, norm := Cosine.prepare([]float32{0, 0})
	if norm != 0 || stored[0] != 0 || stored[1] != 0 {
		t.Fatalf("zero vector: stored=%v norm=%v, want zeros/0", stored, norm)
	}
}

func TestMetric_Distance_Cosine(t *testing.T) {
	a, _ := Cosine.prepare([]float32{1, 0})
	b, _ := Cosine.prepare([]float32{0, 1})
	if d := Cosine.distance(a, b); !approxEqual(d, 1, 1e-6) {
		t.Fatalf("orthogonal cosine distance = %v, want 1", d)
	}
	if d := Cosine.distance(a, a); !approxEqual(d, 0, 1e-6) {
		t.Fatalf("identical cosine distance = %v, want 0", d)
	}
}

func TestMetric_Distance_Euclidean(t *testing.T) {
	a, _ := Euclidean.prepare([]float32{0, 0})
	b, _ := Euclidean.prepare([]float32{3, 4})
	if d := Euclidean.distance(a, b); !approxEqual(d, 5, 1e-5) {
		t.Fatalf("euclidean distance([0 0],[3 4]) = %v, want 5", d)
	}
}

func TestMetric_Distance_DotProduct(t *testing.T) {
	a, _ := DotProduct.prepare([]float32{1, 2})
	b, _ := DotProduct.prepare([]float32{3, 4}) // dot = 11 → 1 - 11 = -10
	if d := DotProduct.distance(a, b); !approxEqual(d, -10, 1e-5) {
		t.Fatalf("dot distance = %v, want -10", d)
	}
}

func TestMetric_String(t *testing.T) {
	cases := map[Metric]string{Cosine: "cosine", DotProduct: "dot", Euclidean: "euclidean", Metric(9): "unknown"}
	for m, want := range cases {
		if got := m.String(); got != want {
			t.Fatalf("%d.String() = %q, want %q", m, got, want)
		}
	}
}
```

> Note: no `math` import / `_ = math.Sqrt` keeper — none of these assertions need `math`.

**(2) Run, expect FAIL:**

```
cd /workspace/haystack/core && go test ./vectorstore/...
```

Expected: `no Go files in .../vectorstore` (or, once `metric.go` exists, `undefined: Cosine`).

**(3) Minimal impl** — copy the five files:

```
cp /workspace/haystack/core/vectorindex/metric.go               /workspace/haystack/core/vectorstore/metric.go
cp /workspace/haystack/core/vectorindex/metric_distance_amd64.go /workspace/haystack/core/vectorstore/metric_distance_amd64.go
cp /workspace/haystack/core/vectorindex/metric_distance_arm64.go /workspace/haystack/core/vectorstore/metric_distance_arm64.go
cp /workspace/haystack/core/vectorindex/dot_arm64.go            /workspace/haystack/core/vectorstore/dot_arm64.go
cp /workspace/haystack/core/vectorindex/dot_arm64.s            /workspace/haystack/core/vectorstore/dot_arm64.s
```

Then, via `Edit`, change the first line `package vectorindex` → `package vectorstore` in each of the **four** `.go` files. Then **delete `storesNormalized`** from `metric.go` (the four lines `// storesNormalized reports …` + `func (m Metric) storesNormalized() bool { return m == Cosine }`) — Phase 1 never calls it and an uncovered 0%-function would sink the go-cov per-function floor.

Notes for the implementer:
- `dot_arm64.s` needs **NO edit** — its first line is `//go:build arm64` (not a package clause) and its asm symbol uses the package-relative `·name(SB)` form, so it links under `package vectorstore` unchanged.
- `dot_arm64.{go,s}` are **arm64-only** (build-tagged); on the amd64 CI gate (ubuntu/windows) they compile to nothing and contribute **no coverage lines**, exactly as in `vectorindex` — they do not affect the go-cov bar. The macOS leg (arm64) exercises them.
- `metric_distance_amd64.go` compiles on **amd64 only** (the GOARCH filename constraint ∩ `//go:build !arm64`); the CI matrix exercises both files (ubuntu/windows amd64, macos arm64).

**(4) Run, expect PASS** — and confirm the copy compiles on both arches via build, not a brittle grep:

```
cd /workspace/haystack/core && go build ./vectorstore/... && go test ./vectorstore/...
```

Expected: `ok  github.com/codetrek/haystack/core/vectorstore`.

**(5) Commit:**

```
cd /workspace/haystack && gofmt -w core/vectorstore/ && git add core/vectorstore && git commit -m "feat(vectorstore): metric-natural storage form (copy from vectorindex)"
```

---

### Task 2 — `validateVector` input guard

**Create:** `core/vectorstore/validate.go`
**Test:** `core/vectorstore/validate_test.go`

**(1) FAILING test** — `core/vectorstore/validate_test.go`:

```go
package vectorstore

import (
	"math"
	"testing"
)

func TestValidateVector(t *testing.T) {
	tests := []struct {
		name    string
		v       []float32
		dim     int
		metric  Metric
		wantErr bool
	}{
		{"ok cosine", []float32{1, 2, 3}, 3, Cosine, false},
		{"ok dim-learn (dim 0)", []float32{1, 2}, 0, Cosine, false},
		{"empty", []float32{}, 3, Cosine, true},
		{"dim mismatch", []float32{1, 2}, 3, Cosine, true},
		{"cosine NaN", []float32{float32(math.NaN()), 1}, 2, Cosine, true},
		{"cosine Inf", []float32{float32(math.Inf(1)), 1}, 2, Cosine, true},
		{"cosine zero-norm ok", []float32{0, 0}, 2, Cosine, false},
		{"dot NaN allowed (no norm check)", []float32{float32(math.NaN()), 1}, 2, DotProduct, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateVector(tc.v, tc.dim, tc.metric)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateVector(%v, dim=%d, %v) err=%v, wantErr=%v", tc.v, tc.dim, tc.metric, err, tc.wantErr)
			}
		})
	}
}
```

**(2) Run, expect FAIL:** `undefined: validateVector`.

**(3) Impl** — `core/vectorstore/validate.go`:

```go
package vectorstore

import (
	"fmt"
	"math"
)

// validateVector rejects inputs that would corrupt the segment or panic the SIMD
// kernel, BEFORE any state mutation. dim == 0 means "dimension not yet learned"
// (first Put), so any non-empty length is accepted and becomes the fixed dim.
// For cosine it additionally rejects vectors whose norm is non-finite or too
// small to normalize without 1/norm overflowing to +Inf. A zero vector (norm 0)
// is allowed: prepare stores it as-is with norm 0. Modeled on the cosine checks
// in vectorindex HNSWIndex.validateVector (hnsw.go:131), reimplemented here as a
// free function (no such function exists to lift).
func validateVector(v []float32, dim int, m Metric) error {
	if len(v) == 0 {
		return fmt.Errorf("vectorstore: empty vector")
	}
	if dim != 0 && len(v) != dim {
		return fmt.Errorf("vectorstore: vector dimension mismatch: got %d, want %d", len(v), dim)
	}
	if m == Cosine {
		n := m.norm(v)
		if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
			return fmt.Errorf("vectorstore: cosine vector has non-finite norm")
		}
		// Reject norms so small that 1/norm overflows to +Inf in float32.
		if n != 0 && math.IsInf(float64(1/n), 0) {
			return fmt.Errorf("vectorstore: cosine vector norm %g too small to normalize", n)
		}
	}
	return nil
}
```

**(4) Run, expect PASS.**

**(5) Commit:**

```
cd /workspace/haystack && gofmt -w core/vectorstore/ && git add core/vectorstore && git commit -m "feat(vectorstore): validateVector input guard"
```

---

### Task 3 — Packed tombstone bitmap

**Create:** `core/vectorstore/bitmap.go`
**Test:** `core/vectorstore/bitmap_test.go`

**(1) FAILING test** — `core/vectorstore/bitmap_test.go`:

```go
package vectorstore

import "testing"

func TestBitmap(t *testing.T) {
	var b bitmap
	if b.get(0) || b.get(100) {
		t.Fatal("fresh bitmap must be all-zero")
	}
	if b.count() != 0 {
		t.Fatalf("count = %d, want 0", b.count())
	}
	b.set(0)
	b.set(63)
	b.set(64) // forces growth into a second word
	b.set(200)
	for _, i := range []int{0, 63, 64, 200} {
		if !b.get(i) {
			t.Fatalf("bit %d should be set", i)
		}
	}
	for _, i := range []int{1, 62, 65, 199, 201} {
		if b.get(i) {
			t.Fatalf("bit %d should be clear", i)
		}
	}
	if b.count() != 4 {
		t.Fatalf("count = %d, want 4", b.count())
	}
	b.set(0) // idempotent
	if b.count() != 4 {
		t.Fatalf("count after re-set = %d, want 4", b.count())
	}
}
```

**(2) Run, expect FAIL:** `undefined: bitmap`.

**(3) Impl** — `core/vectorstore/bitmap.go`:

```go
package vectorstore

import "math/bits"

// bitmap is a growable packed bitset used for per-slot tombstones. Bit i lives
// in word i/64 at offset i%64; get on an out-of-range bit reports false (clear).
type bitmap struct {
	words []uint64
}

func (b *bitmap) set(i int) {
	w := i >> 6
	for w >= len(b.words) {
		b.words = append(b.words, 0)
	}
	b.words[w] |= 1 << uint(i&63)
}

func (b *bitmap) get(i int) bool {
	w := i >> 6
	if w >= len(b.words) {
		return false
	}
	return b.words[w]&(1<<uint(i&63)) != 0
}

func (b *bitmap) count() int {
	n := 0
	for _, w := range b.words {
		n += bits.OnesCount64(w)
	}
	return n
}
```

**(4) Run, expect PASS.**

**(5) Commit:**

```
cd /workspace/haystack && gofmt -w core/vectorstore/ && git add core/vectorstore && git commit -m "feat(vectorstore): packed tombstone bitmap"
```

---

### Task 4 — `SearchResult` + size-k top-k heap

**Create:** `core/vectorstore/result.go`
**Test:** `core/vectorstore/result_test.go`

**(1) FAILING test** — `core/vectorstore/result_test.go`:

```go
package vectorstore

import "testing"

func TestTopK_KeepsSmallestDistances(t *testing.T) {
	tk := newTopK(2)
	tk.offer(SearchResult{DocID: 1, Distance: 0.5})
	tk.offer(SearchResult{DocID: 2, Distance: 0.1})
	tk.offer(SearchResult{DocID: 3, Distance: 0.9}) // worse than both — dropped
	tk.offer(SearchResult{DocID: 4, Distance: 0.3}) // evicts docId 1 (0.5)
	out := tk.sorted()
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].DocID != 2 || out[1].DocID != 4 {
		t.Fatalf("sorted = %+v, want docIds [2 4] ascending by distance", out)
	}
}

func TestTopK_FewerThanK(t *testing.T) {
	tk := newTopK(5)
	tk.offer(SearchResult{DocID: 7, Distance: 0.2})
	out := tk.sorted()
	if len(out) != 1 || out[0].DocID != 7 {
		t.Fatalf("sorted = %+v, want single docId 7", out)
	}
}

func TestTopK_Empty(t *testing.T) {
	if out := newTopK(3).sorted(); len(out) != 0 {
		t.Fatalf("empty topK sorted = %+v, want empty", out)
	}
}

func TestTopK_TieBrokenByDocID(t *testing.T) {
	tk := newTopK(3)
	tk.offer(SearchResult{DocID: 5, Distance: 0.2})
	tk.offer(SearchResult{DocID: 2, Distance: 0.2})
	tk.offer(SearchResult{DocID: 9, Distance: 0.2})
	out := tk.sorted()
	if out[0].DocID != 2 || out[1].DocID != 5 || out[2].DocID != 9 {
		t.Fatalf("equal distances must sort by docId: %+v", out)
	}
}
```

**(2) Run, expect FAIL:** `undefined: newTopK` / `undefined: SearchResult`.

**(3) Impl** — `core/vectorstore/result.go`:

```go
package vectorstore

import (
	"container/heap"
	"sort"
)

// SearchResult holds one nearest-neighbor hit in docId space. The caller maps
// docId back to its string id (the records layer is docId-keyed; see §4.4/§7).
type SearchResult struct {
	DocID    int64
	Distance float32
}

// topK keeps the k smallest-distance results seen, using a max-heap (largest
// distance at the root) so the worst kept result is O(1) to inspect and evict.
type topK struct {
	k int
	h maxHeap
}

func newTopK(k int) *topK { return &topK{k: k} }

// offer adds r if there is room or it beats the current worst kept result.
func (t *topK) offer(r SearchResult) {
	if t.k <= 0 {
		return
	}
	if t.h.Len() < t.k {
		heap.Push(&t.h, r)
		return
	}
	if r.Distance < t.h[0].Distance {
		t.h[0] = r
		heap.Fix(&t.h, 0)
	}
}

// sorted returns the kept results ascending by distance (ties broken by docId
// for determinism).
func (t *topK) sorted() []SearchResult {
	out := make([]SearchResult, len(t.h))
	copy(out, t.h)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Distance != out[j].Distance {
			return out[i].Distance < out[j].Distance
		}
		return out[i].DocID < out[j].DocID
	})
	return out
}

type maxHeap []SearchResult

func (h maxHeap) Len() int           { return len(h) }
func (h maxHeap) Less(i, j int) bool { return h[i].Distance > h[j].Distance } // max-heap
func (h maxHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *maxHeap) Push(x any)        { *h = append(*h, x.(SearchResult)) }
func (h *maxHeap) Pop() any {
	old := *h
	n := len(old)
	r := old[n-1]
	*h = old[:n-1]
	return r
}
```

**(4) Run, expect PASS.**

**(5) Commit:**

```
cd /workspace/haystack && gofmt -w core/vectorstore/ && git add core/vectorstore && git commit -m "feat(vectorstore): SearchResult + size-k top-k heap"
```

---

### Task 5 — fs seam (`osfile.go`, trimmed)

**Create:** `core/vectorstore/osfile.go`
**Test:** none yet (the `osFile` seam is exercised by the WAL fault tests in Task 6). This is a tiny dependency-only file; it is committed together with Task 6's WAL + tests so no production code lands uncovered. **Do this task and Task 6 as one commit** (Task 6 step 5).

**(1) Impl** — `core/vectorstore/osfile.go` (only the interface + the one constructor the WAL needs; the unused `fsCreate/fsOpen/fsRename/fsRemove/fsyncDir` are intentionally NOT copied — each would be a 0%-covered function failing the go-cov per-function floor):

```go
package vectorstore

import "os"

// osFile is the subset of *os.File the WAL uses, abstracted behind an interface
// so tests can inject IO failures (a healthy descriptor's Write/Sync/Truncate/
// Close failing) that are otherwise impossible to trigger. *os.File satisfies it.
type osFile interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Seek(offset int64, whence int) (int64, error)
	Truncate(size int64) error
	Sync() error
	Close() error
}

// fsOpenFile is the injectable file constructor. Production uses os.OpenFile;
// tests override it to return files that fail on chosen operations. It returns a
// true nil interface on error to avoid the typed-nil pitfall.
var fsOpenFile = func(name string, flag int, perm os.FileMode) (osFile, error) {
	f, err := os.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return f, nil
}
```

> No standalone test/commit — proceed directly to Task 6, which adds the WAL that uses this seam and the tests that cover it, then commits both.

---

### Task 6 — Records WAL: framing + record encode/decode + tests (one commit)

**Create:** `core/vectorstore/wal.go`, `core/vectorstore/helpers_test.go` (first appearance), `core/vectorstore/wal_test.go`
**Also commits:** `core/vectorstore/osfile.go` from Task 5.

This task ports the WAL framing from `vectorindex/mmap_wal.go` and is **born with its real behavioral tests** (no throwaway smoke test). The WAL keeps only `OpenWAL/scanLSN/Append/Sync/Reset/Close/Replay` — the HNSW-only `Flush/LSN/SeedLSN` and all HNSW record types are dropped (Phase 1 has no checkpoint/seal flow, so they would be uncovered 0%-functions failing the go-cov floor). The records record-type enum (`recPut`/`recDelete`) and `putRecord`/`deleteRecord` encode/decode live in the same file.

> The framing below is **adapted, not byte-for-byte**, from `mmap_wal.go`: the file is `records.wal` (not `wal.bin`, so vectorstore and vectorindex can share a dir), the type token is `recType` (not `WalRecordType`), and the dropped methods/record-types/encoders are removed. `records.wal` is disjoint from vectorindex's `wal.bin`, and the idtable prefixes 40/41 (Task 9) are distinct from idtable's defaults 28/29 (idtable.go:18) — safe to share Dir and KV.

**(1) FAILING tests.** First create `core/vectorstore/helpers_test.go` with the **full, final import set it needs through Task 14** (so later tasks add only functions, never imports — avoiding error-prone manual import merges):

```go
package vectorstore

import (
	"errors"
	"os"
	"sort"
	"testing"

	"github.com/codetrek/haystack/core/kv"
	"github.com/codetrek/haystack/core/kv/pebblekv"
)

var errInjected = errors.New("injected failure")

// faultFile wraps an osFile and fails the selected operation on demand. Close
// always releases the underlying fd even when injecting a Close error, so tests
// never leak descriptors (Windows fd-leak rule).
type faultFile struct {
	osFile
	failWrite    bool
	failSync     bool
	failTruncate bool
	failClose    bool
}

func (f *faultFile) Write(p []byte) (int, error) {
	if f.failWrite {
		return 0, errInjected
	}
	return f.osFile.Write(p)
}
func (f *faultFile) Sync() error {
	if f.failSync {
		return errInjected
	}
	return f.osFile.Sync()
}
func (f *faultFile) Truncate(size int64) error {
	if f.failTruncate {
		return errInjected
	}
	return f.osFile.Truncate(size)
}
func (f *faultFile) Close() error {
	cerr := f.osFile.Close()
	if f.failClose {
		return errInjected
	}
	return cerr
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// withOpenFileFault makes every fsOpenFile-opened file fault as configured, then
// restores the original constructor at test cleanup.
func withOpenFileFault(t *testing.T, cfg func(*faultFile)) {
	t.Helper()
	orig := fsOpenFile
	t.Cleanup(func() { fsOpenFile = orig })
	fsOpenFile = func(name string, flag int, perm os.FileMode) (osFile, error) {
		f, err := orig(name, flag, perm)
		if err != nil {
			return nil, err
		}
		ff := &faultFile{osFile: f}
		cfg(ff)
		return ff, nil
	}
}

// newTestKV opens a temp pebble store (cacheSize is int64 bytes; 16 MiB) closed
// on test cleanup.
func newTestKV(t *testing.T) kv.Store {
	t.Helper()
	store, err := pebblekv.Open(t.TempDir(), 16<<20)
	requireNoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// openTestStore opens a Store in a fresh temp dir over a fresh KV.
func openTestStore(t *testing.T, m Metric) *Store {
	t.Helper()
	s, err := Open(Options{Dir: t.TempDir(), KV: newTestKV(t), Metric: m})
	requireNoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// bruteForceKNN is the ground-truth oracle: it computes the metric distance for
// every candidate via prepare+distance and returns the k nearest docIds ascending
// by distance (ties by docId).
func bruteForceKNN(m Metric, q []float32, vecs map[int64][]float32, k int) []int64 {
	pq, _ := m.prepare(q)
	type hit struct {
		doc int64
		d   float32
	}
	var hits []hit
	for doc, raw := range vecs {
		stored, _ := m.prepare(raw)
		hits = append(hits, hit{doc, m.distance(stored, pq)})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].d != hits[j].d {
			return hits[i].d < hits[j].d
		}
		return hits[i].doc < hits[j].doc
	})
	if k > len(hits) {
		k = len(hits)
	}
	out := make([]int64, k)
	for i := 0; i < k; i++ {
		out[i] = hits[i].doc
	}
	return out
}
```

> `Store`, `Open`, `Options` referenced by `openTestStore` are defined in Task 9. The package will not compile until then, so `wal_test.go` (below) is the only test exercised at Task 6's commit — and it does NOT use `openTestStore`/`newTestKV`/`bruteForceKNN`. To keep Task 6 compiling and green on its own commit, **add `helpers_test.go` in Task 9** instead (when `Store` exists). For Task 6, create only the smaller helper file `walhelpers_test.go` with just what the WAL tests need:

Create `core/vectorstore/walhelpers_test.go`:

```go
package vectorstore

import (
	"os"
	"testing"
)

var errInjected = errInjectedVal

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// faultFile wraps an osFile and fails the selected operation on demand.
type faultFile struct {
	osFile
	failWrite    bool
	failSync     bool
	failTruncate bool
	failClose    bool
}

func (f *faultFile) Write(p []byte) (int, error) {
	if f.failWrite {
		return 0, errInjected
	}
	return f.osFile.Write(p)
}
func (f *faultFile) Sync() error {
	if f.failSync {
		return errInjected
	}
	return f.osFile.Sync()
}
func (f *faultFile) Truncate(size int64) error {
	if f.failTruncate {
		return errInjected
	}
	return f.osFile.Truncate(size)
}
func (f *faultFile) Close() error {
	cerr := f.osFile.Close()
	if f.failClose {
		return errInjected
	}
	return cerr
}

func withOpenFileFault(t *testing.T, cfg func(*faultFile)) {
	t.Helper()
	orig := fsOpenFile
	t.Cleanup(func() { fsOpenFile = orig })
	fsOpenFile = func(name string, flag int, perm os.FileMode) (osFile, error) {
		f, err := orig(name, flag, perm)
		if err != nil {
			return nil, err
		}
		ff := &faultFile{osFile: f}
		cfg(ff)
		return ff, nil
	}
}
```

> To avoid two definitions of `errInjected`/helpers across files, **collapse the two helper files into one**. Concretely: in Task 6 create ONLY `walhelpers_test.go` above (with `var errInjected = errInjectedVal` replaced by a literal `errors.New` — add `"errors"` to its imports and use `var errInjected = errors.New("injected failure")`). In Task 9, create `storehelpers_test.go` containing only `newTestKV`, `openTestStore`, `bruteForceKNN`, `approxEqual` is already in `metric_test.go`. This keeps each helper defined exactly once and each test file's imports self-contained. (Replace the `helpers_test.go` listing above with this split; the import-set-up-front goal is met per-file.)

The corrected, self-contained `core/vectorstore/walhelpers_test.go`:

```go
package vectorstore

import (
	"errors"
	"os"
	"testing"
)

var errInjected = errors.New("injected failure")

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// faultFile wraps an osFile and fails the selected operation on demand. Close
// always releases the underlying fd even when injecting a Close error.
type faultFile struct {
	osFile
	failWrite    bool
	failSync     bool
	failTruncate bool
	failClose    bool
}

func (f *faultFile) Write(p []byte) (int, error) {
	if f.failWrite {
		return 0, errInjected
	}
	return f.osFile.Write(p)
}
func (f *faultFile) Sync() error {
	if f.failSync {
		return errInjected
	}
	return f.osFile.Sync()
}
func (f *faultFile) Truncate(size int64) error {
	if f.failTruncate {
		return errInjected
	}
	return f.osFile.Truncate(size)
}
func (f *faultFile) Close() error {
	cerr := f.osFile.Close()
	if f.failClose {
		return errInjected
	}
	return cerr
}

func withOpenFileFault(t *testing.T, cfg func(*faultFile)) {
	t.Helper()
	orig := fsOpenFile
	t.Cleanup(func() { fsOpenFile = orig })
	fsOpenFile = func(name string, flag int, perm os.FileMode) (osFile, error) {
		f, err := orig(name, flag, perm)
		if err != nil {
			return nil, err
		}
		ff := &faultFile{osFile: f}
		cfg(ff)
		return ff, nil
	}
}
```

Then create `core/vectorstore/wal_test.go`:

```go
package vectorstore

import (
	"reflect"
	"testing"
)

func TestEncodeDecodePut(t *testing.T) {
	rec := putRecord{
		ID:      "doc-a",
		DocID:   42,
		OldSlot: 7,
		Stored:  []float32{0.6, 0.8},
		Norm:    5,
		Payload: []byte("meta"),
	}
	got := decodePut(encodePut(rec))
	if !reflect.DeepEqual(got, rec) {
		t.Fatalf("decodePut(encodePut) = %+v, want %+v", got, rec)
	}
}

func TestEncodeDecodePut_NoOldSlotEmptyPayload(t *testing.T) {
	rec := putRecord{ID: "x", DocID: 1, OldSlot: -1, Stored: []float32{1, 2, 3}, Norm: 0, Payload: nil}
	got := decodePut(encodePut(rec))
	if got.ID != "x" || got.DocID != 1 || got.OldSlot != -1 || got.Norm != 0 {
		t.Fatalf("scalars wrong: %+v", got)
	}
	if !reflect.DeepEqual(got.Stored, rec.Stored) {
		t.Fatalf("stored = %v, want %v", got.Stored, rec.Stored)
	}
	if len(got.Payload) != 0 {
		t.Fatalf("payload = %v, want empty", got.Payload)
	}
}

func TestEncodeDecodeDelete(t *testing.T) {
	got := decodeDelete(encodeDelete("doc-b", 123, 4))
	if got.ID != "doc-b" || got.DocID != 123 || got.Slot != 4 {
		t.Fatalf("decodeDelete = %+v, want {ID:doc-b DocID:123 Slot:4}", got)
	}
}

func TestWAL_AppendReplayRoundTrip(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	requireNoError(t, err)
	put := encodePut(putRecord{ID: "a", DocID: 1, OldSlot: -1, Stored: []float32{1, 0}, Norm: 0, Payload: []byte("p")})
	del := encodeDelete("a", 1, 0)
	_, err = w.Append(recPut, put)
	requireNoError(t, err)
	_, err = w.Append(recDelete, del)
	requireNoError(t, err)
	requireNoError(t, w.Sync())
	requireNoError(t, w.Close())

	w2, err := OpenWAL(dir)
	requireNoError(t, err)
	defer w2.Close()
	var types []recType
	requireNoError(t, w2.Replay(func(typ recType, payload []byte) error {
		types = append(types, typ)
		return nil
	}))
	if len(types) != 2 || types[0] != recPut || types[1] != recDelete {
		t.Fatalf("replayed types = %v, want [recPut recDelete]", types)
	}
}

func TestWAL_ResetClearsRecords(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	requireNoError(t, err)
	defer w.Close()
	_, err = w.Append(recDelete, encodeDelete("a", 1, 0))
	requireNoError(t, err)
	requireNoError(t, w.Reset())
	n := 0
	requireNoError(t, w.Replay(func(recType, []byte) error { n++; return nil }))
	if n != 0 {
		t.Fatalf("after Reset replay saw %d records, want 0", n)
	}
}

func TestWAL_SyncFault(t *testing.T) {
	dir := t.TempDir()
	withOpenFileFault(t, func(f *faultFile) { f.failSync = true })
	w, err := OpenWAL(dir)
	requireNoError(t, err)
	defer w.Close()
	if _, err := w.Append(recDelete, encodeDelete("a", 1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := w.Sync(); err == nil {
		t.Fatal("expected Sync to surface injected fsync failure")
	}
}

func TestWAL_AppendWriteFault(t *testing.T) {
	dir := t.TempDir()
	withOpenFileFault(t, func(f *faultFile) { f.failWrite = true })
	w, err := OpenWAL(dir)
	requireNoError(t, err)
	defer w.Close()
	// bufio buffers small writes, so force a flush via Sync to surface the
	// injected write error on the underlying file.
	_, _ = w.Append(recDelete, encodeDelete("a", 1, 0))
	if err := w.Sync(); err == nil {
		t.Fatal("expected the injected write failure to surface on flush")
	}
}

func TestWAL_OpenTruncateFault(t *testing.T) {
	dir := t.TempDir()
	withOpenFileFault(t, func(f *faultFile) { f.failTruncate = true }) // scanLSN truncates
	if _, err := OpenWAL(dir); err == nil {
		t.Fatal("OpenWAL should fail when scanLSN truncate fails")
	}
}
```

**(2) Run, expect FAIL:** `undefined: putRecord` / `undefined: OpenWAL` / `undefined: recType`.

**(3) Impl** — `core/vectorstore/wal.go`:

```go
package vectorstore

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
)

// recType identifies a records-layer WAL record. (Distinct numbering from the
// HNSW vectorindex WAL; this log has no graph records.)
type recType uint8

const (
	recPut    recType = 1 // upsert: tombstone old slot (if any) + new slot
	recDelete recType = 2 // tombstone an existing docId
)

// putRecord is the durable form of an upsert. ID is the caller's string key:
// storing it in the WAL makes the id↔docId mapping crash-safe independently of
// idtable's lazily-committed batch (the WAL is the single source of truth for
// the mapping; see store.go replay). OldSlot is the slot to tombstone for this
// docId (-1 when new); the new slot index is implied by append order on apply.
type putRecord struct {
	ID      string
	DocID   int64
	OldSlot int64
	Stored  []float32
	Norm    float32
	Payload []byte
}

// deleteRecord tombstones Slot, which holds DocID for string key ID.
type deleteRecord struct {
	ID    string
	DocID int64
	Slot  int64
}

// --- record payload encode/decode ---

func putString(buf []byte, off int, s string) int {
	binary.LittleEndian.PutUint32(buf[off:], uint32(len(s)))
	off += 4
	copy(buf[off:], s)
	return off + len(s)
}

func getString(b []byte, off int) (string, int) {
	n := int(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	s := string(b[off : off+n])
	return s, off + n
}

// encodePut layout: idLen(4)|id | docId(8) | oldSlot(8) | norm(4) | vecLen(4) | vec(N*4) | payloadLen(4) | payload
func encodePut(r putRecord) []byte {
	size := 4 + len(r.ID) + 8 + 8 + 4 + 4 + len(r.Stored)*4 + 4 + len(r.Payload)
	buf := make([]byte, size)
	off := putString(buf, 0, r.ID)
	binary.LittleEndian.PutUint64(buf[off:], uint64(r.DocID))
	off += 8
	binary.LittleEndian.PutUint64(buf[off:], uint64(r.OldSlot))
	off += 8
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(r.Norm))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], uint32(len(r.Stored)))
	off += 4
	for _, v := range r.Stored {
		binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(v))
		off += 4
	}
	binary.LittleEndian.PutUint32(buf[off:], uint32(len(r.Payload)))
	off += 4
	copy(buf[off:], r.Payload)
	return buf
}

func decodePut(b []byte) putRecord {
	r := putRecord{}
	var off int
	r.ID, off = getString(b, 0)
	r.DocID = int64(binary.LittleEndian.Uint64(b[off:]))
	off += 8
	r.OldSlot = int64(binary.LittleEndian.Uint64(b[off:]))
	off += 8
	r.Norm = math.Float32frombits(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	n := int(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	r.Stored = make([]float32, n)
	for i := 0; i < n; i++ {
		r.Stored[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[off:]))
		off += 4
	}
	pl := int(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	if pl > 0 {
		r.Payload = make([]byte, pl)
		copy(r.Payload, b[off:off+pl])
	}
	return r
}

// encodeDelete layout: idLen(4)|id | docId(8) | slot(8)
func encodeDelete(id string, docID, slot int64) []byte {
	buf := make([]byte, 4+len(id)+8+8)
	off := putString(buf, 0, id)
	binary.LittleEndian.PutUint64(buf[off:], uint64(docID))
	off += 8
	binary.LittleEndian.PutUint64(buf[off:], uint64(slot))
	return buf
}

func decodeDelete(b []byte) deleteRecord {
	id, off := getString(b, 0)
	d := deleteRecord{ID: id}
	d.DocID = int64(binary.LittleEndian.Uint64(b[off:]))
	off += 8
	d.Slot = int64(binary.LittleEndian.Uint64(b[off:]))
	return d
}

// --- CRC WAL framing (adapted from vectorindex/mmap_wal.go) ---

const walHeaderSize = 8 + 4 + 1 // LSN + Length + Type
const walCRCSize = 4
const maxWalPayloadSize = 64 << 20

// WAL is an append-only write-ahead log with CRC32 integrity checks.
type WAL struct {
	file osFile
	lsn  uint64
	mu   sync.Mutex
	buf  *bufio.Writer
}

// OpenWAL opens or creates records.wal in dir, scanning it to find the last
// valid LSN and truncating any torn tail.
func OpenWAL(dir string) (*WAL, error) {
	path := filepath.Join(dir, "records.wal")
	f, err := fsOpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("WAL: open: %w", err)
	}
	w := &WAL{file: f, buf: bufio.NewWriter(f)}
	if err := w.scanLSN(); err != nil {
		f.Close()
		return nil, fmt.Errorf("WAL: scan: %w", err)
	}
	return w, nil
}

func (w *WAL) scanLSN() error {
	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReader(w.file)
	var lastValidOffset int64
	var maxLSN uint64
	for {
		header := make([]byte, walHeaderSize)
		if _, err := io.ReadFull(r, header); err != nil {
			break
		}
		lsn := binary.LittleEndian.Uint64(header[0:8])
		length := binary.LittleEndian.Uint32(header[8:12])
		if length > maxWalPayloadSize {
			break
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(r, payload); err != nil {
			break
		}
		crcBuf := make([]byte, walCRCSize)
		if _, err := io.ReadFull(r, crcBuf); err != nil {
			break
		}
		h := crc32.NewIEEE()
		h.Write(header)
		h.Write(payload)
		if binary.LittleEndian.Uint32(crcBuf) != h.Sum32() {
			break
		}
		maxLSN = lsn
		lastValidOffset += int64(walHeaderSize) + int64(length) + int64(walCRCSize)
	}
	if err := w.file.Truncate(lastValidOffset); err != nil {
		return err
	}
	if _, err := w.file.Seek(lastValidOffset, io.SeekStart); err != nil {
		return err
	}
	w.lsn = maxLSN
	return nil
}

// Append buffers a record and returns its LSN. The caller fsyncs via Sync at the
// commit boundary.
func (w *WAL) Append(typ recType, payload []byte) (uint64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lsn++
	lsn := w.lsn
	header := make([]byte, walHeaderSize)
	binary.LittleEndian.PutUint64(header[0:8], lsn)
	binary.LittleEndian.PutUint32(header[8:12], uint32(len(payload)))
	header[12] = byte(typ)
	h := crc32.NewIEEE()
	h.Write(header)
	h.Write(payload)
	crcBuf := make([]byte, walCRCSize)
	binary.LittleEndian.PutUint32(crcBuf, h.Sum32())
	if _, err := w.buf.Write(header); err != nil {
		return 0, fmt.Errorf("WAL: write header: %w", err)
	}
	if _, err := w.buf.Write(payload); err != nil {
		return 0, fmt.Errorf("WAL: write payload: %w", err)
	}
	if _, err := w.buf.Write(crcBuf); err != nil {
		return 0, fmt.Errorf("WAL: write crc: %w", err)
	}
	return lsn, nil
}

// Sync flushes the buffer and fsyncs the file.
func (w *WAL) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.buf.Flush(); err != nil {
		return err
	}
	return w.file.Sync()
}

// Reset truncates the WAL to 0 bytes while preserving the LSN counter.
func (w *WAL) Reset() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.buf.Flush(); err != nil {
		return err
	}
	if err := w.file.Truncate(0); err != nil {
		return err
	}
	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	w.buf.Reset(w.file)
	return nil
}

// Close flushes and closes the WAL file.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.buf.Flush(); err != nil {
		w.file.Close()
		return err
	}
	return w.file.Close()
}

// Replay invokes fn for every valid record in LSN order; a torn tail is truncated.
func (w *WAL) Replay(fn func(typ recType, payload []byte) error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReader(w.file)
	var lastValidOffset int64
	for {
		header := make([]byte, walHeaderSize)
		if _, err := io.ReadFull(r, header); err != nil {
			break
		}
		length := binary.LittleEndian.Uint32(header[8:12])
		typ := recType(header[12])
		if length > maxWalPayloadSize {
			return fmt.Errorf("wal: payload length %d exceeds max %d", length, maxWalPayloadSize)
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(r, payload); err != nil {
			break
		}
		crcBuf := make([]byte, walCRCSize)
		if _, err := io.ReadFull(r, crcBuf); err != nil {
			break
		}
		h := crc32.NewIEEE()
		h.Write(header)
		h.Write(payload)
		if binary.LittleEndian.Uint32(crcBuf) != h.Sum32() {
			break
		}
		lastValidOffset += int64(walHeaderSize) + int64(length) + int64(walCRCSize)
		if fn != nil {
			if err := fn(typ, payload); err != nil {
				return err
			}
		}
	}
	if err := w.file.Truncate(lastValidOffset); err != nil {
		return err
	}
	if _, err := w.file.Seek(lastValidOffset, io.SeekStart); err != nil {
		return err
	}
	w.buf.Reset(w.file)
	return nil
}
```

> `Replay` drops the `afterLSN` parameter (Phase 1 has no checkpoint, so it always replays from 0) and `scanLSN` retains `lsn` tracking only so a reopened WAL keeps monotonic LSNs across appends; `lsn` is otherwise internal. This keeps every function reachable by a test, clearing the per-function floor.

**(4) Run, expect PASS:**

```
cd /workspace/haystack/core && go test ./vectorstore/...
```

**(5) Commit** (osfile.go from Task 5 + wal.go + tests together):

```
cd /workspace/haystack && gofmt -w core/vectorstore/ && git add core/vectorstore && git commit -m "feat(vectorstore): fs seam + CRC WAL framing + records encode/decode (covered)"
```

---

### Task 7 — In-memory head `segment`

**Create:** `core/vectorstore/segment.go`
**Test:** `core/vectorstore/segment_test.go`

**(1) FAILING test** — `core/vectorstore/segment_test.go`:

```go
package vectorstore

import "testing"

func TestSegment_AppendReadTombstone(t *testing.T) {
	s := newSegment(Cosine)
	slot0 := s.append(10, []float32{0.6, 0.8}, 5, []byte("a"))
	slot1 := s.append(11, []float32{0, 1}, 1, []byte("b"))
	if slot0 != 0 || slot1 != 1 {
		t.Fatalf("slots = %d,%d, want 0,1", slot0, slot1)
	}
	if got, ok := s.slotOfDoc(10); !ok || got != 0 {
		t.Fatalf("slotOfDoc(10) = %d,%v, want 0,true", got, ok)
	}
	v, n, pl, live := s.read(0)
	if !live || n != 5 || string(pl) != "a" || v[0] != 0.6 {
		t.Fatalf("read(0) = %v,%v,%q,%v", v, n, pl, live)
	}
	s.tombstone(0)
	if _, _, _, live := s.read(0); live {
		t.Fatal("slot 0 should be tombstoned")
	}
	if _, ok := s.slotOfDoc(10); ok {
		t.Fatal("slotOfDoc(10) should be gone after tombstone")
	}
}

func TestSegment_AppendCopiesBuffers(t *testing.T) {
	s := newSegment(DotProduct)
	v := []float32{1, 2}
	pl := []byte("x")
	s.append(1, v, 0, pl)
	v[0] = 99 // mutate caller buffers after append
	pl[0] = 'Z'
	gv, _, gpl, _ := s.read(0)
	if gv[0] != 1 || string(gpl) != "x" {
		t.Fatalf("segment must copy inputs: got %v,%q", gv, gpl)
	}
}

func TestSegment_LiveIter(t *testing.T) {
	s := newSegment(DotProduct)
	s.append(1, []float32{1, 0}, 0, nil)
	s.append(2, []float32{0, 1}, 0, nil)
	s.append(3, []float32{1, 1}, 0, nil)
	s.tombstone(1)
	var docs []int64
	s.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		docs = append(docs, docID)
	})
	if len(docs) != 2 || docs[0] != 1 || docs[1] != 3 {
		t.Fatalf("live docs = %v, want [1 3]", docs)
	}
}

func TestSegment_TombstoneOutOfRangeNoPanic(t *testing.T) {
	s := newSegment(Cosine)
	s.tombstone(-1)
	s.tombstone(99) // must not panic
}
```

**(2) Run, expect FAIL:** `undefined: newSegment`.

**(3) Impl** — `core/vectorstore/segment.go`:

```go
package vectorstore

// segment is the in-memory head: a slot-addressed records block. Each slot holds
// a stored-form vector + norm + payload; slotDoc[slot] is the source of truth for
// slot→docId, and docToSlot is the derived reverse index over LIVE slots only
// (the per-segment level of the two-level id model, §4.6). tomb marks deleted
// slots. In Phase 1 there is exactly one segment (the head).
type segment struct {
	metric    Metric
	dim       int           // learned from the first append (0 until then)
	vectors   [][]float32   // slot → stored-form vector
	norms     []float32     // slot → norm (meaningful only for cosine)
	payloads  [][]byte      // slot → payload
	slotDoc   []int64       // slot → docId (source of truth)
	tomb      bitmap        // slot → deleted?
	docToSlot map[int64]int // docId → live slot (derived)
}

func newSegment(m Metric) *segment {
	return &segment{metric: m, docToSlot: make(map[int64]int)}
}

// append stores a new slot and indexes it as the live slot for docID. The vector
// must already be in stored form (caller runs metric.prepare). The slice and
// payload are copied so the caller may reuse its buffers.
func (s *segment) append(docID int64, stored []float32, norm float32, payload []byte) int {
	if s.dim == 0 && len(stored) > 0 {
		s.dim = len(stored)
	}
	vcp := make([]float32, len(stored))
	copy(vcp, stored)
	var pcp []byte
	if len(payload) > 0 {
		pcp = make([]byte, len(payload))
		copy(pcp, payload)
	}
	slot := len(s.vectors)
	s.vectors = append(s.vectors, vcp)
	s.norms = append(s.norms, norm)
	s.payloads = append(s.payloads, pcp)
	s.slotDoc = append(s.slotDoc, docID)
	s.docToSlot[docID] = slot
	return slot
}

// tombstone marks slot deleted and drops it from the derived index (only if that
// docId still points at this slot — guards against an overwritten mapping).
func (s *segment) tombstone(slot int) {
	if slot < 0 || slot >= len(s.slotDoc) {
		return
	}
	s.tomb.set(slot)
	doc := s.slotDoc[slot]
	if cur, ok := s.docToSlot[doc]; ok && cur == slot {
		delete(s.docToSlot, doc)
	}
}

// slotOfDoc returns the live slot for docID.
func (s *segment) slotOfDoc(docID int64) (int, bool) {
	slot, ok := s.docToSlot[docID]
	return slot, ok
}

// read returns the slot's stored vector, norm, payload, and liveness.
func (s *segment) read(slot int) (stored []float32, norm float32, payload []byte, live bool) {
	if slot < 0 || slot >= len(s.vectors) {
		return nil, 0, nil, false
	}
	if s.tomb.get(slot) {
		return nil, 0, nil, false
	}
	return s.vectors[slot], s.norms[slot], s.payloads[slot], true
}

// eachLive visits every non-tombstoned slot in ascending order. The stored slice
// is the internal buffer; callers must not retain it past the callback.
func (s *segment) eachLive(fn func(slot int, docID int64, stored []float32, norm float32)) {
	for slot := range s.vectors {
		if s.tomb.get(slot) {
			continue
		}
		fn(slot, s.slotDoc[slot], s.vectors[slot], s.norms[slot])
	}
}
```

**(4) Run, expect PASS.**

**(5) Commit:**

```
cd /workspace/haystack && gofmt -w core/vectorstore/ && git add core/vectorstore && git commit -m "feat(vectorstore): in-memory head segment (slots + tombstone + derived docToSlot)"
```

---

### Task 8 — `Store.Open`/`Close`/`Metric` + idtable wiring + replay skeleton

**Create:** `core/vectorstore/store.go`, `core/vectorstore/storehelpers_test.go`, `core/vectorstore/store_test.go`
**Test:** `core/vectorstore/store_test.go`

**(1) FAILING test.** First create the store test helpers in `core/vectorstore/storehelpers_test.go` (self-contained imports; `requireNoError`/`faultFile`/`withOpenFileFault` already live in `walhelpers_test.go`; `approxEqual` already lives in `metric_test.go`):

```go
package vectorstore

import (
	"sort"
	"testing"

	"github.com/codetrek/haystack/core/kv"
	"github.com/codetrek/haystack/core/kv/pebblekv"
)

// newTestKV opens a temp pebble store (cacheSize is int64 bytes; 16 MiB) closed
// on test cleanup.
func newTestKV(t *testing.T) kv.Store {
	t.Helper()
	store, err := pebblekv.Open(t.TempDir(), 16<<20)
	requireNoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// openTestStore opens a Store in a fresh temp dir over a fresh KV.
func openTestStore(t *testing.T, m Metric) *Store {
	t.Helper()
	s, err := Open(Options{Dir: t.TempDir(), KV: newTestKV(t), Metric: m})
	requireNoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// bruteForceKNN is the ground-truth oracle: it computes the metric distance for
// every candidate and returns the k nearest docIds ascending by distance (ties
// by docId).
func bruteForceKNN(m Metric, q []float32, vecs map[int64][]float32, k int) []int64 {
	pq, _ := m.prepare(q)
	type hit struct {
		doc int64
		d   float32
	}
	var hits []hit
	for doc, raw := range vecs {
		stored, _ := m.prepare(raw)
		hits = append(hits, hit{doc, m.distance(stored, pq)})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].d != hits[j].d {
			return hits[i].d < hits[j].d
		}
		return hits[i].doc < hits[j].doc
	})
	if k > len(hits) {
		k = len(hits)
	}
	out := make([]int64, k)
	for i := 0; i < k; i++ {
		out[i] = hits[i].doc
	}
	return out
}
```

Then create `core/vectorstore/store_test.go`:

```go
package vectorstore

import "testing"

func TestStore_OpenClose(t *testing.T) {
	s := openTestStore(t, Cosine)
	if s.Metric() != Cosine {
		t.Fatalf("metric = %v, want cosine", s.Metric())
	}
}

func TestStore_OpenRequiresKV(t *testing.T) {
	if _, err := Open(Options{Dir: t.TempDir(), Metric: Cosine}); err == nil {
		t.Fatal("Open without KV should error")
	}
}

func TestStore_OpenRequiresDir(t *testing.T) {
	if _, err := Open(Options{KV: newTestKV(t), Metric: Cosine}); err == nil {
		t.Fatal("Open without Dir should error")
	}
}
```

**(2) Run, expect FAIL:** `undefined: Open` / `undefined: Options` / `undefined: Store`.

**(3) Impl** — `core/vectorstore/store.go` (Open/Close/Metric + replay skeleton; Put/Get/Delete/Search arrive in their own tasks):

```go
package vectorstore

import (
	"encoding/binary"
	"errors"
	"sync"

	"github.com/codetrek/haystack/core/idtable"
	"github.com/codetrek/haystack/core/kv"
)

// Distinct idtable key-prefix bytes for the vectorstore allocator, so it never
// collides with idtable's default doc-id allocator (28/29) when sharing a KV.
const (
	idtableKeyTypeNextId = byte(40)
	idtableKeyTypeKey    = byte(41)
)

// Options configures a Store. KV backs the string→docId idtable; Dir holds the
// records WAL (records.wal).
type Options struct {
	Dir    string
	KV     kv.Store
	Metric Metric
}

// Store is the Phase-1 records layer: one in-memory head segment fronted by an
// idtable (string id → docId) and protected by a WAL. The WAL is the single
// crash-safe source of truth for both the records and the id↔docId mapping. All
// public methods are serialized by mu (single-writer; readers take RLock and
// copy out of the segment).
type Store struct {
	mu      sync.RWMutex
	metric  Metric
	dir     string
	alloc   *idtable.Allocator
	seg     *segment
	wal     *WAL
	idToDoc map[string]int64 // derived from WAL replay; lets reads avoid allocating
}

// Open creates or recovers a Store at opts.Dir, replaying the WAL to rebuild the
// head segment, the id↔docId map, and the allocator state.
func Open(opts Options) (*Store, error) {
	if opts.KV == nil {
		return nil, errors.New("vectorstore: Options.KV is required")
	}
	if opts.Dir == "" {
		return nil, errors.New("vectorstore: Options.Dir is required")
	}
	alloc, err := idtable.New(opts.KV, idtable.Options{
		KeyTypeNextId: idtableKeyTypeNextId,
		KeyTypeKey:    idtableKeyTypeKey,
	})
	if err != nil {
		return nil, err
	}
	w, err := OpenWAL(opts.Dir)
	if err != nil {
		alloc.Close()
		return nil, err
	}
	s := &Store{
		metric:  opts.Metric,
		dir:     opts.Dir,
		alloc:   alloc,
		seg:     newSegment(opts.Metric),
		wal:     w,
		idToDoc: make(map[string]int64),
	}
	if err := s.replay(); err != nil {
		w.Close()
		alloc.Close()
		return nil, err
	}
	return s, nil
}

// Metric returns the store's distance metric.
func (s *Store) Metric() Metric { return s.metric }

// Close flushes and releases the WAL and idtable. Closing the allocator commits
// any pending id→docId mappings re-driven during replay, making the recovered
// state durable.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	werr := s.wal.Close()
	s.alloc.Close()
	return werr
}

// docIDForAlloc maps a string id to its stable int64 docId via the idtable,
// ALLOCATING on first sight. Use only on the write path (Put). The idtable
// returns an 8-byte big-endian id.
func (s *Store) docIDForAlloc(id string) (int64, error) {
	v, err := s.alloc.GetId([]byte(id))
	if err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64([]byte(v))), nil
}

// replay rebuilds in-memory state from the records WAL. Records are applied in
// LSN order; recPut re-drives the allocator for its string id (reconstructing
// the same monotonic docId the original run assigned — see store.go decision #9),
// tombstones the recorded old slot if any, then appends the new slot. recDelete
// tombstones the recorded slot. The idToDoc map is rebuilt as a side effect so
// reads never need to allocate. (Filled here; on a fresh Open the log is empty.)
func (s *Store) replay() error {
	return s.wal.Replay(func(typ recType, payload []byte) error {
		switch typ {
		case recPut:
			r := decodePut(payload)
			// Re-establish id→docId in the allocator and the derived map.
			if _, err := s.docIDForAlloc(r.ID); err != nil {
				return err
			}
			s.idToDoc[r.ID] = r.DocID
			s.applyPut(r)
		case recDelete:
			d := decodeDelete(payload)
			if _, err := s.docIDForAlloc(d.ID); err != nil {
				return err
			}
			s.idToDoc[d.ID] = d.DocID
			s.seg.tombstone(int(d.Slot))
		}
		return nil
	})
}

// applyPut mutates the segment for a (durably logged) put: tombstone the prior
// slot, then append the new one. Shared by Put and replay.
func (s *Store) applyPut(r putRecord) {
	if r.OldSlot >= 0 {
		s.seg.tombstone(int(r.OldSlot))
	}
	s.seg.append(r.DocID, r.Stored, r.Norm, r.Payload)
}
```

> **Determinism note for replay (decision #9):** `idtable` allocates `nextId` monotonically from 1, `++` per *new* key. The WAL preserves insertion order, and replay drives `GetId` in that order for the *same* string ids, so each id is re-assigned the *identical* docId it got originally — even when the pre-crash KV lost the lazily-batched mapping. `r.DocID` from the record is used for the segment (authoritative); the `GetId` call exists to resynchronize the allocator's `nextId` and KV so future `Put`s do not collide. A unit test in Task 13 asserts this under an unclean crash.

**(4) Run, expect PASS.**

**(5) Commit:**

```
cd /workspace/haystack && gofmt -w core/vectorstore/ && git add core/vectorstore && git commit -m "feat(vectorstore): Store Open/Close + idtable wiring + WAL replay skeleton"
```

---

### Task 9 — `Put` (crash-atomic upsert) + `Get` (defensive copy)

**Create:** — (modify `store.go`)
**Test:** `core/vectorstore/store_test.go` (extend)

**(1) FAILING test** — append to `core/vectorstore/store_test.go`:

```go
func TestStore_PutThenGet(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.Put("a", []float32{3, 4}, []byte("payA")))
	v, pl, found, err := s.Get("a")
	requireNoError(t, err)
	if !found {
		t.Fatal("Get(a) not found after Put")
	}
	if !approxEqual(v[0], 3, 1e-4) || !approxEqual(v[1], 4, 1e-4) {
		t.Fatalf("restored vector = %v, want [3 4]", v)
	}
	if string(pl) != "payA" {
		t.Fatalf("payload = %q, want payA", pl)
	}
}

func TestStore_GetUnknownDoesNotAllocate(t *testing.T) {
	s := openTestStore(t, Cosine)
	_, _, found, err := s.Get("never")
	requireNoError(t, err)
	if found {
		t.Fatal("Get of never-put id should be not-found")
	}
	// A subsequent Put must get docId 1 (Get did not burn an id).
	requireNoError(t, s.Put("a", []float32{1, 0}, nil))
	if got := s.idToDoc["a"]; got != 1 {
		t.Fatalf("first Put docId = %d, want 1 (Get must not allocate)", got)
	}
}

func TestStore_GetReturnsCopy_NonCosine(t *testing.T) {
	s := openTestStore(t, DotProduct)
	requireNoError(t, s.Put("a", []float32{1, 2, 3}, nil))
	v, _, _, err := s.Get("a")
	requireNoError(t, err)
	v[0] = 999 // mutating the returned slice must NOT corrupt the segment
	v2, _, _, err := s.Get("a")
	requireNoError(t, err)
	if v2[0] != 1 {
		t.Fatalf("Get must return a copy; segment was corrupted: %v", v2)
	}
}

func TestStore_PutUpsertReplaces(t *testing.T) {
	s := openTestStore(t, DotProduct)
	requireNoError(t, s.Put("a", []float32{1, 0}, nil))
	requireNoError(t, s.Put("a", []float32{0, 9}, []byte("v2")))
	v, pl, found, err := s.Get("a")
	requireNoError(t, err)
	if !found || v[0] != 0 || v[1] != 9 || string(pl) != "v2" {
		t.Fatalf("after upsert Get = %v,%q,%v, want [0 9],v2,true", v, pl, found)
	}
	live := 0
	s.seg.eachLive(func(int, int64, []float32, float32) { live++ })
	if live != 1 {
		t.Fatalf("live slots = %d, want 1 (old slot tombstoned)", live)
	}
}

func TestStore_PutRejectsBadVector(t *testing.T) {
	s := openTestStore(t, Cosine)
	if err := s.Put("a", []float32{}, nil); err == nil {
		t.Fatal("empty vector should be rejected")
	}
	requireNoError(t, s.Put("a", []float32{1, 2}, nil))
	if err := s.Put("b", []float32{1, 2, 3}, nil); err == nil {
		t.Fatal("dim mismatch should be rejected")
	}
}
```

**(2) Run, expect FAIL:** `s.Put undefined` / `s.Get undefined`.

**(3) Impl** — add to `core/vectorstore/store.go`:

```go
// Put inserts or replaces the vector and payload for id. It is crash-atomic: a
// single WAL record (the string id, its docId, the old slot to tombstone if any,
// and the new stored vector + norm + payload) is fsync'd before the in-memory
// state is mutated, so a crash either loses the whole Put or applies it whole on
// replay. The string→docId mapping is recovered from the same WAL record, so Put
// is fully durable on return without depending on idtable's lazy commit.
func (s *Store) Put(id string, v []float32, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateVector(v, s.seg.dim, s.metric); err != nil {
		return err
	}
	docID, err := s.docIDForAlloc(id)
	if err != nil {
		return err
	}
	stored, norm := s.metric.prepare(v)

	oldSlot := int64(-1)
	if slot, ok := s.seg.slotOfDoc(docID); ok {
		oldSlot = int64(slot)
	}
	rec := putRecord{ID: id, DocID: docID, OldSlot: oldSlot, Stored: stored, Norm: norm, Payload: payload}
	if _, err := s.wal.Append(recPut, encodePut(rec)); err != nil {
		return err
	}
	if err := s.wal.Sync(); err != nil {
		return err
	}
	s.idToDoc[id] = docID
	s.applyPut(rec)
	return nil
}

// Get returns the original (restored) vector and payload for id. Reads never
// allocate a docId: an unknown id (never Put) returns found=false. The returned
// vector and payload are fresh copies the caller may mutate freely.
func (s *Store) Get(id string) (v []float32, payload []byte, found bool, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	docID, ok := s.idToDoc[id]
	if !ok {
		return nil, nil, false, nil
	}
	slot, ok := s.seg.slotOfDoc(docID)
	if !ok {
		return nil, nil, false, nil
	}
	stored, norm, pl, live := s.seg.read(slot)
	if !live {
		return nil, nil, false, nil
	}
	// restore is the identity for non-cosine metrics, so it may alias the
	// segment's internal buffer. Always hand the caller a private copy.
	out := append([]float32(nil), s.metric.restore(stored, norm)...)
	plcp := append([]byte(nil), pl...)
	return out, plcp, true, nil
}
```

> Note: `prepare` for cosine allocates a fresh stored slice, so `rec.Stored` is never the caller's `v`; for non-cosine `prepare` returns `v` unchanged, but `segment.append` copies it, so the caller's buffer is never retained. `Get` returns copies for both vector and payload (closes the aliasing/unsafe-sharing bug for dot/euclidean).

**(4) Run, expect PASS.**

**(5) Commit:**

```
cd /workspace/haystack && gofmt -w core/vectorstore/ && git add core/vectorstore && git commit -m "feat(vectorstore): Put crash-atomic upsert + Get (copy-out, no read-path allocation)"
```

---

### Task 10 — `Delete` (tombstone only, no read-path allocation)

**Create:** — (modify `store.go`)
**Test:** `core/vectorstore/store_test.go` (extend)

**(1) FAILING test** — append:

```go
func TestStore_Delete(t *testing.T) {
	s := openTestStore(t, DotProduct)
	requireNoError(t, s.Put("a", []float32{1, 2}, []byte("x")))
	requireNoError(t, s.Delete("a"))
	_, _, found, err := s.Get("a")
	requireNoError(t, err)
	if found {
		t.Fatal("Get(a) should be not-found after Delete")
	}
}

func TestStore_DeleteMissingIsPureNoOp(t *testing.T) {
	s := openTestStore(t, DotProduct)
	if err := s.Delete("never-put"); err != nil {
		t.Fatalf("Delete of missing id should be nil, got %v", err)
	}
	// Must not have allocated an id: first real Put gets docId 1.
	requireNoError(t, s.Put("a", []float32{1, 0}, nil))
	if s.idToDoc["a"] != 1 {
		t.Fatalf("Delete of unknown id must not allocate a docId")
	}
}
```

**(2) Run, expect FAIL:** `s.Delete undefined`.

**(3) Impl** — add to `store.go`:

```go
// Delete tombstones id's current slot. Deleting an unknown or already-deleted id
// is a pure no-op (no WAL write, no idtable allocation). The id↔docId mapping is
// intentionally left in place; a later Put of the same id reuses the same docId.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	docID, ok := s.idToDoc[id]
	if !ok {
		return nil
	}
	slot, ok := s.seg.slotOfDoc(docID)
	if !ok {
		return nil
	}
	if _, err := s.wal.Append(recDelete, encodeDelete(id, docID, int64(slot))); err != nil {
		return err
	}
	if err := s.wal.Sync(); err != nil {
		return err
	}
	s.seg.tombstone(slot)
	return nil
}
```

**(4) Run, expect PASS.**

**(5) Commit:**

```
cd /workspace/haystack && gofmt -w core/vectorstore/ && git add core/vectorstore && git commit -m "feat(vectorstore): Delete (tombstone only, pure no-op for unknown ids)"
```

---

### Task 11 — Brute-force `Search(q,k)` over the head segment

**Create:** — (modify `store.go`)
**Test:** `core/vectorstore/store_test.go` (extend)

**(1) FAILING test** — append:

```go
func TestStore_Search_MatchesOracle_Cosine(t *testing.T) {
	s := openTestStore(t, Cosine)
	raw := map[string][]float32{
		"a": {1, 0, 0, 0},
		"b": {0, 1, 0, 0},
		"c": {0.9, 0.1, 0, 0},
		"d": {0, 0, 1, 0},
	}
	for id, v := range raw {
		requireNoError(t, s.Put(id, v, nil))
	}
	q := []float32{1, 0, 0, 0}
	res, err := s.Search(q, 2)
	requireNoError(t, err)
	if len(res) != 2 {
		t.Fatalf("len(res) = %d, want 2", len(res))
	}
	vecs := map[int64][]float32{}
	for id, v := range raw {
		vecs[s.idToDoc[id]] = v
	}
	want := bruteForceKNN(Cosine, q, vecs, 2)
	if res[0].DocID != want[0] || res[1].DocID != want[1] {
		t.Fatalf("search docIds = [%d %d], want %v", res[0].DocID, res[1].DocID, want)
	}
	if res[0].Distance > res[1].Distance {
		t.Fatalf("results not ascending by distance: %+v", res)
	}
}

func TestStore_Search_MatchesOracle_Euclidean(t *testing.T) {
	s := openTestStore(t, Euclidean)
	raw := map[string][]float32{
		"a": {0, 0},
		"b": {3, 4},   // dist 5 from origin
		"c": {1, 1},   // dist ~1.41
		"d": {10, 10}, // far
	}
	for id, v := range raw {
		requireNoError(t, s.Put(id, v, nil))
	}
	q := []float32{0, 0}
	res, err := s.Search(q, 3)
	requireNoError(t, err)
	vecs := map[int64][]float32{}
	for id, v := range raw {
		vecs[s.idToDoc[id]] = v
	}
	want := bruteForceKNN(Euclidean, q, vecs, 3)
	for i := range want {
		if res[i].DocID != want[i] {
			t.Fatalf("euclidean search[%d] = %d, want %d (full=%v)", i, res[i].DocID, want[i], want)
		}
	}
}

func TestStore_Search_SkipsTombstoned(t *testing.T) {
	s := openTestStore(t, DotProduct)
	requireNoError(t, s.Put("a", []float32{1, 0}, nil))
	requireNoError(t, s.Put("b", []float32{0, 1}, nil))
	requireNoError(t, s.Delete("a"))
	res, err := s.Search([]float32{1, 0}, 5)
	requireNoError(t, err)
	da := s.idToDoc["a"]
	for _, r := range res {
		if r.DocID == da {
			t.Fatal("tombstoned docId must not appear in results")
		}
	}
}

func TestStore_Search_EmptyReturnsNil(t *testing.T) {
	s := openTestStore(t, Cosine)
	res, err := s.Search([]float32{1, 0}, 3)
	requireNoError(t, err)
	if res != nil {
		t.Fatalf("search on empty store = %v, want nil", res)
	}
}

func TestStore_Search_RejectsBadVectorAndK(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.Put("a", []float32{1, 0}, nil))
	if _, err := s.Search([]float32{}, 3); err == nil {
		t.Fatal("empty query should be rejected")
	}
	if _, err := s.Search([]float32{1, 0}, 0); err == nil {
		t.Fatal("k<=0 should be rejected")
	}
}
```

**(2) Run, expect FAIL:** `s.Search undefined`.

**(3) Impl** — add to `store.go`:

```go
// Search returns the k nearest live records to q under the store's metric,
// brute-scanning the single head segment. An empty store returns (nil, nil).
// Results are in docId space (see SearchResult / decision #4).
func (s *Store) Search(q []float32, k int) ([]SearchResult, error) {
	if k <= 0 {
		return nil, errors.New("vectorstore: k must be positive")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := validateVector(q, s.seg.dim, s.metric); err != nil {
		return nil, err
	}
	pq, _ := s.metric.prepare(q)
	tk := newTopK(k)
	s.seg.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		tk.offer(SearchResult{DocID: docID, Distance: s.metric.distance(stored, pq)})
	})
	out := tk.sorted()
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
```

> Lock discipline: `eachLive` hands the callback the segment's internal stored slice; `distance` consumes it synchronously under `RLock` and never retains it, so there is no escape. Writers take the full `Lock`, so no concurrent append/tombstone mutates the slices mid-scan.

**(4) Run, expect PASS.**

**(5) Commit:**

```
cd /workspace/haystack && gofmt -w core/vectorstore/ && git add core/vectorstore && git commit -m "feat(vectorstore): brute-force Search(q,k) over head segment (cosine/dot/euclidean)"
```

---

### Task 12 — Clean reopen recovery (graceful `Close` → reopen)

**Create:** — (no production change; replay was implemented in Task 8)
**Test:** `core/vectorstore/store_test.go` (extend)

**(1) FAILING test** — append. This proves WAL replay rebuilds state after a graceful shutdown. It would FAIL against the Task-8 `replay` only if replay were a stub; since Task 8 implemented full replay, this test passes and locks the behavior in. (To self-verify the red phase, an implementer may temporarily revert `replay` to a no-op and confirm this test fails, then restore it.)

```go
func TestStore_Reopen_AfterClose(t *testing.T) {
	dir := t.TempDir()
	store := newTestKV(t) // shared across both opens, closed at cleanup

	s1, err := Open(Options{Dir: dir, KV: store, Metric: Cosine})
	requireNoError(t, err)
	requireNoError(t, s1.Put("a", []float32{1, 0, 0}, []byte("pa")))
	requireNoError(t, s1.Put("b", []float32{0, 1, 0}, []byte("pb")))
	requireNoError(t, s1.Put("a", []float32{0, 0, 1}, []byte("pa2"))) // upsert
	requireNoError(t, s1.Delete("b"))
	requireNoError(t, s1.Close()) // graceful: commits idtable + flushes WAL

	s2, err := Open(Options{Dir: dir, KV: store, Metric: Cosine})
	requireNoError(t, err)
	defer s2.Close()

	v, pl, found, err := s2.Get("a")
	requireNoError(t, err)
	if !found || string(pl) != "pa2" || !approxEqual(v[2], 1, 1e-4) {
		t.Fatalf("after reopen Get(a) = %v,%q,%v, want [0 0 1],pa2,true", v, pl, found)
	}
	if _, _, found, _ := s2.Get("b"); found {
		t.Fatal("deleted id b must stay deleted after reopen")
	}
	res, err := s2.Search([]float32{0, 0, 1}, 5)
	requireNoError(t, err)
	if len(res) != 1 {
		t.Fatalf("live results after reopen = %d, want 1", len(res))
	}
}
```

**(2) Run, expect PASS** (replay already implemented). If you reverted `replay` to verify the red phase, restore it before committing.

**(3) Impl:** none.

**(5) Commit:**

```
cd /workspace/haystack && gofmt -w core/vectorstore/ && git add core/vectorstore && git commit -m "test(vectorstore): clean reopen recovery (graceful Close → reopen)"
```

---

### Task 13 — Unclean-crash recovery (NO `Close`; WAL is the durable source of truth)

**Create:** — (no production change; this red-proofs the durability design from decision #9)
**Test:** `core/vectorstore/store_test.go` (extend)

This is the test the original plan was missing: it simulates a **real crash** by abandoning the first `Store` **without calling `Close`** (so idtable's lazily-batched id→docId mapping and `nextId` are NEVER committed to KV), then reopens over the same dir + same KV and asserts the records and their string-id mappings survive purely via WAL replay. To avoid the background 5s commit accidentally persisting the batch, the test uses a long `CommitInterval` is not configurable through `Options`; instead the test simply completes in well under 5s, so the timer never fires. We also close ONLY the WAL fd of the abandoned store (so the OS releases the file) without committing the allocator.

To close only the WAL on the abandoned store, add a tiny test-only accessor in `store.go` guarded for test use — but to avoid production API surface, the test instead opens the second store while the first is still live (the first's WAL writes are already fsynced; the second store's `OpenWAL` reads the same file). Pebble allows a single process to share the already-open `kv.Store`. Concretely:

**(1) FAILING test** — append:

```go
func TestStore_CrashRecovery_NoClose_WALIsSourceOfTruth(t *testing.T) {
	dir := t.TempDir()
	store := newTestKV(t) // shared; NOT closed between opens

	s1, err := Open(Options{Dir: dir, KV: store, Metric: Cosine})
	requireNoError(t, err)
	requireNoError(t, s1.Put("a", []float32{1, 0, 0}, []byte("pa")))
	requireNoError(t, s1.Put("b", []float32{0, 1, 0}, []byte("pb")))
	requireNoError(t, s1.Put("a", []float32{0, 0, 1}, []byte("pa2"))) // upsert
	requireNoError(t, s1.Delete("b"))
	docA := s1.idToDoc["a"]

	// CRASH: do NOT call s1.Close(). idtable's id→docId batch and nextId were
	// never committed to KV. Close ONLY the WAL fd so the OS releases the file;
	// the allocator is deliberately left uncommitted to mimic a kill.
	requireNoError(t, s1.wal.Close())

	// Reopen over the SAME dir + SAME KV. Recovery must come entirely from the
	// WAL: the segment, the id→docId map, and a consistent allocator nextId.
	s2, err := Open(Options{Dir: dir, KV: store, Metric: Cosine})
	requireNoError(t, err)
	defer s2.Close()

	// The string id "a" must still resolve to the SAME docId and the upserted
	// vector/payload — proving the WAL, not idtable, carried the mapping.
	if got := s2.idToDoc["a"]; got != docA {
		t.Fatalf("recovered docId for a = %d, want %d (WAL must carry the mapping)", got, docA)
	}
	v, pl, found, err := s2.Get("a")
	requireNoError(t, err)
	if !found || string(pl) != "pa2" || !approxEqual(v[2], 1, 1e-4) {
		t.Fatalf("crash-recovered Get(a) = %v,%q,%v, want [0 0 1],pa2,true", v, pl, found)
	}
	if _, _, found, _ := s2.Get("b"); found {
		t.Fatal("deleted id b must stay deleted after crash recovery")
	}
	res, err := s2.Search([]float32{0, 0, 1}, 5)
	requireNoError(t, err)
	if len(res) != 1 {
		t.Fatalf("live results after crash recovery = %d, want 1", len(res))
	}

	// A fresh Put after recovery must get a NEW, non-colliding docId (nextId was
	// resynced by replay re-driving the allocator).
	requireNoError(t, s2.Put("c", []float32{1, 0, 0}, nil))
	if s2.idToDoc["c"] == docA {
		t.Fatalf("new id c collided with recovered docId %d — nextId not resynced", docA)
	}
}
```

**(2) Run.** Expected behavior:
- **Red-proof:** if `replay` is reverted to a no-op, OR if `Put` is changed to mutate the segment *before* `wal.Sync`, OR if the string id is dropped from the WAL record, this test FAILS (the docId mapping or the records do not survive). Confirm at least the no-op-`replay` revert reddens it, then restore.
- With the implemented design it PASSES: the WAL carried `(id, docId)` for every record; replay re-drove `GetId` in order, reconstructing the same docIds and resyncing `nextId`, and rebuilt the segment + `idToDoc`.

**(3) Impl:** none (the durability design is already in Task 6/8/9). `s1.wal` is package-private and the test is in-package, so the `wal.Close()` reach-in compiles without new production API.

**(5) Commit:**

```
cd /workspace/haystack && gofmt -w core/vectorstore/ && git add core/vectorstore && git commit -m "test(vectorstore): unclean-crash recovery — WAL is the durable id↔docId source of truth"
```

---

### Task 14 — Error-path + Close/Open-fault coverage (clear the function/block bars)

**Create:** — (no production code; coverage-hardening tests)
**Test:** `core/vectorstore/store_test.go` (extend)

These hit the remaining `err != nil` branches that go-cov's per-function and (CI-mode) critical-block floors require: `Close` surfacing a WAL close error, `Open` failing on a WAL scan error, `Put`/`Delete` failing on a WAL Sync error.

**(1) FAILING test** — append:

```go
func TestStore_CloseSurfacesWALError(t *testing.T) {
	dir := t.TempDir()
	store := newTestKV(t)
	withOpenFileFault(t, func(f *faultFile) { f.failClose = true })
	s, err := Open(Options{Dir: dir, KV: store, Metric: Cosine})
	requireNoError(t, err)
	if err := s.Close(); err == nil {
		t.Fatal("Close should surface the injected WAL close error")
	}
}

func TestStore_OpenWALScanError(t *testing.T) {
	dir := t.TempDir()
	store := newTestKV(t)
	withOpenFileFault(t, func(f *faultFile) { f.failTruncate = true }) // scanLSN truncates
	if _, err := Open(Options{Dir: dir, KV: store, Metric: Cosine}); err == nil {
		t.Fatal("Open should fail when WAL scan truncate fails")
	}
}

func TestStore_PutSyncError(t *testing.T) {
	dir := t.TempDir()
	store := newTestKV(t)
	s, err := Open(Options{Dir: dir, KV: store, Metric: Cosine})
	requireNoError(t, err)
	defer s.Close()
	// Replace the WAL's file with a Sync-faulting wrapper after open so the
	// Put's fsync fails.
	s.wal.file = &faultFile{osFile: s.wal.file, failSync: true}
	if err := s.Put("a", []float32{1, 0}, nil); err == nil {
		t.Fatal("Put should surface a WAL Sync failure")
	}
}

func TestStore_DeleteSyncError(t *testing.T) {
	dir := t.TempDir()
	store := newTestKV(t)
	s, err := Open(Options{Dir: dir, KV: store, Metric: Cosine})
	requireNoError(t, err)
	defer s.Close()
	requireNoError(t, s.Put("a", []float32{1, 0}, nil))
	s.wal.file = &faultFile{osFile: s.wal.file, failSync: true}
	if err := s.Delete("a"); err == nil {
		t.Fatal("Delete should surface a WAL Sync failure")
	}
}
```

> `s.wal.file` is package-private and the test is in-package, so swapping in a `faultFile` after open is legal and targets exactly the `Put`/`Delete` Sync-error branch without re-faulting the open path.

**(2) Run, expect PASS** (these exercise existing error returns). If any earlier-noted branch is still uncovered, add a targeted test.

**(3) Impl:** none.

**(4) Run the coverage gate the way CI runs it** (CI-mode is what enforces the floors; a bare run gives a false green):

```
cd /workspace/haystack/core && go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.2 -ci
```

Expected: exit 0, with `vectorstore` at ≥85% package / every function ≥80% / TOTAL ≥90% and no critical uncovered blocks. If a function is <80% or a block is critical, add a targeted test for that branch BEFORE committing (do not edit `core/.go-cov.toml` excludes).

**(5) Commit:**

```
cd /workspace/haystack && gofmt -w core/vectorstore/ && git add core/vectorstore && git commit -m "test(vectorstore): error-path coverage (Close/Open/Put/Delete WAL faults)"
```

---

### Task 15 — Package doc + final gate (build/vet/test/-race/go-cov/gofmt green)

**Create:** `core/vectorstore/doc.go`
**Test:** full local CI-equivalent (no new unit test)

**(1) Run the full local CI-equivalent and capture any red:**

```
cd /workspace/haystack/core && go build ./... && go vet ./vectorstore/... && go test -race ./vectorstore/... && go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.2 -ci
cd /workspace/haystack && gofmt -l core/vectorstore
```

Expected: build/vet/-race clean; `gofmt -l` prints nothing; go-cov `-ci` exit 0. (CI's Core/ubuntu leg runs go-cov *without* `-ci` because `CI=true` is set by GitHub Actions, which flips CIMode on — running `-ci` locally reproduces that exact gate. CI's mac/win legs run `go test -v` and the App job runs only gofmt over `core/`; none run `-race`, so `-race` here is local hygiene, not a CI guarantee.)

**(3) Impl** — `core/vectorstore/doc.go`:

```go
// Package vectorstore is the Phase-1 records layer of the vector store engine.
//
// It stores records as (string id, vector, payload) in a single in-memory "head"
// segment and answers k-nearest-neighbor queries by brute force, synchronously.
// Vectors are kept in their metric-natural stored form (cosine: unit vector +
// norm; dot/euclidean: raw + norm 0); the original vector is reconstructed (as a
// fresh copy) on Get. A string id maps to a stable int64 docId via core/idtable.
//
// Durability: each Put/Delete is written to a CRC-checked write-ahead log and
// fsynced before any in-memory state changes, and the WAL record carries BOTH
// the records data AND the string id, so the WAL is the single crash-safe source
// of truth for the id-to-docId mapping. On reopen the head and all derived maps
// are rebuilt exactly by replaying the log in order (which also re-drives the
// allocator to reconstruct identical docIds), so a Put/Delete is durable on
// return even after an unclean crash.
//
// Two-level id model (architecture §4.6): a global docId-to-segId map plus a
// per-segment docId-to-slot map. In Phase 1 there is exactly one segment (the
// head), so the global level is the identity {every docId -> head} and is
// intentionally not materialized; the per-segment map is segment.docToSlot.
//
// Search returns results in docId space ([]SearchResult{DocID, Distance}).
// Mapping docId back to the caller's string id is the caller's responsibility in
// Phase 1, because idtable has no reverse map. If string-id results are required,
// that is a scope addition beyond Phase 1.
//
// Phase 1 deliberately excludes: sealing the head into immutable on-disk
// segments, the on-disk mmap segment file format + manifest atomic-swap, per-
// segment HNSW/IVF index building, N-way segment merge, compaction / space
// reclaim (Delete only tombstones), attribute filtering, and multiple indexes.
// Those arrive in later phases (see core/docs/vectorstore/architecture.md §8;
// the standalone "payload" phase in §8 is subsumed here because §8.1 folds
// payload into the records layer). The existing core/vectorindex (HNSW) package
// is independent and unaffected.
package vectorstore
```

**(4) Run, expect ALL PASS:**

```
cd /workspace/haystack/core && go build ./... && go vet ./vectorstore/... && go test -race ./vectorstore/... && go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.2 -ci
cd /workspace/haystack && gofmt -l core/vectorstore
```

Expected: builds clean; `vet` clean; `-race` clean; go-cov `-ci` exit 0 with `vectorstore ≥85%` package, every function ≥80%, TOTAL ≥90%, no critical blocks; `gofmt -l` prints nothing.

**(5) Commit:**

```
cd /workspace/haystack && gofmt -w core/vectorstore/ && git add core/vectorstore && git commit -m "docs(vectorstore): package overview; Phase 1 gate green (build/vet/race/go-cov -ci)"
```

---

## Done criteria (Phase 1 exit gate)

1. `cd core && go build ./...` clean; `go vet ./vectorstore/...` clean.
2. `cd core && go test -race ./vectorstore/...` green. (Local hygiene; note CI does **not** run `-race`.)
3. `cd core && go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.2 -ci` exits 0 with `vectorstore` at **≥85% package / ≥80% per-function**, TOTAL **≥90%**, no critical uncovered blocks, and **no entry added to `core/.go-cov.toml` exclude lists**. (The `-ci` flag is mandatory locally: without it go-cov runs in non-CI mode and returns 0 even when below threshold — a false green. GitHub Actions sets `CI=true`, which enables the same enforcement.)
4. `gofmt -l core/vectorstore` prints nothing (App job's `check-fmt.sh`).
5. CI jobs green: **Core (ubuntu/macos/windows)** is the only job that compiles/tests/coverage-gates the new package (ubuntu = go-cov gate, mac/win = `go test -v`); **App (ubuntu)** enforces only repo-wide gofmt on the new files (its root-module `go build ./...` / `test_and_coverage.sh` do NOT compile `core/vectorstore`). No `ci.yml` edit and no `go.work` edit. The mmap-free in-memory head + trimmed `osfile.go`/WAL framing keep the mac/win legs portable (WAL uses only `fsOpenFile`/`Truncate`/`Sync`; no `fsyncDir`/mmap in Phase 1).
6. Public surface delivered exactly per §8.1: `Open(Options)`, `Store.Put(id,vec,payload)`, `Get(id)→(vec,payload,found,err)`, `Delete(id)`, `Search(q,k)→[]SearchResult`, `Metric()`, `Close()`. Crash recovery is verified by BOTH `TestStore_Reopen_AfterClose` (graceful) AND `TestStore_CrashRecovery_NoClose_WALIsSourceOfTruth` (unclean kill; WAL is the durable id↔docId source of truth per §4.8).
7. **Known Phase-1 limitation (surfaced, not buried):** `Search` returns docId, not string id (idtable has no reverse map). If string-id results are a Phase-1 acceptance requirement, that is a scope addition — confirm with the spec owner before shipping.

## Explicitly OUT of Phase 1 (do not implement; later phases)

Sealing head→sealed segments; on-disk mmap segment file format + manifest atomic-swap; per-segment HNSW/IVF graph; N-way merge / size-tiered compaction / space reclaim; attribute indexes + filtered search; multi-index (`CreateVectorIndex`/`Drop`/`Rebuild`); `WaitForIndex`/`IndexLag`; the materialized global `docId→segId` map (degenerate identity in Phase 1). Delete remains tombstone-only; id↔docId mappings are never reverse-deleted.

---

**Relevant grounding files (absolute paths):**
- Architecture: `/workspace/haystack/core/docs/vectorstore/architecture.md` (§3 storage form, §4.6 two-level id, §4.8 recovery/durability, §8.1 scope).
- Reuse sources (copied/ported in Tasks 1, 5, 6): `/workspace/haystack/core/vectorindex/metric.go`, `metric_distance_amd64.go`, `metric_distance_arm64.go`, `dot_arm64.go`, `dot_arm64.s`, `osfile.go`, `mmap_wal.go`.
- API/type templates: `/workspace/haystack/core/vectorindex/mem_store.go` (segment shape), `/workspace/haystack/core/vectorindex/types.go` (`SearchResult`), `/workspace/haystack/core/vectorindex/hnsw.go:131` (`validateVector` cosine checks, a method — reimplemented as a free function here), `/workspace/haystack/core/idtable/idtable.go` (`New`/`GetId` allocate-on-first-sight, lazy 5s/Close batch commit — the reason the WAL must carry the id; 8-byte BE id), `/workspace/haystack/core/kv/kv.go` + `/workspace/haystack/core/kv/pebblekv/db.go` (`Open(path string, cacheSize int64) (kv.Store, error)`).
- Gate config: `/workspace/haystack/core/.go-cov.toml` (total 90 / function 80 / package 85 / print 85, empty excludes; CIMode = `os.Getenv("CI")=="true"`, fail on CRITICAL func/package/total/blocks only in CIMode). CI: `/workspace/haystack/.github/workflows/ci.yml` (Core job working-directory `core`; App job root-module gofmt-only over the new files); `/workspace/haystack/.github/workflows/scripts/{test_and_coverage.sh,check-fmt.sh}`.