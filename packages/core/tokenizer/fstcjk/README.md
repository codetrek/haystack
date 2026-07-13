# fstcjk — FST-backed CJK segmenter (byte-identical to gse v1.0.2)

`fstcjk` segments CJK text **byte-identically to `go-ego/gse` v1.0.2
`Cut(text, true)`**, so token boundaries are unchanged and no reindex is needed.
It ports gse's exact DAG/HMM algorithm but replaces gse's cedar-trie dictionary
with a prebuilt [vellum](https://github.com/blevesearch/vellum) FST that is
embedded into the binary via `//go:embed`. This avoids gse's
parse-and-build-an-in-RAM-trie-at-startup while keeping segmentation faithful.

## What links into the runtime binary

Runtime: `segmenter.go`, `util.go`, `doc.go`, the embedded `dict.fst` +
`dict.totalfreq`, and exactly one third-party runtime dependency,
`github.com/go-ego/gse/hmm` (for out-of-vocabulary segmentation).

NOT runtime (tagged `//go:build tools`): `build.go` (the offline FST builder) and
`gen_test.go` (the generator). Keeping these behind the `tools` tag ensures gse's
dictionary **loader** — and the `cobra`/`pflag` packages pulled in by vellum's CLI
— never link into production.

## The embedded dictionary is a committed, prebuilt artifact

| File | What it is |
|------|------------|
| `dict.fst` | vellum FST mapping each dictionary word (UTF-8 bytes) → freq. ~4.6 MB. |
| `dict.totalfreq` | gse `Dict.TotalFreq()` sidecar (a single float), used for the route-cost normalization. |

Both are generated **offline** from gse's source dictionaries
(`go-ego/gse@v1.0.2/data/dict/zh/s_1.txt` + `t_1.txt`) by `build.go`'s
`BuildFromGse`, which loads gse exactly once via its normal `LoadDict()` path and
dumps the resulting word→freq map (and totalFreq) verbatim. Because the keys,
values, and totalFreq are copied straight out of gse's own in-RAM dictionary, the
FST is the same word→freq map gse would use at runtime — that is what makes
segmentation byte-identical.

Fidelity facts the artifacts must satisfy (asserted by the test suite):

- `totalFreq = 53,226,742` (must equal gse `Dict.TotalFreq()` exactly)
- `keys = 587,207` (must equal gse `Dict.NumTokens()` exactly)
- vellum value = `sum(transition outputs) + final output` — the incremental
  `Accept` walk threading the per-transition output SUM is **mandatory** (reading
  only the FinalOutput gives ~28% fidelity; gse `Find` returns `ok=true, freq=0`
  for prefix-but-not-word nodes like the ASCII bytes `o`/`i`/`h`).

## Regenerating after a gse / dictionary bump (IMPORTANT)

**Fidelity rots silently.** The embedded FST is just bytes: if you bump the gse
version or change its dictionary, nothing fails to compile — the segmenter simply
diverges from the new gse. You MUST regenerate the artifacts and re-run the
fidelity suite on any such change.

Regenerate (writes `dict.fst` + `dict.totalfreq` in place):

```sh
# from core/  (GOWORK=off because this module lives under a go.work workspace)
GOWORK=off go generate ./tokenizer/fstcjk/
```

The `//go:generate` directive (in `doc.go`) runs the tools-tagged generator:

```sh
go test -tags tools -run TestGenerateDictFST -count=1 .
```

Then re-run the fidelity gate (the golden-diff vs a live gse instance):

```sh
GOWORK=off go test ./tokenizer/fstcjk/ -run 'Fidelity|TotalFreqParity' -count=1
```

Regeneration is deterministic: rebuilding from the same gse version produces a
**byte-identical** `dict.fst` (same SHA-256), so a no-op regen leaves the tree
clean. After a genuine gse bump, commit the regenerated artifacts together with
the gse version bump in `go.mod`.

## Platform support — native only (WASM descoped)

This package targets **native builds** and is not buildable for `js/wasm`. The
production load path, `Open()`, embeds `dict.fst` and loads it with
`vellum.Load` over the in-memory bytes (anonymous memory, **no file mmap**),
which is pure Go and portable across every GOOS/GOARCH the project builds (CI
compiles it on Linux, macOS, and Windows).

WASM is out of scope this round because of the pinned dependency versions
(go.mod pins vellum `v1.0.10` and, transitively, `mmap-go v1.0.4`, to stay on the
`go 1.23.0` floor):

- vellum `v1.0.10`'s mmap path uses `blevesearch/mmap-go v1.0.4`, which has only
  `mmap_unix.go` (darwin/dragonfly/freebsd/linux/openbsd/solaris/netbsd) and
  `mmap_windows.go` — **no `js/wasm` target**. (`mmap-go` added `mmap_wasm.go`
  only in v1.2.0, which needs go1.24 and would violate the go1.23 floor.)
- vellum `v1.0.10`'s `nommap` fallback (`vellum_nommap.go`) does not even compile:
  it calls `ioutil.ReadFile(string)`, passing the *type* `string` instead of the
  `path` argument.

Production sidesteps both, since `Open()`/`vellum.Load` touches neither `mmap-go`
nor the broken `nommap` file. The `js/wasm` gap only affects the test-only
`OpenMmap` path. Revisiting WASM would mean bumping vellum/mmap-go past the
go1.23 floor.

## Public surface

- `Open() (*Segmenter, error)` — process-wide singleton over the embedded FST
  (callers must NOT `Close` it). This is the production entry point.
- `(*Segmenter) Cut(str string) []string` — mirrors gse `Cut(str, true)`.
- `OpenMmap(fstPath, totalFreqPath string)` — mmap a prebuilt FST file
  (footprint/resource tests only; native-only as noted above).
- `LoadBytes(data []byte, totalFreq float64)` — load from an in-memory `[]byte`
  (the embed path).

The consumer is `core/tokenizer.CJKTokenizer`, which strips NUL/C0 control bytes
before segmentation (see `cjk_tokenizer.go::normalizeForSegmentation`) and feeds
the result to `Segmenter.Cut`.
