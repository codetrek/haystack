# HAY-007 MmapStore Phase 1 — Code Review

> Reviewer: Claude (automated)
> Date: 2026-04-17
> PR: codetrek/haystack#53
> Branch: feat/hay007-mmap-store

---

## Checklist

### 1. Header 4096 page-aligned — PASS

`pageSize = 4096` (`mmap_format.go:22`). All data files use `pageSize` as header region size. `writeDataFileHeader` writes the struct at offset 0 then `Truncate` to `pageSize + capacity * slotSize`, so data starts at offset 4096. Confirmed in `initAllFiles` for all four files.

### 2. GetVectorRef returns copy (not mmap slice) — PASS

`GetVectorRef` delegates to `GetVector` (`mmap_store_read.go:29`). `GetVector` allocates a fresh `[]float32` via `make` and decodes each float individually from the mmap region (`mmap_store_read.go:20-24`). No mmap-backed slice escapes.

### 3. mmap syscall self-contained (no third-party library) — PASS

- Unix: `mmap_unix.go` calls `syscall.Mmap` / `syscall.Munmap` directly.
- Windows: `mmap_windows.go` calls `syscall.CreateFileMapping` / `syscall.MapViewOfFile` / `syscall.UnmapViewOfFile` directly.
- No external mmap packages in imports.

### 4. Zero CGo — PASS

No `import "C"` in any mmap file. All platform code uses Go's `syscall` package. Compatible with `CGO_ENABLED=0`.

### 5. Little-endian — PASS

All binary reads use `binary.LittleEndian` exclusively. `MetaHeader` written/read via `binary.Write/Read(f, binary.LittleEndian, ...)`. Individual field reads in `mmap_store_read.go` all use `binary.LittleEndian.Uint32/Uint64`.

### 6. MetaHeader compile-time size check — PASS

`mmap_format.go:42`:
```go
var _ [64]byte = [unsafe.Sizeof(MetaHeader{})]byte{}
```
Compile fails if `MetaHeader` is not exactly 64 bytes. Additionally, `TestMetaHeaderSize` provides a runtime assertion.

### 7. Test coverage — PASS (good)

| Test file | Coverage |
|-----------|----------|
| `mmap_test.go` | mmap alloc/free, write-through, zero-length, nil free |
| `mmap_format_test.go` | MetaHeader size, write/read roundtrip, atomic write (tmp cleanup), bad magic |
| `mmap_store_test.go` | OpenMmapStore create/close/reopen, capacity checks, file size validation, param mismatch, invalid opts |
| `mmap_store_read_test.go` | GetVector, GetVectorRef, GetNeighborsL0, GetNeighborsUpper, GetNorm, GetNodeLevel, deleted node, GetEntryPoint, GetNodeId, out-of-range |
| `mmap_integration_test.go` | Full end-to-end: hand-build binary files → open → verify all read paths |
| `mmap_export_test.go` | MemStore→MmapStore export (1000 vectors), roundtrip verify, close+reopen persistence |

Coverage is thorough for a read-only Phase 1. All boundary/error paths tested.

### 8. Code quality — PASS (good)

**Strengths:**
- Clean separation: `mmap.go` (API) / `mmap_unix.go` + `mmap_windows.go` (platform) / `mmap_format.go` (on-disk layout) / `mmap_store.go` (lifecycle) / `mmap_store_read.go` (read paths).
- Struct field ordering in `MetaHeader` explicitly avoids implicit padding (uint32 group then uint64 group).
- Atomic meta writes via write-tmp-fsync-rename pattern.
- Neighbor count clamped to `mmax0`/`m` before reading — defensive against corrupt data.
- `Close()` collects first error without short-circuiting — all resources released even on partial failure.

**Minor observations (non-blocking):**
- `docToNode`/`nodeToDoc` maps have no mutex protection in `GetNodeId`. Since Phase 1 is read-only after open this is fine, but should be addressed when write paths are added.
- `mmapAll` reads header capacities by hardcoded byte offsets (`vectors[8:16]`, etc.) rather than decoding the header struct. Correct but fragile — a comment noting the offsets match the struct layout would help.
- `GraphUpperHeader.NextSlot` is defined in the struct and written in integration tests, but never read by `mmapAll` or stored in `MmapStore`. Phase 2 will need it.

### 9. Bugs and security issues

**No bugs found.** All read paths have bounds checks against capacity before indexing into mmap slices. Neighbor counts are clamped. Out-of-range IDs return errors.

**No security issues.** No user-controlled input reaches mmap offsets without validation. No unchecked arithmetic overflow risk at the target scale (500K vectors × 768d = ~1.5 GB, well within int range on 64-bit).

**One edge case to note (non-blocking):** `getNeighborsUpper` acquires `muGraph.RLock` then `muNodes.RLock` inside it (`readUpperSlot`). If a future write path locks in the opposite order, this is a deadlock. The lock ordering should be documented now.

---

## Verdict

**APPROVE** — Phase 1 read-only MmapStore is clean, correct, well-tested, and meets all design constraints. The three minor observations above are non-blocking and can be addressed in Phase 2.
