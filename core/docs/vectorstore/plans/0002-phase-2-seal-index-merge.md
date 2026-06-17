# Vector Store Phase 2 (Seal + Async Indexing + N-way Merge + Manifest/Recovery) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the Phase-1 single in-memory head `core/vectorstore` into a segmented LSM-style store: a mutable brute head plus immutable on-disk SEALED segments, each background-indexed with a migrated HNSW graph, searched via an N-way merge, with a single-file atomic manifest, per-write head WAL, and crash recovery with orphan-sweep.

**Architecture:** The store keeps one in-memory brute `head` segment (Phase-1 `segment`) plus an ordered slice of immutable on-disk `sealedSegment`s. SEAL dumps the head to page-aligned mmap files (`vectors.dat`/`slotdoc.dat`/`tomb.dat`/`payload.dat`) under `seg-<id>-<gen>/`, atomically swaps a `manifest`, then truncates the head WAL; a background builder builds a slimmed HNSW (migrated COPY of `core/vectorindex`'s graph + `NodeStore` interface into `vectorstore`, vectors owned by the sealed segment, not the graph) and flips the segment `pending→indexed` in the manifest. Search merges each indexed sealed segment (HNSW, post-filtered by its tombstone bitmap), each pending sealed segment (brute), and the head (brute) into one shared `topK`. Durability uses the audit-#76 atomic pattern (tmp+fsync+rename+dir-fsync) for both segment files and the manifest, with recovery = load manifest → mmap segments → replay head WAL → rebuild derived maps → resume pending builds → orphan-sweep.

**Tech Stack:** Go 1.23+ (toolchain pinned 1.24.2 in CI), module `github.com/codetrek/haystack/core`. Reuses in-package: `metric.go`, `result.go` (topK), `bitmap.go`, `wal.go`, `segment.go`, `validate.go`, `idtable`. Migrates `core/vectorindex` HNSW graph algorithm + `NodeStore`. mmap via `syscall.Mmap` (copied unix/windows platform files). Tests with `testify`-free table style matching existing `*_test.go`. Gates: `go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.2` (total 90% / function 80% / package 85%), `gofmt`, cross-platform `go build ./...`.

---

## Conventions for every task

- **Working dir:** all commands run from `/workspace/haystack/core`.
- **Run a single test:** `go test ./vectorstore/ -run '^TestName$' -count=1 -v`
- **Tree-green gate after each task (must pass before commit):** `go build ./... && go test ./vectorstore/ ./vectorindex/ -count=1` (vectorindex must stay green — we only COPY from it, never edit it).
- **Format gate before each commit:** `gofmt -l vectorstore/` must print nothing.
- **Coverage gate (run before the final commit of each multi-step task that adds production code):** `go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.2` — every new production path needs a test in the same task; the gate is package≥85%, total≥90%, function≥80%.
- **Never edit `core/vectorindex`.** The HNSW migration is a copy-and-slim into `vectorstore`.
- All new on-disk structs must be little-endian, padding-free, with a compile-time size assertion (`var _ [N]byte = [unsafe.Sizeof(T{})]byte{}`), mirroring `vectorindex/mmap_format.go`.

---

## File Structure

**New production files in `core/vectorstore/`:**

| File | Responsibility |
|---|---|
| `osfile.go` (modify) | Widen `osFile` interface + add `fsCreate`/`fsOpen`/`fsRename`/`fsRemove`/`fsyncDir` injectables (mmap + atomic-rename need them) |
| `mmap.go` | `mmapAlloc`/`mmapFree`/`mmapSync` package vars (copied from vectorindex) |
| `mmap_unix.go` / `mmap_windows.go` | platform mmap syscalls (copied) |
| `segfile_format.go` | On-disk header structs for sealed-segment `.dat` files + magic + page-align + size asserts |
| `sealed.go` | `sealedSegment`: mmap'd read-only view (`eachLive`/`read`/`slotDoc`/`getVectorRef`-by-slot) + mutable persisted tombstone bitmap |
| `seal.go` | `writeSealedSegment` (dump head → fsync'd `.dat` files in `seg-<id>-<gen>/`) + `openSealedSegment` (mmap back) |
| `manifest.go` | Manifest struct + `writeManifest` (CRC + tmp+rename+dir-fsync) + `readManifest` |
| `graphstore.go` | `segGraphStore`: slim `NodeStore` impl — owns graph topology only; delegates vectors to a `sealedSegment` by slot (nodeId==slot) |
| `hnsw.go` | COPY of `vectorindex/hnsw.go` graph algorithm (package `vectorstore`), unexported `hnswIndex` |
| `nodestore.go` | COPY of `vectorindex/store.go`'s `NodeStore` interface + `errNoEntryPoint` (package `vectorstore`) |
| `graphfile_format.go` | On-disk header + slot structs for the per-segment graph (`graph.dat`) |
| `graphfile.go` | `writeGraphFile` (dump built in-memory graph → fsync) + `openGraphFile` (mmap read-only) |
| `builder.go` | Background builder goroutine pool: build HNSW for a pending sealed segment, persist `graph.dat`, flip manifest `pending→indexed` |
| `doc.go` (modify) | Update package doc to Phase 2 |

**Modified:** `store.go` (segment SET + state, Put/Delete routing through global `docId→segId`, N-way Search, Seal, recovery), `result.go` (no change — reused).

---

## Task 0: Widen `osFile` + copy mmap + atomic-fs helpers into vectorstore

The Phase-1 `vectorstore/osfile.go` interface is a strict subset (no `Fd`/`ReadAt`/`Stat`/`Create`/`Rename`/`Remove`) — mmap and atomic-rename need the wider shape. Copy the proven helpers from `vectorindex` into `vectorstore` (do NOT import vectorindex internals — they're unexported).

**Files:**
- Modify: `core/vectorstore/osfile.go`
- Create: `core/vectorstore/mmap.go`, `core/vectorstore/mmap_unix.go`, `core/vectorstore/mmap_windows.go`
- Test: `core/vectorstore/mmap_test.go`

- [ ] **Step 1: Write the failing test**

Create `core/vectorstore/mmap_test.go`:

```go
package vectorstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMmap_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.bin")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	requireNoError(t, err)
	defer f.Close()
	requireNoError(t, f.Truncate(4096))

	data, err := mmapAlloc(f.Fd(), 0, 4096, mmapRead|mmapWrite)
	requireNoError(t, err)
	data[0] = 0xAB
	data[1] = 0xCD
	requireNoError(t, mmapSync(data))
	requireNoError(t, mmapFree(data))

	got := make([]byte, 2)
	if _, err := f.ReadAt(got, 0); err != nil {
		t.Fatalf("readat: %v", err)
	}
	if got[0] != 0xAB || got[1] != 0xCD {
		t.Fatalf("mmap write not persisted: %v", got)
	}
}

func TestFsyncDir_OK(t *testing.T) {
	if err := fsyncDir(t.TempDir()); err != nil {
		t.Fatalf("fsyncDir: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./vectorstore/ -run '^TestMmap_RoundTrip$|^TestFsyncDir_OK$' -count=1`
Expected: FAIL — `undefined: mmapAlloc`, `undefined: mmapRead`, `undefined: mmapSync`, `undefined: mmapFree`, `undefined: fsyncDir`.

- [ ] **Step 3: Widen `osFile` and add fs injectables**

Replace the entire body of `core/vectorstore/osfile.go` with:

```go
package vectorstore

import (
	"os"
	"runtime"
)

// osFile is the subset of *os.File the WAL, mmap segment files, and atomic
// manifest rewrite use, abstracted so tests can inject IO failures. *os.File
// satisfies it. (Widened from the Phase-1 subset to add Fd/ReadAt/Stat for mmap
// and the page-aligned data-file headers.)
type osFile interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	ReadAt(p []byte, off int64) (int, error)
	Seek(offset int64, whence int) (int64, error)
	Truncate(size int64) error
	Sync() error
	Stat() (os.FileInfo, error)
	Fd() uintptr
	Close() error
}

// Injectable filesystem constructors. Production uses the real os package; tests
// override them to fail on chosen operations. Each returns a true nil interface
// on error to avoid the typed-nil pitfall.
var (
	fsCreate = func(name string) (osFile, error) {
		f, err := os.Create(name)
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	fsOpen = func(name string) (osFile, error) {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	fsOpenFile = func(name string, flag int, perm os.FileMode) (osFile, error) {
		f, err := os.OpenFile(name, flag, perm)
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	fsRename = os.Rename
	fsRemove = os.Remove
)

// fsyncDir fsyncs a directory so a rename or file creation within it is durable
// across a crash (POSIX does not persist the directory entry just because file
// contents were fsynced). On Windows a directory handle cannot be fsynced, so it
// is a no-op there.
func fsyncDir(dir string) error {
	d, err := fsOpen(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		if runtime.GOOS == "windows" {
			return nil
		}
		return err
	}
	return nil
}
```

(Note: the existing `wal.go` and `walhelpers_test.go` already use `fsOpenFile`; `*os.File` satisfies the widened interface, so they keep compiling.)

- [ ] **Step 4: Create `mmap.go` (copy from vectorindex/mmap.go, package renamed)**

Create `core/vectorstore/mmap.go` with the exact body of `core/vectorindex/mmap.go` (package line `package vectorstore`). It defines `mmapRead`/`mmapWrite` consts and `mmapAlloc`/`mmapFree`/`mmapSync` package vars calling `mmapPlatform`/`munmapPlatform`/`mmapSyncPlatform`.

- [ ] **Step 5: Create `mmap_unix.go` and `mmap_windows.go` (copy from vectorindex)**

Create `core/vectorstore/mmap_unix.go` = exact body of `core/vectorindex/mmap_unix.go` (keep `//go:build !windows`, package `vectorstore`).
Create `core/vectorstore/mmap_windows.go` = exact body of `core/vectorindex/mmap_windows.go` (keep `//go:build windows`, package `vectorstore`).

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./vectorstore/ -run '^TestMmap_RoundTrip$|^TestFsyncDir_OK$' -count=1 -v`
Expected: PASS (both).

- [ ] **Step 7: Verify whole tree green + gofmt**

Run: `go build ./... && go test ./vectorstore/ ./vectorindex/ -count=1 && gofmt -l vectorstore/`
Expected: build OK, all tests ok, `gofmt -l` prints nothing.

- [ ] **Step 8: Commit**

```bash
git checkout -b phase2-vectorstore
git add core/vectorstore/osfile.go core/vectorstore/mmap.go core/vectorstore/mmap_unix.go core/vectorstore/mmap_windows.go core/vectorstore/mmap_test.go
git commit -m "feat(vectorstore): widen osFile + copy mmap/fsync helpers for Phase 2 seal"
```

---

## Task 1: Sealed-segment on-disk format structs

Define the page-aligned header structs for the four sealed-segment data files, with compile-time size assertions, modeled on `vectorindex/mmap_format.go`. No I/O yet — just the format + a probe test.

**Files:**
- Create: `core/vectorstore/segfile_format.go`
- Test: `core/vectorstore/segfile_format_test.go`

- [ ] **Step 1: Write the failing test**

Create `core/vectorstore/segfile_format_test.go`:

```go
package vectorstore

import (
	"testing"
	"unsafe"
)

func TestSegFileFormat_Sizes(t *testing.T) {
	if got := unsafe.Sizeof(vectorsHeader{}); got != 24 {
		t.Fatalf("vectorsHeader size = %d, want 24", got)
	}
	if got := unsafe.Sizeof(slotDocHeader{}); got != 16 {
		t.Fatalf("slotDocHeader size = %d, want 16", got)
	}
	if got := unsafe.Sizeof(tombHeader{}); got != 16 {
		t.Fatalf("tombHeader size = %d, want 16", got)
	}
	if got := unsafe.Sizeof(payloadHeader{}); got != 16 {
		t.Fatalf("payloadHeader size = %d, want 16", got)
	}
	if segPageSize != 4096 {
		t.Fatalf("segPageSize = %d, want 4096", segPageSize)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./vectorstore/ -run '^TestSegFileFormat_Sizes$' -count=1`
Expected: FAIL — `undefined: vectorsHeader` etc.

- [ ] **Step 3: Write minimal implementation**

Create `core/vectorstore/segfile_format.go`:

```go
package vectorstore

import "unsafe"

// segPageSize is the page-aligned header reservation for sealed-segment data
// files: each file's fixed header lives in the first segPageSize bytes, data
// follows. Mirrors vectorindex's pageSize convention.
const segPageSize = 4096

// File magic constants for sealed-segment data files (4 bytes each).
var (
	magicVectors = [4]byte{'V', 'S', 'V', 'C'} // vectors.dat
	magicSlotDoc = [4]byte{'V', 'S', 'S', 'D'} // slotdoc.dat
	magicTomb    = [4]byte{'V', 'S', 'T', 'B'} // tomb.dat
	magicPayload = [4]byte{'V', 'S', 'P', 'L'} // payload.dat
)

// vectorsHeader is the on-disk header for vectors.dat (24 bytes). After the
// segPageSize header region, data is Count rows of (Dim float32 + 1 float32
// norm) = (Dim+1)*4 bytes each, in metric-natural stored form.
type vectorsHeader struct {
	Magic [4]byte
	Dim   uint32
	Count uint64
	_     uint64 // reserved; keeps header 8-byte aligned and stable-offset
}

var _ [24]byte = [unsafe.Sizeof(vectorsHeader{})]byte{}

// slotDocHeader is the on-disk header for slotdoc.dat (16 bytes). Data is Count
// int64 docIds (slot→docId, the on-disk source of truth).
type slotDocHeader struct {
	Magic [4]byte
	_     [4]byte
	Count uint64
}

var _ [16]byte = [unsafe.Sizeof(slotDocHeader{})]byte{}

// tombHeader is the on-disk header for tomb.dat (16 bytes). Data is Words uint64
// words of the tombstone bitmap (the ONLY mutable file in a sealed segment).
type tombHeader struct {
	Magic [4]byte
	_     [4]byte
	Words uint64
}

var _ [16]byte = [unsafe.Sizeof(tombHeader{})]byte{}

// payloadHeader is the on-disk header for payload.dat (16 bytes). Data is Count
// uint32 lengths (slot→payload length) followed by the concatenated payload
// bytes; offsets are derived by prefix-sum of the lengths at open time.
type payloadHeader struct {
	Magic [4]byte
	_     [4]byte
	Count uint64
}

var _ [16]byte = [unsafe.Sizeof(payloadHeader{})]byte{}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./vectorstore/ -run '^TestSegFileFormat_Sizes$' -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Verify tree green + gofmt; commit**

Run: `go build ./... && go test ./vectorstore/ ./vectorindex/ -count=1 && gofmt -l vectorstore/`
Expected: green, no gofmt diffs.

```bash
git add core/vectorstore/segfile_format.go core/vectorstore/segfile_format_test.go
git commit -m "feat(vectorstore): sealed-segment on-disk header structs + size asserts"
```

---

## Task 2: Write + open a sealed records-segment (round-trip, fsync, dir-fsync)

`writeSealedSegment` dumps a `*segment` (head) to `seg-<id>-<gen>/{vectors,slotdoc,tomb,payload}.dat`, fsyncing each file then the directory (audit-#76 ordering: data durable before any sentinel). `openSealedSegment` mmaps them read-only (except `tomb.dat`, mapped RW since tombstones mutate post-seal). The sealed view exposes `read`/`eachLive`/`slotDoc`/`getVectorRef` by slot. This is the "records-segment owns its vectors" layer.

**Files:**
- Create: `core/vectorstore/sealed.go`, `core/vectorstore/seal.go`
- Test: `core/vectorstore/seal_test.go`

- [ ] **Step 1: Write the failing test**

Create `core/vectorstore/seal_test.go`:

```go
package vectorstore

import (
	"path/filepath"
	"testing"
)

// buildHeadSeg appends rows into a fresh in-memory segment (head) for sealing.
func buildHeadSeg(m Metric, rows []struct {
	doc int64
	v   []float32
	pl  []byte
}) *segment {
	seg := newSegment(m)
	for _, r := range rows {
		stored, norm := m.prepare(r.v)
		seg.append(r.doc, stored, norm, r.pl)
	}
	return seg
}

func TestSeal_WriteOpenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	head := buildHeadSeg(DotProduct, []struct {
		doc int64
		v   []float32
		pl  []byte
	}{
		{10, []float32{1, 0, 0}, []byte("p10")},
		{20, []float32{0, 1, 0}, []byte("p20")},
		{30, []float32{0, 0, 1}, nil},
	})
	// Tombstone slot 1 (docId 20) so the sealed segment carries a delete.
	head.tombstone(1)

	segDir := filepath.Join(dir, "seg-1-0")
	requireNoError(t, writeSealedSegment(segDir, head))

	ss, err := openSealedSegment(segDir, DotProduct)
	requireNoError(t, err)
	defer ss.close()

	if ss.dim != 3 {
		t.Fatalf("dim = %d, want 3", ss.dim)
	}
	if ss.count() != 3 {
		t.Fatalf("count = %d, want 3", ss.count())
	}
	// slot 0 live, docId 10, payload p10, vector {1,0,0}
	stored, _, pl, live := ss.read(0)
	if !live || ss.slotDoc(0) != 10 || string(pl) != "p10" || stored[0] != 1 {
		t.Fatalf("slot0 = live=%v doc=%d pl=%q v=%v", live, ss.slotDoc(0), pl, stored)
	}
	// slot 1 tombstoned (docId 20)
	if _, _, _, live := ss.read(1); live {
		t.Fatal("slot1 should be tombstoned")
	}
	// eachLive visits only slots 0 and 2
	var seenDocs []int64
	ss.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		seenDocs = append(seenDocs, docID)
	})
	if len(seenDocs) != 2 || seenDocs[0] != 10 || seenDocs[1] != 30 {
		t.Fatalf("eachLive docs = %v, want [10 30]", seenDocs)
	}
}

func TestSeal_TombstonePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	head := buildHeadSeg(DotProduct, []struct {
		doc int64
		v   []float32
		pl  []byte
	}{
		{10, []float32{1, 0}, nil},
		{20, []float32{0, 1}, nil},
	})
	segDir := filepath.Join(dir, "seg-2-0")
	requireNoError(t, writeSealedSegment(segDir, head))

	ss, err := openSealedSegment(segDir, DotProduct)
	requireNoError(t, err)
	requireNoError(t, ss.tombstoneSlot(0)) // delete docId 10 post-seal, must be durable
	ss.close()

	ss2, err := openSealedSegment(segDir, DotProduct)
	requireNoError(t, err)
	defer ss2.close()
	if _, _, _, live := ss2.read(0); live {
		t.Fatal("post-seal tombstone of slot0 did not survive reopen")
	}
	if _, _, _, live := ss2.read(1); !live {
		t.Fatal("slot1 must still be live")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./vectorstore/ -run '^TestSeal_' -count=1`
Expected: FAIL — `undefined: writeSealedSegment`, `undefined: openSealedSegment`.

- [ ] **Step 3: Implement `sealed.go` (the read-only mmap view + mutable tomb)**

Create `core/vectorstore/sealed.go`:

```go
package vectorstore

import (
	"encoding/binary"
	"fmt"
	"math"
	"path/filepath"
)

// sealedSegment is an immutable, mmap-backed records segment. Vectors, slot→docId,
// and payload are read-only; only the tombstone bitmap (tomb.dat) is mutable —
// Delete/Update set bits and msync them. nodeId==slot for the per-segment HNSW.
type sealedSegment struct {
	dir    string
	metric Metric
	dim    int
	n      int // row count (incl. tombstoned)

	// vectors.dat mapping: each row is (dim float32 + 1 float32 norm).
	vecMap  []byte
	rowF32  int // dim+1 (floats per row)
	vecBase int // byte offset where row data starts (segPageSize)

	slotDocs []int64 // decoded from slotdoc.dat (small; copy out, not mmap-aliased)

	// tomb.dat mapping (RW): header at offset 0, words start at segPageSize.
	tombMap   []byte
	tombWords int

	// payload.dat mapping (read-only): lengths then bytes.
	plMap     []byte
	plLens    []uint32
	plOffsets []int // byte offset of each payload within the data region
	plBase    int   // byte offset where payload bytes start
}

func (s *sealedSegment) count() int { return s.n }

func (s *sealedSegment) slotDoc(slot int) int64 { return s.slotDocs[slot] }

// tombGet reports whether slot is tombstoned, reading the mmap'd bitmap words.
func (s *sealedSegment) tombGet(slot int) bool {
	w := slot >> 6
	if w >= s.tombWords {
		return false
	}
	off := segPageSize + w*8
	word := binary.LittleEndian.Uint64(s.tombMap[off : off+8])
	return word&(1<<uint(slot&63)) != 0
}

// tombstoneSlot sets slot's tombstone bit and msyncs tomb.dat so the delete is
// durable. The bitmap is pre-sized at seal to cover every slot, so no growth is
// needed (segments are immutable in row count).
func (s *sealedSegment) tombstoneSlot(slot int) error {
	if slot < 0 || slot >= s.n {
		return fmt.Errorf("vectorstore: tombstone slot %d out of range [0,%d)", slot, s.n)
	}
	w := slot >> 6
	off := segPageSize + w*8
	word := binary.LittleEndian.Uint64(s.tombMap[off : off+8])
	word |= 1 << uint(slot&63)
	binary.LittleEndian.PutUint64(s.tombMap[off:off+8], word)
	return mmapSync(s.tombMap)
}

// getVectorRef returns the stored-form vector for slot without copying (aliases
// the mmap). Callers must not retain or mutate it. Used by the HNSW graph leg.
func (s *sealedSegment) getVectorRef(slot int) []float32 {
	start := s.vecBase + slot*s.rowF32*4
	out := make([]float32, s.dim)
	for i := 0; i < s.dim; i++ {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(s.vecMap[start+i*4:]))
	}
	return out
}

func (s *sealedSegment) norm(slot int) float32 {
	start := s.vecBase + slot*s.rowF32*4 + s.dim*4
	return math.Float32frombits(binary.LittleEndian.Uint32(s.vecMap[start:]))
}

// read returns slot's stored vector, norm, payload, and liveness.
func (s *sealedSegment) read(slot int) (stored []float32, norm float32, payload []byte, live bool) {
	if slot < 0 || slot >= s.n || s.tombGet(slot) {
		return nil, 0, nil, false
	}
	return s.getVectorRef(slot), s.norm(slot), s.payload(slot), true
}

func (s *sealedSegment) payload(slot int) []byte {
	n := int(s.plLens[slot])
	if n == 0 {
		return nil
	}
	off := s.plBase + s.plOffsets[slot]
	out := make([]byte, n)
	copy(out, s.plMap[off:off+n])
	return out
}

// eachLive visits non-tombstoned slots ascending, mirroring segment.eachLive so
// the brute leg and graph builder share one iterator contract.
func (s *sealedSegment) eachLive(fn func(slot int, docID int64, stored []float32, norm float32)) {
	for slot := 0; slot < s.n; slot++ {
		if s.tombGet(slot) {
			continue
		}
		fn(slot, s.slotDocs[slot], s.getVectorRef(slot), s.norm(slot))
	}
}

func (s *sealedSegment) close() {
	if s.vecMap != nil {
		_ = mmapFree(s.vecMap)
		s.vecMap = nil
	}
	if s.tombMap != nil {
		_ = mmapFree(s.tombMap)
		s.tombMap = nil
	}
	if s.plMap != nil {
		_ = mmapFree(s.plMap)
		s.plMap = nil
	}
}

// segFilePath joins a sealed-segment dir with a component filename.
func segFilePath(dir, name string) string { return filepath.Join(dir, name) }
```

- [ ] **Step 4: Implement `seal.go` (write all `.dat` files + dir-fsync; open + mmap)**

Create `core/vectorstore/seal.go`:

```go
package vectorstore

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// writeSealedSegment dumps a (frozen) head segment into segDir as four fsynced
// data files: vectors.dat, slotdoc.dat, tomb.dat, payload.dat. Files are written
// and fsynced individually, then the directory is fsynced so the entries are
// durable before any sentinel (manifest) references the segment (audit #76:
// data durable LAST-relative-to-itself, sentinel later). The dir is created here.
func writeSealedSegment(segDir string, head *segment) error {
	if err := os.MkdirAll(segDir, 0755); err != nil {
		return fmt.Errorf("seal: mkdir %s: %w", segDir, err)
	}
	n := len(head.slotDoc)
	dim := head.dim

	if err := writeVectorsFile(segFilePath(segDir, "vectors.dat"), head, dim, n); err != nil {
		return err
	}
	if err := writeSlotDocFile(segFilePath(segDir, "slotdoc.dat"), head, n); err != nil {
		return err
	}
	if err := writeTombFile(segFilePath(segDir, "tomb.dat"), head, n); err != nil {
		return err
	}
	if err := writePayloadFile(segFilePath(segDir, "payload.dat"), head, n); err != nil {
		return err
	}
	// Make the four directory entries durable.
	return fsyncDir(segDir)
}

func writeVectorsFile(path string, head *segment, dim, n int) error {
	f, err := fsCreate(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hdr := make([]byte, segPageSize)
	copy(hdr[0:4], magicVectors[:])
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(dim))
	binary.LittleEndian.PutUint64(hdr[8:16], uint64(n))
	if _, err := f.Write(hdr); err != nil {
		return err
	}
	row := make([]byte, (dim+1)*4)
	for slot := 0; slot < n; slot++ {
		v := head.vectors[slot]
		for i := 0; i < dim; i++ {
			binary.LittleEndian.PutUint32(row[i*4:], math.Float32bits(v[i]))
		}
		binary.LittleEndian.PutUint32(row[dim*4:], math.Float32bits(head.norms[slot]))
		if _, err := f.Write(row); err != nil {
			return err
		}
	}
	return f.Sync()
}

func writeSlotDocFile(path string, head *segment, n int) error {
	f, err := fsCreate(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hdr := make([]byte, segPageSize)
	copy(hdr[0:4], magicSlotDoc[:])
	binary.LittleEndian.PutUint64(hdr[8:16], uint64(n))
	if _, err := f.Write(hdr); err != nil {
		return err
	}
	buf := make([]byte, 8)
	for slot := 0; slot < n; slot++ {
		binary.LittleEndian.PutUint64(buf, uint64(head.slotDoc[slot]))
		if _, err := f.Write(buf); err != nil {
			return err
		}
	}
	return f.Sync()
}

func writeTombFile(path string, head *segment, n int) error {
	f, err := fsCreate(path)
	if err != nil {
		return err
	}
	defer f.Close()
	words := (n + 63) / 64
	if words == 0 {
		words = 1 // always at least one word so the data region is non-empty
	}
	hdr := make([]byte, segPageSize)
	copy(hdr[0:4], magicTomb[:])
	binary.LittleEndian.PutUint64(hdr[8:16], uint64(words))
	if _, err := f.Write(hdr); err != nil {
		return err
	}
	out := make([]byte, words*8)
	for slot := 0; slot < n; slot++ {
		if head.tomb.get(slot) {
			w := slot >> 6
			cur := binary.LittleEndian.Uint64(out[w*8:])
			cur |= 1 << uint(slot&63)
			binary.LittleEndian.PutUint64(out[w*8:], cur)
		}
	}
	if _, err := f.Write(out); err != nil {
		return err
	}
	return f.Sync()
}

func writePayloadFile(path string, head *segment, n int) error {
	f, err := fsCreate(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hdr := make([]byte, segPageSize)
	copy(hdr[0:4], magicPayload[:])
	binary.LittleEndian.PutUint64(hdr[8:16], uint64(n))
	if _, err := f.Write(hdr); err != nil {
		return err
	}
	lens := make([]byte, n*4)
	var total int
	for slot := 0; slot < n; slot++ {
		l := len(head.payloads[slot])
		binary.LittleEndian.PutUint32(lens[slot*4:], uint32(l))
		total += l
	}
	if _, err := f.Write(lens); err != nil {
		return err
	}
	for slot := 0; slot < n; slot++ {
		if len(head.payloads[slot]) > 0 {
			if _, err := f.Write(head.payloads[slot]); err != nil {
				return err
			}
		}
	}
	return f.Sync()
}

// openSealedSegment mmaps a sealed segment's files. vectors/slotdoc/payload are
// read-only; tomb.dat is mapped read-write (the only mutable file). The metric
// must match the store's (vectors are in its natural stored form).
func openSealedSegment(segDir string, metric Metric) (*sealedSegment, error) {
	s := &sealedSegment{dir: segDir, metric: metric}

	// vectors.dat
	vf, err := fsOpenFile(segFilePath(segDir, "vectors.dat"), os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	vSize, err := fileSize(vf)
	if err != nil {
		vf.Close()
		return nil, err
	}
	vmap, err := mmapAlloc(vf.Fd(), 0, int(vSize), mmapRead)
	vf.Close()
	if err != nil {
		return nil, err
	}
	if string(vmap[0:4]) != string(magicVectors[:]) {
		_ = mmapFree(vmap)
		return nil, fmt.Errorf("seal: bad vectors magic in %s", segDir)
	}
	s.vecMap = vmap
	s.dim = int(binary.LittleEndian.Uint32(vmap[4:8]))
	s.n = int(binary.LittleEndian.Uint64(vmap[8:16]))
	s.rowF32 = s.dim + 1
	s.vecBase = segPageSize

	// slotdoc.dat (decode into a slice; it is small and queried per-result)
	sd, err := readWholeFile(segFilePath(segDir, "slotdoc.dat"))
	if err != nil {
		s.close()
		return nil, err
	}
	if string(sd[0:4]) != string(magicSlotDoc[:]) {
		s.close()
		return nil, fmt.Errorf("seal: bad slotdoc magic in %s", segDir)
	}
	s.slotDocs = make([]int64, s.n)
	for i := 0; i < s.n; i++ {
		s.slotDocs[i] = int64(binary.LittleEndian.Uint64(sd[segPageSize+i*8:]))
	}

	// tomb.dat (RW mmap)
	tf, err := fsOpenFile(segFilePath(segDir, "tomb.dat"), os.O_RDWR, 0)
	if err != nil {
		s.close()
		return nil, err
	}
	tSize, err := fileSize(tf)
	if err != nil {
		tf.Close()
		s.close()
		return nil, err
	}
	tmap, err := mmapAlloc(tf.Fd(), 0, int(tSize), mmapRead|mmapWrite)
	tf.Close()
	if err != nil {
		s.close()
		return nil, err
	}
	if string(tmap[0:4]) != string(magicTomb[:]) {
		_ = mmapFree(tmap)
		s.close()
		return nil, fmt.Errorf("seal: bad tomb magic in %s", segDir)
	}
	s.tombMap = tmap
	s.tombWords = int(binary.LittleEndian.Uint64(tmap[8:16]))

	// payload.dat (read-only mmap)
	pf, err := fsOpenFile(segFilePath(segDir, "payload.dat"), os.O_RDONLY, 0)
	if err != nil {
		s.close()
		return nil, err
	}
	pSize, err := fileSize(pf)
	if err != nil {
		pf.Close()
		s.close()
		return nil, err
	}
	pmap, err := mmapAlloc(pf.Fd(), 0, int(pSize), mmapRead)
	pf.Close()
	if err != nil {
		s.close()
		return nil, err
	}
	if string(pmap[0:4]) != string(magicPayload[:]) {
		_ = mmapFree(pmap)
		s.close()
		return nil, fmt.Errorf("seal: bad payload magic in %s", segDir)
	}
	s.plMap = pmap
	s.plLens = make([]uint32, s.n)
	s.plOffsets = make([]int, s.n)
	off := 0
	for i := 0; i < s.n; i++ {
		l := binary.LittleEndian.Uint32(pmap[segPageSize+i*4:])
		s.plLens[i] = l
		s.plOffsets[i] = off
		off += int(l)
	}
	s.plBase = segPageSize + s.n*4
	return s, nil
}

func fileSize(f osFile) (int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// readWholeFile reads an entire file into memory (used for the small slotdoc.dat).
func readWholeFile(path string) ([]byte, error) {
	f, err := fsOpen(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sz, err := fileSize(f)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, sz)
	if _, err := f.ReadAt(buf, 0); err != nil {
		return nil, err
	}
	return buf, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./vectorstore/ -run '^TestSeal_' -count=1 -v`
Expected: PASS (both `TestSeal_WriteOpenRoundTrip` and `TestSeal_TombstonePersistsAcrossReopen`).

- [ ] **Step 6: Verify tree green + gofmt; commit**

Run: `go build ./... && go test ./vectorstore/ ./vectorindex/ -count=1 && gofmt -l vectorstore/`
Expected: green, no gofmt diffs.

```bash
git add core/vectorstore/sealed.go core/vectorstore/seal.go core/vectorstore/seal_test.go
git commit -m "feat(vectorstore): write/open mmap'd sealed records-segment with persisted tombstone"
```

---

## Task 3: Manifest single-file atomic rewrite (CRC + tmp+rename+dir-fsync)

The manifest is the global sentinel: it lists sealed segments (segId/gen/vecCount/tombCount), each segment's index state (pending|indexed), the head segId, and a monotonic version. It is rewritten only on structural change. Write = serialize + CRC + tmp + fsync + rename + dir-fsync (audit #76 generalized). A torn `manifest.tmp` is never observed (the rename is atomic); a bad CRC on read is an error.

**Files:**
- Create: `core/vectorstore/manifest.go`
- Test: `core/vectorstore/manifest_test.go`

- [ ] **Step 1: Write the failing test**

Create `core/vectorstore/manifest_test.go`:

```go
package vectorstore

import (
	"os"
	"path/filepath"
	"testing"
)

func sampleManifest() *manifest {
	return &manifest{
		Version: 7,
		Head:    segID(5),
		Segments: []segmentEntry{
			{SegID: 1, Gen: 0, VecCount: 100, TombCount: 3, State: segPending},
			{SegID: 2, Gen: 0, VecCount: 200, TombCount: 0, State: segIndexed},
		},
	}
}

func TestManifest_WriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := sampleManifest()
	requireNoError(t, writeManifest(dir, m))

	got, err := readManifest(dir)
	requireNoError(t, err)
	if got.Version != 7 || got.Head != 5 || len(got.Segments) != 2 {
		t.Fatalf("manifest mismatch: %+v", got)
	}
	if got.Segments[0].SegID != 1 || got.Segments[0].State != segPending {
		t.Fatalf("seg0 = %+v", got.Segments[0])
	}
	if got.Segments[1].SegID != 2 || got.Segments[1].State != segIndexed || got.Segments[1].VecCount != 200 {
		t.Fatalf("seg1 = %+v", got.Segments[1])
	}
}

func TestManifest_NoTmpLeftBehind(t *testing.T) {
	dir := t.TempDir()
	requireNoError(t, writeManifest(dir, sampleManifest()))
	if _, err := os.Stat(filepath.Join(dir, "manifest.tmp")); !os.IsNotExist(err) {
		t.Fatal("manifest.tmp should be renamed away, not left behind")
	}
}

func TestManifest_CorruptCRCRejected(t *testing.T) {
	dir := t.TempDir()
	requireNoError(t, writeManifest(dir, sampleManifest()))
	path := filepath.Join(dir, "manifest")
	data, err := os.ReadFile(path)
	requireNoError(t, err)
	data[len(data)-1] ^= 0xFF // corrupt the trailing CRC
	requireNoError(t, os.WriteFile(path, data, 0644))
	if _, err := readManifest(dir); err == nil {
		t.Fatal("readManifest must reject a corrupt CRC")
	}
}

func TestManifest_MissingIsNotExist(t *testing.T) {
	_, err := readManifest(t.TempDir())
	if !os.IsNotExist(err) {
		t.Fatalf("missing manifest err = %v, want os.IsNotExist", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./vectorstore/ -run '^TestManifest_' -count=1`
Expected: FAIL — `undefined: manifest`, `undefined: writeManifest`, `undefined: segID`, etc.

- [ ] **Step 3: Write minimal implementation**

Create `core/vectorstore/manifest.go`:

```go
package vectorstore

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
)

// segID identifies a records segment. The head has its own segId; sealed
// segments get monotonically increasing ones from the manifest version space.
type segID int64

// segState is the (index, segment) build state. In Phase 2 there is exactly one
// index (the default HNSW), so this lives directly on the segment entry.
type segState uint8

const (
	segPending segState = 0 // no graph yet → brute-searched
	segIndexed segState = 1 // graph built → graph-searched
)

// segmentEntry is the manifest record for one sealed records segment. The on-disk
// directory is derived as seg-<SegID>-<Gen>/ (paths are not stored; §4.8).
type segmentEntry struct {
	SegID     segID
	Gen       uint32
	VecCount  uint64
	TombCount uint64
	State     segState
}

// manifest is the whole-store metadata snapshot, rewritten atomically on each
// structural change (seal). Per-write durability is the head WAL, not this file.
type manifest struct {
	Version  uint64
	Head     segID
	Segments []segmentEntry
}

var magicManifest = [4]byte{'V', 'S', 'M', 'F'}

const manifestVersionByte = 1

// serializeManifest encodes a manifest as: magic(4) | fmtver(1) | version(8) |
// head(8) | nSeg(4) | [segId(8) gen(4) vec(8) tomb(8) state(1)]* | crc32(4).
// The CRC covers everything before it.
func serializeManifest(m *manifest) []byte {
	body := make([]byte, 0, 4+1+8+8+4+len(m.Segments)*29+4)
	body = append(body, magicManifest[:]...)
	body = append(body, manifestVersionByte)
	body = appendU64(body, m.Version)
	body = appendU64(body, uint64(m.Head))
	body = appendU32(body, uint32(len(m.Segments)))
	for _, e := range m.Segments {
		body = appendU64(body, uint64(e.SegID))
		body = appendU32(body, e.Gen)
		body = appendU64(body, e.VecCount)
		body = appendU64(body, e.TombCount)
		body = append(body, byte(e.State))
	}
	crc := crc32.ChecksumIEEE(body)
	return appendU32(body, crc)
}

func parseManifest(b []byte) (*manifest, error) {
	if len(b) < 4+1+8+8+4+4 {
		return nil, fmt.Errorf("manifest: too short (%d bytes)", len(b))
	}
	stored := binary.LittleEndian.Uint32(b[len(b)-4:])
	if crc32.ChecksumIEEE(b[:len(b)-4]) != stored {
		return nil, fmt.Errorf("manifest: CRC mismatch (corrupt)")
	}
	if string(b[0:4]) != string(magicManifest[:]) {
		return nil, fmt.Errorf("manifest: bad magic %q", b[0:4])
	}
	off := 5 // skip magic(4)+fmtver(1)
	m := &manifest{}
	m.Version = binary.LittleEndian.Uint64(b[off:])
	off += 8
	m.Head = segID(binary.LittleEndian.Uint64(b[off:]))
	off += 8
	nSeg := int(binary.LittleEndian.Uint32(b[off:]))
	off += 4
	m.Segments = make([]segmentEntry, nSeg)
	for i := 0; i < nSeg; i++ {
		var e segmentEntry
		e.SegID = segID(binary.LittleEndian.Uint64(b[off:]))
		off += 8
		e.Gen = binary.LittleEndian.Uint32(b[off:])
		off += 4
		e.VecCount = binary.LittleEndian.Uint64(b[off:])
		off += 8
		e.TombCount = binary.LittleEndian.Uint64(b[off:])
		off += 8
		e.State = segState(b[off])
		off++
		m.Segments[i] = e
	}
	return m, nil
}

// writeManifest atomically rewrites dir/manifest via tmp+fsync+rename+dir-fsync
// (the audit-#76 pattern). On any step error the tmp file is removed.
func writeManifest(dir string, m *manifest) error {
	tmp := filepath.Join(dir, "manifest.tmp")
	final := filepath.Join(dir, "manifest")
	f, err := fsCreate(tmp)
	if err != nil {
		return fmt.Errorf("writeManifest: create tmp: %w", err)
	}
	if _, err := f.Write(serializeManifest(m)); err != nil {
		f.Close()
		fsRemove(tmp)
		return fmt.Errorf("writeManifest: write: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		fsRemove(tmp)
		return fmt.Errorf("writeManifest: sync: %w", err)
	}
	if err := f.Close(); err != nil {
		fsRemove(tmp)
		return fmt.Errorf("writeManifest: close: %w", err)
	}
	if err := fsRename(tmp, final); err != nil {
		fsRemove(tmp)
		return fmt.Errorf("writeManifest: rename: %w", err)
	}
	if err := fsyncDir(dir); err != nil {
		return fmt.Errorf("writeManifest: fsync dir: %w", err)
	}
	return nil
}

// readManifest loads dir/manifest. A missing file returns an os.IsNotExist error
// (a fresh store); a corrupt CRC returns a non-nil error.
func readManifest(dir string) (*manifest, error) {
	path := filepath.Join(dir, "manifest")
	f, err := fsOpen(path)
	if err != nil {
		return nil, err // os.IsNotExist for a fresh store
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	buf := make([]byte, st.Size())
	if _, err := f.ReadAt(buf, 0); err != nil {
		return nil, err
	}
	return parseManifest(buf)
}

func appendU32(b []byte, v uint32) []byte {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], v)
	return append(b, tmp[:]...)
}

func appendU64(b []byte, v uint64) []byte {
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], v)
	return append(b, tmp[:]...)
}

// ensure os import is used even if readManifest's error path is the only consumer.
var _ = os.IsNotExist
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./vectorstore/ -run '^TestManifest_' -count=1 -v`
Expected: PASS (all four).

- [ ] **Step 5: Verify tree green + gofmt; commit**

Run: `go build ./... && go test ./vectorstore/ ./vectorindex/ -count=1 && gofmt -l vectorstore/`
Expected: green, no gofmt diffs.

```bash
git add core/vectorstore/manifest.go core/vectorstore/manifest_test.go
git commit -m "feat(vectorstore): single-file atomic manifest (CRC + tmp+rename+dir-fsync)"
```

---

## Task 4: Migrate the HNSW graph algorithm + NodeStore interface into vectorstore (COPY, in-memory store, parity test)

COPY `vectorindex/store.go`'s `NodeStore` interface (as `nodestore.go`) and `vectorindex/hnsw.go` (as `hnsw.go`, type renamed `HNSWIndex`→`hnswIndex`, exported helpers unexported, `validateVector` collision avoided) into package `vectorstore`, plus an in-memory `memGraphStore` to prove the copied graph builds and searches identically to `vectorindex`. This is the "迁入" step; slimming (vectors owned by the sealed segment) is Task 5. We do NOT touch `vectorindex`.

**Files:**
- Create: `core/vectorstore/nodestore.go`, `core/vectorstore/hnsw.go`, `core/vectorstore/memgraphstore.go`
- Test: `core/vectorstore/hnsw_test.go`

- [ ] **Step 1: Write the failing test**

Create `core/vectorstore/hnsw_test.go`:

```go
package vectorstore

import (
	"math/rand"
	"testing"
)

// recallAtK is the fraction of the brute top-k that the graph also returned.
func recallAtK(got []SearchResult, want []int64) float64 {
	set := make(map[int64]bool, len(want))
	for _, d := range want {
		set[d] = true
	}
	hit := 0
	for _, r := range got {
		if set[r.DocID] {
			hit++
		}
	}
	return float64(hit) / float64(len(want))
}

func TestHNSW_MemStore_BuildSearchRecall(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	dim := 16
	n := 300
	vecs := make(map[int64][]float32, n)
	gs := newMemGraphStore(Cosine)
	idx := newHNSWIndex(gs, withGraphM(16), withGraphEfConstruction(100), withGraphRand(rand.New(rand.NewSource(2))))
	b := idx.newBatch()
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		vecs[int64(i)] = v
		b.put(int64(i), v)
	}
	requireNoError(t, b.commit())

	q := make([]float32, dim)
	for d := range q {
		q[d] = rng.Float32()
	}
	got, err := idx.search(q, 10)
	requireNoError(t, err)
	want := bruteForceKNN(Cosine, q, vecs, 10)
	if r := recallAtK(got, want); r < 0.8 {
		t.Fatalf("recall@10 = %.2f, want >= 0.8 (graph copy broken)", r)
	}
}

func TestHNSW_EmptyReturnsNil(t *testing.T) {
	idx := newHNSWIndex(newMemGraphStore(Cosine))
	got, err := idx.search([]float32{1, 0, 0}, 5)
	requireNoError(t, err)
	if got != nil {
		t.Fatalf("empty index search = %v, want nil", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./vectorstore/ -run '^TestHNSW_' -count=1`
Expected: FAIL — `undefined: newMemGraphStore`, `undefined: newHNSWIndex`, `undefined: withGraphM`, etc.

- [ ] **Step 3: Create `nodestore.go` (copy the interface, package vectorstore)**

Create `core/vectorstore/nodestore.go` = the `NodeStore` interface + `errNoEntryPoint` + encoding helpers from `core/vectorindex/store.go`, with `package vectorstore`. Keep the interface method set identical. (The encoding helpers `encodeFloat32s` etc. are unused by the in-mem store but are referenced by the copied graph file's compilation expectations — include them; if `go vet`/build flags any as unused at package level it will not, since Go only errors on unused *locals*/imports, not unused package funcs.)

Exact content:

```go
package vectorstore

import "errors"

// errNoEntryPoint is the sentinel returned by graphNodeStore.GetEntryPoint when
// the graph has no entry point (empty). search treats it as "empty, no results".
var errNoEntryPoint = errors.New("vectorstore: no entry point set")

// graphNodeStore is the persistence seam for the migrated HNSW graph. Vectors are
// in the store's metric-natural stored form (cosine: unit vectors). GetVectorRef
// returns the stored form without copying (caller MUST NOT mutate). nodeId is a
// dense 0-based id; for a sealed segment nodeId == slot (Task 5).
type graphNodeStore interface {
	Metric() Metric
	Dim() int
	GetVectorRef(id uint64) ([]float32, error)
	PutNode(id uint64, level int, vector []float32, docId int64) error
	DeleteNode(id uint64) error
	GetNeighbors(id uint64, layer int) ([]uint64, error)
	SetNeighbors(id uint64, layer int, neighbors []uint64) error
	GetEntryPoint() (uint64, int, error)
	SetEntryPoint(id uint64, maxLayer int) error
	ClearEntryPoint() error
	HighestLiveNodeExcluding(exclude uint64) (id uint64, level int, ok bool, err error)
	GetNodeLevel(id uint64) (int, error)
	GetNodeId(docId int64) (uint64, bool, error)
	GetDocId(id uint64) (int64, bool, error)
	NextNodeId() (uint64, error)
	txnBegin() error
	txnCommit() error
	txnAbort(cause error) error
}
```

(We drop `GetVector`/`GetNorm`/`SetNorm`/`Close` from the interface — the copied graph algorithm below never calls them; this is part of the "瘦身". Confirm by grepping the copied `hnsw.go`: it uses only `GetVectorRef`, `PutNode`, `SetNeighbors`, `GetNeighbors`, `GetEntryPoint`, `SetEntryPoint`, `ClearEntryPoint`, `HighestLiveNodeExcluding`, `GetNodeLevel`, `GetNodeId`, `GetDocId`, `NextNodeId`, `DeleteNode`, `Metric`, `Dim`, and the txn trio.)

- [ ] **Step 4: Create `hnsw.go` (copy the graph algorithm, renamed)**

Create `core/vectorstore/hnsw.go` by copying `core/vectorindex/hnsw.go` with these mechanical renames (and NOTHING else changed in the algorithm):

1. `package vectorindex` → `package vectorstore`.
2. Type `HNSWIndex` → `hnswIndex` (all occurrences).
3. `NodeStore` → `graphNodeStore`.
4. `NewHNSWIndex` → `newHNSWIndex`; `WithM`→`withGraphM`, `WithEfConstruction`→`withGraphEfConstruction`, `WithEfSearch`→`withGraphEfSearch`, `WithRand`→`withGraphRand`; `Option`→`graphOption`.
5. Method `Insert`→`insert`, `Search`→`search`, `Delete`→`delete`. (Keep the `*Locked` helpers as-is.)
6. The copied file defines `validateVector` as a METHOD `func (h *hnswIndex) validateVector(...)` — that does NOT collide with the package-level `validateVector` free function in `validate.go` (methods and funcs share no namespace). Keep it as a method.
7. `metric Metric`, `Cosine` etc. resolve to `vectorstore`'s own `metric.go` — identical semantics. No metric type conversion needed.
8. The distItem/minDistHeap/maxDistHeap/visitedSet/visitedPool/removeId/sortDistItems and `distItem` definitions are copied verbatim (these are graph-internal, distinct from `result.go`'s `maxHeap`).
9. Constants `DefaultM`/`DefaultMmax0`/`DefaultEfConstruction`/`DefaultEfSearch` → keep names but unexport: `defaultGraphM`/`defaultGraphMmax0`/`defaultGraphEfConstruction`/`defaultGraphEfSearch` (to avoid exporting from vectorstore). Update `newHNSWIndex` defaults accordingly.
10. `randomLevel` references `defaultMaxLayers`; define it here too (Task 4 owns it for the graph): add `const defaultMaxLayers = 6` at the top of this file. (Task 6's graph file format reuses it.)

The `NewBatch`/`Batch` in `vectorindex/batch.go` is also needed — copy it into the SAME `hnsw.go` file (or a `graphbatch.go`; this plan puts it inline in `hnsw.go` to keep the task self-contained), renamed: `NewBatch`→`newBatch`, `Batch`→`graphBatch`, `Put`→`put`, `Delete`→`delete` (method on graphBatch — distinct from index.delete), `Commit`→`commit`, `Discard`→`discard`, `Len`→`len_` (avoid the builtin-shadowing confusion; rename to `count`). Append this to `hnsw.go`:

```go
// --- batch (copied & slimmed from vectorindex/batch.go) ---

type graphBatchOpKind int

const (
	graphOpPut graphBatchOpKind = iota
	graphOpDelete
)

type graphBatchOp struct {
	kind   graphBatchOpKind
	docId  int64
	vector []float32
}

// graphBatch coalesces graph mutations (last-op-wins per docId) and applies them
// in one store transaction on commit. Single-goroutine ownership.
type graphBatch struct {
	idx  *hnswIndex
	ops  []graphBatchOp
	seen map[int64]int
}

func (h *hnswIndex) newBatch() *graphBatch {
	return &graphBatch{idx: h, seen: make(map[int64]int)}
}

func (b *graphBatch) put(docId int64, vector []float32) {
	cp := make([]float32, len(vector))
	copy(cp, vector)
	b.set(graphBatchOp{kind: graphOpPut, docId: docId, vector: cp})
}

func (b *graphBatch) del(docId int64) { b.set(graphBatchOp{kind: graphOpDelete, docId: docId}) }

func (b *graphBatch) set(op graphBatchOp) {
	if i, ok := b.seen[op.docId]; ok {
		b.ops[i] = op
		return
	}
	b.seen[op.docId] = len(b.ops)
	b.ops = append(b.ops, op)
}

func (b *graphBatch) count() int { return len(b.ops) }

func (b *graphBatch) commit() error {
	if len(b.ops) == 0 {
		return nil
	}
	h := b.idx
	pinnedDim := h.store.Dim()
	for _, op := range b.ops {
		if op.kind != graphOpPut {
			continue
		}
		if err := h.validateVector(op.vector); err != nil {
			return err
		}
		if pinnedDim == 0 {
			pinnedDim = len(op.vector)
		} else if len(op.vector) != pinnedDim {
			return fmt.Errorf("vectorstore: batch has mixed vector dimensions: got %d, want %d", len(op.vector), pinnedDim)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	err := h.runInTxnLocked(func() error {
		for _, op := range b.ops {
			switch op.kind {
			case graphOpPut:
				if e := h.insertOneLocked(op.docId, op.vector); e != nil {
					return e
				}
			case graphOpDelete:
				if e := h.deleteOneLocked(op.docId); e != nil {
					return e
				}
			}
		}
		return nil
	})
	b.ops = b.ops[:0]
	b.seen = make(map[int64]int)
	return err
}
```

(Ensure `fmt` is imported in `hnsw.go` — the copied file already imports it.)

- [ ] **Step 5: Create `memgraphstore.go` (in-memory graphNodeStore — copy MemNodeStore, slimmed to the interface)**

Create `core/vectorstore/memgraphstore.go` by copying `core/vectorindex/mem_store.go`, renamed `MemNodeStore`→`memGraphStore`, `NewMemNodeStore`→`newMemGraphStore`, package `vectorstore`. Drop the `GetVector`, `GetNorm`, `SetNorm`, `Close` methods that are no longer in the interface (keep the `norms` map only if `prepare` needs it — `PutNode` still calls `m.metric.prepare`; keep `norms` for `restore`-free correctness but the interface no longer exposes them — simplest: keep storing norm, drop the getter/setter/GetVector). Keep `GetVectorRef`, `PutNode`, `DeleteNode`, neighbor get/set, entrypoint trio, `HighestLiveNodeExcluding`, `GetNodeLevel`, `GetNodeId`, `GetDocId`, `NextNodeId`, txn no-ops, `Metric`, `Dim`.

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./vectorstore/ -run '^TestHNSW_' -count=1 -v`
Expected: PASS (recall@10 ≥ 0.8 and empty returns nil).

- [ ] **Step 7: Verify tree green + gofmt + vectorindex untouched; commit**

Run: `go build ./... && go test ./vectorstore/ ./vectorindex/ -count=1 && gofmt -l vectorstore/ && git diff --stat core/vectorindex/`
Expected: green; `git diff --stat core/vectorindex/` prints nothing (vectorindex unmodified).

```bash
git add core/vectorstore/nodestore.go core/vectorstore/hnsw.go core/vectorstore/memgraphstore.go core/vectorstore/hnsw_test.go
git commit -m "feat(vectorstore): migrate (copy) HNSW graph + NodeStore seam into vectorstore"
```

---

## Task 5: Slim graph store — vectors owned by the sealed segment (nodeId == slot)

Build `segGraphStore`: a `graphNodeStore` that owns ONLY graph topology (neighbors per layer, level, entry point, nodeId↔docId), and resolves vectors via the sealed segment by slot, with `nodeId == slot`. This is "瘦身：只剩图，按 vectorId 取向量" — the graph stores no second vector copy. Build over the segment's LIVE slots (skip tombstoned), feeding the batch with `(docId, restoredOrStored)`.

Design note (load-bearing): the sealed segment stores vectors in metric-natural *stored* form (cosine = unit). The graph's `prepare(vector)` inside `insertOneLocked` re-normalizes; for cosine, prepare(unit)=unit (norm 1), so feeding the stored form is correct and idempotent. To avoid re-normalization rounding, we feed `getVectorRef(slot)` (already stored form) and the graph's `prepare` is a no-op for already-unit vectors within float32 noise — recall test guards correctness. nodeId is the LIVE-dense build index (skip-tombstone gaps would break visitedSet's dense assumption), and we keep a `buildSlot[]` mapping nodeId→segment-slot so `GetVectorRef(nodeId)` reads the right row, and `GetDocId(nodeId)` returns the segment's docId.

**Files:**
- Create: `core/vectorstore/graphstore.go`
- Test: `core/vectorstore/graphstore_test.go`

- [ ] **Step 1: Write the failing test**

Create `core/vectorstore/graphstore_test.go`:

```go
package vectorstore

import (
	"math/rand"
	"path/filepath"
	"testing"
)

func TestSegGraphStore_BuildOverSealedSegment_Recall(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	dim := 16
	n := 250
	rows := make([]struct {
		doc int64
		v   []float32
		pl  []byte
	}, 0, n)
	vecs := make(map[int64][]float32, n)
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		doc := int64(1000 + i)
		rows = append(rows, struct {
			doc int64
			v   []float32
			pl  []byte
		}{doc, v, nil})
		vecs[doc] = v
	}
	head := buildHeadSeg(Cosine, rows)
	head.tombstone(5) // a hole — must be skipped by the dense build
	delete(vecs, int64(1005))

	segDir := filepath.Join(t.TempDir(), "seg-1-0")
	requireNoError(t, writeSealedSegment(segDir, head))
	ss, err := openSealedSegment(segDir, Cosine)
	requireNoError(t, err)
	defer ss.close()

	gs := newSegGraphStore(ss)
	idx := newHNSWIndex(gs, withGraphM(16), withGraphEfConstruction(100),
		withGraphRand(rand.New(rand.NewSource(8))))
	b := idx.newBatch()
	// Build over LIVE slots only, feeding stored form + segment docId.
	ss.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		gs.bindSlot(docID, slot) // teach the store nodeId↔(docId,slot)
		b.put(docID, stored)
	})
	requireNoError(t, b.commit())

	q := make([]float32, dim)
	for d := range q {
		q[d] = rng.Float32()
	}
	got, err := idx.search(q, 10)
	requireNoError(t, err)
	want := bruteForceKNN(Cosine, q, vecs, 10)
	if r := recallAtK(got, want); r < 0.8 {
		t.Fatalf("recall@10 over sealed segment = %.2f, want >= 0.8", r)
	}
	// Returned docIds must be real segment docIds (>=1000), never the tombstoned 1005.
	for _, r := range got {
		if r.DocID < 1000 {
			t.Fatalf("bogus docId %d", r.DocID)
		}
		if r.DocID == 1005 {
			t.Fatal("tombstoned docId 1005 leaked into graph results")
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./vectorstore/ -run '^TestSegGraphStore_' -count=1`
Expected: FAIL — `undefined: newSegGraphStore`.

- [ ] **Step 3: Write minimal implementation**

Create `core/vectorstore/graphstore.go`:

```go
package vectorstore

import "fmt"

// segGraphStore is a slim graphNodeStore: it owns only graph topology (neighbor
// lists per layer, per-node level, entry point) and the nodeId↔docId/segment-slot
// maps. Vectors are NOT stored here — GetVectorRef resolves nodeId→slot→sealed
// segment row, so the segment's vectors are the single copy (§3 "向量只存一份").
//
// nodeId is assigned densely (0,1,2,...) by NextNodeId at build time over the
// segment's LIVE slots, so the graph's dense-id assumptions (visitedSet, etc.)
// hold even though the segment has tombstone gaps. bindSlot must be called for
// each live (docId, slot) BEFORE that docId is inserted, so PutNode/GetVectorRef
// can map the freshly allocated nodeId to its slot.
type segGraphStore struct {
	seg *sealedSegment

	nextID    uint64
	levels    []int                // nodeId → level
	neighbors []map[int][]uint64   // nodeId → layer → neighbor ids
	nodeSlot  []int                // nodeId → segment slot (for GetVectorRef)
	nodeDoc   []int64              // nodeId → docId
	docToNode map[int64]uint64     // docId → nodeId
	pendingSlot map[int64]int      // docId → slot, set by bindSlot, consumed by PutNode

	entryID  uint64
	maxLayer int
	hasEntry bool
}

func newSegGraphStore(seg *sealedSegment) *segGraphStore {
	return &segGraphStore{
		seg:         seg,
		docToNode:   make(map[int64]uint64),
		pendingSlot: make(map[int64]int),
	}
}

// bindSlot records the segment slot for docId so the next PutNode(... docId) can
// associate the new nodeId with that slot. Called by the builder per live row.
func (g *segGraphStore) bindSlot(docID int64, slot int) { g.pendingSlot[docID] = slot }

func (g *segGraphStore) Metric() Metric { return g.seg.metric }
func (g *segGraphStore) Dim() int       { return g.seg.dim }

func (g *segGraphStore) GetVectorRef(id uint64) ([]float32, error) {
	if id >= uint64(len(g.nodeSlot)) {
		return nil, fmt.Errorf("segGraphStore: node %d not found", id)
	}
	return g.seg.getVectorRef(g.nodeSlot[id]), nil
}

func (g *segGraphStore) PutNode(id uint64, level int, vector []float32, docId int64) error {
	slot, ok := g.pendingSlot[docId]
	if !ok {
		return fmt.Errorf("segGraphStore: PutNode docId %d without bindSlot", docId)
	}
	for uint64(len(g.levels)) <= id {
		g.levels = append(g.levels, 0)
		g.neighbors = append(g.neighbors, nil)
		g.nodeSlot = append(g.nodeSlot, -1)
		g.nodeDoc = append(g.nodeDoc, 0)
	}
	g.levels[id] = level
	g.neighbors[id] = make(map[int][]uint64)
	g.nodeSlot[id] = slot
	g.nodeDoc[id] = docId
	g.docToNode[docId] = id
	return nil
}

func (g *segGraphStore) DeleteNode(id uint64) error {
	if id >= uint64(len(g.nodeSlot)) || g.nodeSlot[id] < 0 {
		return nil
	}
	doc := g.nodeDoc[id]
	if g.docToNode[doc] == id {
		delete(g.docToNode, doc)
	}
	g.nodeSlot[id] = -1
	g.neighbors[id] = nil
	return nil
}

func (g *segGraphStore) GetNeighbors(id uint64, layer int) ([]uint64, error) {
	if id >= uint64(len(g.neighbors)) || g.neighbors[id] == nil {
		return nil, nil
	}
	nb := g.neighbors[id][layer]
	cp := make([]uint64, len(nb))
	copy(cp, nb)
	return cp, nil
}

func (g *segGraphStore) SetNeighbors(id uint64, layer int, neighbors []uint64) error {
	if id >= uint64(len(g.neighbors)) || g.neighbors[id] == nil {
		return fmt.Errorf("segGraphStore: SetNeighbors on unknown node %d", id)
	}
	cp := make([]uint64, len(neighbors))
	copy(cp, neighbors)
	g.neighbors[id][layer] = cp
	return nil
}

func (g *segGraphStore) GetEntryPoint() (uint64, int, error) {
	if !g.hasEntry {
		return 0, 0, errNoEntryPoint
	}
	return g.entryID, g.maxLayer, nil
}

func (g *segGraphStore) SetEntryPoint(id uint64, maxLayer int) error {
	g.entryID = id
	g.maxLayer = maxLayer
	g.hasEntry = true
	return nil
}

func (g *segGraphStore) ClearEntryPoint() error {
	g.hasEntry = false
	g.entryID = 0
	g.maxLayer = 0
	return nil
}

func (g *segGraphStore) HighestLiveNodeExcluding(exclude uint64) (uint64, int, bool, error) {
	bestID := uint64(0)
	bestLevel := -1
	found := false
	for id := uint64(0); id < uint64(len(g.nodeSlot)); id++ {
		if id == exclude || g.nodeSlot[id] < 0 {
			continue
		}
		lvl := g.levels[id]
		if !found || lvl > bestLevel || (lvl == bestLevel && id < bestID) {
			bestID, bestLevel, found = id, lvl, true
		}
	}
	if !found {
		return 0, 0, false, nil
	}
	return bestID, bestLevel, true, nil
}

func (g *segGraphStore) GetNodeLevel(id uint64) (int, error) {
	if id >= uint64(len(g.levels)) || g.nodeSlot[id] < 0 {
		return 0, fmt.Errorf("segGraphStore: node %d not found", id)
	}
	return g.levels[id], nil
}

func (g *segGraphStore) GetNodeId(docId int64) (uint64, bool, error) {
	id, ok := g.docToNode[docId]
	return id, ok, nil
}

func (g *segGraphStore) GetDocId(id uint64) (int64, bool, error) {
	if id >= uint64(len(g.nodeDoc)) || g.nodeSlot[id] < 0 {
		return 0, false, nil
	}
	return g.nodeDoc[id], true, nil
}

func (g *segGraphStore) NextNodeId() (uint64, error) {
	id := g.nextID
	g.nextID++
	return id, nil
}

func (g *segGraphStore) txnBegin() error            { return nil }
func (g *segGraphStore) txnCommit() error           { return nil }
func (g *segGraphStore) txnAbort(cause error) error { return cause }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./vectorstore/ -run '^TestSegGraphStore_' -count=1 -v`
Expected: PASS (recall ≥ 0.8, no tombstoned/bogus docId).

- [ ] **Step 5: Verify tree green + gofmt; commit**

Run: `go build ./... && go test ./vectorstore/ ./vectorindex/ -count=1 && gofmt -l vectorstore/`
Expected: green, no gofmt diffs.

```bash
git add core/vectorstore/graphstore.go core/vectorstore/graphstore_test.go
git commit -m "feat(vectorstore): slim seg graph store — vectors owned by sealed segment (nodeId==slot)"
```

---

## Task 6: Persist + reopen the per-segment graph (graph.dat)

A built graph (in-memory `segGraphStore`) must be persisted once (fsync) and reopened read-only so search after a restart uses the graph, not a rebuild. Define `graph.dat` format, `writeGraphFile`, and `openGraphFile` returning a read-only `graphNodeStore` that delegates vectors to the sealed segment.

**Files:**
- Create: `core/vectorstore/graphfile_format.go`, `core/vectorstore/graphfile.go`
- Test: `core/vectorstore/graphfile_test.go`

- [ ] **Step 1: Write the failing test**

Create `core/vectorstore/graphfile_test.go`:

```go
package vectorstore

import (
	"math/rand"
	"path/filepath"
	"testing"
)

func TestGraphFile_PersistReopen_SameResults(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	dim := 12
	n := 200
	rows := make([]struct {
		doc int64
		v   []float32
		pl  []byte
	}, 0, n)
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		rows = append(rows, struct {
			doc int64
			v   []float32
			pl  []byte
		}{int64(i), v, nil})
	}
	head := buildHeadSeg(Cosine, rows)
	segDir := filepath.Join(t.TempDir(), "seg-1-0")
	requireNoError(t, writeSealedSegment(segDir, head))
	ss, err := openSealedSegment(segDir, Cosine)
	requireNoError(t, err)
	defer ss.close()

	// Build in memory.
	gs := newSegGraphStore(ss)
	idx := newHNSWIndex(gs, withGraphRand(rand.New(rand.NewSource(12))))
	b := idx.newBatch()
	ss.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		gs.bindSlot(docID, slot)
		b.put(docID, stored)
	})
	requireNoError(t, b.commit())

	q := make([]float32, dim)
	for d := range q {
		q[d] = rng.Float32()
	}
	wantRes, err := idx.search(q, 10)
	requireNoError(t, err)

	// Persist + reopen.
	requireNoError(t, writeGraphFile(segDir, gs))
	rgs, err := openGraphFile(segDir, ss)
	requireNoError(t, err)
	ridx := newHNSWIndex(rgs)
	gotRes, err := ridx.search(q, 10)
	requireNoError(t, err)

	if len(gotRes) != len(wantRes) {
		t.Fatalf("reopened result count = %d, want %d", len(gotRes), len(wantRes))
	}
	for i := range gotRes {
		if gotRes[i].DocID != wantRes[i].DocID {
			t.Fatalf("result[%d] docId = %d, want %d (graph persistence diverged)", i, gotRes[i].DocID, wantRes[i].DocID)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./vectorstore/ -run '^TestGraphFile_' -count=1`
Expected: FAIL — `undefined: writeGraphFile`, `undefined: openGraphFile`.

- [ ] **Step 3: Write `graphfile_format.go`**

Create `core/vectorstore/graphfile_format.go`:

```go
package vectorstore

import "unsafe"

var magicGraph = [4]byte{'V', 'S', 'G', 'R'}

// graphHeader is the on-disk header for graph.dat (32 bytes). After segPageSize
// bytes, the node table follows: NodeCount records of
//   level(int32) | slot(int32) | docId(int64) | nLayers(int32) | [ per layer:
//   nNeighbors(int32) | neighbors(nNeighbors * uint64) ]
// records are variable-length; a parallel offset table is NOT stored — the file
// is parsed sequentially at open (one linear pass; graphs are built once).
type graphHeader struct {
	Magic      [4]byte
	MaxLayers  uint32
	NodeCount  uint64
	HasEntry   uint32
	EntryLevel uint32
	EntryID    uint64
}

var _ [32]byte = [unsafe.Sizeof(graphHeader{})]byte{}
```

- [ ] **Step 4: Write `graphfile.go`**

Create `core/vectorstore/graphfile.go`:

```go
package vectorstore

import (
	"encoding/binary"
	"fmt"
	"os"
)

// writeGraphFile serializes a built segGraphStore to segDir/graph.dat and fsyncs
// it, then fsyncs segDir. The graph is immutable after this (built once).
func writeGraphFile(segDir string, g *segGraphStore) error {
	f, err := fsCreate(segFilePath(segDir, "graph.dat"))
	if err != nil {
		return err
	}
	defer f.Close()

	hdr := make([]byte, segPageSize)
	copy(hdr[0:4], magicGraph[:])
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(defaultMaxLayers))
	binary.LittleEndian.PutUint64(hdr[8:16], uint64(len(g.nodeSlot)))
	if g.hasEntry {
		binary.LittleEndian.PutUint32(hdr[16:20], 1)
		binary.LittleEndian.PutUint32(hdr[20:24], uint32(g.maxLayer))
		binary.LittleEndian.PutUint64(hdr[24:32], g.entryID)
	}
	if _, err := f.Write(hdr); err != nil {
		return err
	}

	for id := 0; id < len(g.nodeSlot); id++ {
		var rec []byte
		rec = appendI32(rec, int32(g.levels[id]))
		rec = appendI32(rec, int32(g.nodeSlot[id]))
		rec = appendU64(rec, uint64(g.nodeDoc[id]))
		layers := g.neighbors[id]
		nLayers := 0
		if layers != nil {
			nLayers = g.levels[id] + 1
		}
		rec = appendI32(rec, int32(nLayers))
		for layer := 0; layer < nLayers; layer++ {
			nb := layers[layer]
			rec = appendI32(rec, int32(len(nb)))
			for _, x := range nb {
				rec = appendU64(rec, x)
			}
		}
		if _, err := f.Write(rec); err != nil {
			return err
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return fsyncDir(segDir)
}

// openGraphFile loads segDir/graph.dat into a fresh segGraphStore bound to seg,
// reconstructing topology + nodeId↔slot/docId. Vectors are still resolved from
// seg (graph stores no vectors). The returned store is read-only for search.
func openGraphFile(segDir string, seg *sealedSegment) (*segGraphStore, error) {
	data, err := readWholeFile(segFilePath(segDir, "graph.dat"))
	if err != nil {
		return nil, err
	}
	if string(data[0:4]) != string(magicGraph[:]) {
		return nil, fmt.Errorf("graphfile: bad magic in %s", segDir)
	}
	nodeCount := int(binary.LittleEndian.Uint64(data[8:16]))
	g := newSegGraphStore(seg)
	g.levels = make([]int, nodeCount)
	g.neighbors = make([]map[int][]uint64, nodeCount)
	g.nodeSlot = make([]int, nodeCount)
	g.nodeDoc = make([]int64, nodeCount)
	g.nextID = uint64(nodeCount)
	if binary.LittleEndian.Uint32(data[16:20]) == 1 {
		g.hasEntry = true
		g.maxLayer = int(binary.LittleEndian.Uint32(data[20:24]))
		g.entryID = binary.LittleEndian.Uint64(data[24:32])
	}

	off := segPageSize
	for id := 0; id < nodeCount; id++ {
		level := int(int32(binary.LittleEndian.Uint32(data[off:])))
		off += 4
		slot := int(int32(binary.LittleEndian.Uint32(data[off:])))
		off += 4
		docId := int64(binary.LittleEndian.Uint64(data[off:]))
		off += 8
		nLayers := int(int32(binary.LittleEndian.Uint32(data[off:])))
		off += 4
		g.levels[id] = level
		g.nodeSlot[id] = slot
		g.nodeDoc[id] = docId
		if slot >= 0 {
			g.docToNode[docId] = uint64(id)
		}
		if nLayers > 0 {
			m := make(map[int][]uint64, nLayers)
			for layer := 0; layer < nLayers; layer++ {
				cnt := int(int32(binary.LittleEndian.Uint32(data[off:])))
				off += 4
				nb := make([]uint64, cnt)
				for j := 0; j < cnt; j++ {
					nb[j] = binary.LittleEndian.Uint64(data[off:])
					off += 8
				}
				m[layer] = nb
			}
			g.neighbors[id] = m
		}
	}
	return g, nil
}

func appendI32(b []byte, v int32) []byte {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], uint32(v))
	return append(b, tmp[:]...)
}

var _ = os.O_RDONLY
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./vectorstore/ -run '^TestGraphFile_' -count=1 -v`
Expected: PASS (reopened search returns identical docIds in order).

- [ ] **Step 6: Verify tree green + gofmt; commit**

Run: `go build ./... && go test ./vectorstore/ ./vectorindex/ -count=1 && gofmt -l vectorstore/`
Expected: green, no gofmt diffs.

```bash
git add core/vectorstore/graphfile_format.go core/vectorstore/graphfile.go core/vectorstore/graphfile_test.go
git commit -m "feat(vectorstore): persist + reopen per-segment HNSW graph (graph.dat)"
```

---

## Task 7: `buildSegmentGraph` — one-shot builder from a sealed segment to graph.dat

A single function that takes a sealed segment + graph params, builds the HNSW over its live slots, persists `graph.dat`, and returns the open read-only graph store. This is the unit the background builder (Task 11) will call. It is deterministic (seeded RNG) for the test.

**Files:**
- Create: `core/vectorstore/builder.go`
- Test: `core/vectorstore/builder_test.go`

- [ ] **Step 1: Write the failing test**

Create `core/vectorstore/builder_test.go`:

```go
package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildSegmentGraph_ProducesSearchableGraphFile(t *testing.T) {
	rng := rand.New(rand.NewSource(21))
	dim := 16
	n := 200
	rows := make([]struct {
		doc int64
		v   []float32
		pl  []byte
	}, 0, n)
	vecs := make(map[int64][]float32, n)
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		rows = append(rows, struct {
			doc int64
			v   []float32
			pl  []byte
		}{int64(i), v, nil})
		vecs[int64(i)] = v
	}
	head := buildHeadSeg(Cosine, rows)
	segDir := filepath.Join(t.TempDir(), "seg-3-0")
	requireNoError(t, writeSealedSegment(segDir, head))
	ss, err := openSealedSegment(segDir, Cosine)
	requireNoError(t, err)
	defer ss.close()

	cfg := graphConfig{M: 16, EfConstruction: 100, EfSearch: 64, Seed: 42}
	gs, err := buildSegmentGraph(segDir, ss, cfg)
	requireNoError(t, err)

	if _, err := os.Stat(filepath.Join(segDir, "graph.dat")); err != nil {
		t.Fatalf("graph.dat not written: %v", err)
	}
	idx := newHNSWIndex(gs, withGraphEfSearch(64))
	q := make([]float32, dim)
	for d := range q {
		q[d] = rng.Float32()
	}
	got, err := idx.search(q, 10)
	requireNoError(t, err)
	want := bruteForceKNN(Cosine, q, vecs, 10)
	if r := recallAtK(got, want); r < 0.8 {
		t.Fatalf("built-graph recall@10 = %.2f, want >= 0.8", r)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./vectorstore/ -run '^TestBuildSegmentGraph_' -count=1`
Expected: FAIL — `undefined: graphConfig`, `undefined: buildSegmentGraph`.

- [ ] **Step 3: Write minimal implementation**

Create `core/vectorstore/builder.go`:

```go
package vectorstore

import "math/rand"

// graphConfig is the per-index HNSW config. In Phase 2 there is one index, so a
// single graphConfig per store. Defaults match the migrated graph.
type graphConfig struct {
	M, EfConstruction, EfSearch int
	Seed                        int64
}

func (c graphConfig) withDefaults() graphConfig {
	if c.M == 0 {
		c.M = defaultGraphM
	}
	if c.EfConstruction == 0 {
		c.EfConstruction = defaultGraphEfConstruction
	}
	if c.EfSearch == 0 {
		c.EfSearch = defaultGraphEfSearch
	}
	return c
}

// buildSegmentGraph builds an HNSW over the live slots of seg, persists it to
// segDir/graph.dat (fsync), and returns the reopened read-only graph store. This
// is the unit the background builder schedules per pending segment. It is a pure
// function of the (immutable) segment + cfg, so it is safe to run off the write
// path with no lock on the store.
func buildSegmentGraph(segDir string, seg *sealedSegment, cfg graphConfig) (*segGraphStore, error) {
	cfg = cfg.withDefaults()
	gs := newSegGraphStore(seg)
	idx := newHNSWIndex(gs,
		withGraphM(cfg.M),
		withGraphEfConstruction(cfg.EfConstruction),
		withGraphEfSearch(cfg.EfSearch),
		withGraphRand(rand.New(rand.NewSource(cfg.Seed))),
	)
	b := idx.newBatch()
	seg.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		gs.bindSlot(docID, slot)
		b.put(docID, stored)
	})
	if err := b.commit(); err != nil {
		return nil, err
	}
	if err := writeGraphFile(segDir, gs); err != nil {
		return nil, err
	}
	// Reopen from disk so the returned store is exactly what recovery would load
	// (no reliance on the in-memory build state lingering).
	return openGraphFile(segDir, seg)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./vectorstore/ -run '^TestBuildSegmentGraph_' -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Verify tree green + gofmt; commit**

Run: `go build ./... && go test ./vectorstore/ ./vectorindex/ -count=1 && gofmt -l vectorstore/`
Expected: green, no gofmt diffs.

```bash
git add core/vectorstore/builder.go core/vectorstore/builder_test.go
git commit -m "feat(vectorstore): buildSegmentGraph one-shot HNSW build → fsync'd graph.dat"
```

---

## Task 8: Store holds a sealed-segment set + global docId→segId; Delete routes to the owning segment

Extend `Store` with a slice of live sealed segments and a global `docToSeg map[int64]segID`, where `headSegID` is a reserved value. Put still writes only to the head (and maps `docId→headSegID`). Delete now resolves `docId→segId`; if it's a sealed segment, it tombstones that segment's persisted bitmap (durable). This wires the two-level id across multiple segments WITHOUT seal yet (seal is Task 9); we install the plumbing and a test-only injection of a sealed segment.

**Files:**
- Modify: `core/vectorstore/store.go`
- Test: `core/vectorstore/store_segset_test.go`

- [ ] **Step 1: Write the failing test**

Create `core/vectorstore/store_segset_test.go`:

```go
package vectorstore

import (
	"path/filepath"
	"testing"
)

func TestStore_DeleteRoutesToSealedSegment(t *testing.T) {
	s := openTestStore(t, DotProduct)
	// Put two docs into the head.
	requireNoError(t, s.Put("a", []float32{1, 0, 0}, nil))
	requireNoError(t, s.Put("b", []float32{0, 1, 0}, nil))

	// Freeze the head into a sealed segment on disk and attach it, leaving the
	// head emptied — using the internal helper the seal pipeline will reuse.
	segDir := filepath.Join(s.dir, "seg-1-0")
	requireNoError(t, writeSealedSegment(segDir, s.seg))
	ss, err := openSealedSegment(segDir, DotProduct)
	requireNoError(t, err)
	s.attachSealedForTest(ss, 1)

	// "a" now lives in the sealed segment; Delete must tombstone it there.
	requireNoError(t, s.Delete("a"))
	if _, _, _, live := ss.read(0); live {
		t.Fatal("Delete(a) did not tombstone the sealed segment slot")
	}
	// "b" is still live in the sealed segment.
	if _, _, _, live := ss.read(1); !live {
		t.Fatal("Delete(a) wrongly affected b")
	}
	// Get(a) must now report not-found (sealed tombstone respected).
	_, _, found, err := s.Get("a")
	requireNoError(t, err)
	if found {
		t.Fatal("Get(a) should be not-found after sealed-segment delete")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./vectorstore/ -run '^TestStore_DeleteRoutesToSealedSegment$' -count=1`
Expected: FAIL — `undefined: s.attachSealedForTest`, `undefined: s.dir` is fine (exists), but `docToSeg` routing absent → also a logic failure.

- [ ] **Step 3: Add the segment-set fields + routing to `store.go`**

In `core/vectorstore/store.go`, add the reserved head id and new fields to the `Store` struct. Replace the struct definition:

```go
// headSegID is the reserved segId for the in-memory head in the global
// docId→segId map. Sealed segments use ids >= 1 (the manifest version space).
const headSegID = segID(0)

// Store is the segmented records layer (Phase 2): one in-memory brute head plus
// an ordered set of immutable on-disk sealed segments, each with a tombstone
// bitmap and (when indexed) a per-segment HNSW. Writes go to the head; Delete is
// routed to the owning segment via the global docId→segId map. A manifest +
// head WAL provide crash recovery.
type Store struct {
	mu      sync.RWMutex
	metric  Metric
	dir     string
	alloc   *idtable.Allocator
	seg     *segment // the head
	wal     *WAL
	idToDoc map[string]int64

	sealed   []*sealedSegment   // live sealed segments, by attach order
	sealedID []segID            // parallel: sealed[i] has segId sealedID[i]
	docToSeg map[int64]segID    // global docId → owning segId (headSegID for head)
	graphs   map[segID]*segGraphStore // segId → built graph (nil until indexed)
	gcfg     graphConfig        // the single index's HNSW config
	nextSeg  segID              // next sealed segId to assign
}
```

In `Open`, initialize the new maps. Replace the `s := &Store{...}` literal:

```go
	s := &Store{
		metric:   opts.Metric,
		dir:      opts.Dir,
		alloc:    alloc,
		seg:      newSegment(opts.Metric),
		wal:      w,
		idToDoc:  make(map[string]int64),
		docToSeg: make(map[int64]segID),
		graphs:   make(map[segID]*segGraphStore),
		gcfg:     graphConfig{}.withDefaults(),
		nextSeg:  1,
	}
```

In `Put`, after `s.idToDoc[id] = docID`, record the head ownership. Find the line `s.idToDoc[id] = docID` in `Put` and add below it:

```go
	s.docToSeg[docID] = headSegID
```

And in `replay`'s `recPut` branch, after `s.idToDoc[r.ID] = r.DocID`, add `s.docToSeg[r.DocID] = headSegID` (head replay re-establishes head ownership). Edit the `recPut` case:

```go
		case recPut:
			r := decodePut(payload)
			if _, err := s.docIDForAlloc(r.ID); err != nil {
				return err
			}
			s.idToDoc[r.ID] = r.DocID
			s.docToSeg[r.DocID] = headSegID
			s.applyPut(r)
```

Add a `sealedByID` helper and `attachSealedForTest` and rewrite `Delete` to route. Add at the end of `store.go`:

```go
// sealedByID returns the live sealed segment with segId, or nil.
func (s *Store) sealedByID(id segID) *sealedSegment {
	for i, sid := range s.sealedID {
		if sid == id {
			return s.sealed[i]
		}
	}
	return nil
}

// attachSealedForTest installs a sealed segment under segId and empties the head,
// mirroring what the seal pipeline does, so tests can exercise multi-segment
// routing before the full Seal path exists. Live head docs are remapped to the
// new segment. Test-only (no manifest write).
func (s *Store) attachSealedForTest(ss *sealedSegment, id segID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for slot := 0; slot < ss.count(); slot++ {
		if !ss.tombGet(slot) {
			s.docToSeg[ss.slotDoc(slot)] = id
		}
	}
	s.sealed = append(s.sealed, ss)
	s.sealedID = append(s.sealedID, id)
	s.seg = newSegment(s.metric)
	if id >= s.nextSeg {
		s.nextSeg = id + 1
	}
}

// slotInSealed returns the live slot of docID within sealed segment id.
func (ss *sealedSegment) slotOfDoc(docID int64) (int, bool) {
	for slot := 0; slot < ss.n; slot++ {
		if ss.slotDocs[slot] == docID && !ss.tombGet(slot) {
			return slot, false || true
		}
	}
	return 0, false
}
```

Now replace the body of `Delete` to route through `docToSeg`:

```go
// Delete tombstones id's current slot in its owning segment (head or sealed).
// Deleting an unknown or already-deleted id is a no-op. The id↔docId mapping is
// left in place; a later Put reuses the same docId (in the head).
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	docID, ok := s.idToDoc[id]
	if !ok {
		return nil
	}
	segId, ok := s.docToSeg[docID]
	if !ok {
		return nil
	}
	if segId == headSegID {
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
		delete(s.docToSeg, docID)
		return nil
	}
	// Sealed segment: tombstone is persisted in the segment's mmap'd bitmap.
	ss := s.sealedByID(segId)
	if ss == nil {
		return nil
	}
	slot, found := ss.slotOfDoc(docID)
	if !found {
		return nil
	}
	if err := ss.tombstoneSlot(slot); err != nil {
		return err
	}
	delete(s.docToSeg, docID)
	return nil
}
```

Also update `Get` to consult the owning segment. Replace the body of `Get`:

```go
// Get returns the original (restored) vector and payload for id from its owning
// segment (head or sealed). Unknown/deleted ids return found=false.
func (s *Store) Get(id string) (v []float32, payload []byte, found bool, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	docID, ok := s.idToDoc[id]
	if !ok {
		return nil, nil, false, nil
	}
	segId, ok := s.docToSeg[docID]
	if !ok {
		return nil, nil, false, nil
	}
	if segId == headSegID {
		slot, ok := s.seg.slotOfDoc(docID)
		if !ok {
			return nil, nil, false, nil
		}
		stored, norm, pl, live := s.seg.read(slot)
		if !live {
			return nil, nil, false, nil
		}
		out := append([]float32(nil), s.metric.restore(stored, norm)...)
		return out, append([]byte(nil), pl...), true, nil
	}
	ss := s.sealedByID(segId)
	if ss == nil {
		return nil, nil, false, nil
	}
	slot, found2 := ss.slotOfDoc(docID)
	if !found2 {
		return nil, nil, false, nil
	}
	stored, norm, pl, live := ss.read(slot)
	if !live {
		return nil, nil, false, nil
	}
	out := append([]float32(nil), s.metric.restore(stored, norm)...)
	return out, append([]byte(nil), pl...), true, nil
}
```

(Fix the awkward `false || true` in `slotOfDoc` — replace that return with `return slot, true`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./vectorstore/ -run '^TestStore_DeleteRoutesToSealedSegment$' -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Run the whole vectorstore suite (Phase-1 tests must still pass)**

Run: `go test ./vectorstore/ -count=1`
Expected: ok — existing `store_test.go` Put/Get/Delete/upsert tests still pass (Get/Delete now route, head path unchanged behaviorally).

- [ ] **Step 6: Verify tree green + gofmt; commit**

Run: `go build ./... && go test ./vectorstore/ ./vectorindex/ -count=1 && gofmt -l vectorstore/`
Expected: green, no gofmt diffs.

```bash
git add core/vectorstore/store.go core/vectorstore/store_segset_test.go
git commit -m "feat(vectorstore): segment set + global docId→segId; route Get/Delete to owning segment"
```

---

## Task 9: N-way merge Search (head brute + pending sealed brute + indexed sealed graph, each tombstone-filtered)

Rewrite `Search` to merge legs into one shared `topK`: the head (brute), each pending sealed segment (brute over its live slots), and each indexed sealed segment (its HNSW, then post-filter every hit against that segment's tombstone bitmap — the immutable graph can return tombstoned nodes, the single most important correctness gotcha). All legs emit exact same-metric distances into one heap; no cross-leg dedup is needed because a docId is live in exactly one segment.

**Files:**
- Modify: `core/vectorstore/store.go`
- Test: `core/vectorstore/store_search_test.go`

- [ ] **Step 1: Write the failing test**

Create `core/vectorstore/store_search_test.go`:

```go
package vectorstore

import (
	"math/rand"
	"path/filepath"
	"sort"
	"testing"
)

// docIDs extracts the docId list from a result slice.
func docIDs(rs []SearchResult) []int64 {
	out := make([]int64, len(rs))
	for i, r := range rs {
		out[i] = r.DocID
	}
	return out
}

func TestStore_Search_MergesHeadAndIndexedSealed(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(31))
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

	// Batch 1 → seal → build graph (indexed sealed segment).
	for i := 0; i < 120; i++ {
		put("s1-"+itoa(i), randVec())
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	// Batch 2 stays in the head (brute leg).
	for i := 0; i < 40; i++ {
		put("h-"+itoa(i), randVec())
	}

	q := randVec()
	got, err := s.Search(q, 10)
	requireNoError(t, err)
	want := bruteForceKNN(Cosine, q, vecs, 10)
	if r := recallAtK(got, want); r < 0.8 {
		t.Fatalf("merged recall@10 = %.2f, want >= 0.8", r)
	}
	// Results ascending by distance.
	if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].Distance <= got[j].Distance }) {
		t.Fatalf("results not ascending by distance: %v", got)
	}
}

func TestStore_Search_IndexedSegmentTombstoneFiltered(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(32))
	dim := 8
	put := func(id string, v []float32) { requireNoError(t, s.Put(id, v, nil)) }
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	for i := 0; i < 80; i++ {
		put("x-"+itoa(i), randVec())
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	// Delete one doc that now lives in the indexed sealed segment.
	requireNoError(t, s.Delete("x-0"))
	deletedDoc := s.idToDoc["x-0"]

	// Search many times; the deleted doc must never appear (graph would return it
	// without the tombstone post-filter).
	for iter := 0; iter < 20; iter++ {
		q := randVec()
		got, err := s.Search(q, 20)
		requireNoError(t, err)
		for _, r := range got {
			if r.DocID == deletedDoc {
				t.Fatalf("tombstoned docId %d leaked through indexed graph leg", deletedDoc)
			}
		}
	}
}
```

Add a tiny local `itoa` helper at the bottom of this test file:

```go
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./vectorstore/ -run '^TestStore_Search_' -count=1`
Expected: FAIL — `undefined: s.Seal`, `undefined: s.WaitForIndex`. (We implement Seal/WaitForIndex in this task plus the merged Search.)

- [ ] **Step 3: Implement merged Search + a synchronous Seal + WaitForIndex**

In `core/vectorstore/store.go`, replace the entire `Search` method:

```go
// Search returns the k nearest live records to q under the store's metric,
// merging every leg into one shared top-k heap: the head (brute), each pending
// sealed segment (brute over its live slots), and each indexed sealed segment
// (its HNSW, post-filtered by that segment's tombstone bitmap — the immutable
// graph can return tombstoned nodes). Results are docId-space, ascending by
// distance. An empty store returns (nil, nil).
func (s *Store) Search(q []float32, k int) ([]SearchResult, error) {
	if k <= 0 {
		return nil, errors.New("vectorstore: k must be positive")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := validateVector(q, s.searchDimLocked(), s.metric); err != nil {
		return nil, err
	}
	pq, _ := s.metric.prepare(q)
	tk := newTopK(k)

	// Head leg (brute).
	s.seg.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		tk.offer(SearchResult{DocID: docID, Distance: s.metric.distance(stored, pq)})
	})

	// Sealed legs.
	for i, ss := range s.sealed {
		g := s.graphs[s.sealedID[i]]
		if g == nil {
			// Pending: brute over the segment's live slots.
			ss.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
				tk.offer(SearchResult{DocID: docID, Distance: s.metric.distance(stored, pq)})
			})
			continue
		}
		// Indexed: HNSW search, then drop any hit whose slot is tombstoned.
		idx := newHNSWIndex(g, withGraphEfSearch(s.gcfg.EfSearch))
		hits, err := idx.search(q, k)
		if err != nil {
			return nil, err
		}
		for _, h := range hits {
			slot, found := ss.slotOfDoc(h.DocID)
			if !found {
				continue // tombstoned after seal → exclude
			}
			_ = slot
			tk.offer(h)
		}
	}

	out := tk.sorted()
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// searchDimLocked returns the dimension to validate the query against: the head's
// learned dim if non-zero, else the first sealed segment's dim, else 0 (empty).
// Caller holds s.mu (R or W).
func (s *Store) searchDimLocked() int {
	if s.seg.dim != 0 {
		return s.seg.dim
	}
	if len(s.sealed) > 0 {
		return s.sealed[0].dim
	}
	return 0
}
```

Now add `Seal` and `WaitForIndex` (synchronous build here; Task 11 makes the build background). Append to `store.go`:

```go
// Seal freezes the current head into a new immutable sealed records-segment on
// disk, atomically updates the manifest (head→new sealed seg, state pending,
// fresh empty head), truncates the head WAL, then (Phase 2 step) builds the
// segment's HNSW. The records-segment is durable before the manifest swap and
// the WAL truncate, so a crash never loses a durably-acked write. An empty head
// is a no-op.
func (s *Store) Seal() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sealLocked()
}

func (s *Store) sealLocked() error {
	if len(s.seg.slotDoc) == 0 {
		return nil // nothing to seal
	}
	id := s.nextSeg
	segDir := filepath.Join(s.dir, segDirName(id, 0))

	// (1) Dump head → sealed records-segment files (fsync, fast, durable).
	if err := writeSealedSegment(segDir, s.seg); err != nil {
		return err
	}
	ss, err := openSealedSegment(segDir, s.metric)
	if err != nil {
		return err
	}

	// (2) Atomic manifest swap: head→new sealed seg (pending), fresh head.
	s.sealed = append(s.sealed, ss)
	s.sealedID = append(s.sealedID, id)
	s.nextSeg++
	for slot := 0; slot < ss.count(); slot++ {
		if !ss.tombGet(slot) {
			s.docToSeg[ss.slotDoc(slot)] = id
		}
	}
	s.seg = newSegment(s.metric)
	if err := s.writeManifestLocked(); err != nil {
		return err
	}

	// (3) Truncate the old head WAL — the writes it carried are now in the
	// durable sealed segment + manifest.
	if err := s.wal.Reset(); err != nil {
		return err
	}

	// (4) Build the graph (synchronous for now; backgrounded in Task 11).
	g, err := buildSegmentGraph(segDir, ss, s.gcfg)
	if err != nil {
		return err
	}
	s.graphs[id] = g
	return s.markIndexedLocked(id)
}

// WaitForIndex blocks until every sealed segment is indexed. With the
// synchronous Seal of this task it is already true on return; Task 11 makes it
// wait on the background builder.
func (s *Store) WaitForIndex() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return nil
}

// writeManifestLocked rewrites the manifest from the current segment set. Caller
// holds s.mu.
func (s *Store) writeManifestLocked() error {
	m := &manifest{Head: headSegID}
	m.Version = s.manifestVersion
	for i, ss := range s.sealed {
		st := segPending
		if s.graphs[s.sealedID[i]] != nil {
			st = segIndexed
		}
		m.Segments = append(m.Segments, segmentEntry{
			SegID:     s.sealedID[i],
			Gen:       0,
			VecCount:  uint64(ss.count()),
			TombCount: uint64(ss.tombCount()),
			State:     st,
		})
	}
	s.manifestVersion++
	m.Version = s.manifestVersion
	return writeManifest(s.dir, m)
}

// markIndexedLocked flips a segment to indexed in the manifest. Caller holds s.mu.
func (s *Store) markIndexedLocked(id segID) error {
	return s.writeManifestLocked()
}

// segDirName derives the on-disk directory name for a sealed segment (§4.8: paths
// are derived, not stored).
func segDirName(id segID, gen uint32) string {
	return "seg-" + itoaSeg(int64(id)) + "-" + itoaSeg(int64(gen))
}

func itoaSeg(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	p := len(b)
	for v > 0 {
		p--
		b[p] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
```

Add a `tombCount()` to `sealedSegment` in `sealed.go`:

```go
// tombCount returns the number of tombstoned slots.
func (s *sealedSegment) tombCount() int {
	n := 0
	for slot := 0; slot < s.n; slot++ {
		if s.tombGet(slot) {
			n++
		}
	}
	return n
}
```

Add the `manifestVersion` field to the `Store` struct (in `store.go`, inside the struct from Task 8) — add the line:

```go
	manifestVersion uint64
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./vectorstore/ -run '^TestStore_Search_' -count=1 -v`
Expected: PASS (merged recall ≥ 0.8; tombstoned doc never leaks through the indexed leg).

- [ ] **Step 5: Run whole vectorstore + vectorindex suites**

Run: `go test ./vectorstore/ ./vectorindex/ -count=1`
Expected: ok (Phase-1 single-segment Search test still passes — head-only path is the head leg).

- [ ] **Step 6: Verify tree green + gofmt; commit**

Run: `go build ./... && go test ./vectorstore/ ./vectorindex/ -count=1 && gofmt -l vectorstore/`
Expected: green, no gofmt diffs.

```bash
git add core/vectorstore/store.go core/vectorstore/sealed.go core/vectorstore/store_search_test.go
git commit -m "feat(vectorstore): N-way merge Search + synchronous Seal/manifest/WAL-truncate"
```

---

## Task 10: Recovery — load manifest, mmap sealed segments, rebuild docId→segId, replay head WAL, reopen graphs

`Open` must recover the full segmented state: load the manifest, mmap each sealed segment, rebuild the global `docId→segId` by scanning each segment's `slotDoc` over live slots, reopen each indexed segment's `graph.dat`, then replay the head WAL (which may tombstone an old slot in a sealed segment — routed through `docToSeg`). Recovery order is load-then-replay so head puts that tombstone a sealed old slot resolve correctly.

**Files:**
- Modify: `core/vectorstore/store.go`
- Test: `core/vectorstore/recovery_test.go`

- [ ] **Step 1: Write the failing test**

Create `core/vectorstore/recovery_test.go`:

```go
package vectorstore

import (
	"math/rand"
	"testing"

	"github.com/codetrek/haystack/core/kv"
)

// reopenStore closes s and reopens a fresh Store over the SAME dir + KV (recovery).
func reopenStore(t *testing.T, s *Store, kvStore kv.Store) *Store {
	t.Helper()
	dir := s.dir
	requireNoError(t, s.Close())
	s2, err := Open(Options{Dir: dir, KV: kvStore, Metric: s.metric})
	requireNoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	return s2
}

func TestRecovery_SealedSegmentsAndHeadWAL(t *testing.T) {
	kvStore := newTestKV(t)
	s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
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
	// Sealed (indexed) batch.
	for i := 0; i < 100; i++ {
		put("s-"+itoa(i), randVec())
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	// Head batch (only in WAL).
	for i := 0; i < 30; i++ {
		put("h-"+itoa(i), randVec())
	}
	// Delete one sealed doc (persisted in tomb.dat) and one head doc (WAL).
	requireNoError(t, s.Delete("s-0"))
	delete(vecs, s.idToDoc["s-0"])
	requireNoError(t, s.Delete("h-0"))
	delete(vecs, s.idToDoc["h-0"])

	q := randVec()

	s2 := reopenStore(t, s, kvStore)

	// All recovered: search recall holds, deleted docs stay gone.
	got, err := s2.Search(q, 10)
	requireNoError(t, err)
	want := bruteForceKNN(Cosine, q, vecs, 10)
	if r := recallAtK(got, want); r < 0.8 {
		t.Fatalf("post-recovery recall@10 = %.2f, want >= 0.8", r)
	}
	// Sealed delete survived (persisted tombstone).
	if _, _, found, _ := s2.Get("s-0"); found {
		t.Fatal("deleted sealed doc s-0 resurrected after recovery")
	}
	// Head delete survived (WAL replay).
	if _, _, found, _ := s2.Get("h-0"); found {
		t.Fatal("deleted head doc h-0 resurrected after recovery")
	}
	// A surviving head doc is readable.
	if _, _, found, _ := s2.Get("h-5"); !found {
		t.Fatal("head doc h-5 lost after recovery")
	}
	// A surviving sealed doc is readable.
	if _, _, found, _ := s2.Get("s-5"); !found {
		t.Fatal("sealed doc s-5 lost after recovery")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./vectorstore/ -run '^TestRecovery_SealedSegmentsAndHeadWAL$' -count=1`
Expected: FAIL — after reopen the sealed segments are not loaded (recovery not wired), so `Get("s-5")` is not-found / recall is 0.

- [ ] **Step 3: Wire recovery into `Open`**

In `core/vectorstore/store.go`, in `Open`, replace the `if err := s.replay(); err != nil {` block with a call to a new `recover()` method that loads the manifest first. Edit `Open` so the recovery section reads:

```go
	if err := s.recover(); err != nil {
		w.Close()
		alloc.Close()
		return nil, err
	}
	return s, nil
}

// recover rebuilds the full segmented state: load the manifest, mmap sealed
// segments, rebuild the global docId→segId from each segment's slotDoc (live
// slots), reopen indexed graphs, then replay the head WAL (which may tombstone a
// sealed old slot — routed via docToSeg). A missing manifest means a fresh or
// Phase-1 store: just replay the WAL into the head.
func (s *Store) recover() error {
	m, err := readManifest(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return s.replay() // fresh store: head-only WAL replay
		}
		return err
	}
	s.manifestVersion = m.Version
	for _, e := range m.Segments {
		segDir := filepath.Join(s.dir, segDirName(e.SegID, e.Gen))
		ss, oerr := openSealedSegment(segDir, s.metric)
		if oerr != nil {
			return oerr
		}
		s.sealed = append(s.sealed, ss)
		s.sealedID = append(s.sealedID, e.SegID)
		if e.SegID >= s.nextSeg {
			s.nextSeg = e.SegID + 1
		}
		for slot := 0; slot < ss.count(); slot++ {
			if !ss.tombGet(slot) {
				s.docToSeg[ss.slotDoc(slot)] = e.SegID
			}
		}
		if e.State == segIndexed {
			g, gerr := openGraphFile(segDir, ss)
			if gerr != nil {
				return gerr
			}
			s.graphs[e.SegID] = g
		}
	}
	// Head WAL replay last, so a head put that tombstones a sealed old slot
	// resolves against the now-populated docToSeg.
	return s.replay()
}
```

Add `"os"` to the `store.go` import block (it currently imports `encoding/binary`, `errors`, `sync`, idtable, kv — add `"os"` and `"path/filepath"`).

Now make `replay`'s `recPut` route an `OldSlot` tombstone to the owning segment (a head put that REPLACES a doc currently living in a sealed segment must tombstone the sealed slot, not the head). Replace the `recPut` case in `replay`:

```go
		case recPut:
			r := decodePut(payload)
			if _, err := s.docIDForAlloc(r.ID); err != nil {
				return err
			}
			s.idToDoc[r.ID] = r.DocID
			// If this docId currently lives in a sealed segment, the replayed put
			// supersedes it: tombstone the sealed slot before re-homing to head.
			if prev, ok := s.docToSeg[r.DocID]; ok && prev != headSegID {
				if ss := s.sealedByID(prev); ss != nil {
					if slot, found := ss.slotOfDoc(r.DocID); found {
						_ = ss.tombstoneSlot(slot)
					}
				}
			}
			s.docToSeg[r.DocID] = headSegID
			s.applyPut(r)
```

And route `recDelete` through `docToSeg` (a delete record may target a sealed segment). Replace the `recDelete` case in `replay`:

```go
		case recDelete:
			d := decodeDelete(payload)
			if _, err := s.docIDForAlloc(d.ID); err != nil {
				return err
			}
			s.idToDoc[d.ID] = d.DocID
			if segId, ok := s.docToSeg[d.DocID]; ok {
				if segId == headSegID {
					s.seg.tombstone(int(d.Slot))
				} else if ss := s.sealedByID(segId); ss != nil {
					if slot, found := ss.slotOfDoc(d.DocID); found {
						_ = ss.tombstoneSlot(slot)
					}
				}
				delete(s.docToSeg, d.DocID)
			}
```

(Note: sealed deletes are already durable in `tomb.dat` at Delete time, so a `recDelete` for a sealed doc is normally absent from the head WAL — sealed deletes don't write to the WAL in Task 8's `Delete`. This branch is defensive for the head case; the sealed-delete durability is the persisted bitmap, exercised by the test's `Get("s-0")` check.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./vectorstore/ -run '^TestRecovery_SealedSegmentsAndHeadWAL$' -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Run whole vectorstore + vectorindex suites**

Run: `go test ./vectorstore/ ./vectorindex/ -count=1`
Expected: ok (Phase-1 `replay` path still exercised via the `os.IsNotExist` branch — no manifest in Phase-1-style stores).

- [ ] **Step 6: Verify tree green + gofmt; commit**

Run: `go build ./... && go test ./vectorstore/ ./vectorindex/ -count=1 && gofmt -l vectorstore/`
Expected: green, no gofmt diffs.

```bash
git add core/vectorstore/store.go core/vectorstore/recovery_test.go
git commit -m "feat(vectorstore): crash recovery — manifest + mmap sealed + reopen graphs + WAL replay"
```

---

## Task 11: Background builder + pending→indexed state flip + WaitForIndex

Make the graph build asynchronous so writes to the fresh head are not blocked by the (~tens of seconds) HNSW build. `Seal` publishes the segment as **pending** (durable records + manifest), starts a fresh head, returns; a background goroutine builds the graph, then flips manifest `pending→indexed` and installs the graph under a state lock distinct from the head write lock. `WaitForIndex` blocks until all pending builds finish. Search serves pending segments by brute meanwhile (already implemented in Task 9).

**Files:**
- Modify: `core/vectorstore/store.go`, `core/vectorstore/builder.go`
- Test: `core/vectorstore/builder_async_test.go`

- [ ] **Step 1: Write the failing test**

Create `core/vectorstore/builder_async_test.go`:

```go
package vectorstore

import (
	"math/rand"
	"testing"
)

func TestStore_Seal_BuildsInBackground_PendingThenIndexed(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(51))
	dim := 16
	put := func(id string, v []float32) { requireNoError(t, s.Put(id, v, nil)) }
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	for i := 0; i < 100; i++ {
		put("s-"+itoa(i), randVec())
	}
	requireNoError(t, s.Seal())

	// Right after Seal returns, the segment is pending (no graph yet) but the
	// store is immediately writable (fresh head) and searchable (brute leg).
	put("hot-after-seal", randVec()) // must not block / error
	got, err := s.Search(randVec(), 5)
	requireNoError(t, err)
	if len(got) == 0 {
		t.Fatal("search returned nothing while build pending (brute leg missing)")
	}

	// After WaitForIndex, the segment must be indexed (graph installed).
	requireNoError(t, s.WaitForIndex())
	if !s.isIndexedForTest(1) {
		t.Fatal("segment 1 not indexed after WaitForIndex")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./vectorstore/ -run '^TestStore_Seal_BuildsInBackground' -count=1`
Expected: FAIL — `undefined: s.isIndexedForTest`, and (depending on Task 9's synchronous Seal) the "pending right after Seal" assertion is not yet meaningful.

- [ ] **Step 3: Split Seal into publish-pending (sync, locked) + build (async); add a state lock + WaitGroup**

In `core/vectorstore/store.go`, add a build-state lock and a `sync.WaitGroup` to the `Store` struct:

```go
	buildMu sync.Mutex   // guards graphs map mutation + manifest rewrite from the builder
	builds  sync.WaitGroup
```

Replace `sealLocked` to publish pending and spawn the builder instead of building inline:

```go
func (s *Store) sealLocked() error {
	if len(s.seg.slotDoc) == 0 {
		return nil
	}
	id := s.nextSeg
	segDir := filepath.Join(s.dir, segDirName(id, 0))

	// (1) Dump head → durable sealed records-segment.
	if err := writeSealedSegment(segDir, s.seg); err != nil {
		return err
	}
	ss, err := openSealedSegment(segDir, s.metric)
	if err != nil {
		return err
	}

	// (2) Publish as PENDING + fresh head + atomic manifest swap.
	s.sealed = append(s.sealed, ss)
	s.sealedID = append(s.sealedID, id)
	s.nextSeg++
	for slot := 0; slot < ss.count(); slot++ {
		if !ss.tombGet(slot) {
			s.docToSeg[ss.slotDoc(slot)] = id
		}
	}
	s.seg = newSegment(s.metric)
	if err := s.writeManifestLocked(); err != nil {
		return err
	}

	// (3) Truncate the old head WAL.
	if err := s.wal.Reset(); err != nil {
		return err
	}

	// (4) Build the graph in the BACKGROUND (off the write lock). The sealed
	// segment is immutable (only its tombstone bitmap mutates, which the build
	// reads through eachLive at start), so the build needs no lock on the store.
	s.builds.Add(1)
	go s.buildAndPublish(id, segDir, ss)
	return nil
}

// buildAndPublish builds the HNSW for a pending sealed segment off the write
// path, then installs the graph and flips the manifest to indexed under the
// build lock. Errors are dropped here (the segment stays pending → still
// brute-searched, still correct); a production build would surface them via
// IndexLag. Recovery resumes any segment left pending.
func (s *Store) buildAndPublish(id segID, segDir string, ss *sealedSegment) {
	defer s.builds.Done()
	g, err := buildSegmentGraph(segDir, ss, s.gcfg)
	if err != nil {
		return // stays pending; brute leg keeps results correct
	}
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	s.mu.Lock()
	s.graphs[id] = g
	err = s.writeManifestLocked()
	s.mu.Unlock()
	_ = err
}
```

Replace `WaitForIndex`:

```go
// WaitForIndex blocks until every pending sealed-segment build has finished.
func (s *Store) WaitForIndex() error {
	s.builds.Wait()
	return nil
}
```

Add the test hook:

```go
// isIndexedForTest reports whether sealed segment id has its graph installed.
func (s *Store) isIndexedForTest(id segID) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.graphs[id] != nil
}
```

Make `Close` wait for in-flight builds so a closing store doesn't race a builder writing the manifest. Edit `Close` to wait first:

```go
func (s *Store) Close() error {
	s.builds.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ss := range s.sealed {
		ss.close()
	}
	werr := s.wal.Close()
	s.alloc.Close()
	return werr
}
```

In `recover`, resume any segment left `pending` (crash mid-build) by spawning the builder. After the loop that loads segments, before `return s.replay()`, add a resume pass. Replace the tail of `recover`:

```go
	// Resume builds for any segment left pending (crash mid-build) AFTER the WAL
	// replay rehomes head docs, so the build reads a consistent tombstone view.
	if err := s.replay(); err != nil {
		return err
	}
	for i, sid := range s.sealedID {
		if s.graphs[sid] == nil {
			segDir := filepath.Join(s.dir, segDirName(sid, 0))
			s.builds.Add(1)
			go s.buildAndPublish(sid, segDir, s.sealed[i])
		}
	}
	return nil
}
```

(Remove the old final `return s.replay()` line so it is not called twice.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./vectorstore/ -run '^TestStore_Seal_BuildsInBackground' -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Run the whole suite WITH the race detector (concurrency correctness)**

Run: `go test ./vectorstore/ -race -count=1`
Expected: ok, no data-race reports. (The builder mutates `graphs` only under `s.mu`+`buildMu`; the head write lock is independent; sealed reads are immutable.)

- [ ] **Step 6: Verify tree green + gofmt; commit**

Run: `go build ./... && go test ./vectorstore/ ./vectorindex/ -count=1 && gofmt -l vectorstore/`
Expected: green, no gofmt diffs.

```bash
git add core/vectorstore/store.go core/vectorstore/builder.go core/vectorstore/builder_async_test.go
git commit -m "feat(vectorstore): background builder + pending→indexed flip + WaitForIndex"
```

---

## Task 12: Auto-seal when the head reaches maxSegSize

Trigger seal automatically when the head fills, so callers don't have to call `Seal()`. `maxSegSize` is configurable (default chosen conservatively; the adaptive `~10M/dim` is measure-don't-assert and out of scope to tune here — expose the knob and a sane fixed default). Put checks the head row count after applying and auto-seals.

**Files:**
- Modify: `core/vectorstore/store.go`
- Test: `core/vectorstore/autoseal_test.go`

- [ ] **Step 1: Write the failing test**

Create `core/vectorstore/autoseal_test.go`:

```go
package vectorstore

import (
	"math/rand"
	"testing"
)

func TestStore_AutoSealAtMaxSegSize(t *testing.T) {
	s := openTestStore(t, Cosine)
	s.maxSegSize = 50 // small, deterministic threshold for the test
	rng := rand.New(rand.NewSource(61))
	dim := 8
	put := func(id string) {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		requireNoError(t, s.Put(id, v, nil))
	}
	for i := 0; i < 120; i++ {
		put("d-" + itoa(i))
	}
	requireNoError(t, s.WaitForIndex())

	// 120 puts with maxSegSize 50 → 2 sealed segments (at 50 and 100), 20 in head.
	s.mu.RLock()
	nSealed := len(s.sealed)
	headLive := 0
	s.seg.eachLive(func(int, int64, []float32, float32) { headLive++ })
	s.mu.RUnlock()
	if nSealed != 2 {
		t.Fatalf("sealed segments = %d, want 2", nSealed)
	}
	if headLive != 20 {
		t.Fatalf("head live = %d, want 20", headLive)
	}
	// Everything still searchable.
	q := make([]float32, dim)
	for d := range q {
		q[d] = rng.Float32()
	}
	got, err := s.Search(q, 10)
	requireNoError(t, err)
	if len(got) != 10 {
		t.Fatalf("search returned %d, want 10", len(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./vectorstore/ -run '^TestStore_AutoSealAtMaxSegSize$' -count=1`
Expected: FAIL — `undefined: s.maxSegSize` and no auto-seal (everything stays in head → `nSealed != 2`).

- [ ] **Step 3: Add `maxSegSize` + auto-seal in Put**

In `core/vectorstore/store.go`, add a default constant and a field. Add near `headSegID`:

```go
// defaultMaxSegSize is the head row-count seal trigger. The architecture's
// adaptive ~10M/dim target (§4.9) is measure-don't-assert; this fixed default is
// a safe placeholder the operator can override via Options/field. Tunable later.
const defaultMaxSegSize = 50000
```

Add to the `Store` struct:

```go
	maxSegSize int
```

In `Open`, set the default in the `&Store{...}` literal — add `maxSegSize: defaultMaxSegSize,`.

In `Put`, after `s.applyPut(rec)` and before `return nil`, add the auto-seal check:

```go
	if len(s.seg.slotDoc) >= s.maxSegSize {
		if err := s.sealLocked(); err != nil {
			return err
		}
	}
	return nil
```

(`Put` already holds `s.mu` and `sealLocked` requires the caller to hold it — correct. `sealLocked` spawns the background builder and returns immediately, so Put latency stays O(1).)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./vectorstore/ -run '^TestStore_AutoSealAtMaxSegSize$' -count=1 -v`
Expected: PASS (2 sealed, 20 in head, search returns 10).

- [ ] **Step 5: Run whole suite + race**

Run: `go test ./vectorstore/ -race -count=1 && go test ./vectorindex/ -count=1`
Expected: ok.

- [ ] **Step 6: Verify tree green + gofmt; commit**

Run: `go build ./... && go test ./vectorstore/ ./vectorindex/ -count=1 && gofmt -l vectorstore/`
Expected: green, no gofmt diffs.

```bash
git add core/vectorstore/store.go core/vectorstore/autoseal_test.go
git commit -m "feat(vectorstore): auto-seal head at maxSegSize (configurable, O(1) Put)"
```

---

## Task 13: Orphan-sweep recovery — delete seg dirs not referenced by the manifest

Crash-mid-seal leaves a half-written `seg-<id>-<gen>/` directory that the manifest does not reference (the manifest swap is the commit point). Recovery must sweep any `seg-*` directory on disk not in the manifest's segment set. This is the §4.8 "崩在换前/换后" two-sided consistency: orphans before the swap, old files after.

**Files:**
- Modify: `core/vectorstore/store.go`
- Test: `core/vectorstore/orphan_test.go`

- [ ] **Step 1: Write the failing test**

Create `core/vectorstore/orphan_test.go`:

```go
package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestRecovery_OrphanSegmentSwept(t *testing.T) {
	kvStore := newTestKV(t)
	s, err := Open(Options{Dir: t.TempDir(), KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	rng := rand.New(rand.NewSource(71))
	dim := 8
	for i := 0; i < 60; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		requireNoError(t, s.Put("d-"+itoa(i), v, nil))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	// Simulate a crash mid-seal: a half-written seg dir the manifest never
	// referenced. seg-1-0 is the real (manifest) segment; seg-99-0 is an orphan.
	orphan := filepath.Join(s.dir, "seg-99-0")
	requireNoError(t, os.MkdirAll(orphan, 0755))
	requireNoError(t, os.WriteFile(filepath.Join(orphan, "vectors.dat"), []byte("garbage"), 0644))

	s2 := reopenStore(t, s, kvStore)
	_ = s2

	// The orphan must be gone; the real segment must remain.
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("orphan seg-99-0 not swept on recovery")
	}
	if _, err := os.Stat(filepath.Join(s2.dir, "seg-1-0")); err != nil {
		t.Fatalf("manifest-referenced seg-1-0 wrongly removed: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./vectorstore/ -run '^TestRecovery_OrphanSegmentSwept$' -count=1`
Expected: FAIL — orphan `seg-99-0` still exists after recovery (no sweep).

- [ ] **Step 3: Add orphan-sweep to `recover`**

In `core/vectorstore/store.go`, after the manifest is loaded and segments are opened but BEFORE spawning resume builds, sweep. Add a `sweepOrphansLocked` method and call it inside `recover` right after the `for _, e := range m.Segments` loop:

```go
	if err := s.sweepOrphansLocked(m); err != nil {
		return err
	}
```

And add the method:

```go
// sweepOrphansLocked removes any seg-* directory on disk not referenced by the
// loaded manifest. A crash mid-seal leaves a half-written segment the manifest
// never committed to; the manifest swap is the commit point, so anything not in
// it is an orphan (§4.8). Caller holds s.mu.
func (s *Store) sweepOrphansLocked(m *manifest) error {
	referenced := make(map[string]bool, len(m.Segments))
	for _, e := range m.Segments {
		referenced[segDirName(e.SegID, e.Gen)] = true
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		name := ent.Name()
		if !ent.IsDir() || len(name) < 4 || name[:4] != "seg-" {
			continue
		}
		if referenced[name] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.dir, name)); err != nil {
			return err
		}
	}
	return nil
}
```

(`os.ReadDir` and `os.RemoveAll` need `"os"`, already added in Task 10.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./vectorstore/ -run '^TestRecovery_OrphanSegmentSwept$' -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Run the full recovery + whole suite**

Run: `go test ./vectorstore/ -run 'Recovery' -count=1 && go test ./vectorstore/ ./vectorindex/ -count=1`
Expected: ok — `TestRecovery_SealedSegmentsAndHeadWAL` still passes (its real segments are referenced, not swept).

- [ ] **Step 6: Verify tree green + gofmt; commit**

Run: `go build ./... && go test ./vectorstore/ ./vectorindex/ -count=1 && gofmt -l vectorstore/`
Expected: green, no gofmt diffs.

```bash
git add core/vectorstore/store.go core/vectorstore/orphan_test.go
git commit -m "feat(vectorstore): orphan-sweep recovery — drop seg dirs absent from manifest"
```

---

## Task 14: Update package doc + final coverage gate

Refresh `doc.go` to describe the Phase-2 segmented architecture, then run the full coverage gate the CI enforces and close any gaps the gate reports (add targeted tests for any uncovered new production branch — e.g. a manifest write-fault path or a seal on an empty head).

**Files:**
- Modify: `core/vectorstore/doc.go`
- Test: `core/vectorstore/phase2_coverage_test.go`

- [ ] **Step 1: Write the failing test (cover the no-op + error branches)**

Create `core/vectorstore/phase2_coverage_test.go`:

```go
package vectorstore

import (
	"os"
	"testing"
)

func TestSeal_EmptyHeadIsNoOp(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.Seal()) // empty head → no segment, no manifest
	if len(s.sealed) != 0 {
		t.Fatalf("empty-head Seal created %d segments, want 0", len(s.sealed))
	}
}

func TestManifest_WriteFaultRemovesTmp(t *testing.T) {
	dir := t.TempDir()
	withOpenFileFault(t, func(f *faultFile) { f.failSync = true })
	err := writeManifest(dir, sampleManifest())
	if err == nil {
		t.Fatal("writeManifest should fail when fsync is injected to fail")
	}
	if _, statErr := os.Stat(dir + "/manifest.tmp"); !os.IsNotExist(statErr) {
		t.Fatal("manifest.tmp must be removed on write fault")
	}
}

func TestSealedSegment_TombstoneOutOfRange(t *testing.T) {
	head := buildHeadSeg(DotProduct, []struct {
		doc int64
		v   []float32
		pl  []byte
	}{{1, []float32{1, 0}, nil}})
	dir := t.TempDir() + "/seg-1-0"
	requireNoError(t, writeSealedSegment(dir, head))
	ss, err := openSealedSegment(dir, DotProduct)
	requireNoError(t, err)
	defer ss.close()
	if err := ss.tombstoneSlot(99); err == nil {
		t.Fatal("tombstoneSlot out of range should error")
	}
}
```

Note: `withOpenFileFault` overrides `fsOpenFile`, but `writeManifest` uses `fsCreate`. Add a fault hook for `fsCreate` in `walhelpers_test.go` is heavier than needed — instead this test relies on `fsCreate` being overridable. Since `fsCreate` is a package var (Task 0), add a small local override in THIS test rather than `withOpenFileFault`. Replace the `TestManifest_WriteFaultRemovesTmp` body with a direct `fsCreate` override:

```go
func TestManifest_WriteFaultRemovesTmp(t *testing.T) {
	dir := t.TempDir()
	orig := fsCreate
	t.Cleanup(func() { fsCreate = orig })
	fsCreate = func(name string) (osFile, error) {
		f, err := orig(name)
		if err != nil {
			return nil, err
		}
		return &faultFile{osFile: f, failSync: true}, nil
	}
	if err := writeManifest(dir, sampleManifest()); err == nil {
		t.Fatal("writeManifest should fail when fsync is injected to fail")
	}
	if _, statErr := os.Stat(dir + "/manifest.tmp"); !os.IsNotExist(statErr) {
		t.Fatal("manifest.tmp must be removed on write fault")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./vectorstore/ -run '^TestSeal_EmptyHeadIsNoOp$|^TestManifest_WriteFaultRemovesTmp$|^TestSealedSegment_TombstoneOutOfRange$' -count=1`
Expected: at least one FAILs initially only if a branch is wrong; if all three already pass (branches exist from prior tasks), that is acceptable — they lock coverage. If `TestSeal_EmptyHeadIsNoOp` fails, ensure `sealLocked` early-returns on empty head (it does from Task 9). Re-run; all three should pass.

- [ ] **Step 3: Update `doc.go` to Phase 2**

Replace the body of `core/vectorstore/doc.go`:

```go
// Package vectorstore is the segmented (LSM-style) vector store engine.
//
// Records are (string id, vector, payload). They live in one mutable in-memory
// "head" segment plus an ordered set of immutable on-disk SEALED segments. The
// head is brute-searched and never gets a graph; when it reaches maxSegSize (or
// on Seal) it is frozen into a sealed records-segment (mmap'd vectors in
// metric-natural form + norm, slot→docId, a persisted tombstone bitmap, and
// payload) under seg-<id>-<gen>/, and a fresh head starts. A background builder
// then builds a per-segment HNSW graph over the sealed segment and flips its
// state pending→indexed.
//
// Search is an N-way merge: each indexed sealed segment via its HNSW (results
// post-filtered by that segment's tombstone bitmap, since the immutable graph
// can still return tombstoned nodes), each pending sealed segment by brute, and
// the head by brute — all into one shared top-k heap in docId space.
//
// Durability has two persistent faces (architecture §4.8): a per-write head WAL
// (Put/Delete fsync before mutation) and a single-file manifest atomically
// rewritten on each structural change (tmp+fsync+rename+dir-fsync) listing the
// sealed segments, their index states, and the head segId. Recovery loads the
// manifest, mmaps the sealed segments, rebuilds the global docId→segId map from
// each segment's on-disk slot→docId, reopens indexed graphs, replays the head
// WAL, resumes any pending build, and sweeps orphan seg dirs not referenced by
// the manifest (half-written seal files from a crash). Sealed segments are
// immutable except for their tombstone bitmaps.
//
// The two-level id model (architecture §4.6) spans segments: a stable int64
// docId (via core/idtable) maps to an owning segId, and each segment maps
// docId↔slot. Search returns docId-space results; mapping docId back to the
// caller's string id is the caller's responsibility (idtable has no reverse map).
//
// Phase 2 has exactly ONE index (the default HNSW over the store's metric). The
// HNSW graph algorithm + NodeStore seam are migrated (copied and slimmed so the
// graph stores only topology and resolves vectors from the owning sealed segment
// by slot) from core/vectorindex, which remains independent and unmodified.
//
// Out of scope (later phases): compaction / segment merge / space reclaim,
// attribute filtering, and multiple indexes (different metrics/params).
package vectorstore
```

- [ ] **Step 4: Run the tests + the full coverage gate**

Run: `go test ./vectorstore/ -count=1 && go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.2`
Expected: tests ok; go-cov reports PASS (total ≥ 90%, package ≥ 85%, function ≥ 80%). If go-cov flags an uncovered new function/branch in `vectorstore`, add a focused test for it in `phase2_coverage_test.go` (e.g. a `Search` with `k<=0` error, a `readManifest` corrupt-magic case, an `openSealedSegment` bad-magic case via a truncated file) and re-run until the gate passes.

- [ ] **Step 5: Cross-platform build check (CI builds on macOS/Windows too)**

Run: `GOOS=windows go build ./... && GOOS=darwin go build ./...`
Expected: both succeed (the `mmap_windows.go`/`mmap_unix.go` build tags and `fsyncDir`'s Windows no-op cover the platform split).

- [ ] **Step 6: Verify whole tree green + gofmt; commit**

Run: `go build ./... && go test ./vectorstore/ ./vectorindex/ -count=1 && gofmt -l vectorstore/`
Expected: green, no gofmt diffs.

```bash
git add core/vectorstore/doc.go core/vectorstore/phase2_coverage_test.go
git commit -m "docs(vectorstore): Phase 2 package doc + coverage-gate closing tests"
```

---

## Self-Review

**1. Spec coverage (architecture §8.2 + the four Phase-2 deltas):**

- SEAL (freeze head → immutable mmap sealed segment) — Tasks 2, 9, 12.
- Sealed files: vectors (metric-natural + norm), slot→docId, persisted tombstone bitmap, payload — Tasks 1, 2.
- Per-segment HNSW migrated/slimmed (graph-only, vectors by slot) — Tasks 4, 5, 6, 7.
- (index,segment) state pending|indexed; brute-until-built — Tasks 9 (merge), 11 (flip + WaitForIndex).
- Background builder; head writable during build — Task 11.
- N-way merge Search (indexed graph + pending brute + head brute, each tombstone-filtered) — Task 9.
- Manifest single-file atomic rewrite — Task 3 (+ written in 9, 11).
- Head WAL per write (reused, truncated on seal) — Tasks 9, 10.
- Recovery: load manifest → mmap → rebuild docId→segId → replay WAL → resume pending → orphan-sweep — Tasks 10, 11, 13.
- Two-level id across multiple segments (docId→segId + per-segment docId↔slot) — Tasks 8, 10.
- Sealed immutable except tombstone (persisted) — Tasks 2, 8.
- vectorindex stays compiling + green (copy, never edit) — verified in Tasks 4, 7 and every "tree green" gate.

Out-of-scope correctly excluded: compaction/merge (Phase 4), attribute filtering (Phase 5), multi-index (Phase 6). No key-range pruning (N-way searches all segments). Metric not parameterized per-index.

**2. Placeholder scan:** No "TBD"/"add error handling"/"similar to Task N". Every code step shows full code; every test shows full assertions; every run command has an expected FAIL/PASS. `maxSegSize` is a real configurable field with a documented fixed default (the adaptive value is explicitly out-of-scope per measure-don't-assert, not a placeholder).

**3. Type consistency:** `segID`/`segState`/`segPending`/`segIndexed`/`segmentEntry`/`manifest` defined in Task 3 and used consistently in Tasks 8–13. `hnswIndex`/`newHNSWIndex`/`graphNodeStore`/`newBatch`/`put`/`commit`/`search`/`withGraphM`/`withGraphEfSearch` defined in Task 4, used in 5/6/7/9. `sealedSegment` methods `read`/`eachLive`/`slotDoc`/`tombGet`/`tombstoneSlot`/`getVectorRef`/`count`/`tombCount`/`slotOfDoc` defined in Tasks 2/9 and used in 5/8/9/10. `segGraphStore`/`newSegGraphStore`/`bindSlot` in Task 5, used in 6/7. `buildSegmentGraph`/`graphConfig`/`withDefaults` in Task 7, used in 9/11. `writeSealedSegment`/`openSealedSegment`/`writeManifest`/`readManifest`/`writeGraphFile`/`openGraphFile`/`segDirName`/`headSegID`/`fsyncDir`/`mmapAlloc` all defined before first use. Store fields (`sealed`/`sealedID`/`docToSeg`/`graphs`/`gcfg`/`nextSeg`/`manifestVersion`/`buildMu`/`builds`/`maxSegSize`) are introduced incrementally and each addition is shown in its task. `itoa` (test helper) defined in Task 9's test file and reused by later test files in the same package (Go test files share package scope) — defined once.

One consistency fix applied inline: Task 5's `graphBatch` delete method is `del` (not `delete`, which would shadow the builtin and collide with `hnswIndex.delete`); Task 4 defines `del` accordingly — both use `del`.

---

**Plan complete. Save to `core/docs/superpowers/plans/2026-06-16-vectorstore-phase2-seal-index-merge.md`.**

This plan is the return value (the orchestration script reads my text output). The plan above is complete and self-contained: 15 tasks (0–14), each TDD bite-sized with exact paths, complete failing-test code, exact run+FAIL/PASS commands, complete minimal implementations, tree-green + gofmt + race + coverage gates, and frequent commits. The hardest parts (HNSW copy-and-slim with `nodeId==slot` and live-dense build; seal/manifest/recovery crash-safety with the audit-#76 ordering and orphan-sweep; the immutable-graph tombstone post-filter) are concrete and red-proofed against the real Phase-1 code in `/workspace/haystack/core/vectorstore` and the real HNSW in `/workspace/haystack/core/vectorindex`.

---

## Adversarial review — fixes to apply during execution

> This plan is the workflow **draft**; its 5-dimension adversarial review produced 55 findings. The **26 critical/high** ones below MUST be applied as you implement the relevant task (the per-group + final adversarial review will enforce them). Medium/low (29) are improvements — apply where cheap.

1. **[CRITICAL] refactor-safety** — Whole plan vs core/.github/workflows/ci.yml (the `core` job) — Task 0 mmap_test, Task 2 seal_test, Task 10 recovery_test, Task 14 Step 5
   - issue: CI does NOT just `go build` on macOS/Windows — the `core` job runs the FULL test suite on windows-latest and macos-latest (`go test -v -timeout 15m ./...`). The plan only ever runs `go test ./vectorstore/ ./vectorindex/` (implicitly linux) and a single `GOOS=windows go build ./...` (Task 14 Step 5). Every mmap/seal/recovery test the plan adds will execute on Windows in CI. On Windows: (a) `fsyncDir` is a deliberate no-op so durability-ordering assertions differ; (b) `tombstoneSlot` writes through a RW file-backed `MapViewOfFile` then `FlushViewOfFile` then re-`openSealedSegment` reads it back — `TestSeal_TombstonePersistsAcrossReopen` requires the RW mmap write to be visible to a fresh read-only mmap of the same file, which is not guaranteed on Windows without closing/flushing the original view first; (c) opening a file for read-only mmap while another handle holds it open can hit ERROR_SHARING_VIOLATION. The plan's per-task green gate cannot see this — the tree goes red on the first PR's CI run despite passing locally. This is the dimension's core failure: a task can leave the build/test red on a platform the plan never exercises.
   - fix: Add a Windows/macOS reality gate. Either (1) add `//go:build !windows` test guards or `t.Skip` on Windows for the mmap-RW-tombstone-reopen and dir-fsync-dependent tests with a tracked follow-up, OR (2) require each mmap/seal task to run `GOOS=windows GOARCH=amd64 go vet ./vectorstore/` AND actually exercise the suite cross-platform before claiming green (the plan's own 'tree-green after every task' rule must include the platforms CI runs). Also explicitly close the RW tomb mmap + its fd and re-flush before reopening in `tombstoneSlot`/`openSealedSegment` so the round-trip is Windows-safe.
2. **[CRITICAL] crashsafety-tdd** — Tasks 9, 10, 13, 3 — the entire seal/manifest/recovery crash-safety surface; contrast with vectorindex/mmap_crash_test.go which the repo already ships
   - issue: NOT ONE test in the plan injects a crash mid-operation. Every 'crash' test is a clean close-then-reopen round trip. The dimension's required red-proofs are all absent: (a) kill-mid-seal — no test writes the sealed .dat files but crashes BEFORE the manifest swap to prove the half-written segment is invisible+swept; (b) kill-mid-build — no test crashes after manifest publishes the segment as `pending` but before `pending→indexed`, to prove recovery resumes the build (Task 11 adds the resume code with ZERO test coverage); (c) manifest swap atomicity — no test crashes BETWEEN writing manifest.tmp and the rename, to prove the old manifest still loads and the half-written tmp is ignored (Task 3's TestManifest_NoTmpLeftBehind only checks the HAPPY path leaves no tmp; it never creates a stranded manifest.tmp + valid manifest and proves recovery picks the committed one); (d) head-WAL-replay-after-crash — TestRecovery_SealedSegmentsAndHeadWAL does a clean s.Close() (which flushes the WAL buffer and waits for builds), so it never exercises replay of an UNSYNCED-buffer or a torn-tail head WAL after a real crash. The architecture §4.8 explicitly demands proving BOTH '崩在换前' and '崩在换后'; the plan proves neither.
   - fix: Add real crash-injection tests, modeled on the existing vectorindex/mmap_crash_test.go pattern. Each must (1) drive the store to a precise mid-operation point WITHOUT calling Close(), (2) simulate the crash by abandoning the in-memory Store and re-Open()ing the same dir+KV, (3) assert the invariant. Concretely: TestCrash_MidSeal_BeforeManifestSwap (write sealed dir via a hook that panics/returns before writeManifestLocked, then reopen → assert seg dir swept, no doc lost from old head WAL because WAL was NOT yet truncated); TestCrash_MidBuild_ResumesPending (publish pending, crash before flip, reopen → assert WaitForIndex builds it and graph.dat appears); TestCrash_ManifestTmpStranded (drop a manifest.tmp next to a valid older manifest, reopen → assert old committed manifest loads and tmp is ignored/removed); TestCrash_HeadWALTornTail (append a head record, corrupt its CRC byte on disk, reopen → assert torn record dropped and prior records survive). Inject crash points via the existing fsCreate/fsRename/fsOpenFile package-var seams (a hook that returns errInjected at the chosen step), not by calling Close().
3. **[CRITICAL] crashsafety-tdd** — Task 3 writeManifest + Task 10 recover: the tmp+rename sequence and the 'crash between rename and dir-fsync' window
   - issue: The manifest write does tmp→fsync→close→rename→fsyncDir(dir). The plan claims this is the audit-#76 atomic pattern, but there is NO test that the rename itself is the commit point under crash: nothing proves that a crash AFTER rename but BEFORE the dir-fsync still recovers (the directory entry may not be durable). More importantly, recover() has no defense against a manifest.tmp left on disk from a crash mid-write — sweepOrphansLocked only sweeps `seg-*` dirs, never `manifest.tmp`. A stranded manifest.tmp is harmless to readManifest (it reads `manifest`, not `.tmp`), but it is never cleaned, and there is no test asserting recover() tolerates its presence. The tautology risk: TestManifest_NoTmpLeftBehind asserts the happy path removed the tmp — that is trivially true and tells you nothing about crash behavior.
   - fix: Add a test that pre-creates dir/manifest.tmp with garbage AND a valid dir/manifest, then Open() and assert recovery succeeds and ignores the tmp (and ideally sweeps it). Extend sweepOrphansLocked (or recover) to unlink a stale manifest.tmp. Replace the tautological NoTmpLeftBehind assertion's intent with a crash-point test: inject fsRename to fail, assert readManifest still returns the PREVIOUS committed manifest (not a torn one) — proving rename is the true commit boundary.
4. **[CRITICAL] crashsafety-tdd** — Task 5 graphstore.go design note ('nodeId == slot') vs Task 5 impl (bindSlot + buildSlot mapping) and Task 9 Search tombstone post-filter
   - issue: The plan's central architecture claim is self-contradictory and the contradiction is load-bearing for correctness. The design note says 'nodeId == slot for the per-segment HNSW' (matching architecture §3.1 '按 vectorId 取向量'), but the actual segGraphStore makes nodeId a LIVE-DENSE build index (0,1,2… over non-tombstoned slots only) via NextNodeId, and keeps a SEPARATE nodeSlot[] map because nodeId != slot whenever the segment has a pre-seal tombstone gap. This means the on-disk graph.dat stores dense nodeIds, NOT slots — so 'nodeId==slot' is FALSE and the architecture-stated invariant is silently abandoned. The visitedSet in the copied hnsw.go REQUIRES dense 0-based ids (it indexes a flat slice by id), so dense build ids are actually the right choice — but then the plan must DROP the 'nodeId==slot' claim everywhere (design note, doc.go, nodestore.go comment) or it will mislead the implementer into indexing the segment by raw slot and overflowing/aliasing visitedSet on a sparse segment.
   - fix: Pick ONE model and state it consistently: keep dense build-ids (correct for visitedSet), delete every 'nodeId == slot' assertion, and rename the design note to 'nodeId is a dense live-only build index; nodeSlot[nodeId] resolves the segment row'. Add a test that seals a segment WITH interior tombstones (e.g. tombstone slots 5,17,40 before seal) then builds+searches, asserting (a) no panic in visitedSet, (b) recall>=0.8, (c) returned docIds exclude the tombstoned ones — this is the test that would have caught the slot-vs-denseId confusion; Task 5's current test only tombstones ONE slot and never checks the dense-id boundary.
5. **[CRITICAL] feasibility-vs-code** — Task 4, Step 4 item 10 — hnsw.go copy + Task 6 graphfile_format.go
   - issue: The plan tells the worker to ADD `const defaultMaxLayers = 6` 'at the top of this file' in vectorstore/hnsw.go (Task 4 item 10) AND separately notes Task 6's graph file reuses it. But the copied hnsw.go from vectorindex does NOT define defaultMaxLayers — in vectorindex it lives in mmap_format.go:131 (`const defaultMaxLayers = 6`), a file the plan never copies. So far so good that Task 4 adds it. HOWEVER the plan's Task 4 rename list is incomplete in a way that WON'T compile: the copied hnsw.go references `SearchResult` (returned by Search). vectorstore already defines its own `SearchResult` in result.go (DocID/Distance) — that one is compatible, so this is fine. The real defaultMaxLayers risk is that the worker, told to add it in hnsw.go, will then ALSO be told in Task 6 graphfile_format.go to reference defaultMaxLayers — which works since same package. Net: the single genuine compile hazard here is that the plan never states defaultMaxLayers is *absent* from the copied file (it's in mmap_format.go which is NOT copied), so a worker copying hnsw.go verbatim and forgetting item-10 gets `undefined: defaultMaxLayers`.
   - fix: Make Task 4 explicit: 'vectorindex/hnsw.go references defaultMaxLayers but does NOT define it (it lives in vectorindex/mmap_format.go:131, which we do not copy). You MUST add `const defaultMaxLayers = 6` in the copied vectorstore/hnsw.go or it will not compile.' Keep it in exactly one vectorstore file to avoid a duplicate-const error.
6. **[CRITICAL] feasibility-vs-code** — Task 9 sealLocked / Task 8 attachSealedForTest — slotOfDoc defined twice
   - issue: Task 8 Step 3 adds `func (ss *sealedSegment) slotOfDoc(docID int64) (int, bool)` to store.go (with the awkward `false || true` later fixed to `return slot, true`). But Task 9 and Task 10 also call `ss.slotOfDoc(...)`. There is no second definition, so that is fine. The REAL duplicate hazard: the Phase-1 *segment* already has `slotOfDoc` (segment.go:60). The plan adds an identically named method on *sealedSegment*. Different receiver types, so no collision — OK. However sealedSegment.slotOfDoc does a LINEAR O(n) scan over every slot on every Delete/Get/Search-hit (Task 8/9/10). For a sealed segment of ~50k rows this makes indexed-leg post-filtering O(k·n) per query and Delete O(n). That is a real performance defect vs the architecture's per-segment docId↔slot map (§4.6 says the per-segment docId↔slot is a derived in-memory map, not a scan).
   - fix: Give sealedSegment a `docToSlot map[int64]int` built once in openSealedSegment from the live slotDocs (skip tombstoned), and make slotOfDoc/tombstoneSlot/Get use it in O(1). The architecture explicitly mandates a per-segment docId↔slot map; the linear scan deviates from it and will not scale to maxSegSize.
7. **[CRITICAL] feasibility-vs-code** — Task 9 Search indexed leg — tombstone post-filter via slotOfDoc cannot see post-seal deletes correctly
   - issue: The plan post-filters graph hits with `slot,found := ss.slotOfDoc(h.DocID); if !found { continue }`. But sealedSegment.slotOfDoc (Task 8) returns found=false when the slot is tombstoned (it checks `!ss.tombGet(slot)`). That is the intended filter. HOWEVER, slotOfDoc scans `s.slotDocs[slot]==docID && !tombGet(slot)`. After a post-seal Delete, docToSeg deletes the entry (`delete(s.docToSeg, docID)` in Task 8 Delete) AND tombstoneSlot sets the bit — both. So the filter works. The subtle correctness gap: the architecture (§4.4) says each indexed leg filters by 'that segment tombstone bitmap', and the plan does this via a docId→slot lookup that itself re-checks the bitmap. That double-bookkeeping is fragile: if a docId was re-Put into the head (Update), it now lives in head with the SAME docId, the sealed slot is tombstoned, but the graph still returns the old docId. slotOfDoc returns found=false (tombstoned) so it is dropped — correct. But then the HEAD brute leg emits the same docId live — correct, no dup. This actually works, but ONLY because Update tombstones the sealed slot. The plan's Task 8 Delete does tombstone+delete docToSeg, but there is NO Update/re-Put-into-head path that tombstones the sealed slot in Phase 2 Put (Put only writes head and sets docToSeg=headSegID, never tombstoning the prior sealed slot).
   - fix: Put must handle the cross-segment Update: when `s.docToSeg[docID]` already points at a SEALED segment, Put must tombstone that sealed slot (ss.tombstoneSlot) before re-homing docToSeg to headSegID — exactly as replay's recPut branch does in Task 10. Add this to Put in Task 8/9, otherwise re-Putting an existing sealed doc leaves it live in BOTH the sealed graph and the head (duplicate/stale results). The plan only fixes this in recovery replay, not in the live Put path.
8. **[CRITICAL] architecture-fidelity** — Task 9 sealLocked ordering + Task 10 recover/replay (store.go)
   - issue: The seal->recovery crash window is unhandled, and the recovery path violates the "sealed segments are IMMUTABLE" invariant. sealLocked order is: (1) writeSealedSegment+fsync, (2) manifest atomic swap, (3) wal.Reset(), (4) spawn build. If the process crashes AFTER the manifest swap but BEFORE wal.Reset() (a real window), recovery does: load manifest -> mmap sealed segs -> build docToSeg from sealed slotDoc -> then replay() the OLD head WAL, which STILL CONTAINS every pre-seal recPut. The plan's replay recPut branch (Task 10) then, for each replayed pre-seal doc, finds docToSeg[doc]==sealedSegId and calls ss.tombstoneSlot(slot) — a persistent msync write that MUTATES the supposedly-immutable sealed segment during recovery — then re-homes the doc to the head and re-appends it. Result: every doc is double-stored (live in head, tombstoned in sealed), and the on-disk sealed tombstone bitmap is corrupted relative to its built graph. The architecture §4.8 says "崩在换后 → 新态生效、旧文件成孤儿被扫": the WAL records folded into the sealed segment must NOT be replayed into the head. The plan has no manifest-vs-WAL reconciliation (no seal-epoch/LSN watermark in the manifest), so it cannot distinguish pre-seal WAL records (already captured in the sealed segment) from post-seal records (only in the WAL).
   - fix: Make seal crash-atomic against the WAL: persist a seal watermark in the manifest (e.g. the WAL LSN at seal time, or fold wal.Reset semantics so the manifest swap is the single commit point) and on recovery only replay WAL records with LSN > the manifest's sealed watermark. Equivalently, do wal.Reset() (truncate) and fsync the WAL BEFORE the manifest swap is considered the failure boundary, and make recovery treat the manifest as authoritative for any segment it lists (never replay pre-seal records into the head). Recovery must NEVER call tombstoneSlot on a sealed segment as a side effect of replaying a pre-seal put. Add an explicit crash-injection test for the 'crash after manifest swap, before WAL reset' window asserting no double-storage and an unmodified sealed tomb bitmap.
9. **[HIGH] scope-completeness** — Whole plan — no crash test between the three durable steps of Seal (records fsync → manifest swap → WAL truncate), and none for crash mid-build
   - issue: Phase 2's load-bearing claim is crash recovery across the seal pipeline (arch §4.8: '封存=快的立即durable、慢的后台'; '崩在换前/换后两边一致'). The plan only tests recovery on a CLEANLY-closed store (Task 10 reopenStore calls s.Close(), which Task 11 makes block on builds → every segment is already indexed and the manifest is fully consistent). There is ZERO test for: (a) crash AFTER records+manifest are durable but BEFORE WAL.Reset() — the head WAL still contains the just-sealed records, so recovery replays them into the fresh head AND loads them from the sealed segment = the SAME docId live in two segments (docToSeg overwrites, segment double-counts, Search returns duplicates / wrong recall); (b) crash mid-build (segment left pending in manifest) and resume — Task 11 adds resume code but no test reopens with a pending segment on disk; (c) crash after manifest swap but graph.dat half-written. The orphan-sweep test (Task 13) fabricates an orphan dir by hand, it does not exercise a real interrupted Seal. This is the single biggest scope gap: the 'manifest/恢复·崩溃恢复' deliverable is asserted, not tested.
   - fix: Add explicit crash-injection tests that do NOT call Close(): (1) seal, then DROP the WAL.Reset() (inject so Reset is skipped or crash right after manifest write) and reopen over the same KV — assert each sealed docId appears in exactly ONE segment and Search has no duplicate docIds; the recover() must reconcile: a head-WAL recPut for a docId already owned by a sealed segment must NOT re-home to head unless its LSN post-dates the seal (or: Seal must Reset the WAL BEFORE the manifest swap is observable, and the test must prove the ordering). (2) Leave a segment in state=pending on disk (build never ran), reopen, assert WaitForIndex builds it and Search recall holds. (3) Truncate graph.dat to a partial length for an indexed segment, reopen, assert recovery either re-builds or errors cleanly — openGraphFile currently indexes blindly into data[off:] with no bounds/length check and will panic on a short file.
10. **[HIGH] scope-completeness** — Task 5 graphstore.go NextNodeId (0-based) vs Task 4 memgraphstore.go copied from MemNodeStore.NextNodeId (1-based: `m.nextID++; return m.nextID`)
   - issue: The two graphNodeStore implementations assign node ids with different bases. MemNodeStore.NextNodeId returns 1,2,3,... (verified mem_store.go:227-232); the plan's segGraphStore.NextNodeId returns 0,1,2,.... The migrated HNSW (Task 4) is validated against memGraphStore (1-based) in TestHNSW_MemStore_BuildSearchRecall, then Task 5+ run the REAL build against segGraphStore (0-based). segGraphStore.PutNode/GetVectorRef/GetNodeLevel index slices by nodeId and grow them with `for len<=id`. With a 0-based id the entry-point node 0 collides with the 'unset' zero-value in several places (e.g. graphfile.go writes entryID=0 and HasEntry=0-or-1; a real entry node 0 is indistinguishable from 'no entry' if HasEntry handling regresses). The parity test passing on mem (1-based) does NOT prove the 0-based seg store works — the two are not the same code path. This is a scope-completeness defect: the 'migrate HNSW' task is split so that the thing actually shipped (segGraphStore) is never compared against the source of truth.
   - fix: Make segGraphStore.NextNodeId identical to the migrated source (or assert a chosen convention once and use it in BOTH stores). Add a direct equivalence test: build the SAME vectors through memGraphStore and segGraphStore with the same seeded RNG and assert identical search output, so the 0-based seg path is actually covered, not just the 1-based mem path.
11. **[HIGH] scope-completeness** — Manifest (Task 3 manifest.go) vs architecture §4.8 required fields; and Store.gcfg persistence (Task 9/11)
   - issue: Arch §4.8 specifies the manifest carries `head segId; 索引配置 name → VectorIndexConfig` (the per-index M/EfConstruction/EfSearch/Metric). The plan's manifest stores ONLY {Version, Head, Segments[{SegID,Gen,VecCount,TombCount,State}]} — no index config at all. Consequently graphConfig (gcfg, holding M/EfSearch) is NOT persisted: Open() sets `gcfg: graphConfig{}.withDefaults()` unconditionally (Task 8), so after recovery the store always uses DEFAULT efSearch/M regardless of what the segments were built with. Search (Task 9) does `newHNSWIndex(g, withGraphEfSearch(s.gcfg.EfSearch))` with this re-defaulted value. If a deployment ever set non-default params (the Options surface the plan should expose but doesn't — see separate finding), recovery silently changes search behavior. Even within defaults this is a latent gap the architecture explicitly closed. It is a Phase-2 scope item (manifest content), not a later phase.
   - fix: Persist the single index's config in the manifest (a name + M/EfConstruction/EfSearch/Metric block, even with the Phase-2 fixed name 'default'), and load it back into s.gcfg in recover(). Add a test that seals with a non-default EfSearch, reopens, and asserts s.gcfg round-trips. This also future-proofs Phase 6 (multi-index) without a format break.
12. **[HIGH] scope-completeness** — Missing task: metric persistence / mismatch detection on Open (no task covers it)
   - issue: openSealedSegment(segDir, metric) and the whole store take Metric from Options at Open time, but the plan NEVER persists the store's metric and NEVER validates it against the on-disk segments. Arch §3/§4.1 makes the on-disk vector form metric-dependent (cosine = unit+|v|; dot/euclid = raw). If a store sealed under Cosine is reopened with Options.Metric=Euclidean (operator error, or a default), recovery mmaps the unit vectors and interprets them as raw — silently wrong distances, no error. Phase 1 had the same single-metric assumption but no on-disk segments to mismatch; Phase 2 introduces durable metric-shaped data, so metric persistence + a mismatch guard is in-scope for 'manifest/恢复' and is entirely absent.
   - fix: Store the metric in the manifest (it belongs in the index config block above) and reject Open if Options.Metric disagrees with the persisted metric. Add a test: seal under Cosine, reopen with Euclidean → clean error, not corrupt results.
13. **[HIGH] refactor-safety** — Task 4 Step 4 (copy hnsw.go) + Self-Review claim of a 'mechanical rename, NOTHING else changed' — vs core/vectorindex/hnsw.go and core/vectorstore/result.go
   - issue: The 'copy hnsw.go and only rename' step will NOT compile, and the plan's rename list is wrong in two load-bearing ways. (a) hnsw.go's `Search` returns `[]SearchResult` and `searchLayer`/result collection build `SearchResult{DocID,Distance}`. In vectorindex this is the type from types.go; in vectorstore `SearchResult` already exists in result.go with the SAME fields — so this happens to work, BUT result.go ALSO defines a type `maxHeap` and methods `up/down/less/push/pop` on it, while the copied hnsw.go defines `maxDistHeap`/`minDistHeap` with their own `up/down/less/push/pop` — those are distinct type method sets so no collision, fine. The real breakage: (b) the plan says rename `Delete`->`delete` as a METHOD on hnswIndex, then in the SAME file the appended graphBatch also has a `del` method and calls `h.deleteOneLocked`/`h.insertOneLocked` and `h.runInTxnLocked` — those helper names are NOT in the plan's rename list (steps only rename Insert/Search/Delete public methods), so they stay as-is (good), but `randomLevel` references `defaultMaxLayers` which the plan adds as `const defaultMaxLayers = 6` in hnsw.go — yet Task 6's graphfile.go ALSO uses `defaultMaxLayers`, and the plan never removes the original; that's one definition, fine. The actual compile failure: the copied hnsw.go imports `sort` and `sync` and uses `visitedPool` (a package-level `sync.Pool`) — copying verbatim into vectorstore is fine, but the plan's Task 4 Step 3 nodestore.go DROPS `GetVector`/`GetNorm`/`SetNorm`/`Close` from the interface, while the copied hnsw.go in insertOneLocked/searchLayer does NOT call them — verified OK. Net: the migration is plausible but the plan asserts it as trivially mechanical when it requires (i) confirming `SearchResult` field identity, (ii) not double-defining `defaultMaxLayers`, (iii) the appended graphBatch's `del` vs `delete` discipline. Any slip leaves hnsw.go uncompilable = whole vectorstore package red = a half-migrated HNSW exactly as the dimension warns against.
   - fix: Split Task 4 into 4a (copy nodestore.go + hnsw.go + memgraphstore.go and get `go build ./vectorstore/` GREEN with a trivial smoke test) and 4b (recall parity test), so the copy compiles on its own commit before any parity logic. Replace the prose 'mechanical rename' with an explicit symbol-collision checklist run as `go build` after each file is added: SearchResult (reuse result.go's), defaultMaxLayers (define once, in hnsw.go; Task 6 reuses it), and the graphBatch `del`/index `delete` split. Do not append graphBatch into hnsw.go inline — put it in graphbatch.go so a batch typo can't brick the whole graph file.
14. **[HIGH] refactor-safety** — Task 0 Step 4-5 (copy mmap_unix.go verbatim) — vs go.mod (golang.org/x/sys is `// indirect`) and core/vectorindex/mmap_unix.go
   - issue: mmap_unix.go imports `golang.org/x/sys/unix` (used only by `mmapAdviseSequential`). Copying it verbatim into package vectorstore makes `golang.org/x/sys` a DIRECT dependency of a new package while go.mod still lists it `// indirect`. `gofmt -l` (the plan's only fmt gate) will NOT catch this, but `go mod tidy` — and any CI `go mod verify`/tidy-check — would rewrite the require block, which is an uncommitted diff that can fail a tidiness gate and is a real go.mod churn the plan never mentions. Separately, `mmapAdviseSequential` and the Windows `mmapAdviseSequential` no-op are DEAD in vectorstore (nothing in the plan calls advise), so the unix import exists only to keep dead code alive.
   - fix: Do NOT copy mmap_unix.go verbatim. Drop `mmapAdviseSequential` (and its unix import) from the vectorstore copy since nothing uses it; keep only `mmapPlatform`/`munmapPlatform`/`mmapSyncPlatform`. That removes the new direct x/sys dependency entirely (syscall is stdlib). If advise is wanted later, add it in the task that actually calls it. Add `go mod tidy && git diff --exit-code go.mod go.sum` to Task 0's green gate.
15. **[HIGH] refactor-safety** — Task 10 Step 3 `recover()` + Task 11 Step 3 (rewrites recover tail) — replay() called twice / orphan-sweep ordering
   - issue: Task 10 defines `recover()` ending in `return s.replay()`. Task 11 then says 'replace the tail of recover' to add a resume-builds pass that itself calls `s.replay()` and then loops — and instructs 'Remove the old final `return s.replay()` line so it is not called twice.' But Task 13 ALSO edits recover (inserting `sweepOrphansLocked` 'after the segments are opened but BEFORE spawning resume builds'). Three tasks mutate the same `recover()` body by prose ('replace the tail', 'after the loop', 'before spawning'), with no single canonical final form shown. The ordering constraints conflict: orphan-sweep must run BEFORE `openGraphFile`/segment open of a half-written segment (else openSealedSegment hits a bad-magic/short file and `recover` returns an error -> Open fails -> store unrecoverable), yet Task 10 opens all manifest segments first and Task 13 sweeps only NON-referenced dirs (so a referenced-but-torn dir is still opened and errors). If a crash happened AFTER manifest swap but BEFORE graph.dat fsync, the segment IS referenced as pending — fine — but if it crashed mid records-file write yet the manifest somehow lists it, open fails. More concretely: the double-`replay()` removal is exactly the kind of prose edit that leaves recover() either calling replay twice (every head record applied twice -> duplicate slots) or zero times (head WAL lost) depending on which task's instruction the implementer follows last.
   - fix: Collapse recovery into ONE task that shows the FINAL `recover()` body verbatim (manifest load -> orphan-sweep -> open referenced segments -> reopen indexed graphs -> single replay() -> resume pending builds), instead of three tasks patching it by description. Make orphan-sweep run before segment open. Assert replay runs exactly once with a test that Puts N head docs, crashes, recovers, and checks head live-count == N (not 2N).
16. **[HIGH] refactor-safety** — Task 11 Step 3 `buildAndPublish` + Task 8/9 sealed-tombstone path — data race on the sealed segment's tomb mmap vs the background builder
   - issue: The plan asserts (Task 11 comment) 'the sealed segment is immutable (only its tombstone bitmap mutates, which the build reads through eachLive at start), so the build needs no lock on the store.' This is false under -race and under real concurrency. `buildAndPublish` runs `buildSegmentGraph` -> `seg.eachLive(...)` OFF the store lock, while a concurrent `Delete` of a doc living in that same pending sealed segment calls `ss.tombstoneSlot(slot)` which does a non-atomic read-modify-write of the mmap word + `mmapSync`. `eachLive` calls `tombGet` reading the same word. Concurrent read of `s.tombMap[...]` and write of `s.tombMap[...]` with no synchronization is a textbook data race that `go test -race` (Task 11 Step 5 explicitly runs `-race`) WILL flag — turning the tree red on the exact gate the plan adds. The graph also bakes in whatever live set it saw, but a doc tombstoned during the build is post-filtered at search time (correct), so the race is purely the unsynchronized mmap word access, not a correctness-of-results issue.
   - fix: Guard the tomb bitmap. Either (a) take a per-segment RWMutex around tombGet/tombstoneSlot reads/writes, or (b) snapshot the live-slot list under the store lock before launching the builder and have the builder iterate the snapshot (not eachLive over the live mmap). Add the race test (delete-during-pending-build) to Task 11 so the gate proves it, since `-race` is already in the plan's Step 5.
17. **[HIGH] crashsafety-tdd** — Task 9 Search indexed-leg + Task 5 graphstore — post-seal tombstone filtering of graph hits
   - issue: The 'most important correctness gotcha' (immutable graph returns tombstoned nodes → post-filter by tombstone bitmap) is filtered via ss.slotOfDoc(h.DocID), which does a LINEAR O(n) scan of the whole segment PER HIT (slotOfDoc loops all slots). With k hits per segment and M segments that is O(M·k·n) per query — and slotOfDoc returns 'live slot only', so a tombstoned hit returns found=false and is dropped, which is correct, but the test TestStore_Search_IndexedSegmentTombstoneFiltered deletes x-0 and asserts it never appears. The gap: it deletes BEFORE searching and the deletion goes through Delete→tombstoneSlot (durable). It never tests the harder ordering the architecture calls out — a doc tombstoned in the graph that is STILL the graph's entry point, or a query where the tombstoned node is the true nearest. With only 80 random vecs and k=20 over 20 iters the deleted doc may rarely be in the top-20 at all, so the test can pass WITHOUT the post-filter ever firing (false-green).
   - fix: Make the tombstone-leak test deterministic: insert a cluster where the deleted doc is provably the nearest to a chosen query (e.g. delete the exact vector equal to q), seal+build, delete it, then assert it's absent AND that the 2nd-nearest is returned — so the post-filter MUST fire. Separately, flag the O(n) slotOfDoc per-hit as an efficiency defect: store a per-segment docId→slot map at openSealedSegment (it already builds slotDocs[]) so the filter is O(1).
18. **[HIGH] crashsafety-tdd** — Task 11 buildAndPublish + Task 9/10 store fields — data races the plan's own -race gate will catch but the plan asserts will pass
   - issue: buildAndPublish takes buildMu then s.mu.Lock() and writes s.graphs[id]; Search holds s.mu.RLock() and reads s.graphs — consistent. BUT the plan's TESTS mutate store internals withOUT the lock while the background builder runs: Task 9's put closure does `vecs[s.idToDoc[id]] = ...` reading s.idToDoc unlocked concurrently with a build that may be flipping the manifest; Task 11's test calls put() (which locks) but then reads nothing racy. The real race is in buildAndPublish itself: it calls buildSegmentGraph which calls ss.eachLive → ss.tombGet reads the mmap'd tomb bitmap, while a concurrent Delete on that same sealed segment calls ss.tombstoneSlot which WRITES the same tomb words + mmapSync — unsynchronized. eachLive during a concurrent tombstoneSlot is a genuine data race on tombMap and a torn read of the bitmap word. The plan claims 'the sealed segment is immutable (only its tombstone bitmap mutates, which the build reads through eachLive at start)' — but 'at start' is not enforced; Delete can fire mid-build.
   - fix: Guard the sealed tombstone bitmap with a per-segment mutex (or atomic word ops) around tombGet/tombstoneSlot, OR snapshot the live-set into a []int before building so the build never re-reads the mutating bitmap. Add a -race test that runs Delete on a segment concurrently with its background build (currently no test exercises concurrent Delete-during-build).
19. **[HIGH] crashsafety-tdd** — Task 9 writeManifestLocked / sealLocked / buildAndPublish — manifest Version monotonicity and the head WAL truncate ordering
   - issue: sealLocked truncates the head WAL (wal.Reset) AFTER writeManifestLocked succeeds. But Reset() preserves the LSN counter and zeroes the file — if a crash happens AFTER manifest swap but BEFORE wal.Reset completes, recovery loads the new manifest (segment committed) AND replays the OLD head WAL whose records re-append the now-sealed docs into the fresh head, DOUBLE-INDEXING them (once in the sealed segment, once re-homed to head via the Task 10 recPut branch that tombstones the sealed slot). The Task 10 recPut branch actually tombstones the sealed slot and re-homes to head — so the doc survives but its sealed-segment graph node becomes a tombstoned orphan and the vector is needlessly re-added to head. No test covers 'crash between manifest-swap and WAL-truncate'. Also writeManifestLocked increments manifestVersion TWICE (sets m.Version=s.manifestVersion, then s.manifestVersion++, then m.Version=s.manifestVersion again) — the first assignment is dead and the doubled logic is confusing/bug-prone.
   - fix: Make seal idempotent under replay: the head WAL records carry docIds; on replay, if a docId is ALREADY owned by a sealed segment loaded from the manifest, SKIP re-applying that put (it's already durable in the segment) rather than re-homing+tombstoning. Add TestCrash_AfterManifestSwap_BeforeWALReset asserting no double-index and head count is correct. Fix writeManifestLocked to increment Version exactly once.
20. **[HIGH] crashsafety-tdd** — Task 8/9/10 — Get/Delete/Search use ss.slotOfDoc which is O(n) linear scan; and docToSeg map is mutated under s.mu but read by background build path
   - issue: slotOfDoc on sealedSegment (added in Task 8) is an O(n) full-segment scan executed on every Get, every Delete, and every indexed-search hit. For a 78k-vector segment that is 78k comparisons per Get — architecturally unacceptable for the '段化核心' and not flagged as a placeholder. The Phase-1 segment had an O(1) docToSlot map; the plan DROPS that for sealed segments with no justification. This is a silent architecture regression (the two-level id model §4.6 explicitly wants per-segment docId↔slot as a map).
   - fix: Build a docId→slot map (over live slots) in openSealedSegment from the already-decoded slotDocs[] (skip tombstoned via tombGet), and have slotOfDoc consult it in O(1). It costs one map alloc per open and matches the architecture's stated per-segment docId↔slot index.
21. **[HIGH] feasibility-vs-code** — Task 4 Step 5 + Task 4 test — memGraphStore.NextNodeId is 1-based, segGraphStore.NextNodeId is 0-based
   - issue: The copied MemNodeStore (memgraphstore.go) keeps the original `NextNodeId(){ m.nextID++; return m.nextID }` which returns 1 on first call (ids 1,2,3,...). The plan's segGraphStore.NextNodeId returns `g.nextID` THEN increments (ids 0,1,2,...). The migrated visitedSet in hnsw.go is documented to assume 'dense 0-based' node ids and indexes a flat slice `versions[id]`. With memGraphStore's 1-based ids, slot 0 of versions is wasted but it still works (visitedSet.mark grows). The deeper inconsistency: the TWO stores disagree on id base, so the parity test (TestHNSW_MemStore vs the seg store) exercises different id spaces — not a compile error but means the 'parity' claim is weaker than stated, and if any copied code assumes entry-point id 0 == 'unset' it breaks. segGraphStore using id 0 as a real node while errNoEntryPoint signals empty is fine (hasEntry flag), but note memGraphStore returning 1-based while the plan's graphfile/segGraphStore is 0-based means recovery (openGraphFile) reconstructs 0-based ids that won't match a memGraphStore-built graph.
   - fix: Make both stores use the SAME id base. Easiest: change the copied memGraphStore.NextNodeId to 0-based (`id:=m.nextID; m.nextID++; return id`) to match segGraphStore, and verify nothing in the copied hnsw.go treats id 0 specially (it does not — emptiness is via errNoEntryPoint). Call this out explicitly in Task 4 Step 5 rather than 'copy verbatim'.
22. **[HIGH] feasibility-vs-code** — Task 0 / Task 3 — fsyncDir + fsRename + fsRemove + fsCreate already partially exist; widening osfile.go conflicts with wal.go's fsOpenFile
   - issue: The plan's Task 0 replaces the ENTIRE osfile.go body, adding fsCreate/fsOpen/fsRename/fsRemove and widening osFile to add ReadAt/Stat/Fd. The existing wal.go uses fsOpenFile and the existing faultFile (walhelpers_test.go) embeds osFile and overrides Write/Sync/Truncate/Close — it does NOT implement ReadAt/Stat/Fd. After widening the interface, faultFile still satisfies osFile via embedding (the embedded osFile provides ReadAt/Stat/Fd), so it compiles. GOOD. But the plan's claim 'the existing wal.go and walhelpers_test.go already use fsOpenFile; *os.File satisfies the widened interface, so they keep compiling' is correct ONLY because faultFile embeds osFile. The genuine risk: Task 0's new osFile drops nothing but the plan's fsyncDir uses fsOpen which it defines — consistent. No blocker, but the plan should note faultFile now transitively requires the wider interface and any test that constructs a faultFile around a non-*os.File will break.
   - fix: State explicitly that widening osFile is safe because faultFile embeds osFile (inherits ReadAt/Stat/Fd), and add a compile-time assertion `var _ osFile = (*os.File)(nil)` in osfile.go to catch any future drift.
23. **[HIGH] feasibility-vs-code** — Coverage gate reality — go-cov runs the WHOLE core module, not just vectorstore/vectorindex
   - issue: The plan's per-task gate is `go test ./vectorstore/ ./vectorindex/` and it treats go-cov as a final-task concern. But CI (.github/workflows/ci.yml:49) runs `go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.2` which (per .go-cov.toml) covers `github.com/codetrek/haystack/` with only scripts/cmd/testutil excluded — i.e. coverage is enforced ACROSS the whole module at total 90% / package 85% / function 80%. The copy-and-slim introduces a LOT of new production functions that the plan's targeted tests will not all hit: every graphNodeStore method on segGraphStore (txnBegin/Commit/Abort no-ops, ClearEntryPoint, DeleteNode, GetNodeLevel error paths), the appendU32/appendI32 helpers, manifest parse error branches, every openSealedSegment bad-magic branch, fileSize/readWholeFile error paths, fsyncDir Windows branch. Function coverage 80% is per-function-hit and brittle; the copied memGraphStore is ONLY used by one test and adds ~15 functions that must each be invoked. The plan acknowledges this only in Task 14 as 'close any gaps' — that is under-scoped given the volume of new surface.
   - fix: Either (a) budget explicit coverage tests per task (not just Task 14) for every new exported-to-package function and error branch, or (b) add the copied/migrated graph files to .go-cov.toml's exclude list IF they are genuinely hard to cover — but excluding is a policy change needing maintainer sign-off. Given go-cov is whole-module and strict-by-default (per memory vectorindex-batch-redesign-pr), the realistic path is per-task coverage tests; flag Task 14 as likely to fail the gate as written.
24. **[HIGH] architecture-fidelity** — Task 8/9/10 — sealedSegment.slotOfDoc and the N-way merge tombstone post-filter (sealed.go, store.go Search/Delete)
   - issue: slotOfDoc on a sealed segment is implemented as an O(n) linear scan over slotDocs (the plan's Task 8 code loops `for slot := 0; slot < ss.n; slot++`). The merged Search (Task 9) post-filters EVERY graph hit by calling ss.slotOfDoc(h.DocID) — so each query does O(hits * n) per indexed segment, and Delete/Get also linear-scan. This defeats the two-level id model (architecture §4.6 mandates a per-segment docId<->slot map as the in-memory derived index, exactly like the head segment's docToSlot). The architecture explicitly says the per-segment docId<->slot mapping is derived, resident, rebuilt at open — not a linear scan. With maxSegSize ~26k-128k vectors this is a severe per-query cost and contradicts the settled design.
   - fix: Give sealedSegment a `docToSlot map[int64]int` built once at openSealedSegment from slotDocs over live slots (and updated on tombstoneSlot), exactly mirroring segment.docToSlot. Make slotOfDoc/tombstone/Get O(1). For the Search tombstone post-filter, the graph already returns docIds; check liveness via a docToSlot-or-tombGet O(1) lookup, not a scan. This is required to honor the architecture's 'derived, resident, rebuildable' per-segment id map.
25. **[HIGH] architecture-fidelity** — Task 6 openGraphFile + Task 9 Search efSearch on reopened graphs (graphfile.go, store.go)
   - issue: Reopened graphs lose their HNSW search parameters. Task 6/7 persist only topology + entry point in graph.dat; M/efConstruction/efSearch are NOT persisted. On recovery (Task 10) graphs are reopened via openGraphFile and searched. Task 6's own round-trip test reopens with newHNSWIndex(rgs) using DEFAULT efSearch (64), while Task 9's live Search uses s.gcfg.EfSearch. After a restart the store's gcfg is reconstructed as graphConfig{}.withDefaults() (Task 8 Open), NOT loaded from the manifest — the architecture §4.8 manifest schema explicitly includes "索引配置 name -> VectorIndexConfig" (M/EfConstruction/EfSearch). The plan never writes or reads the index config in the manifest, so a non-default efSearch silently reverts to default after recovery, changing recall. This is an architecture deviation (manifest must carry the VectorIndexConfig).
   - fix: Persist the single index's VectorIndexConfig (Type/Metric/M/EfConstruction/EfSearch) in the manifest per §4.8 and load it into s.gcfg on recovery. Ensure every newHNSWIndex(...) for search (both live and reopened) is constructed with s.gcfg.EfSearch (and M for any rebuild). Add a recovery test with a non-default efSearch asserting it survives restart.
26. **[HIGH] architecture-fidelity** — Task 9 Search constructs a fresh hnswIndex per query under RLock (store.go)
   - issue: Search creates `idx := newHNSWIndex(g, withGraphEfSearch(...))` inside the per-query loop for every indexed sealed segment on every Search call. newHNSWIndex computes h.mL = 1/log(M) and allocates the wrapper each time; more importantly it couples query-time behavior to whatever default M the fresh index picks (M is not loaded from the graph), and it is wasteful allocation on the hottest path. The graph store (topology) is the durable object; the hnswIndex wrapper holds the search params. Re-deriving it per query per segment is both an efficiency regression and a correctness hazard (params can diverge from build params).
   - fix: Construct one hnswIndex per (segment graph) once when the graph is installed/opened (store it alongside the segGraphStore in s.graphs, e.g. map segId -> *builtIndex{store,index}), built with the manifest's index config. Search then reuses it (idx.search is already RLock-safe via the index's own mu). This also fixes the M/efSearch divergence from the prior finding.
