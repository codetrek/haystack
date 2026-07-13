// Package fstcjk is an FST-backed CJK segmenter that is byte-identical to
// go-ego/gse v1.0.2 Cut(text, true).
//
// # What ships at runtime
//
// Only segmenter.go + util.go + doc.go compile into the runtime binary, plus the
// embedded dictionary artifacts (dict.fst, dict.totalfreq) and one runtime
// dependency: go-ego/gse/hmm (for out-of-vocabulary segmentation). The offline
// builder (build.go) and the generator (gen_test.go) are tagged "//go:build
// tools", so gse's dictionary LOADER — and cobra/pflag pulled in by vellum's CLI
// — never link into production.
//
// # How the dictionary loads
//
// The production entry point, Open(), embeds dict.fst via //go:embed and loads
// it with vellum.Load over the in-memory bytes (anonymous memory; NO file mmap).
// This path is pure Go and portable across every GOOS/GOARCH the build supports
// (it is what CI builds on Linux, macOS, and Windows). OpenMmap() is an
// alternative that mmaps a prebuilt FST file via vellum.Open; it is used only by
// footprint/resource tests, not by the production wrapper.
//
// # WASM is descoped this round (native-only)
//
// This package targets native builds. It is NOT buildable for js/wasm, for two
// independent reasons rooted in the pinned dependency versions (go.mod pins
// vellum v1.0.10 and, transitively, mmap-go v1.0.4 to stay on the go1.23 floor):
//
//   - vellum v1.0.10's mmap path imports blevesearch/mmap-go v1.0.4, whose only
//     platform files are mmap_unix.go (darwin/dragonfly/freebsd/linux/openbsd/
//     solaris/netbsd) and mmap_windows.go. There is no js/wasm target; mmap-go
//     only added mmap_wasm.go in v1.2.0, which requires go1.24 and would violate
//     the go1.23 floor.
//   - vellum v1.0.10's non-mmap fallback (vellum_nommap.go, behind the `nommap`
//     build tag) does not even compile: it calls ioutil.ReadFile(string) —
//     passing the type `string` instead of the `path` argument — so the `nommap`
//     escape hatch cannot be used to dodge the mmap-go platform gap either.
//
// Production avoids both of these because Open() uses vellum.Load (embedded
// bytes), which touches neither mmap-go nor the broken nommap file. The js/wasm
// gap therefore only blocks the (test-only) OpenMmap path. Revisiting WASM would
// require bumping vellum/mmap-go past the go1.23 floor and is out of scope here.
//
// # Regenerating the embedded dictionary
//
// dict.fst and dict.totalfreq are committed, prebuilt artifacts. They are
// regenerated from gse's source dictionaries (data/dict/zh/s_1.txt + t_1.txt) by
// the tools-tagged generator. On ANY gse or dictionary bump you MUST regenerate
// them and re-run the fidelity suite — fidelity rots SILENTLY with no compile
// error, because the embedded FST is just bytes. See fstcjk/README.md.
//
// The go:generate directive below regenerates both artifacts in place:
//
//go:generate go test -tags tools -run TestGenerateDictFST -count=1 .
package fstcjk
