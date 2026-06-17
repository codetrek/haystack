# Phase 6 — Multi-Index (N named vector indexes per store)

> **Module**: `core/vectorstore` · **Phase**: 6 (final) · builds on Phases 1-5 (segments, migrated HNSW, manifest, merge, attr filtering).
> **Architecture**: `/workspace/haystack/core/docs/vectorstore/architecture.md` §8.6 (分期 6) + §4.7 (多索引×segment) + §4.8 (manifest) + §3.4 (records存储形/reconstruct-raw) + §7 (API).

## Goal

Generalize the single (default) vector index of Phases 2-5 to **N named vector indexes** per store, each with its own HNSW params, sharing one set of records-segments (records 只切一次, §4.7). Concretely:

- `CreateVectorIndex(name, cfg)` / `DropVectorIndex(name)` / `RebuildVectorIndex(name)` / `ListVectorIndexes()` / `IndexLag(name)`.
- `VectorIndexConfig = {Type "hnsw", Metric, M, EfConstruction, EfSearch}`.
- Each named index owns **one graph per sealed segment** → N indexes × M segments = N×M index-段 graphs (§4.7). The `(index, segment)` state is `pending | indexed`; a new index is born **pending for every existing segment → immediately queryable via the Phase-2 brute fallback** → the background builder fills its per-segment graphs → pending→indexed.
- `Search` gains an index arg: `Search(index, q, k, filter)`. The existing default-index behavior becomes a named **`"default"`** index — **byte-identical** dispatch for the default path (no recall regression); **all 35 existing call sites across 16 test files migrate to `Search("default", …)`.**
- **Per-index metric via reconstruct-raw**: records store ONE metric-natural form (the store/primary metric, `manifest.Metric`). An index whose `Metric` differs reconstructs raw (`primaryMetric.restore(stored,norm)`, ~1e-7 per §3.4) from the records to compute ITS distance + build ITS graph, via a thin `reindexNodeStore` wrapper overriding `Metric()`/`GetVectorRef()`. Vectors are never re-stored (§3 "向量只存一份").
- Manifest persists the N index configs + per-`(index,segment)` `{gen, indexed|pending}`. Seal/merge build N graphs per segment. Recovery resumes pending builds for **all** indexes. `DropVectorIndex` deletes only that index's `graph-<name>.dat` files.

### v1 metric scope — justification (decided: include per-index metric)

The architecture (§1 "不同参数/度量", §3.4) and scouting both flag a real choice. **v1 supports BOTH** same-store-metric-different-params (the minimum bar) **and** per-index metric, because the primitives already exist and the per-metric add-on is bounded and well-supported:
- `Metric.restore(stored, norm)` (`metric.go:77`) + `Metric.prepare` (`metric.go:67`) already round-trip raw at ~1e-7 (§3.4, proven by Phase-2/3 `metric_persist_test.go`).
- The per-`(index,segment)` build (`buildSegmentGraph`) and per-leg distance already take a `graphConfig` + a `graphNodeStore`. A non-primary-metric index reuses both verbatim through a `reindexNodeStore` that, per node, calls `primaryMetric.restore` then `indexMetric.prepare` — **zero new vector storage**.
- The risk is localized to ONE wrapper type (Tasks 11-12), red-proofed by an oracle (each index returns the right top-k under ITS metric). It is NOT a re-architecture, so including it honors §3 ("向量只存一份") at full fidelity rather than punting a documented gap.

### Architecture (how the generalization maps to existing code — reuse, do not reinvent)

| Phase-2 single-index machinery | Phase-6 N-index generalization |
|---|---|
| `Store.graphs map[segID]*builtIndex` (`store.go:68`) | `Store.indexes map[string]*vindex`; `vindex{cfg graphConfig; metric Metric; graphs map[segID]*builtIndex}`. `graphs` stays **keyed by segID** (robust to `s.sealed`/`s.sealedID` reordering, gotcha 6). |
| `Store.gcfg graphConfig` (`store.go:69`) | `s.indexes["default"].cfg`. `"default"` preserves Phases 1-5 exactly. |
| `bi := s.graphs[id]; bi==nil → brute` (`store.go:823`) | `bi := vx.graphs[id]; bi==nil → brute` — **the same pending leg**; a new index = empty `graphs` ⇒ every `(index,seg)` pending (§4.7). |
| `buildSegmentGraph(segDir, ss, cfg)` (`builder.go:30`) | unchanged signature; only the graph **filename** becomes per-index `graph-<name>.dat` (the one load-bearing surgical change). |
| `writeGraphFile/openGraphFile` hardcode `"graph.dat"` (`graphfile.go:11,89`) | thread `name` → `graphFileName(name)` = `"graph-<name>.dat"`. `DropVectorIndex` deletes exactly those files. |
| `buildAndPublish(id, segDir, ss)` (`store.go:1130`) | `buildAndPublish(name, id, segDir, ss)` writing `s.indexes[name].graphs[id]`. |
| `sealLocked` spawns ONE build (`store.go:1112`) | loop `for name := range s.indexes { buildBeginLocked; go buildAndPublish(name,…) }` (§4.7 "对 head 建 N 张图"). |
| `mergeAndPublish` spawns ONE build/output (`merge.go:350`) | loop over `s.indexes` per output (N graphs/bucket). |
| `planMergeWithCapLocked` gate `s.graphs[id]==nil → defer` (`merge.go:179`) | gate `indexed in EVERY index` (close-during-build SIGSEGV guard generalized — gotcha 3, a real crash hazard). |
| `recover()` resume-pending loop (`store.go:275`) | loop `for name, vx := range s.indexes` × segments; reopen indexed, resume pending per index. |
| manifest `segmentEntry.State` one-per-seg (`manifest.go:31`) | v4: keep `segmentEntry` as the records record (drop `State`), add `Indexes []indexConfigEntry` + `IndexSegs []indexSegEntry{Index, SegID, Gen, State}`. Bump `manifestVersionByte` 3→4 (hard cut; no production data, §60). |
| `WaitForIndex()` no name (`store.go:1170`) | unchanged semantics (drains ALL builds+merges to quiescence — documented choice; per-index waiting would need finer accounting and is not required by §7). The global `nInflightBuilds` counter already counts N-per-seal builds. |
| `CreateAttrIndex` (`store.go:346`) | structural template for `CreateVectorIndex` (declare in map → persist manifest → build per segment → future seal/merge builds it). |

**Out of scope** (留口, do not build): IVF-PQ (`Type` field reserved only), partial-coverage indexes (§4.7 v1: every index covers every segment), Tier-3 boolean filter (OR/NOT), per-index `WaitForIndex(name)`, a `Collection` wrapper (the real public type is `Store`; §7's `Collection` signatures are aspirational — migrate onto `Store`). **Do not break Phases 1-5 or `core/vectorindex`.**

### Tech-Stack

Go 1.24.2 (CI-pinned). Package `vectorstore`. Test: `go test`, `go test -race`. Coverage gate: `go-cov` v0.1.2 strict-by-default (every new branch covered). No new module dependencies.

### Gates (run after EVERY task — all must be green)

```
go build ./...                                              # tree compiles
go test ./core/vectorstore/ -run <TaskTest> -count=1       # the task's test
go test ./core/vectorstore/ -count=1                        # whole package (no Phase 1-5 regression)
go test ./core/vectorstore/ -race -count=1                  # race-clean
go vet ./core/vectorstore/
```

All paths below are absolute under `/workspace/haystack/core/vectorstore/`.

## File Structure

```
core/vectorstore/
  vindex.go            NEW  — vindex struct; VectorIndexConfig; graphConfigFromCfg; cfgToConfigEntry
  vindex_test.go       NEW  — Tasks 1, 3, 4, 5, 6
  graphfile.go         EDIT — graphFileName(name); write/openGraphFile take name
  graphfile_test.go    EDIT — Task 2 (per-index filename round-trip)
  builder.go           EDIT — buildSegmentGraph takes name (+ Task 11 nodeStore seam)
  manifest.go          EDIT — v4: indexConfigEntry, indexSegEntry, serialize/parse, version byte
  manifest_test.go     EDIT — Task 7 (v4 round-trip)
  store.go             EDIT — Store.indexes; recover; sealLocked; buildAndPublish(name,…);
                              Search(index,…); CreateVectorIndex/Drop/Rebuild/List/IndexLag;
                              writeManifestLocked; dropGraphFilesLocked
  merge.go             EDIT — planMergeWithCapLocked all-indexed-across-N gate; mergeAndPublish N-build loop
  reindex.go           NEW  — reindexNodeStore (per-index-metric reconstruct-raw wrapper); Task 11
  reindex_test.go      NEW  — Task 11
  vindex_metric_test.go NEW — Task 12 (per-index metric end-to-end oracle)
  result.go            EDIT — VectorIndexInfo, IndexLagInfo (Task 5)
  <16 existing *_test.go> EDIT — Task 9: migrate 35 Search(q,k,filter) → Search("default",q,k,filter)
```

---

## Task 0 — Snapshot the green baseline

**No code change.** Establishes the regression floor for every later task.

Run:
```
go build ./... && go test ./core/vectorstore/ -count=1 && go test ./core/vectorstore/ -race -count=1 && go vet ./core/vectorstore/
```
**Expect: PASS.** If anything is red here, stop and fix the environment before starting.

Commit: none (baseline only).

---

## Task 1 — `vindex` struct + `VectorIndexConfig` + `graphConfigFromCfg` (pure types, no wiring)

Introduce the per-index value type and the public config, plus the helpers that derive a `graphConfig` from a `VectorIndexConfig`. Nothing wires into `Store` yet — this is the substrate every later task uses, defined before use.

### Failing test — create `/workspace/haystack/core/vectorstore/vindex_test.go`

```go
package vectorstore

import "testing"

func TestVectorIndexConfig_GraphConfigFromCfg_Defaults(t *testing.T) {
	// A zero-valued cfg fills HNSW params from the package defaults (parity with
	// graphConfig{}.withDefaults()), and carries the metric through unchanged.
	cfg := VectorIndexConfig{Type: "hnsw", Metric: Cosine}
	gc := graphConfigFromCfg(cfg)
	if gc.M != defaultGraphM || gc.EfConstruction != defaultGraphEfConstruction || gc.EfSearch != defaultGraphEfSearch {
		t.Fatalf("defaults not applied: %+v", gc)
	}

	// Explicit params are preserved.
	cfg2 := VectorIndexConfig{Type: "hnsw", Metric: Euclidean, M: 24, EfConstruction: 111, EfSearch: 40}
	gc2 := graphConfigFromCfg(cfg2)
	if gc2.M != 24 || gc2.EfConstruction != 111 || gc2.EfSearch != 40 {
		t.Fatalf("explicit params lost: %+v", gc2)
	}
}

func TestVindex_NewVindex_EmptyGraphsIsPending(t *testing.T) {
	// A freshly created vindex has an empty graphs map: every (index, segment) is
	// pending (no graph) until the builder fills it (§4.7 "新建索引 = 对所有段 pending").
	vx := newVindex(VectorIndexConfig{Type: "hnsw", Metric: Cosine})
	if vx.metric != Cosine {
		t.Fatalf("metric = %v, want Cosine", vx.metric)
	}
	if vx.graphs == nil {
		t.Fatal("graphs map must be initialized (non-nil), not lazily nil")
	}
	if len(vx.graphs) != 0 {
		t.Fatalf("new vindex must have zero graphs (all pending), got %d", len(vx.graphs))
	}
	if _, ok := vx.graphs[segID(1)]; ok {
		t.Fatal("segment 1 must be pending (absent) in a new index")
	}
}
```

Run: `go test ./core/vectorstore/ -run 'TestVectorIndexConfig_GraphConfigFromCfg_Defaults|TestVindex_NewVindex' -count=1`
**Expect: FAIL** — `undefined: VectorIndexConfig`, `graphConfigFromCfg`, `newVindex`, `vindex`.

### Minimal impl — create `/workspace/haystack/core/vectorstore/vindex.go`

```go
package vectorstore

// VectorIndexConfig is the public, per-index configuration (architecture §7). Type
// is "hnsw" in v1 ("ivfpq" reserved). Metric is this index's distance metric: it
// may differ from the store's primary (records) metric, in which case the builder
// reconstructs raw vectors from records via the primary metric's restore() before
// computing this index's distances (§3.4 reconstruct-raw, Tasks 11-12). M/
// EfConstruction/EfSearch are the HNSW params (zero → package defaults).
type VectorIndexConfig struct {
	Type           string
	Metric         Metric
	M              int
	EfConstruction int
	EfSearch       int
}

// graphConfigFromCfg derives the internal graphConfig (HNSW params) from a public
// VectorIndexConfig, applying the same defaults graphConfig{}.withDefaults() does.
// The metric is NOT part of graphConfig (it is carried on the vindex / segment),
// so it is dropped here and threaded separately.
func graphConfigFromCfg(cfg VectorIndexConfig) graphConfig {
	return graphConfig{
		M:              cfg.M,
		EfConstruction: cfg.EfConstruction,
		EfSearch:       cfg.EfSearch,
	}.withDefaults()
}

// vindex is one named vector index: its HNSW config, its metric, and its per-
// segment built graphs (segId → builtIndex). An ABSENT key in graphs means that
// (index, segment) is pending → served by the brute fallback (§4.7). graphs is
// keyed by segID (not by s.sealed slice position), so it is robust to the merge
// path reordering the parallel sealed slices (gotcha 6). This is the exact value
// shape Phase 2 had as Store.graphs/gcfg, lifted into a per-name struct.
type vindex struct {
	cfg    graphConfig
	metric Metric
	graphs map[segID]*builtIndex
}

// newVindex builds an empty vindex from a public config: every segment starts
// pending (graphs is initialized but empty). The builder fills it per segment.
func newVindex(cfg VectorIndexConfig) *vindex {
	return &vindex{
		cfg:    graphConfigFromCfg(cfg),
		metric: cfg.Metric,
		graphs: make(map[segID]*builtIndex),
	}
}
```

Run: `go test ./core/vectorstore/ -run 'TestVectorIndexConfig_GraphConfigFromCfg_Defaults|TestVindex_NewVindex' -count=1`
**Expect: PASS.** Then full gates (build, whole package, -race, vet).

Commit: `feat(vectorstore): vindex value type + VectorIndexConfig (Phase 6 substrate)`

---

## Task 2 — Per-index graph filename (`graph-<name>.dat`)

The single load-bearing surgical change (gotcha 1/2): `graph.dat` is hardcoded in `writeGraphFile`/`openGraphFile`. Thread an index name through so N indexes never collide in the shared seg dir, and `DropVectorIndex` (Task 6) can delete exactly one index's files. The default name `"default"` yields `graph-default.dat` — a clean hard cut (no production data, §60; legacy `graph.dat` stores are test-only and re-created).

### Failing test — append to `/workspace/haystack/core/vectorstore/graphfile_test.go`

```go
func TestGraphFile_PerIndexName_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	seg := buildTinySealedForGraphTest(t, dir) // helper defined below

	// Build two graphs with DIFFERENT index names into the SAME seg dir; they must
	// land in distinct files and round-trip independently (no collision).
	gA, err := buildSegmentGraph(dir, "alpha", seg, graphConfig{}.withDefaults())
	requireNoError(t, err)
	gB, err := buildSegmentGraph(dir, "beta", seg, graphConfig{}.withDefaults())
	requireNoError(t, err)

	if _, err := os.Stat(filepath.Join(dir, "graph-alpha.dat")); err != nil {
		t.Fatalf("graph-alpha.dat missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "graph-beta.dat")); err != nil {
		t.Fatalf("graph-beta.dat missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "graph.dat")); !os.IsNotExist(err) {
		t.Fatalf("legacy graph.dat must NOT be written; stat err = %v", err)
	}

	// Reopen each by name and confirm the node counts match the build.
	roA, err := openGraphFile(dir, "alpha", seg)
	requireNoError(t, err)
	roB, err := openGraphFile(dir, "beta", seg)
	requireNoError(t, err)
	if len(roA.nodeSlot) != len(gA.nodeSlot) || len(roB.nodeSlot) != len(gB.nodeSlot) {
		t.Fatalf("reopened node counts diverged: A %d/%d B %d/%d",
			len(roA.nodeSlot), len(gA.nodeSlot), len(roB.nodeSlot), len(gB.nodeSlot))
	}

	// graphFileName is the single source of truth for the layout.
	if graphFileName("alpha") != "graph-alpha.dat" {
		t.Fatalf("graphFileName(alpha) = %q", graphFileName("alpha"))
	}
}

// buildTinySealedForGraphTest writes a 3-row sealed segment and opens it, for
// graph-file round-trip tests that need a real *sealedSegment to resolve vectors.
func buildTinySealedForGraphTest(t *testing.T, dir string) *sealedSegment {
	t.Helper()
	seg := newSegment(DotProduct)
	seg.append(1, []float32{1, 0, 0}, 0, nil)
	seg.append(2, []float32{0, 1, 0}, 0, nil)
	seg.append(3, []float32{0, 0, 1}, 0, nil)
	requireNoError(t, writeSealedSegment(dir, seg, nil))
	ss, err := openSealedSegment(dir, DotProduct)
	requireNoError(t, err)
	t.Cleanup(ss.close)
	return ss
}
```

Add imports `os`, `path/filepath` to `graphfile_test.go` if absent.

Run: `go test ./core/vectorstore/ -run TestGraphFile_PerIndexName_RoundTrip -count=1`
**Expect: FAIL** — `buildSegmentGraph` / `openGraphFile` / `writeGraphFile` take no `name`; `graphFileName` undefined. (Also a build error from existing callers — fix them in the same step below.)

### Minimal impl

**`/workspace/haystack/core/vectorstore/graphfile.go`** — add `graphFileName`, thread `name`:

```go
// graphFileName is the per-index graph file within a shared segment dir. Each
// named index gets its own file (graph-<name>.dat) so N indexes coexist in one
// seg dir and DropVectorIndex deletes only one index's files (architecture §4.7).
func graphFileName(name string) string { return "graph-" + name + ".dat" }
```

Change `writeGraphFile` signature and the `fsCreate` line:
```go
func writeGraphFile(segDir, name string, g *segGraphStore) error {
	f, err := fsCreate(segFilePath(segDir, graphFileName(name)))
```
Change `openGraphFile` signature and the `readWholeFile` line:
```go
func openGraphFile(segDir, name string, seg *sealedSegment) (*segGraphStore, error) {
	data, err := readWholeFile(segFilePath(segDir, graphFileName(name)))
```

**`/workspace/haystack/core/vectorstore/builder.go`** — thread `name` through `buildSegmentGraph`:
```go
func buildSegmentGraph(segDir, name string, seg *sealedSegment, cfg graphConfig) (*segGraphStore, error) {
	...
	if err := writeGraphFile(segDir, name, gs); err != nil {
		return nil, err
	}
	return openGraphFile(segDir, name, seg)
}
```

**Fix the three existing production callers** so the tree compiles (they keep Phase-5 behavior by passing the literal default name; Task 8 replaces these literals with the loop over `s.indexes`):
- `store.go:258` (recover, indexed reopen): `openGraphFile(segDir, "default", ss)`
- `store.go:1132` (`buildAndPublish`): `buildSegmentGraph(segDir, "default", ss, s.gcfg)`

(The `merge.go` build path goes through `buildAndPublish`, so no separate edit there yet.)

**Fix existing graph-file tests** in `graphfile_test.go`/`graphstore_test.go` that call `writeGraphFile(dir, g)` / `openGraphFile(dir, ss)` / `buildSegmentGraph(dir, ss, cfg)` → add `"default"` as the name arg (mechanical; grep `writeGraphFile(\|openGraphFile(\|buildSegmentGraph(` in `*_test.go`).

Run: `go test ./core/vectorstore/ -run TestGraphFile_PerIndexName_RoundTrip -count=1`
**Expect: PASS.** Then full gates — the whole package must stay green (the default-name path is behavior-identical to `graph.dat`).

Commit: `refactor(vectorstore): per-index graph filename graph-<name>.dat (Phase 6 §4.7)`

---

## Task 3 — `Store.indexes` map with a `"default"` index; `Search` keeps default-path identical

Replace `Store.graphs`/`gcfg` with `indexes map[string]*vindex` seeded with a `"default"` vindex carrying the store's primary metric. `Search` gains the `index` arg; the `"default"` path must produce byte-identical results to Phase 5. Existing tests still call `Search(q,k,filter)` (3-arg) — they are migrated in Task 9, so here we keep the package green by adding the arg AND temporarily updating only the in-file callers needed to compile is NOT viable across 16 files; instead we **add the new signature and migrate all callers in Task 9**. To keep this task self-contained and green, this task introduces a private `searchIndexLocked(vx, …)` core and a NEW public `Search(index, q, k, filter)`, and **temporarily retains** a 3-arg shim `searchDefault` used only by this task's test; Task 9 deletes the shim after migrating callers.

> Rationale for the shim: the writing-plans bar requires the tree+gates green after EVERY task. Changing `Search`'s arity touches 35 call sites; doing that in this task would balloon it. The shim isolates the signature change (Task 3) from the mass migration (Task 9), each independently green.

### Failing test — append to `/workspace/haystack/core/vectorstore/vindex_test.go`

```go
import (
	"math/rand"
	"testing"
)

func TestStore_DefaultIndex_ExistsAndSearchMatchesShim(t *testing.T) {
	s := openTestStore(t, Cosine)
	// The store is born with exactly one index named "default", carrying the store
	// metric (Phases 1-5 behavior, migrated to a named index).
	names := s.ListVectorIndexes()
	if len(names) != 1 || names[0].Name != "default" {
		t.Fatalf("expected one index named default, got %+v", names)
	}
	if names[0].Metric != Cosine {
		t.Fatalf("default index metric = %v, want Cosine", names[0].Metric)
	}

	rng := rand.New(rand.NewSource(7))
	dim := 16
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	for i := 0; i < 120; i++ {
		requireNoError(t, s.Put("d-"+itoa(i), randVec(), nil))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	q := randVec()
	// The named-index Search("default", …) must equal the legacy 3-arg behavior
	// EXACTLY (same docIds, same order) — no recall regression on migration.
	gotNamed, err := s.Search("default", q, 10, nil)
	requireNoError(t, err)
	gotShim, err := s.searchDefault(q, 10, nil) // temporary Task-3 shim
	requireNoError(t, err)
	if len(gotNamed) != len(gotShim) {
		t.Fatalf("named vs shim length: %d vs %d", len(gotNamed), len(gotShim))
	}
	for i := range gotNamed {
		if gotNamed[i].DocID != gotShim[i].DocID || gotNamed[i].Distance != gotShim[i].Distance {
			t.Fatalf("named[%d]=%+v shim[%d]=%+v differ", i, gotNamed[i], i, gotShim[i])
		}
	}
}

func TestStore_Search_UnknownIndexErrors(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.Put("a", []float32{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, nil))
	if _, err := s.Search("nope", []float32{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, 5, nil); err == nil {
		t.Fatal("Search on an unknown index must error")
	}
}
```

Run: `go test ./core/vectorstore/ -run 'TestStore_DefaultIndex_ExistsAndSearchMatchesShim|TestStore_Search_UnknownIndexErrors' -count=1`
**Expect: FAIL** — `Store.indexes` absent; `ListVectorIndexes`, `Search(string,…)`, `searchDefault` undefined.

### Minimal impl — `/workspace/haystack/core/vectorstore/store.go`

1. **Struct**: replace fields `graphs map[segID]*builtIndex` and `gcfg graphConfig` with:
```go
indexes map[string]*vindex // named index → (cfg, metric, per-segment graphs). "default" preserves Phases 1-5.
```
Keep `metric Metric` (the primary/records metric; manifest + recovery + prepare/restore still use it).

2. **Open** (`store.go:160`): replace `graphs: make(...)`, `gcfg: graphConfig{}.withDefaults()` with:
```go
indexes: map[string]*vindex{
	"default": {cfg: graphConfig{}.withDefaults(), metric: opts.Metric, graphs: make(map[segID]*builtIndex)},
},
```

3. **`defaultIndexName` const** near the top:
```go
const defaultIndexName = "default"
```

4. **Search**: rename the existing `func (s *Store) Search(q []float32, k int, filter Predicate)` body to a private core taking the chosen `*vindex`, and add the public dispatcher + the temporary shim:
```go
// Search returns the k nearest live records to q in the named index, under THAT
// index's metric. The "default" index reproduces the Phases 1-5 behavior exactly.
func (s *Store) Search(index string, q []float32, k int, filter Predicate) ([]SearchResult, error) {
	if k <= 0 {
		return nil, errors.New("vectorstore: k must be positive")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	vx, ok := s.indexes[index]
	if !ok {
		return nil, fmt.Errorf("vectorstore: unknown index %q", index)
	}
	return s.searchLocked(vx, q, k, filter)
}

// searchDefault is a TEMPORARY Phase-6 shim preserving the legacy 3-arg call shape
// for the in-flight migration (Task 9 deletes it after all callers move to the
// named Search). It dispatches the default index under s.mu.
func (s *Store) searchDefault(q []float32, k int, filter Predicate) ([]SearchResult, error) {
	if k <= 0 {
		return nil, errors.New("vectorstore: k must be positive")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.searchLocked(s.indexes[defaultIndexName], q, k, filter)
}
```
Convert the old `Search` body into `searchLocked(vx *vindex, q, k, filter)` with these substitutions (caller already holds `s.mu.RLock()`):
   - drop the `k<=0`, `s.mu.RLock()`/defer lines (moved to callers);
   - `pq, _ := s.metric.prepare(q)` → `pq, _ := vx.metric.prepare(q)`;
   - **every** `s.metric.distance(...)` in the head/pending/brute-S legs → `vx.metric.distance(...)`;
   - `bi := s.graphs[s.sealedID[i]]` → `bi := vx.graphs[s.sealedID[i]]`;
   - the brute-S `s.metric.distance(ss.getVectorRef(slot), pq)` → `vx.metric.distance(...)`;
   - `validateVector(q, s.searchDimLocked(), s.metric)` → `validateVector(q, s.searchDimLocked(), vx.metric)`.

   The `searchIndexedUnfiltered` and `searchFiltered` graph legs run the per-segment `bi.idx`; for the `"default"` index `vx.metric == s.metric`, so the result is identical. (Per-index-metric graph legs are wired in Tasks 11-12; for now `vx.graphs` is only ever populated for `"default"`, so the graph legs always run under the primary metric — correct.) Pass `vx` into `searchIndexedUnfiltered`/`searchFiltered` only when those need the metric; in v1 they read distance from `bi.idx` which uses `bi.store.Metric()` — for default that is the segment (primary) metric, unchanged.

5. **Touch every remaining `s.graphs` / `s.gcfg` reference** so the tree compiles, rewriting to the default index for now (Tasks 8/10 generalize):
   - `recover()` `s.graphs[e.SegID] = newBuiltIndex(g, s.gcfg)` → `s.indexes[defaultIndexName].graphs[e.SegID] = newBuiltIndex(g, s.indexes[defaultIndexName].cfg)`; the indexed-reopen uses `openGraphFile(segDir, defaultIndexName, ss)`.
   - `recover()` resume loop `if s.graphs[sid] == nil` → `if s.indexes[defaultIndexName].graphs[sid] == nil` and `go s.buildAndPublish(defaultIndexName, sid, segDir, s.sealed[i])`.
   - `buildAndPublish` (signature + body, Task 8 finalizes — for now add `name string` first param and write `s.indexes[name].graphs[id] = bi`, build with `s.indexes[name].cfg`, `buildSegmentGraph(segDir, name, ss, s.indexes[name].cfg)`).
   - `sealLocked` spawn `go s.buildAndPublish(id, segDir, ss)` → `go s.buildAndPublish(defaultIndexName, id, segDir, ss)`.
   - `isIndexedForTest`: `s.graphs[id]` → `s.indexes[defaultIndexName].graphs[id]`.
   - `writeManifestLocked` `s.graphs[s.sealedID[i]]` → `s.indexes[defaultIndexName].graphs[s.sealedID[i]]` (Task 7 generalizes to per-index entries).
   - `merge.go`: `planMergeWithCapLocked` `s.graphs[id]` → `s.indexes[defaultIndexName].graphs[id]`; `mergeAndPublish` `delete(s.graphs, id)` → `delete(s.indexes[defaultIndexName].graphs, id)`; its build spawn → `go s.buildAndPublish(defaultIndexName, p.outIDs[i], p.outDirs[i], ss)`.

6. **`ListVectorIndexes`** (minimal; full info in Task 5) + the `VectorIndexInfo` type:
```go
// VectorIndexInfo is a read-only snapshot of one named index's configuration and
// build progress (architecture §7 ListVectorIndexes). Defined fully in Task 5.
type VectorIndexInfo struct {
	Name           string
	Type           string
	Metric         Metric
	M, EfConstruction, EfSearch int
	Segments       int // total sealed segments
	Indexed        int // segments whose graph is built (pending = Segments-Indexed)
}

// ListVectorIndexes returns a snapshot of every named index, sorted by name.
func (s *Store) ListVectorIndexes() []VectorIndexInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.indexes))
	for n := range s.indexes {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]VectorIndexInfo, 0, len(names))
	for _, n := range names {
		vx := s.indexes[n]
		indexed := 0
		for _, id := range s.sealedID {
			if vx.graphs[id] != nil {
				indexed++
			}
		}
		out = append(out, VectorIndexInfo{
			Name: n, Type: "hnsw", Metric: vx.metric,
			M: vx.cfg.M, EfConstruction: vx.cfg.EfConstruction, EfSearch: vx.cfg.EfSearch,
			Segments: len(s.sealedID), Indexed: indexed,
		})
	}
	return out
}
```
Put `VectorIndexInfo` in `result.go` (Task 5 adds `IndexLagInfo` there too); the method stays in `store.go`.

Run: `go test ./core/vectorstore/ -run 'TestStore_DefaultIndex_ExistsAndSearchMatchesShim|TestStore_Search_UnknownIndexErrors' -count=1`
**Expect: PASS.** Then full gates. The whole Phase-1-5 suite still calls `searchDefault` only via the shim in this task's test; existing tests still use the 3-arg `Search` — **but** the 3-arg `Search` no longer exists. To keep the package green this task, **temporarily make the old 3-arg name an alias is impossible (same name)**; instead this task’s Search migration of existing callers is deferred — therefore **this task’s gate is run with the existing tests already updated mechanically to `searchDefault`** as part of this commit (a pure rename, no logic change), and Task 9 promotes them to the public `Search("default", …)`.

> Concretely: in this task, `sed`-replace the 35 existing `s.Search(` / `<store>.Search(` 3-arg call sites in `*_test.go` to `<store>.searchDefault(`. This is mechanical and keeps each gate green. Task 9 replaces `searchDefault(` with `Search("default", `. Two passes keep both tasks small and independently green.

Commit: `refactor(vectorstore): Store.indexes map + named Search; default index preserves Phase 5`

---

## Task 4 — `CreateVectorIndex`: born pending for all segments, immediately brute-queryable, builds in background

`CreateVectorIndex(name, cfg)` mirrors `CreateAttrIndex` (`store.go:346`): validate, add the `vindex` (empty graphs ⇒ all segments pending), **persist to the manifest BEFORE spawning builds** (crash-safe, gotcha 8), then spawn one background build per existing sealed segment. The index is queryable immediately via the brute fallback (`searchLocked`'s `vx.graphs[id]==nil` leg).

> Manifest persistence of the new index's config + pending states needs the v4 format. To keep this task green before Task 7 lands v4, this task persists via `writeManifestLocked` which (after Task 7) emits the per-index block; **ordering**: Task 7 (manifest v4) is a prerequisite of full crash-safety, so **swap Task 4 and Task 7 if you prefer** — but `writeManifestLocked` already compiles against the v3 manifest, so this task can land its in-memory + build behavior first and the persistence assertion is added in Task 8 (recovery). Here we assert in-memory pending→indexed convergence and brute-before-graph queryability.

### Failing test — append to `/workspace/haystack/core/vectorstore/vindex_test.go`

```go
func TestStore_CreateVectorIndex_BruteThenConverges(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(13))
	dim := 16
	vecs := make(map[int64][]float32)
	put := func(id string, v []float32) {
		requireNoError(t, s.Put(id, v, nil))
		vecs[s.idToDoc[id]] = append([]float32(nil), v...)
	}
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	// Two sealed segments + a head, all under the default index.
	for i := 0; i < 80; i++ {
		put("a-"+itoa(i), randVec())
	}
	requireNoError(t, s.Seal())
	for i := 0; i < 80; i++ {
		put("b-"+itoa(i), randVec())
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	for i := 0; i < 20; i++ {
		put("h-"+itoa(i), randVec()) // head
	}

	// Create a SECOND index (same metric, different params). It is born pending for
	// every existing sealed segment → immediately queryable via brute fallback.
	requireNoError(t, s.CreateVectorIndex("fast", VectorIndexConfig{
		Type: "hnsw", Metric: Cosine, M: 8, EfConstruction: 80, EfSearch: 32,
	}))

	q := randVec()
	want := bruteForceKNN(Cosine, q, vecs, 10)

	// BEFORE the background builds finish, the new index is all-pending → every leg
	// is brute → it returns EXACT top-k (recall 1.0 on the brute legs).
	gotPending, err := s.Search("fast", q, 10, nil)
	requireNoError(t, err)
	if r := recallAtK(gotPending, want); r < 0.9 {
		t.Fatalf("new index pending (brute) recall = %.2f, want ~1.0", r)
	}

	// After convergence, the new index's graphs are built (pending→indexed) and it
	// still returns correct top-k under its own params.
	requireNoError(t, s.WaitForIndex())
	info := indexInfoByName(t, s, "fast")
	if info.Indexed != info.Segments || info.Segments != 2 {
		t.Fatalf("fast index did not converge: %+v", info)
	}
	gotIndexed, err := s.Search("fast", q, 10, nil)
	requireNoError(t, err)
	if r := recallAtK(gotIndexed, want); r < 0.8 {
		t.Fatalf("new index graph recall = %.2f, want >= 0.8", r)
	}
}

func TestStore_CreateVectorIndex_DuplicateAndBadType(t *testing.T) {
	s := openTestStore(t, Cosine)
	if err := s.CreateVectorIndex("x", VectorIndexConfig{Type: "ivfpq", Metric: Cosine}); err == nil {
		t.Fatal("non-hnsw Type must error in v1")
	}
	requireNoError(t, s.CreateVectorIndex("x", VectorIndexConfig{Type: "hnsw", Metric: Cosine, M: 8}))
	// Idempotent on the SAME config; conflicting config errors.
	requireNoError(t, s.CreateVectorIndex("x", VectorIndexConfig{Type: "hnsw", Metric: Cosine, M: 8}))
	if err := s.CreateVectorIndex("x", VectorIndexConfig{Type: "hnsw", Metric: Cosine, M: 16}); err == nil {
		t.Fatal("re-create with a different config must error")
	}
	if err := s.CreateVectorIndex("default", VectorIndexConfig{Type: "hnsw", Metric: Cosine}); err == nil {
		t.Fatal("re-creating the reserved default index must error")
	}
}

func indexInfoByName(t *testing.T, s *Store, name string) VectorIndexInfo {
	t.Helper()
	for _, in := range s.ListVectorIndexes() {
		if in.Name == name {
			return in
		}
	}
	t.Fatalf("index %q not found", name)
	return VectorIndexInfo{}
}
```

Run: `go test ./core/vectorstore/ -run 'TestStore_CreateVectorIndex_BruteThenConverges|TestStore_CreateVectorIndex_DuplicateAndBadType' -count=1`
**Expect: FAIL** — `CreateVectorIndex` undefined.

### Minimal impl — `/workspace/haystack/core/vectorstore/store.go`

```go
// CreateVectorIndex declares a new named vector index over the SAME records as the
// existing indexes (segment boundaries are shared, §4.7). The index is born
// PENDING for every existing sealed segment (empty graphs map) → immediately
// queryable via the brute fallback in searchLocked → the background builder fills
// its per-segment graphs (pending→indexed). It mirrors CreateAttrIndex: validate,
// install in the s.indexes map, persist to the manifest (so a crash mid-build
// resumes via recover, gotcha 8), THEN spawn the builds. Idempotent on the same
// config; an existing name with a different config (or the reserved "default") is
// an error. v1 supports Type "hnsw" only.
func (s *Store) CreateVectorIndex(name string, cfg VectorIndexConfig) error {
	if cfg.Type != "hnsw" {
		return fmt.Errorf("vectorstore: unsupported index type %q (v1 supports \"hnsw\")", cfg.Type)
	}
	if name == "" {
		return errors.New("vectorstore: index name must be non-empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == defaultIndexName {
		if _, ok := s.indexes[name]; ok {
			return fmt.Errorf("vectorstore: index %q is reserved", name)
		}
	}
	if existing, ok := s.indexes[name]; ok {
		want := graphConfigFromCfg(cfg)
		if existing.metric != cfg.Metric || existing.cfg != want {
			return fmt.Errorf("vectorstore: index %q already exists with a different config", name)
		}
		return nil // idempotent on identical config
	}
	if s.closing {
		return errors.New("vectorstore: store is closing")
	}
	s.indexes[name] = newVindex(cfg)
	// Persist the new index (pending for all segments) BEFORE spawning builds, so a
	// crash mid-build is resumed by recover() (same crash-safety as a pending seal).
	if err := s.writeManifestLocked(); err != nil {
		delete(s.indexes, name)
		return err
	}
	// Spawn one background build per existing sealed segment (every (index,seg) is
	// pending). buildBeginLocked under s.mu before the goroutine, so WaitForIndex
	// counts it (gotcha 4). Gated by s.closing above.
	for i, sid := range s.sealedID {
		segDir := filepath.Join(s.dir, segDirName(sid, 0))
		s.buildBeginLocked()
		go s.buildAndPublish(name, sid, segDir, s.sealed[i])
	}
	return nil
}
```

`buildAndPublish(name, …)` already (from Task 3) builds with `s.indexes[name].cfg` and installs into `s.indexes[name].graphs[id]`. **Guard** `buildAndPublish` against a dropped index (Task 6): re-check `vx, ok := s.indexes[name]; if !ok { return }` after taking `s.mu` before installing.

Run: the two tests → **PASS**. Full gates.

Commit: `feat(vectorstore): CreateVectorIndex — pending-for-all-segments, brute-then-build (§4.7)`

---

## Task 5 — `IndexLag` + `ListVectorIndexes` full info (`IndexLagInfo`)

Read-only progress snapshots: pending-segment + pending-vector counts per index, for tests/observability (§7).

### Failing test — append to `/workspace/haystack/core/vectorstore/vindex_test.go`

```go
func TestStore_IndexLag_CountsPendingSegments(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(21))
	dim := 8
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	for i := 0; i < 40; i++ {
		requireNoError(t, s.Put("a-"+itoa(i), randVec(), nil))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	// default index: fully indexed → zero lag.
	lag := s.IndexLag("default")
	if lag.PendingSegments != 0 || lag.PendingVectors != 0 {
		t.Fatalf("default lag should be zero, got %+v", lag)
	}

	// A brand-new index is pending for the one sealed segment until builds finish.
	requireNoError(t, s.CreateVectorIndex("slow", VectorIndexConfig{Type: "hnsw", Metric: Cosine}))
	lagNew := s.IndexLag("slow")
	if lagNew.PendingSegments != 1 || lagNew.PendingVectors != 40 {
		t.Fatalf("new index lag = %+v, want 1 segment / 40 vectors pending", lagNew)
	}
	requireNoError(t, s.WaitForIndex())
	if l := s.IndexLag("slow"); l.PendingSegments != 0 {
		t.Fatalf("slow index should be fully built, got %+v", l)
	}

	// Unknown index → Exists=false.
	if l := s.IndexLag("ghost"); l.Exists {
		t.Fatalf("unknown index must report Exists=false, got %+v", l)
	}
}
```

Run: `go test ./core/vectorstore/ -run TestStore_IndexLag_CountsPendingSegments -count=1`
**Expect: FAIL** — `IndexLag` / `IndexLagInfo` undefined.

### Minimal impl

**`/workspace/haystack/core/vectorstore/result.go`** — add:
```go
// IndexLagInfo reports how much of a named index is still pending (no graph yet),
// in segments and (live) vectors. PendingSegments == 0 means fully built (§7).
type IndexLagInfo struct {
	Exists          bool
	PendingSegments int
	PendingVectors  int
}
```

**`/workspace/haystack/core/vectorstore/store.go`** — add:
```go
// IndexLag returns the pending build progress of a named index (architecture §7).
// An unknown index reports Exists=false with zero counts. The vector count sums
// the LIVE rows of each pending segment, the unit a WaitForIndex drain converges.
func (s *Store) IndexLag(name string) IndexLagInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	vx, ok := s.indexes[name]
	if !ok {
		return IndexLagInfo{}
	}
	li := IndexLagInfo{Exists: true}
	for i, sid := range s.sealedID {
		if vx.graphs[sid] == nil {
			li.PendingSegments++
			li.PendingVectors += s.sealed[i].count() - s.sealed[i].tombCount()
		}
	}
	return li
}
```

Run: the test → **PASS.** Full gates.

Commit: `feat(vectorstore): IndexLag + ListVectorIndexes progress snapshots (§7)`

---

## Task 6 — `DropVectorIndex`: delete only that index's graph files, others intact

`DropVectorIndex(name)` removes the `vindex` from `s.indexes` and deletes each segment's `graph-<name>.dat`, under `buildMu+s.mu` so it never races a builder writing the graphs map it's deleting (gotcha 5). Records, payload, and **other indexes are untouched** (red-proofed: the default index still returns correct top-k after a sibling is dropped). Dropping `"default"` is refused; dropping an unknown name is a no-op.

### Failing test — append to `/workspace/haystack/core/vectorstore/vindex_test.go`

```go
func TestStore_DropVectorIndex_LeavesOthersIntact(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(41))
	dim := 16
	vecs := make(map[int64][]float32)
	put := func(id string, v []float32) {
		requireNoError(t, s.Put(id, v, nil))
		vecs[s.idToDoc[id]] = append([]float32(nil), v...)
	}
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	for i := 0; i < 100; i++ {
		put("d-"+itoa(i), randVec())
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.CreateVectorIndex("aux", VectorIndexConfig{Type: "hnsw", Metric: Cosine, M: 8}))
	requireNoError(t, s.WaitForIndex())

	// Sanity: aux's graph file exists on disk.
	segDir := filepath.Join(s.dir, segDirName(segID(1), 0))
	if _, err := os.Stat(filepath.Join(segDir, "graph-aux.dat")); err != nil {
		t.Fatalf("graph-aux.dat should exist before drop: %v", err)
	}

	requireNoError(t, s.DropVectorIndex("aux"))

	// aux is gone from the map and its graph file is deleted; the default index's
	// file and records are untouched.
	if _, err := os.Stat(filepath.Join(segDir, "graph-aux.dat")); !os.IsNotExist(err) {
		t.Fatalf("graph-aux.dat must be deleted, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(segDir, "graph-default.dat")); err != nil {
		t.Fatalf("graph-default.dat must survive a sibling drop: %v", err)
	}
	names := s.ListVectorIndexes()
	if len(names) != 1 || names[0].Name != "default" {
		t.Fatalf("after drop, only default should remain, got %+v", names)
	}
	if _, err := s.Search("aux", randVec(), 5, nil); err == nil {
		t.Fatal("Search on the dropped index must error")
	}

	// The surviving default index still returns correct top-k.
	q := randVec()
	got, err := s.Search("default", q, 10, nil)
	requireNoError(t, err)
	want := bruteForceKNN(Cosine, q, vecs, 10)
	if r := recallAtK(got, want); r < 0.8 {
		t.Fatalf("default index recall after sibling drop = %.2f, want >= 0.8", r)
	}

	// Records intact: a doc is still Gettable.
	if _, _, found, _ := s.Get("d-0"); !found {
		t.Fatal("records must survive DropVectorIndex")
	}
}

func TestStore_DropVectorIndex_DefaultRefusedUnknownNoop(t *testing.T) {
	s := openTestStore(t, Cosine)
	if err := s.DropVectorIndex("default"); err == nil {
		t.Fatal("dropping the default index must be refused")
	}
	requireNoError(t, s.DropVectorIndex("never-existed")) // no-op
}
```

Run: `go test ./core/vectorstore/ -run 'TestStore_DropVectorIndex_LeavesOthersIntact|TestStore_DropVectorIndex_DefaultRefusedUnknownNoop' -count=1`
**Expect: FAIL** — `DropVectorIndex` / `dropGraphFilesLocked` undefined.

### Minimal impl — `/workspace/haystack/core/vectorstore/store.go`

```go
// DropVectorIndex removes a named index: its in-memory vindex, its graph-<name>.dat
// file in every sealed segment dir, and its manifest entries. Records, payload, and
// all OTHER indexes are untouched (architecture §4.7). It takes buildMu+s.mu so the
// removal never races a buildAndPublish writing the graphs map (buildAndPublish
// re-checks the index still exists after taking s.mu — gotcha 5). In-flight builds
// for this index harmlessly no-op on that re-check. The reserved "default" index
// cannot be dropped; an unknown name is a no-op.
func (s *Store) DropVectorIndex(name string) error {
	if name == defaultIndexName {
		return fmt.Errorf("vectorstore: cannot drop the reserved %q index", name)
	}
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.indexes[name]; !ok {
		return nil // unknown → no-op (idempotent)
	}
	delete(s.indexes, name)
	if err := s.dropGraphFilesLocked(name); err != nil {
		return err
	}
	return s.writeManifestLocked()
}

// dropGraphFilesLocked removes graph-<name>.dat from every sealed segment dir. A
// missing file is fine (the (index,seg) was still pending). Caller holds s.mu (and
// buildMu, so no builder is mid-install for this index).
func (s *Store) dropGraphFilesLocked(name string) error {
	for _, sid := range s.sealedID {
		p := segFilePath(filepath.Join(s.dir, segDirName(sid, 0)), graphFileName(name))
		if err := fsRemove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
```

Confirm `buildAndPublish` (Task 4 note) re-checks: after `s.mu.Lock()`, `vx, ok := s.indexes[name]; if !ok { return }` before `vx.graphs[id] = bi`.

Run: the two tests → **PASS.** Full gates (race-critical: the drop-vs-build window).

Commit: `feat(vectorstore): DropVectorIndex — deletes only graph-<name>.dat, others intact (§4.7)`

---

## Task 7 — Manifest v4: per-index configs + per-`(index,segment)` state

Persist the N index configs (`Indexes []indexConfigEntry`) and per-`(index,segment)` build state (`IndexSegs []indexSegEntry`). Drop `segmentEntry.State` (it becomes the records record). Bump `manifestVersionByte` 3→4 (hard cut). `writeManifestLocked` emits the per-index block from `s.indexes`.

### Failing test — append to `/workspace/haystack/core/vectorstore/manifest_test.go`

```go
func TestManifest_V4_PerIndexRoundTrip(t *testing.T) {
	m := &manifest{
		Version: 9,
		Head:    headSegID,
		Metric:  Cosine,
		Segments: []segmentEntry{
			{SegID: 1, Gen: 0, VecCount: 50, TombCount: 2},
			{SegID: 2, Gen: 0, VecCount: 30, TombCount: 0},
		},
		Indexes: []indexConfigEntry{
			{Name: "default", Type: "hnsw", Metric: Cosine, M: 16, EfConstruction: 200, EfSearch: 64},
			{Name: "euclid", Type: "hnsw", Metric: Euclidean, M: 8, EfConstruction: 80, EfSearch: 32},
		},
		IndexSegs: []indexSegEntry{
			{Index: "default", SegID: 1, Gen: 0, State: segIndexed},
			{Index: "default", SegID: 2, Gen: 0, State: segIndexed},
			{Index: "euclid", SegID: 1, Gen: 0, State: segIndexed},
			{Index: "euclid", SegID: 2, Gen: 0, State: segPending},
		},
	}
	b := serializeManifest(m)
	got, err := parseManifest(b)
	requireNoError(t, err)
	if got.Version != 9 || got.Metric != Cosine || len(got.Segments) != 2 {
		t.Fatalf("records block round-trip broken: %+v", got)
	}
	if len(got.Indexes) != 2 || got.Indexes[1].Name != "euclid" || got.Indexes[1].Metric != Euclidean || got.Indexes[1].M != 8 {
		t.Fatalf("index config block round-trip broken: %+v", got.Indexes)
	}
	if len(got.IndexSegs) != 4 || got.IndexSegs[3].Index != "euclid" || got.IndexSegs[3].State != segPending {
		t.Fatalf("index-seg state block round-trip broken: %+v", got.IndexSegs)
	}
	// fmtver byte is bumped to 4.
	if b[4] != 4 {
		t.Fatalf("manifest format version byte = %d, want 4", b[4])
	}
}

func TestManifest_V4_RejectsV3Byte(t *testing.T) {
	m := &manifest{Version: 1, Head: headSegID, Metric: Cosine}
	b := serializeManifest(m)
	b[4] = 3 // forge a v3 byte
	// CRC will mismatch first (b[4] is covered), but either way parse must reject.
	if _, err := parseManifest(b); err == nil {
		t.Fatal("a non-v4 format byte must be rejected (hard cut)")
	}
}
```

Run: `go test ./core/vectorstore/ -run 'TestManifest_V4_PerIndexRoundTrip|TestManifest_V4_RejectsV3Byte' -count=1`
**Expect: FAIL** — `indexConfigEntry`, `indexSegEntry`, `manifest.Indexes`, `manifest.IndexSegs` undefined; `segmentEntry.State` may still be referenced.

### Minimal impl — `/workspace/haystack/core/vectorstore/manifest.go`

1. Drop `State` from `segmentEntry`:
```go
type segmentEntry struct {
	SegID     segID
	Gen       uint32
	VecCount  uint64
	TombCount uint64
}
```
2. Add the two new entry types + manifest fields:
```go
// indexConfigEntry persists one named index's config (architecture §4.8 "索引配置
// name → VectorIndexConfig"). Bytes: nameLen(2) name | type(1, 0=hnsw) | metric(1)
// | M(4) | EfConstruction(4) | EfSearch(4).
type indexConfigEntry struct {
	Name           string
	Type           string
	Metric         Metric
	M, EfConstruction, EfSearch int
}

// indexSegEntry persists one (index, segment) build state (§4.8 "index-段:
// (indexName,segId)→{gen,状态}"). Bytes: nameLen(2) name | segId(8) | gen(4) |
// state(1).
type indexSegEntry struct {
	Index string
	SegID segID
	Gen   uint32
	State segState
}
```
Add to `manifest`:
```go
	Indexes   []indexConfigEntry
	IndexSegs []indexSegEntry
```
3. `manifestVersionByte` → `4`. Update the doc comment.
4. `serializeManifest`: after the segments block (now without the per-seg state byte), append the two new blocks before the CRC:
```go
	// segments: drop the per-seg state byte (state is now per-(index,seg)).
	body = appendU32(body, uint32(len(m.Segments)))
	for _, e := range m.Segments {
		body = appendU64(body, uint64(e.SegID))
		body = appendU32(body, e.Gen)
		body = appendU64(body, e.VecCount)
		body = appendU64(body, e.TombCount)
	}
	// index configs (v4).
	body = appendU32(body, uint32(len(m.Indexes)))
	for _, ic := range m.Indexes {
		body = appendU16(body, uint16(len(ic.Name)))
		body = append(body, ic.Name...)
		body = append(body, indexTypeByte(ic.Type))
		body = append(body, byte(ic.Metric))
		body = appendU32(body, uint32(ic.M))
		body = appendU32(body, uint32(ic.EfConstruction))
		body = appendU32(body, uint32(ic.EfSearch))
	}
	// per-(index,segment) states (v4).
	body = appendU32(body, uint32(len(m.IndexSegs)))
	for _, is := range m.IndexSegs {
		body = appendU16(body, uint16(len(is.Index)))
		body = append(body, is.Index...)
		body = appendU64(body, uint64(is.SegID))
		body = appendU32(body, is.Gen)
		body = append(body, byte(is.State))
	}
```
with helpers (place in `manifest.go`):
```go
func indexTypeByte(t string) byte {
	if t == "hnsw" {
		return 0
	}
	return 0 // v1: only hnsw; future types add codes here
}

func indexTypeFromByte(b byte) string { return "hnsw" }
```
5. `parseManifest`: bump the version-byte check to `4`; after parsing the segments block (no state byte), parse the two new blocks. Add bounds checks mirroring the attr-decl parsing (each `if off+N > len(b)-4 { return ... truncated }`). For `Segments`, drop the `e.State = segState(b[off]); off++` line. Then:
```go
	// index configs (v4)
	if off+4 > len(b)-4 { return nil, fmt.Errorf("manifest: truncated index count") }
	nIdx := int(binary.LittleEndian.Uint32(b[off:])); off += 4
	m.Indexes = make([]indexConfigEntry, 0, nIdx)
	for i := 0; i < nIdx; i++ {
		if off+2 > len(b)-4 { return nil, fmt.Errorf("manifest: truncated index name len") }
		nl := int(binary.LittleEndian.Uint16(b[off:])); off += 2
		if off+nl+10 > len(b)-4 { return nil, fmt.Errorf("manifest: truncated index config") }
		name := string(b[off : off+nl]); off += nl
		typ := indexTypeFromByte(b[off]); off++
		met := Metric(b[off]); off++
		mM := int(binary.LittleEndian.Uint32(b[off:])); off += 4
		efC := int(binary.LittleEndian.Uint32(b[off:])); off += 4
		efS := int(binary.LittleEndian.Uint32(b[off:])); off += 4
		m.Indexes = append(m.Indexes, indexConfigEntry{Name: name, Type: typ, Metric: met, M: mM, EfConstruction: efC, EfSearch: efS})
	}
	// per-(index,segment) states (v4)
	if off+4 > len(b)-4 { return nil, fmt.Errorf("manifest: truncated index-seg count") }
	nIS := int(binary.LittleEndian.Uint32(b[off:])); off += 4
	m.IndexSegs = make([]indexSegEntry, 0, nIS)
	for i := 0; i < nIS; i++ {
		if off+2 > len(b)-4 { return nil, fmt.Errorf("manifest: truncated index-seg name len") }
		nl := int(binary.LittleEndian.Uint16(b[off:])); off += 2
		if off+nl+13 > len(b)-4 { return nil, fmt.Errorf("manifest: truncated index-seg entry") }
		idx := string(b[off : off+nl]); off += nl
		sid := segID(binary.LittleEndian.Uint64(b[off:])); off += 8
		gen := binary.LittleEndian.Uint32(b[off:]); off += 4
		st := segState(b[off]); off++
		m.IndexSegs = append(m.IndexSegs, indexSegEntry{Index: idx, SegID: sid, Gen: gen, State: st})
	}
```
6. **`writeManifestLocked`** (`store.go`): drop the per-seg `State` computation; emit the index blocks:
```go
	for i, ss := range s.sealed {
		m.Segments = append(m.Segments, segmentEntry{
			SegID: s.sealedID[i], Gen: 0,
			VecCount: uint64(ss.count()), TombCount: uint64(ss.tombCount()),
		})
	}
	// Index configs + per-(index,segment) states (sorted by name for determinism).
	inames := make([]string, 0, len(s.indexes))
	for n := range s.indexes {
		inames = append(inames, n)
	}
	sort.Strings(inames)
	for _, n := range inames {
		vx := s.indexes[n]
		m.Indexes = append(m.Indexes, indexConfigEntry{
			Name: n, Type: "hnsw", Metric: vx.metric,
			M: vx.cfg.M, EfConstruction: vx.cfg.EfConstruction, EfSearch: vx.cfg.EfSearch,
		})
		for _, sid := range s.sealedID {
			st := segPending
			if vx.graphs[sid] != nil {
				st = segIndexed
			}
			m.IndexSegs = append(m.IndexSegs, indexSegEntry{Index: n, SegID: sid, Gen: 0, State: st})
		}
	}
```
7. **Fix `manifest_test.go`** existing v3 round-trip tests: any literal `segmentEntry{… State: segIndexed}` drops `State`; any `b[4] != 3` assertion → `4`. (Mechanical.)

Run: the two new tests → **PASS.** Full gates (whole package — recovery tests still pass because Task 8 wires recover to the new blocks; **if recover reads `e.State` it will not compile** — so Task 7 and Task 8 must land together OR Task 7 temporarily keeps recover compiling by treating any segment as pending then resuming. To keep Task 7 green standalone: in `recover()` replace the `if e.State == segIndexed { … }` block with "leave pending; the resume loop rebuilds" — i.e. delete the indexed-reopen, always resume-build. This is correct (just rebuilds graphs on open) and keeps Task 7 green; Task 8 restores the reopen-from-disk optimization keyed by `IndexSegs`.)

Commit: `feat(vectorstore): manifest v4 — per-index configs + per-(index,segment) state (§4.8)`

---

## Task 8 — Seal/recover/build over N indexes; recover reopens indexed graphs per index

Generalize the three spawn/load sites to loop over `s.indexes`: `sealLocked` builds N graphs per new segment; `recover()` loads each index's config from the manifest, reopens `indexed` graphs from `IndexSegs`, and resumes every `pending` `(index,seg)` build. Red-proofed by a crash-recovery test: a store with two indexes, one segment indexed and one pending in a sibling index, recovers both correctly.

### Failing test — append to `/workspace/haystack/core/vectorstore/vindex_test.go`

```go
func TestStore_Recover_RestoresAllIndexesAndResumesPending(t *testing.T) {
	dir := t.TempDir()
	kvs := newTestKV(t)
	rng := rand.New(rand.NewSource(61))
	dim := 16
	vecs := make(map[int64][]float32)
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}

	{
		s, err := Open(Options{Dir: dir, KV: kvs, Metric: Cosine})
		requireNoError(t, err)
		put := func(id string, v []float32) {
			requireNoError(t, s.Put(id, v, nil))
			vecs[s.idToDoc[id]] = append([]float32(nil), v...)
		}
		for i := 0; i < 60; i++ {
			put("a-"+itoa(i), randVec())
		}
		requireNoError(t, s.Seal()) // seg 1 (default builds N=1 graph)
		requireNoError(t, s.CreateVectorIndex("aux", VectorIndexConfig{Type: "hnsw", Metric: Cosine, M: 8}))
		requireNoError(t, s.WaitForIndex()) // both indexes built for seg 1
		requireNoError(t, s.Close())
	}

	// Reopen: both indexes' configs + states load from the manifest; indexed graphs
	// reopen from disk, no rebuild needed.
	s2, err := Open(Options{Dir: dir, KV: kvs, Metric: Cosine})
	requireNoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	requireNoError(t, s2.WaitForIndex())

	names := s2.ListVectorIndexes()
	if len(names) != 2 {
		t.Fatalf("recover lost an index, got %+v", names)
	}
	for _, in := range names {
		if in.Indexed != in.Segments || in.Segments != 1 {
			t.Fatalf("index %q not fully indexed after recover: %+v", in.Name, in)
		}
	}

	q := randVec()
	want := bruteForceKNN(Cosine, q, vecs, 10)
	for _, name := range []string{"default", "aux"} {
		got, err := s2.Search(name, q, 10, nil)
		requireNoError(t, err)
		if r := recallAtK(got, want); r < 0.8 {
			t.Fatalf("index %q recall after recover = %.2f, want >= 0.8", name, r)
		}
	}
}
```

Run: `go test ./core/vectorstore/ -run TestStore_Recover_RestoresAllIndexesAndResumesPending -count=1`
**Expect: FAIL** — recover (Task 7 left everything pending/rebuild) loses the `"aux"` index config (it never reconstructs `s.indexes["aux"]`), so `ListVectorIndexes` returns 1.

### Minimal impl — `/workspace/haystack/core/vectorstore/store.go`

**`recover()`** — after loading `m`, reconstruct `s.indexes` from `m.Indexes` (defaulting to a `"default"` index if the manifest predates the index block, which never happens under v4 but keeps the empty-manifest path sane):
```go
	// Reconstruct the named indexes from the manifest. A v4 manifest always carries
	// at least the "default" index; synthesize it if somehow absent.
	if len(m.Indexes) == 0 {
		s.indexes = map[string]*vindex{
			defaultIndexName: {cfg: graphConfig{}.withDefaults(), metric: s.metric, graphs: make(map[segID]*builtIndex)},
		}
	} else {
		s.indexes = make(map[string]*vindex, len(m.Indexes))
		for _, ic := range m.Indexes {
			s.indexes[ic.Name] = newVindex(VectorIndexConfig{
				Type: ic.Type, Metric: ic.Metric, M: ic.M, EfConstruction: ic.EfConstruction, EfSearch: ic.EfSearch,
			})
		}
	}
```
Build a quick lookup of indexed `(index,seg)` from `m.IndexSegs`:
```go
	indexed := make(map[[2]interface{}]bool) // not this — use a string key
```
Use a string key instead:
```go
	indexedState := make(map[string]segState, len(m.IndexSegs))
	for _, is := range m.IndexSegs {
		indexedState[is.Index+"\x00"+itoaSeg(int64(is.SegID))] = is.State
	}
```
In the per-segment open loop, **remove** the old single `if e.State == segIndexed { openGraphFile(...) }` block (Task 7 deleted it). After the loop and after `replay()`, replace the single resume loop with the per-index loop:
```go
	for name, vx := range s.indexes {
		for i, sid := range s.sealedID {
			key := name + "\x00" + itoaSeg(int64(sid))
			if indexedState[key] == segIndexed {
				g, gerr := openGraphFile(filepath.Join(s.dir, segDirName(sid, 0)), name, s.sealed[i])
				if gerr == nil {
					vx.graphs[sid] = newBuiltIndex(g, vx.cfg)
					continue
				}
				// fall through to rebuild on a torn/missing graph file
			}
			if vx.graphs[sid] == nil {
				s.buildBeginLocked()
				go s.buildAndPublish(name, sid, filepath.Join(s.dir, segDirName(sid, 0)), s.sealed[i])
			}
		}
	}
```
> Note: iterating `s.indexes` while spawning is safe (we hold `s.mu`; goroutines take `s.mu` later). `buildAndPublish`'s post-`s.mu` re-check `s.indexes[name]` is fine.

**`sealLocked`** — replace the single spawn (`store.go:1112`) with the N-index loop:
```go
	if s.closing {
		return nil
	}
	for name := range s.indexes {
		s.buildBeginLocked()
		go s.buildAndPublish(name, id, segDir, ss)
	}
```

**`buildAndPublish(name, id, segDir, ss)`** — final form (builds under the index's metric via a node store seam; for v1 default/same-metric it is the plain `buildSegmentGraph`; Task 11 introduces the per-metric branch). For now:
```go
func (s *Store) buildAndPublish(name string, id segID, segDir string, ss *sealedSegment) {
	defer s.buildDone()
	s.mu.RLock()
	vx, ok := s.indexes[name]
	s.mu.RUnlock()
	if !ok {
		return // index dropped before this build ran
	}
	gs, err := buildSegmentGraph(segDir, name, ss, vx.cfg)
	if err != nil {
		return
	}
	bi := newBuiltIndex(gs, vx.cfg)
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	vx, ok = s.indexes[name]
	if !ok {
		return // dropped during the off-lock build
	}
	vx.graphs[id] = bi
	_ = s.writeManifestLocked()
	s.maybeMergeLocked()
}
```

Run: the test → **PASS.** Full gates, including `-race` (the recover resume + drop re-check).

Commit: `feat(vectorstore): seal/recover/build over N indexes; recover reopens per-index graphs (§4.7/§4.8)`

---

## Task 9 — Migrate all callers to `Search("default", …)`; remove the shim

Mechanical: replace the 35 `searchDefault(` test call sites (introduced as a rename in Task 3) with `Search("default", `, and delete `searchDefault` from `store.go`.

### Steps

1. `grep -rl 'searchDefault(' *_test.go` → for each file, replace `<recv>.searchDefault(` with `<recv>.Search("default", `.
2. Delete `func (s *Store) searchDefault(...)` from `store.go`.

### Failing-first check

This task has no new test; its "red" is that **after deleting `searchDefault` but before migrating callers, the package fails to compile** (`undefined: searchDefault`). So the ordering within the task is: migrate callers first (package green via `Search`), then delete the shim (still green). To honor the TDD red/green discipline, add one guard test asserting the shim is gone by behavior parity (already covered by Task 3's `TestStore_DefaultIndex_ExistsAndSearchMatchesShim` — **update it** to drop the `searchDefault` comparison and instead assert `Search("default", …)` against `bruteForceKNN`):

Replace, in `vindex_test.go`, the shim comparison block with:
```go
	got, err := s.Search("default", q, 10, nil)
	requireNoError(t, err)
	want := bruteForceKNN(Cosine, q, vecs, 10)
	if r := recallAtK(got, want); r < 0.8 {
		t.Fatalf("default Search recall = %.2f, want >= 0.8", r)
	}
```
(and remove the now-unused `vecs` only if it becomes unused — keep it; it feeds `want`).

Run: `go test ./core/vectorstore/ -count=1` → **PASS** (all 16 test files now call the named `Search`). Then `-race`, `vet`, `build`.

Commit: `refactor(vectorstore): migrate all Search callers to named "default" index; drop shim`

---

## Task 10 — Merge builds N graphs/output; all-indexed-across-N gate (close-during-build SIGSEGV guard)

Generalize merge: an input is mergeable only when indexed in **every** index (gotcha 3 — else the swap `close()`s an mmap a sibling builder is mid-read → SIGSEGV); each output bucket spawns N background builds. Red-proofed: a multi-index store reclaims a deflated segment and both indexes remain correct on the merged output.

### Failing test — append to `/workspace/haystack/core/vectorstore/vindex_test.go`

```go
func TestStore_Merge_BuildsAllIndexGraphsPerOutput(t *testing.T) {
	s := openTestStore(t, Cosine)
	// Shrink the merge policy so a single deflated segment triggers a repack.
	s.mcfg = mergeConfig{MergeFloor: 0.9, Fanout: 99, MaxMergedSize: 1 << 20, TargetSegCount: 99}
	rng := rand.New(rand.NewSource(71))
	dim := 16
	vecs := make(map[int64][]float32)
	put := func(id string, v []float32) {
		requireNoError(t, s.Put(id, v, nil))
		vecs[s.idToDoc[id]] = append([]float32(nil), v...)
	}
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	for i := 0; i < 100; i++ {
		put("m-"+itoa(i), randVec())
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.CreateVectorIndex("aux", VectorIndexConfig{Type: "hnsw", Metric: Cosine, M: 8}))
	requireNoError(t, s.WaitForIndex()) // seg 1 indexed in BOTH default and aux

	// Delete >10% so the segment falls below MergeFloor → delete-driven repack.
	for i := 0; i < 30; i++ {
		requireNoError(t, s.Delete("m-"+itoa(i)))
		delete(vecs, s.idToDoc["m-"+itoa(i)])
	}
	requireNoError(t, s.Compact())
	requireNoError(t, s.WaitForMerge())
	requireNoError(t, s.WaitForIndex()) // both indexes built on the merged output

	// Every index must be fully indexed on the new segment set, and both must return
	// correct top-k over the surviving (non-deleted) docs.
	for _, in := range s.ListVectorIndexes() {
		if in.Indexed != in.Segments {
			t.Fatalf("index %q not fully indexed after merge: %+v", in.Name, in)
		}
	}
	q := randVec()
	want := bruteForceKNN(Cosine, q, vecs, 10)
	for _, name := range []string{"default", "aux"} {
		got, err := s.Search(name, q, 10, nil)
		requireNoError(t, err)
		if r := recallAtK(got, want); r < 0.8 {
			t.Fatalf("index %q recall after merge = %.2f, want >= 0.8", name, r)
		}
		// Deleted docs must not resurface in the merged output.
		for _, h := range got {
			if _, ok := vecs[h.DocID]; !ok {
				t.Fatalf("index %q returned a deleted/merged-away doc %d", name, h.DocID)
			}
		}
	}
}
```

Run: `go test ./core/vectorstore/ -run TestStore_Merge_BuildsAllIndexGraphsPerOutput -count=1`
**Expect: FAIL** — merge only builds the default graph per output (`aux` never gets graphs on the merged segment), so `aux.Indexed != aux.Segments`. (And the gate `s.graphs[id]==nil` only checked default, risking a premature merge of a segment still pending in `aux`.)

### Minimal impl — `/workspace/haystack/core/vectorstore/merge.go`

1. **All-indexed-across-N gate** in `planMergeWithCapLocked` (`merge.go:179`):
```go
		if !s.fullyIndexedLocked(id) {
			return nil, nil // pending in SOME index — defer (avoids close-during-build across all N graphs)
		}
```
Add the helper (in `store.go` or `merge.go`):
```go
// fullyIndexedLocked reports whether segment id has its graph built in EVERY named
// index. A merge may only consume such a segment: the swap close()s its mmap, which
// would unmap memory a still-pending index's background builder is mid-read
// (SIGSEGV, gotcha 3). Caller holds s.mu.
func (s *Store) fullyIndexedLocked(id segID) bool {
	for _, vx := range s.indexes {
		if vx.graphs[id] == nil {
			return false
		}
	}
	return true
}
```
2. **Drop inputs from all indexes** in `mergeAndPublish` step 2b: replace `delete(s.indexes[defaultIndexName].graphs, id)` with:
```go
			for _, vx := range s.indexes {
				delete(vx.graphs, id)
			}
```
3. **N builds per output** in step 2e (`merge.go:350`):
```go
	for i, ss := range outSS {
		for name := range s.indexes {
			s.buildBeginLocked()
			go s.buildAndPublish(name, p.outIDs[i], p.outDirs[i], ss)
		}
	}
```

Run: the test → **PASS.** Full gates incl. `-race` (the merge concurrency tests in the suite must stay green — they exercise the swap window).

Commit: `feat(vectorstore): merge builds N graphs/output, all-indexed-across-N gate (gotcha 3, §4.7)`

---

## Task 11 — `reindexNodeStore`: per-index-metric reconstruct-raw wrapper

A non-primary-metric index builds its graph + computes distances over **raw** vectors reconstructed from the records (which store the *primary* metric's natural form). The wrapper overrides `Metric()` (returns the index metric) and `GetVectorRef()` (returns `indexMetric.prepare(primaryMetric.restore(stored, norm))`), reusing `buildSegmentGraph`/`newBuiltIndex`/the search legs unchanged. This task wires it into `buildSegmentGraph` (a node-store seam) and proves the round-trip in isolation.

### Failing test — create `/workspace/haystack/core/vectorstore/reindex_test.go`

```go
package vectorstore

import (
	"math"
	"testing"
)

func TestReindexNodeStore_ReconstructsRawAndRepreparesUnderIndexMetric(t *testing.T) {
	// Records are stored under the PRIMARY metric (cosine: unit + norm). A reindex
	// store for an index whose metric is Euclidean must hand the graph builder the
	// RAW vector re-prepared under Euclidean (identity), reconstructed from the
	// cosine unit·norm at ~1e-7 (§3.4).
	dir := t.TempDir()
	seg := buildTinySealedForGraphTest(t, dir) // 3 rows; defined in graphfile_test.go

	// Wrap the segment's graph store as a Euclidean reindex over a cosine-stored seg.
	// (The seg here was written under DotProduct in the helper; use DotProduct as the
	// primary and Euclidean as the index metric — both raw-identity, so the
	// reconstruction is exact and the test is deterministic.)
	rs := newReindexNodeStore(seg, DotProduct, Euclidean)

	if rs.Metric() != Euclidean {
		t.Fatalf("reindex Metric() = %v, want Euclidean (the INDEX metric)", rs.Metric())
	}
	// Bind a node for slot 0 (docId 1) and read its vector back through the wrapper.
	requireNoError(t, rs.PutNode(mustNextID(t, rs), 0, nil, 1))
	rs.bindSlot(1, 0)
	got, err := rs.GetVectorRef(0)
	requireNoError(t, err)
	want := []float32{1, 0, 0} // slot 0's raw vector
	if len(got) != 3 {
		t.Fatalf("reconstructed dim = %d, want 3", len(got))
	}
	for i := range want {
		if math.Abs(float64(got[i]-want[i])) > 1e-6 {
			t.Fatalf("reconstruct[%d] = %v, want ~%v (>1e-6 off)", i, got[i], want[i])
		}
	}
}

func mustNextID(t *testing.T, rs *reindexNodeStore) uint64 {
	t.Helper()
	id, err := rs.NextNodeId()
	requireNoError(t, err)
	return id
}
```

> Note: this test reads `GetVectorRef` after `PutNode`+`bindSlot`. To keep the wrapper minimal we make `GetVectorRef` resolve `nodeSlot[id]` like `segGraphStore`; reuse the embedded base store's slot resolution.

Run: `go test ./core/vectorstore/ -run TestReindexNodeStore_ReconstructsRaw -count=1`
**Expect: FAIL** — `newReindexNodeStore`, `reindexNodeStore` undefined.

### Minimal impl — create `/workspace/haystack/core/vectorstore/reindex.go`

```go
package vectorstore

// reindexNodeStore wraps a segGraphStore for an index whose metric DIFFERS from the
// store's primary (records) metric. The records store vectors in the primary
// metric's natural form (cosine: unit + norm); this index needs them in ITS metric's
// form. reindexNodeStore overrides exactly two methods of the embedded base store:
//
//   - Metric() returns the INDEX metric (so the HNSW computes this index's distance).
//   - GetVectorRef(id) reconstructs the RAW vector from the records via the primary
//     metric's restore() (cosine unit·norm → raw, ~1e-7, §3.4), then re-prepares it
//     under the index metric. No vector is re-stored on disk (§3 "向量只存一份").
//
// All other graphNodeStore methods (topology, nodeId↔slot/docId, entry point) are
// promoted from the embedded *segGraphStore unchanged, so buildSegmentGraph,
// newBuiltIndex, search, and searchFiltered all work verbatim against it.
type reindexNodeStore struct {
	*segGraphStore
	primary Metric
	index   Metric
}

// newReindexNodeStore builds a reindex store over seg. primary is the records
// (store) metric; index is this index's metric.
func newReindexNodeStore(seg *sealedSegment, primary, index Metric) *reindexNodeStore {
	return &reindexNodeStore{
		segGraphStore: newSegGraphStore(seg),
		primary:       primary,
		index:         index,
	}
}

// Metric returns the INDEX metric, overriding the embedded store's primary metric.
func (r *reindexNodeStore) Metric() Metric { return r.index }

// GetVectorRef resolves nodeId→slot, reads the stored (primary-form) vector + norm,
// reconstructs the raw vector via the primary metric, and re-prepares it under the
// index metric. The result is the form the index's HNSW distance expects.
func (r *reindexNodeStore) GetVectorRef(id uint64) ([]float32, error) {
	base, err := r.segGraphStore.GetVectorRef(id) // stored (primary) form for the row
	if err != nil {
		return nil, err
	}
	slot := r.segGraphStore.nodeSlot[id]
	raw := r.primary.restore(base, r.segGraphStore.seg.norm(slot)) // unit·norm → raw (~1e-7)
	prepared, _ := r.index.prepare(raw)
	return prepared, nil
}
```

> `segGraphStore.GetVectorRef` returns `seg.getVectorRef(slot)` (stored form). For cosine-primary that is the unit vector; `restore(unit, norm)` rebuilds raw. For raw-primary `restore` is identity. Then `index.prepare` puts it in the index's form. This is the §3.4 reconstruct-raw path with zero new storage.

Wire it into **`buildSegmentGraph`** so a non-primary index uses it. Add a primary-metric param:
```go
func buildSegmentGraph(segDir, name string, seg *sealedSegment, cfg graphConfig) (*segGraphStore, error)
```
stays — but the **per-metric** build needs the index metric. Introduce a sibling builder used only by `buildAndPublish` when `vx.metric != s.metric`:
```go
// buildSegmentGraphReindex builds an index's graph over seg using a metric that
// differs from the records' primary metric, reconstructing raw per node (§3.4). It
// returns a plain *segGraphStore (the reindex wrapper is a build-time concern; the
// persisted graph is topology only, reopened normally — but the SEARCH leg must
// also reconstruct, see Task 12).
func buildSegmentGraphReindex(segDir, name string, seg *sealedSegment, primary, index Metric, cfg graphConfig) (*segGraphStore, error) {
	cfg = cfg.withDefaults()
	rs := newReindexNodeStore(seg, primary, index)
	idx := newHNSWIndex(rs,
		withGraphM(cfg.M),
		withGraphEfConstruction(cfg.EfConstruction),
		withGraphEfSearch(cfg.EfSearch),
		withGraphRand(rand.New(rand.NewSource(cfg.Seed))),
	)
	b := idx.newBatch()
	seg.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		rs.bindSlot(docID, slot)
		// The batch.put vector arg is ignored by the store (GetVectorRef resolves the
		// row); pass the stored form for API symmetry.
		b.put(docID, stored)
	})
	if err := b.commit(); err != nil {
		return nil, err
	}
	if err := writeGraphFile(segDir, name, rs.segGraphStore); err != nil {
		return nil, err
	}
	return openGraphFile(segDir, name, seg)
}
```

> Verify `batch.put`'s vector arg is unused by `segGraphStore` (it is — `PutNode` ignores `vector` and `GetVectorRef` resolves the row). If `newBatch().put` requires the prepared vector for distance during construction, pass `rs.index.prepare(rs.primary.restore(stored, norm))` instead. **Check `graphbatch.go`** during impl and use whichever the batch consumes; the node store's `GetVectorRef` override is the authoritative source either way.

Run: the test → **PASS.** Full gates.

Commit: `feat(vectorstore): reindexNodeStore — per-index-metric reconstruct-raw build (§3.4)`

---

## Task 12 — Per-index metric end-to-end: each index returns the right top-k under ITS metric

Wire `buildAndPublish` to choose `buildSegmentGraphReindex` when `vx.metric != s.metric`, and make the **reopened** graph + **search legs** for a non-primary index reconstruct raw too (the persisted graph is topology only; at open, the per-segment graph must resolve vectors under the index metric). Red-proofed by an oracle: a store with primary=Cosine and a second index metric=Euclidean returns, for each index, exactly the top-k that index's metric ranks.

### Failing test — create `/workspace/haystack/core/vectorstore/vindex_metric_test.go`

```go
package vectorstore

import (
	"math/rand"
	"testing"
)

func TestStore_PerIndexMetric_EachReturnsItsOwnTopK(t *testing.T) {
	// Primary (records) metric = Cosine. A second index uses Euclidean. For the SAME
	// query, the two indexes must return DIFFERENT, each-metric-correct top-k.
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(123))
	dim := 12
	vecs := make(map[int64][]float32)
	put := func(id string, v []float32) {
		requireNoError(t, s.Put(id, v, nil))
		vecs[s.idToDoc[id]] = append([]float32(nil), v...)
	}
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()*4 - 2 // varied magnitudes so cosine != euclidean ordering
		}
		return v
	}
	for i := 0; i < 150; i++ {
		put("v-"+itoa(i), randVec())
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.CreateVectorIndex("euclid", VectorIndexConfig{
		Type: "hnsw", Metric: Euclidean, M: 16, EfConstruction: 200, EfSearch: 64,
	}))
	requireNoError(t, s.WaitForIndex())

	q := randVec()
	wantCos := bruteForceKNN(Cosine, q, vecs, 10)
	wantEuc := bruteForceKNN(Euclidean, q, vecs, 10)

	gotCos, err := s.Search("default", q, 10, nil)
	requireNoError(t, err)
	gotEuc, err := s.Search("euclid", q, 10, nil)
	requireNoError(t, err)

	if r := recallAtK(gotCos, wantCos); r < 0.8 {
		t.Fatalf("default(cosine) recall = %.2f vs cosine oracle, want >= 0.8", r)
	}
	if r := recallAtK(gotEuc, wantEuc); r < 0.8 {
		t.Fatalf("euclid index recall = %.2f vs EUCLIDEAN oracle, want >= 0.8", r)
	}
	// The euclid index must NOT just echo the cosine ranking: its recall against the
	// cosine oracle should be materially lower (the metrics order these vectors
	// differently). This proves the per-index metric actually drove the search.
	if r := recallAtK(gotEuc, wantCos); r >= 0.8 {
		t.Fatalf("euclid result matches the COSINE oracle (%.2f) — per-index metric not applied", r)
	}
}
```

Run: `go test ./core/vectorstore/ -run TestStore_PerIndexMetric_EachReturnsItsOwnTopK -count=1`
**Expect: FAIL** — `buildAndPublish` builds every index with `buildSegmentGraph` (primary metric), and the search leg uses `bi.idx` whose store metric is the primary; the euclid index returns the cosine ranking → it matches `wantCos` and fails the last assertion (and likely misses `wantEuc`).

### Minimal impl

**`buildAndPublish`** (`store.go`) — branch on metric:
```go
	var gs *segGraphStore
	if vx.metric == s.metric {
		gs, err = buildSegmentGraph(segDir, name, ss, vx.cfg)
	} else {
		gs, err = buildSegmentGraphReindex(segDir, name, ss, s.metric, vx.metric, vx.cfg)
	}
	if err != nil {
		return
	}
	bi := newBuiltIndexFor(gs, ss, s.metric, vx.metric, vx.cfg)
```

**Reopened-graph + search must reconstruct too.** The persisted graph stores topology only; on reopen, `newBuiltIndex` wraps the plain `segGraphStore` whose `Metric()` returns the **segment (primary)** metric and whose `GetVectorRef` returns the **stored (primary) form**. For a non-primary index that is wrong for search. Introduce `newBuiltIndexFor` that, when `index != primary`, wraps the reopened store in a `reindexNodeStore` so the search-time `bi.idx` computes the index metric over reconstructed raw:
```go
// newBuiltIndexFor wraps a reopened graph store in a search-ready index under the
// INDEX metric. When index == primary it is exactly newBuiltIndex (the segment's
// own metric). When they differ, the reopened topology store is re-wrapped in a
// reindexNodeStore so GetVectorRef reconstructs raw + re-prepares under the index
// metric at SEARCH time too (symmetric with the build, §3.4).
func newBuiltIndexFor(gs *segGraphStore, seg *sealedSegment, primary, index Metric, cfg graphConfig) *builtIndex {
	if index == primary {
		return newBuiltIndex(gs, cfg)
	}
	rs := &reindexNodeStore{segGraphStore: gs, primary: primary, index: index}
	cfg = cfg.withDefaults()
	return &builtIndex{
		store: gs,
		idx: newHNSWIndex(rs,
			withGraphM(cfg.M),
			withGraphEfConstruction(cfg.EfConstruction),
			withGraphEfSearch(cfg.EfSearch),
		),
	}
}
```
> `builtIndex.store` stays the plain `*segGraphStore` (the search leg's `member`/`nodeSlot` access in `searchLocked` uses `bi.store.nodeSlot` — unchanged). Only `bi.idx`'s node store is the reindex wrapper, so distance computations reconstruct. `reindexNodeStore` embeds `*segGraphStore`, so `nodeSlot`/topology promote correctly.

**Use `newBuiltIndexFor` at the two reopen sites** keyed by metric:
- `recover()` indexed-reopen: `vx.graphs[sid] = newBuiltIndexFor(g, s.sealed[i], s.metric, vx.metric, vx.cfg)`.
- `buildAndPublish`: as shown above.

**Search leg for a pending non-primary index** (brute legs in `searchLocked`): the head/pending brute legs already use `vx.metric.distance(stored, pq)` (Task 3) — but `stored` is the **primary** form and `pq` is `vx.metric.prepare(q)`. For a non-primary index these are in different spaces. Fix the brute legs to reconstruct when `vx.metric != s.metric`:
```go
	// In searchLocked, precompute the reconstruction need once:
	reindex := vx.metric != s.metric
	dvec := func(stored []float32, norm float32) []float32 {
		if !reindex {
			return stored
		}
		prepared, _ := vx.metric.prepare(s.metric.restore(stored, norm))
		return prepared
	}
```
Then every brute leg distance becomes `vx.metric.distance(dvec(stored, norm), pq)`. The head leg reads `s.seg.vectors[slot]` (stored primary form) + needs the head norm — head stores stored form; reconstruct via `s.metric.restore(s.seg.vectors[slot], s.seg.norms[slot])` (confirm the head segment exposes the per-slot norm; `segment` has a `norms`/`norm` accessor — **check `segment.go` during impl** and use the live-row norm from `eachLive`, which already yields `norm`). Use the `eachLive` callback's `norm` arg in `headBruteEvalLocked` and the pending-sealed leg (both already receive `norm`), so no new accessor is needed — pass `vx` + `reindex` into `headBruteEvalLocked`.

Run: the test → **PASS.** Then the FULL suite, `-race`, `vet`, `build`. Confirm the default-metric path is unchanged (reindex=false ⇒ `dvec` returns `stored` verbatim, byte-identical to Phase 5).

Commit: `feat(vectorstore): per-index metric end-to-end — build+reopen+brute reconstruct raw (§3.4)`

---

## Task 13 — `RebuildVectorIndex`: drop graphs + respawn builds (param change / repair)

`RebuildVectorIndex(name)` marks every `(index,seg)` pending (clears `vx.graphs`), deletes its graph files, and respawns builds — reusing the recover resume pattern. Useful for a param change or a torn-graph repair. Red-proofed: after a rebuild the index converges and returns correct top-k.

### Failing test — append to `/workspace/haystack/core/vectorstore/vindex_test.go`

```go
func TestStore_RebuildVectorIndex_DropsAndReconverges(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(81))
	dim := 16
	vecs := make(map[int64][]float32)
	put := func(id string, v []float32) {
		requireNoError(t, s.Put(id, v, nil))
		vecs[s.idToDoc[id]] = append([]float32(nil), v...)
	}
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	for i := 0; i < 90; i++ {
		put("r-"+itoa(i), randVec())
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	if l := s.IndexLag("default"); l.PendingSegments != 0 {
		t.Fatalf("precondition: default should be built, got %+v", l)
	}

	requireNoError(t, s.RebuildVectorIndex("default"))
	// Immediately after rebuild kickoff the segment is pending again (brute), and the
	// graph file was deleted then rebuilt.
	requireNoError(t, s.WaitForIndex())
	if l := s.IndexLag("default"); l.PendingSegments != 0 {
		t.Fatalf("default did not reconverge after rebuild, got %+v", l)
	}
	q := randVec()
	got, err := s.Search("default", q, 10, nil)
	requireNoError(t, err)
	want := bruteForceKNN(Cosine, q, vecs, 10)
	if r := recallAtK(got, want); r < 0.8 {
		t.Fatalf("recall after rebuild = %.2f, want >= 0.8", r)
	}

	if err := s.RebuildVectorIndex("ghost"); err == nil {
		t.Fatal("rebuilding an unknown index must error")
	}
}
```

Run: `go test ./core/vectorstore/ -run TestStore_RebuildVectorIndex_DropsAndReconverges -count=1`
**Expect: FAIL** — `RebuildVectorIndex` undefined.

### Minimal impl — `/workspace/haystack/core/vectorstore/store.go`

```go
// RebuildVectorIndex marks a named index pending for every sealed segment (clears
// its built graphs), deletes its graph files, persists the pending state, and
// respawns the per-segment builds (the same machinery CreateVectorIndex and
// recover use). It is the entry point for a param/metric change repair or a torn-
// graph rebuild from records. The index stays queryable throughout via the brute
// fallback. Unknown name → error; the reserved "default" CAN be rebuilt.
func (s *Store) RebuildVectorIndex(name string) error {
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	vx, ok := s.indexes[name]
	if !ok {
		return fmt.Errorf("vectorstore: unknown index %q", name)
	}
	if s.closing {
		return errors.New("vectorstore: store is closing")
	}
	vx.graphs = make(map[segID]*builtIndex) // all (index,seg) → pending
	if err := s.dropGraphFilesLocked(name); err != nil {
		return err
	}
	if err := s.writeManifestLocked(); err != nil {
		return err
	}
	for i, sid := range s.sealedID {
		s.buildBeginLocked()
		go s.buildAndPublish(name, sid, filepath.Join(s.dir, segDirName(sid, 0)), s.sealed[i])
	}
	return nil
}
```

Run: the test → **PASS.** Full gates incl. `-race` (the drop-files-then-respawn under `buildMu`).

Commit: `feat(vectorstore): RebuildVectorIndex — drop graphs + respawn builds (§7)`

---

## Task 14 — Doc + final full-suite sweep

1. Update `/workspace/haystack/core/vectorstore/doc.go` package overview to document Phase 6: the `s.indexes` map, the `"default"` index, per-`(index,segment)` pending|indexed, `graph-<name>.dat` layout, manifest v4, and the reconstruct-raw per-index-metric path. (Prose only; no behavior.)
2. Final gates over the whole package, twice (flake check):
```
go build ./...
go test ./core/vectorstore/ -count=1
go test ./core/vectorstore/ -race -count=1
go test ./core/vectorstore/ -count=1   # second run: deterministic
go vet ./core/vectorstore/
go test ./core/vectorindex/ -count=1   # Phase 6 must not touch vectorindex
```
All **PASS**.

Commit: `docs(vectorstore): Phase 6 multi-index package overview; final sweep`

---

## Correctness invariants red-proofed (the required guarantees)

| Invariant (from the task) | Red-proofed in |
|---|---|
| Each index returns the right top-k under ITS metric/params | Task 4 (params, brute+graph), **Task 12 (metric oracle: euclid ≠ cosine ranking)** |
| A dropped index leaves others intact (records/files/search) | Task 6 (`graph-default.dat` survives, default recall ≥ 0.8, `Get` works, `graph-aux.dat` deleted) |
| A new index converges from brute to graph | Task 4 (pending brute recall ~1.0 → indexed recall ≥ 0.8, `Indexed==Segments`) |
| Per-`(index,segment)` pending\|indexed unifies head+new-index | Tasks 3/4 (brute leg via `vx.graphs[id]==nil`), Task 5 (`IndexLag` counts pending) |
| Manifest persists N configs + per-`(index,seg)` state; recovery resumes all | Task 7 (v4 round-trip), **Task 8 (two indexes recover, indexed reopen + pending resume)** |
| Seal/merge build N graphs/segment; close-during-build guard generalized | Task 8 (seal N-build), **Task 10 (merge N-build + all-indexed-across-N gate, race-clean)** |
| Default path unchanged (no recall regression) | Task 3 (named == shim byte-for-byte), Task 9 (oracle), `dvec` reindex=false is verbatim |
| No Phase 1-5 / `core/vectorindex` regression | Every task's full-package gate + Task 14 `vectorindex` run |

---

## Sequencing notes (dependencies)

- Tasks **1→2→3** are the substrate (types → filename → store map+Search). **3** must land the caller rename (`searchDefault`) so the package stays green; **9** promotes to the public named `Search` and removes the shim.
- **7 (manifest v4)** must land with or before **8 (recover)**; Task 7's standalone-green trick is to make recover always-resume (no `e.State` read), which Task 8 then optimizes with `IndexSegs`.
- **11→12** are the per-index-metric pair; **12** depends on **8** (per-index build/reopen) and **3** (`vx.metric` brute legs).
- **10** depends on **8** (`buildAndPublish(name,…)` + `s.indexes` loop). **13** depends on **6** (`dropGraphFilesLocked`).
- **Every** task ends with the full gate suite green (build, package, `-race`, `vet`); no task leaves the tree red.

---

## Adversarial review — fixes to apply during execution

> Plan = workflow draft; its 5-dimension adversarial review produced 51 findings. The **22 blocker/high** below MUST be applied as you implement the relevant task.

1. **[CRITICAL] architecture-fidelity** — Every task's Gates block, e.g. `go test ./core/vectorstore/ -count=1`
   - issue: All gate commands use the path `./core/vectorstore/`, but the module root is `/workspace/haystack/core` (go.mod = github.com/codetrek/haystack/core) and that is the agent's working directory. I ran `go test ./core/vectorstore/` from there and it FAILS: `stat /workspace/haystack/core/core/vectorstore: directory not found ... FAIL ./core/vectorstore [setup failed]`. The package is at `./vectorstore/` relative to the core module. So literally every gate in the plan (build/test/race/vet, ~14 tasks) is unrunnable as written — the TDD red/green loop never executes.
   - fix: Either run gates from `/workspace/haystack` (the outer module that nests core) using `go test ./core/vectorstore/...`, or from `/workspace/haystack/core` using `go test ./vectorstore/ -count=1` (and `go vet ./vectorstore/`, `go build ./...`). Pick one and make it consistent across all tasks; I verified `cd /workspace/haystack/core && go test ./vectorstore/ -run X` works.
2. **[CRITICAL] architecture-fidelity** — Task 11 buildSegmentGraphReindex — `b.put(docID, stored)`; and hnsw.go insertOneLocked lines 219, 262
   - issue: The per-index-metric build is built on a false assumption. The plan says GetVectorRef is 'the authoritative source' for distances during construction, but it is NOT for the node being inserted. In `insertOneLocked` the NEW node's own vector comes from the passed `vector` arg: line 219 `prepared, _ := h.metric.prepare(vector)` and line 262 `selectNeighborsHeuristic(vector, ...)`. Only the NEIGHBORS come from `GetVectorRef`. `buildSegmentGraphReindex` passes `b.put(docID, stored)` where `stored` is the PRIMARY-metric form (cosine unit), and the reindex `GetVectorRef` returns the INDEX-metric form (reconstructed raw, re-prepared). So during graph construction the inserted node is measured in primary form while its candidate neighbors are measured in index form — two different vector spaces. The graph topology built for the non-primary index is therefore wrong, and Task 12's recall>=0.8-vs-Euclidean-oracle assertion can fail. The plan hand-waves this as 'check graphbatch.go during impl and use whichever the batch consumes' — but it is load-bearing and has one correct answer.
   - fix: In buildSegmentGraphReindex, pass the INDEX-form vector to put: `b.put(docID, rs.index.prepare(rs.primary.restore(stored, norm))[...])` (or have eachLive hand the reconstructed raw and prepare under index metric), so the new node and its GetVectorRef'd neighbors are in the SAME space. Add a dedicated red test that asserts the BUILT graph (not just brute) returns the correct Euclidean top-k, since Task 12's WaitForIndex path exercises exactly this.
3. **[CRITICAL] scope-completeness** — Task 11 (buildSegmentGraphReindex / reindexNodeStore) vs hnsw.go:158-300 insertOneLocked
   - issue: Per-index-metric BUILD is incorrect as written (Task 11). The plan's buildSegmentGraphReindex passes b.put(docID, stored) with the PRIMARY stored form while overriding GetVectorRef to return index-prepared reconstructed-raw. But hnsw insertOneLocked (hnsw.go:219,262) consumes that put vector directly: it does prepared,_ := h.metric.prepare(vector) and selectNeighborsHeuristic(query=vector,...). With h.metric now the INDEX metric, the new node's vector is in primary-stored space (e.g. cosine unit vector) while its neighbors come from GetVectorRef in index-prepared space (e.g. euclidean raw). The two are compared in DIFFERENT spaces during the SAME insert -> a wrong graph, not just a wrong search. The plan's own Task-11 note hedges ('If newBatch().put requires the prepared vector... pass rs.index.prepare(rs.primary.restore(stored,norm)) instead') but presents the WRONG variant as the primary impl and defers the decision to 'check during impl'. This is the load-bearing correctness step of the entire per-index-metric feature and it is specified wrong.
   - fix: Make buildSegmentGraphReindex pass the SAME reconstructed-and-index-prepared vector the GetVectorRef override returns: in the eachLive callback compute idxVec,_ := index.prepare(primary.restore(stored, norm)) and b.put(docID, idxVec). Verify against hnsw.go:219/262 that build-time query vector and GetVectorRef neighbor vectors are then in one space. Add the oracle assertion at BUILD granularity (not just end-to-end Search) so a space mismatch fails loudly.
4. **[CRITICAL] feasibility-vs-code** — 
   - issue: Task 11/12 reconstruct-raw is threaded into GetVectorRef ONLY, but the HNSW BUILD path uses the inserted node's OWN vector from the b.put(...) arg, not GetVectorRef. In hnsw.go insertOneLocked: line 219 `prepared,_ := h.metric.prepare(vector)`, line 262 `selectNeighborsHeuristic(vector,...)`, and validateVector all use `vector` (the b.put arg); segGraphStore.PutNode IGNORES the vector arg. Only NEIGHBOR vectors come from GetVectorRef. Task 11's concrete buildSegmentGraphReindex passes `stored` (the PRIMARY-metric form, e.g. cosine UNIT) to b.put. With h.metric = the index metric (Euclidean), prepare(unit) = unit (identity), so the inserted node is placed using its UNIT vector while its neighbors are reconstructed RAW euclidean vectors — mismatched spaces during construction => a corrupt graph. The plan flags this only as a parenthetical hedge ('if newBatch().put requires the prepared vector... pass ... instead') and its authoritative-GetVectorRef claim is FALSE for the self-vector. The reindex search path (searchFiltered: prepare(query) + nodeDist via GetVectorRef) IS correctly covered by the override, so Task 12's recall test could still pass on near-unit cosine vectors and mask the build defect.
   - fix: In buildSegmentGraphReindex, feed b.put the RECONSTRUCTED+RE-PREPARED vector, not `stored`: per live row compute `raw := primary.restore(stored, norm); idxForm,_ := index.prepare(raw)` and call `b.put(docID, idxForm)`. Then GetVectorRef (neighbors) and the b.put arg (self) are in the SAME index-metric space. Make the Task-11 test assert against a DISTINCT-ordering oracle at build time (not just search), e.g. build with cosine-stored vectors of varied magnitude and assert the euclid graph's neighbor distances differ from the cosine ones, so the build-space mismatch cannot pass silently.
5. **[CRITICAL] feasibility-vs-code** — 
   - issue: Task 2 (per-index graph filename) and Task 3 (remove Store.graphs) under-count the test migration surface; multiple existing tests will FAIL TO COMPILE or break, violating the 'gates green after every task' bar. (a) Tests directly access the field s.graphs that Task 3 deletes: coverage_phase4_test.go:119 `s.graphs[s.sealedID[0]]` and merge_crash_test.go:147 `s.graphs[outID]`. The plan's File Structure only lists 'migrate 35 Search callers' and never migrates these field accesses → Task 3 gate is RED. (b) Tests hardcode the literal filename "graph.dat": builder_test.go:43-44 (asserts graph.dat written), graphfile_test.go:115-119 (truncation test reads/writes graph.dat), recovery_branches_test.go:95,121-122 (os.Remove/os.Stat graph.dat in the crash-mid-build resume test). Task 2 changes the default file to graph-default.dat but its 'mechanical grep of writeGraphFile(/openGraphFile(' does NOT touch these string literals → those tests break at Task 2.
   - fix: Add to Task 2: update the hardcoded "graph.dat" literals in builder_test.go (43), graphfile_test.go (115,119), recovery_branches_test.go (95,121) to "graph-default.dat". Add to Task 3: migrate the direct s.graphs field reads in coverage_phase4_test.go:119 and merge_crash_test.go:147 to s.indexes[defaultIndexName].graphs (and isIndexedForTest already abstracts the others). Re-audit with `grep -rn 's\.graphs\|graph\.dat\|s\.gcfg' *_test.go` before declaring the task green.
6. **[CRITICAL] correctness-tdd** — reindex.go (Task 11 buildSegmentGraphReindex `b.put(docID, stored)`); hnsw.go:219,262,282,474,505,841
   - issue: Task 11/12 reconstruct-raw build is WRONG: the HNSW build computes the new node's distances from the vector PASSED to graphBatch.put (insertOneLocked line 219: `prepared, _ := h.metric.prepare(vector)` and selectNeighborsHeuristic line 262 uses `vector`), NOT from GetVectorRef. Task 11's buildSegmentGraphReindex calls `b.put(docID, stored)` with the PRIMARY-form (cosine UNIT) vector, while the reindexNodeStore.GetVectorRef override returns the index-prepared RAW vector. So during construction the inserted node's query vector is in primary/unit space and its neighbors (fetched via GetVectorRef at hnsw.go:282/291/474/505/782/841) are in index/raw space — distances are computed across mismatched magnitude spaces and the graph is garbage for any cosine-primary + Euclidean-index combo. The plan itself hedges ('If newBatch().put requires the prepared vector ... pass rs.index.prepare(rs.primary.restore(stored,norm)) instead ... Check graphbatch.go during impl') — i.e. it is UNRESOLVED, yet the design is presented as bounded/low-risk.
   - fix: In buildSegmentGraphReindex, pass the SAME reconstructed-raw form to b.put that GetVectorRef returns: `raw := primary.restore(stored, norm); b.put(docID, index.prepare(raw))`. Add a Task-11 failing test with Cosine PRIMARY + Euclidean INDEX (not DotProduct primary, which is restore-identity and hides the bug) asserting the BUILT graph's nearest neighbor equals the Euclidean brute oracle — the current Task-11 test uses DotProduct primary and cannot catch this. Then Task 12's end-to-end oracle is the second line of defense, not the first.
7. **[CRITICAL] refactor-safety** — reindex.go buildSegmentGraphReindex (Task 11); hnsw.go:219,262 insertOneLocked; reindex_test.go (Task 11 test never builds a graph)
   - issue: Task 11/12 per-index-metric BUILD is broken and not red-proofed. The HNSW insert path does NOT take the new node's own vector from the store — insertOneLocked (hnsw.go:219 `prepared,_ := h.metric.prepare(vector)` and hnsw.go:262 `selectNeighborsHeuristic(vector,...)`) uses the per-node `op.vector` passed through the batch, while every NEIGHBOR vector comes from store.GetVectorRef. The plan's buildSegmentGraphReindex calls `b.put(docID, stored)` with the PRIMARY-metric stored form (cosine unit vector). With h.metric=Euclidean, prepare(unitVector) is identity, so the new node is placed using its unit vector while neighbors are reconstructed-raw — two different vector spaces → a corrupt graph. The plan only hand-waves this ('If newBatch().put requires the prepared vector ... pass rs.index.prepare(rs.primary.restore(stored,norm))') and never writes it. Worse, the Task 11 test ONLY exercises GetVectorRef; it never builds a graph, so the build defect passes green. Task 12's recall>=0.8 oracle is the only thing that would catch it, and that test depends on the unwritten fix — so as written, Task 12 fails or silently under-recalls.
   - fix: In buildSegmentGraphReindex, feed the batch the INDEX-metric form of each node's own vector: `raw := primary.restore(stored, norm); b.put(docID, index.prepare(raw))` (norm is available from eachLive). The reindexNodeStore.GetVectorRef override handles the neighbor side. Add an explicit Task-11 test that BUILDS a reindex graph and asserts the node's own self-consistency (e.g. nearest-neighbor of an indexed point is itself under the index metric), not just a single GetVectorRef round-trip. Keep Task 12's euclid-vs-cosine oracle as the end-to-end gate.
8. **[CRITICAL] refactor-safety** — store.go:808 (head attr brute-S), store.go:862 (indexed filtered brute-S); plan Task 12 `dvec` helper + 'pass vx into headBruteEvalLocked'
   - issue: Task 12 per-index-metric SEARCH reconstruction misses two of the four distance legs. The search has 4 direct-distance sites: store.go:833 (pending-sealed brute) and :924 (head full-scan brute) both come from eachLive and DO carry `norm`; but store.go:808 (head FILTERED brute-S via s.seg.attr.evalSeg, uses `s.seg.vectors[slot]`) and store.go:862 (indexed FILTERED brute-S, uses `ss.getVectorRef(slot)`) have NO norm in scope. The plan's `dvec(stored, norm)` helper only covers the eachLive legs and explicitly only rewires headBruteEvalLocked. So filtered search on a non-primary-metric index over the head attr-index path or the indexed brute-S path computes distance in the WRONG space (unit-form vs reconstructed-raw). Task 12's test uses filter=nil, so it never exercises 808/862 — the gap ships green.
   - fix: Thread reconstruction into all four sites: at :808 use `s.seg.norms[slot]`; at :862 use `ss.norm(slot)` (sealed.go:140 exposes it). Define one `dvec(stored, norm)` and apply at 808/833/862/924. Add a Task-12 test variant WITH a filter (Eq/Range) on a non-primary-metric index hitting both the head brute-S leg (declared attr) and the indexed brute-S leg (card<=attrSearchT), oracle'd against bruteForceKNN under the index metric over the matching subset.
9. **[HIGH] architecture-fidelity** — Task 12 'Search leg for a pending non-primary index' — the `dvec` helper
   - issue: Task 12 patches only the UNFILTERED brute legs (head + pending sealed) to reconstruct raw via `dvec`. But searchLocked has a THIRD brute leg the plan never touches: the FILTERED brute-S leg at store.go:862, `tk.offer(... Distance: s.metric.distance(ss.getVectorRef(slot), pq))`. For a non-primary-metric index with a filter and a small |S_seg| (card <= attrSearchT), this computes `s.metric` (primary) distance between a primary-form stored vector and `pq` prepared under `vx.metric` — wrong metric AND mismatched spaces. The plan claims per-index metric works 'end-to-end' but a filtered search on a non-primary index returns wrong distances. The head brute-S leg (store.go:808, `s.metric.distance(s.seg.vectors[slot], pq)`) has the same defect and is also unpatched.
   - fix: Route ALL brute distance call sites in searchLocked through the reconstruction helper when `vx.metric != s.metric`: the unfiltered head/pending legs, the filtered head brute-S leg (line 808), and the filtered sealed brute-S leg (line 862). The head leg needs the per-slot norm — segment exposes `s.seg.norms[slot]` (a field, no accessor; the plan's 'check segment.go for a norm accessor' is wrong — there is none, use the field or the `eachLive` norm arg).
10. **[HIGH] scope-completeness** — Task 3 and Task 9 (35-call-site claim) vs actual 47 in *_test.go
   - issue: Search call-site migration count is wrong and incomplete (Tasks 3 & 9). The plan repeatedly states '35 existing call sites across 16 test files' and that Tasks 3/9 sed-replace exactly those. Actual count is 47 .Search( call sites across 16 files. Since Task 3 changes Search's arity (3-arg -> 4-arg) and the whole-package gate must stay green, a miscount means missed call sites that fail to compile. The plan's mechanical-sed instruction is keyed to a number that does not match the tree.
   - fix: Recount before planning: grep -c on the real tree (47, 16 files). Drive the migration off `grep -rln '\.Search('` rather than a hardcoded count, and gate on `go build` for the test package, not on having touched N sites.
11. **[HIGH] scope-completeness** — Task 2 vs builder_test.go:43, recovery_branches_test.go:95/121, graphfile_test.go:115
   - issue: Task 2 (per-index graph filename) does not enumerate the test files that reference the literal graph.dat by NAME for direct fs ops, so they break when the default file becomes graph-default.dat. builder_test.go:43 os.Stat(...,'graph.dat'); recovery_branches_test.go:95 os.Remove(...,'graph.dat') and :121 os.Stat(...,'graph.dat'); graphfile_test.go:115 path:=.../'graph.dat'. The plan's Task-2 migration list only mentions writeGraphFile/openGraphFile/buildSegmentGraph CALLERS, not these string-literal filesystem assertions, so the package will fail (stat/remove the now-renamed file) after Task 2.
   - fix: Add these files explicitly to Task 2's migration: builder_test.go and recovery_branches_test.go and graphfile_test.go must change the 'graph.dat' literals to graphFileName("default")=='graph-default.dat' (or call the helper). graphfile_test.go's truncation test that opens graph.dat directly must write/truncate graph-default.dat.
12. **[HIGH] scope-completeness** — Task 7 (drop segmentEntry.State) vs recovery_branches_test.go:92-102,159-166
   - issue: Dropping segmentEntry.State (Task 7) breaks recovery_branches_test.go, which the plan never enumerates. That test (lines 96-102) does m,_:=readManifest; m.Segments[i].State=segPending; writeManifest(m) to simulate a crash-mid-build and assert recover() resumes the default index's build; and lines 159-166 construct &manifest{Segments:[]segmentEntry{{...State:segPending}}}. The plan's Task 7 only says it will 'mechanically' fix manifest_test.go State literals. recovery_branches_test.go is a second consumer of the removed field with real recovery semantics (drop graph.dat -> mark pending -> reopen -> resume). It must be rewritten to drive IndexSegs[default] state, which the plan does not call out. Compounding this: the plan's Task-7 'always-resume (ignore State)' standalone-green trick would temporarily PASS this test for the wrong reason, then Task 8 re-introduces State-keyed reopen and re-exposes it.
   - fix: Add recovery_branches_test.go to Task 7's migration with a concrete rewrite: replace m.Segments[i].State manipulation with setting/clearing the m.IndexSegs entry for (index="default", segId) to segPending, and the literal-manifest construction to include an IndexSegs entry. Re-verify the crash-mid-build resume assertion (isIndexedForTest(1) after WaitForIndex) under the new schema.
13. **[HIGH] scope-completeness** — Task 12 vs store.go:799-810 (inline filtered head leg)
   - issue: Per-index-metric SEARCH brute legs miss the inline filtered-head path (Task 12). The plan says to thread vx+reindex+dvec into headBruteEvalLocked and the brute legs. But the filtered head path is NOT in headBruteEvalLocked: it is inline in Search at store.go:799-810 (s.seg.attr.evalSeg -> iterate -> tk.offer(... s.metric.distance(s.seg.vectors[slot], pq))). For a non-primary-metric index this path computes distance in primary space with an index-prepared pq -> wrong results whenever a filter is present and the head has an attr index. The plan only patches headBruteEvalLocked and the sealed pending/brute-S legs, leaving this fourth leg under the primary metric.
   - fix: Enumerate ALL distance sites that must reconstruct for a non-primary index: (1) inline filtered head leg store.go:808, (2) headBruteEvalLocked store.go:924, (3) sealed pending brute store.go:833, (4) sealed brute-S store.go:862. Apply dvec(stored,norm) (= index.prepare(primary.restore(stored,norm))) to each. For the head, norm is available as s.seg.norms[slot]; for sealed legs norm comes from eachLive / ss.norm(slot). Add a filtered + per-index-metric oracle test (current Task 12 test passes nil filter only).
14. **[HIGH] feasibility-vs-code** — 
   - issue: The 'migrate 35 Search call sites across 16 files' count is wrong: the actual count of Store.Search(q,k,filter) 3-arg call sites is 47 (grep -rE '(s|s2|store|c)\.Search\(' *_test.go | wc -l = 47), across the 16 files listed. The plan's Task 3 sed-rename and Task 9 promotion both key off the '35' figure. An undercount risks leaving ~12 sites un-migrated, which (since the 3-arg Search no longer exists after Task 3) is a hard compile failure, not a silent miss.
   - fix: Replace '35' with the verified 47 throughout Tasks 3/9 and the File Structure, and gate the migration on a zero-residual grep: after the sed pass, `grep -rnE '\.Search\([^"]' *_test.go` must return only the new named-index form (bi.idx.search uses a different receiver and is unaffected). Verify count empirically in Task 0 rather than hardcoding it.
15. **[HIGH] feasibility-vs-code** — 
   - issue: Task 12's per-index-metric brute-leg fix is incomplete for the HEAD leg. searchLocked's head path has TWO branches: (1) the inline attr-index brute-S at store.go:804-809 `tk.offer(... s.metric.distance(s.seg.vectors[slot], pq))`, and (2) headBruteEvalLocked at store.go:919-926. The plan only mentions threading vx+reindex into headBruteEvalLocked and the inline `dvec` helper, but NOT the attr-index brute-S branch (line 808), which also computes s.metric.distance(unit-stored, pq=index-prepared) — wrong space for a non-primary index when a filter is present AND the head has a declared attr index. A filtered Search on a euclid index over a cosine-primary store would return wrong head distances.
   - fix: Apply the dvec reconstruction to BOTH head branches. Either route the inline shead.iterate offer through the same dvec(s.seg.vectors[slot], s.seg.norms[slot]) used by headBruteEvalLocked, or (simpler) when reindex==true skip the attr-index brute-S fast path and always use headBruteEvalLocked (it already has the dvec seam). Add a per-index-metric + filter test (euclid index, head with a declared attr) to red-proof it; Task 12's current oracle uses nil filter only.
16. **[HIGH] feasibility-vs-code** — 
   - issue: Task 7's 'standalone-green trick' for manifest v4 is internally inconsistent with what Task 8 needs and risks a non-green intermediate. Task 7 proposes making recover() 'always-resume (no e.State read)' by DELETING the indexed-graph reopen so the package compiles after dropping segmentEntry.State. But recover() at store.go:253-259 currently keys the reopen on `e.State == segIndexed`; the v4 manifest moves state to IndexSegs which Task 7 does not yet wire into recover. Meanwhile existing recovery tests (recovery_branches_test.go:34 'segment becomes indexed → recover reopens graph.dat', merge_crash_test.go crash-mid-build) assert reopen-from-disk vs rebuild timing and graph.dat existence — an 'always rebuild' recover changes observable behavior (rebuild spawns a build goroutine even for already-indexed segments) and may flip those tests. The plan asserts this is 'correct (just rebuilds)' but does not verify against those specific tests.
   - fix: Land manifest v4 (Task 7) and the recover IndexSegs wiring (Task 8) as ONE task, or keep Task 7 truly behavior-neutral by retaining a single-index State view derived from IndexSegs (filter IndexSegs to index=="default") so recover's reopen branch is unchanged. Either way, run recovery_branches_test.go and merge_crash_test.go in Task 7's gate explicitly — they are the load-bearing regression for the 'always rebuild' shortcut.
17. **[HIGH] correctness-tdd** — store.go:799-809 (filtered head-attr leg); Task 12 impl notes
   - issue: Per-index-metric SEARCH only reconstructs in the legs the plan lists (head full-scan + pending-sealed brute + brute-S), but MISSES the filtered-head-attr leg at store.go:799-809 which does `s.metric.distance(s.seg.vectors[slot], pq)` inside `shead.iterate`. That callback does not receive `norm`, and the plan's Task 12 only says 'pass vx+reindex into headBruteEvalLocked' — it never touches the line-808 path. A FILTERED Search on a non-primary index over head docs therefore computes primary-space distance against an index-metric query (wrong results), and NO task tests filtered search on a non-primary-metric index, so it ships silently broken.
   - fix: Make line 808 reconstruct when reindex: `dvec(s.seg.vectors[slot], s.seg.norms[slot])` (norms is accessible as s.seg.norms[slot], a field on segment). Add a Task-12 test variant: per-index metric + non-nil filter, oracle = bruteForceKNN over reconstructed-raw under the index metric on the filter-matching set, covering head + pending + indexed legs.
18. **[HIGH] correctness-tdd** — merge.go planMergeWithCapLocked (gate change) + planReclamationLocked:455; Task 10
   - issue: Task 10's all-indexed-across-N gate (`fullyIndexedLocked`) silently introduces a merge-liveness regression the plan claims does not exist. A freshly CreateVectorIndex'd index is pending for EVERY segment, so until its background builds finish, `fullyIndexedLocked(id)` is false for every segment and planReclamationLocked/planMergeWithCapLocked defer ALL merges (both delete-driven and growth-driven). Under index-creation churn this stalls reclamation. The plan asserts 'no behavior change' and has no test for merge-progress-while-an-index-is-pending.
   - fix: Either (a) document and accept the deferral and add a test that a merge eventually fires AFTER WaitForIndex with two indexes (proving liveness, not deadlock), or (b) scope the gate to 'indexed in every index that already covers this seg' and let a just-created index's build race the merge by having buildAndPublish/mergeAndPublish tolerate a missing input graph for the new index. (a) is simpler and matches §4.7; pick it explicitly rather than leaving the regression unanalyzed.
19. **[HIGH] refactor-safety** — Plan File Structure note + Task 3/9 ('35 ... 16 test files'); actual: 47 sites / 16 files
   - issue: Factual count error undermines the migration tasks' sizing. The plan repeatedly asserts '35 existing call sites across 16 test files' for the Search migration (Tasks 3 and 9). Actual count is 47 `.Search(` call sites across 16 files (file count right, call count off by 34%). Tasks 3 and 9 are pure mechanical sed migrations whose green-ness depends on hitting EVERY site in one commit; an undercount risks leaving stragglers that break the build mid-task (the exact 'half-generalized, tree red' hazard the dimension guards against). Also the production-caller claim is worth stating: there are ZERO non-test Search callers, so no library code migrates — good, but the plan never verifies this.
   - fix: Correct the count to 47. In Tasks 3/9, make the migration grep-driven and assert completeness: after sed, run `grep -rn '\.Search([^"]' *_test.go` and require zero 3-arg matches before compiling. Note explicitly that no production code calls Search (verified) so only *_test.go changes.
20. **[HIGH] refactor-safety** — Task 3 (searchDefault shim rationale) vs Task 3 ('Concretely: sed-replace the ... call sites to searchDefault') and Task 9
   - issue: Task 3 is not actually green-able as a single atomic commit the way written; the plan itself thrashes on this. It first proposes a `searchDefault` shim 'used only by this task's test', then mid-task admits the 3-arg Search no longer exists so ALL existing tests must be sed-migrated to `searchDefault` in the SAME commit, then Task 9 migrates `searchDefault`→`Search("default",`. That is a real, workable sequence, but the plan's own prose contradicts itself ('temporarily retains a 3-arg shim used only by this task's test' vs 'sed-replace the 47 existing call sites to searchDefault'). A reader following the first statement leaves the tree red. The two-hop rename (Search→searchDefault→Search("default")) also churns every test file twice for no behavioral reason.
   - fix: Collapse to ONE migration: in Task 3, change Search's signature to Search(index,q,k,filter) and sed all 47 test sites directly to `Search("default", q, k, filter)` in the same commit (the package stays green because the new signature + migrated callers land together). Delete the searchDefault shim concept entirely; drop Task 9's second rename hop (keep only its oracle-strengthening test edit). This removes a whole churn pass and the self-contradiction.
21. **[HIGH] refactor-safety** — Task 6 dropGraphFilesLocked / Task 13 RebuildVectorIndex; cf. manifest.go writeManifest dir-fsync + store.go sweepOrphansLocked (only sweeps seg dirs)
   - issue: DropVectorIndex (Task 6) and RebuildVectorIndex (Task 13) delete graph files but never fsync the segment directory, breaking the crash-safety model the rest of the store upholds. Every other structural mutation here pairs file ops with fsyncDir + an atomic manifest swap (writeManifest does tmp+fsync+rename+dir-fsync; sealLocked/merge fsync). dropGraphFilesLocked does `fsRemove(p)` per seg dir then writeManifestLocked — the manifest swap is durable, but the unlink of graph-<name>.dat is NOT dir-fsynced, so a crash after the manifest commit (index gone from manifest) but before the unlink hits disk leaves an orphan graph-<name>.dat with no manifest entry. Recovery's sweepOrphansLocked only sweeps whole seg-* dirs not in the manifest, NOT stray files inside a live seg dir — so the orphan graph file persists forever (cosmetic leak, but also: if the index name is later re-Created, a torn/stale graph-<name>.dat could be openGraphFile'd).
   - fix: After removing graph-<name>.dat from each seg dir, fsyncDir(segDir) before the manifest swap (order: delete files → dir-fsync → manifest commit, so the manifest is never ahead of a non-durable unlink). Alternatively/additionally, extend sweepOrphansLocked to remove graph-<name>.dat for any name not in the manifest's index set on recover, and add a test that re-Creates a dropped name and asserts no stale graph is loaded.
22. **[HIGH] refactor-safety** — Task 7 manifest.go:68 cap hint + byte-layout comment; Task 7 TestManifest_V4_RejectsV3Byte
   - issue: Manifest v4 size/scaling note is internally inconsistent and the rejection test is mis-reasoned. (1) The plan keeps `segmentEntry` round-trip but the existing serializeManifest preallocates `len(m.Segments)*29` (manifest.go:68) including the 1-byte state; dropping State makes the entry 28 bytes — harmless over-alloc, but the plan never updates the capacity hint or the byte-layout comment, and the existing manifest_test.go asserts specific encodings that will break. (2) Task 7's TestManifest_V4_RejectsV3Byte forges b[4]=3 then says 'CRC will mismatch first (b[4] is covered), but either way parse must reject' — that test does NOT prove the version-byte gate; it proves the CRC gate. A real v3-vs-v4 incompat test must re-CRC after setting b[4]=3 (recompute crc32 over b[:len-4]) so the version check is the thing that fails, otherwise the assertion is vacuous.
   - fix: Update the cap hint to `len(m.Segments)*28 + len(m.Indexes)*~16 + len(m.IndexSegs)*~15` and the format comment to drop the per-seg state byte and add the two new blocks. Rewrite the reject test to set b[4]=3 AND re-stamp the trailing CRC (crc32 over b[:len-4]) so parseManifest fails specifically on the version byte, not the CRC. Also enumerate which existing manifest_test.go assertions change (the v3 `state(1)` byte in segmentEntry encoding, any `*29` size math).
