package vectorindex

import (
	"errors"
	"os"
	"testing"
)

// errInjected is the canonical fault returned by injected failures.
var errInjected = errors.New("injected fault")

// faultFile wraps an osFile and fails the selected operations with errInjected.
// Unset operations delegate to the embedded file.
type faultFile struct {
	osFile
	failWrite    bool
	failSync     bool
	failClose    bool
	failTruncate bool
	failSeek     bool
	failStat     bool
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

func (f *faultFile) Close() error {
	// Always release the underlying handle even when injecting a failure, so
	// tests never leak an fd / file lock (which on Windows blocks the TempDir
	// cleanup from deleting the file).
	err := f.osFile.Close()
	if f.failClose {
		return errInjected
	}
	return err
}

func (f *faultFile) Truncate(size int64) error {
	if f.failTruncate {
		return errInjected
	}
	return f.osFile.Truncate(size)
}

func (f *faultFile) Seek(off int64, whence int) (int64, error) {
	if f.failSeek {
		return 0, errInjected
	}
	return f.osFile.Seek(off, whence)
}

func (f *faultFile) Stat() (os.FileInfo, error) {
	if f.failStat {
		return nil, errInjected
	}
	return f.osFile.Stat()
}

// wrapNextCreate makes the next fsCreate whose path contains match return a
// faultFile configured by tmpl (its osFile is filled in). Restored via Cleanup.
func wrapNextCreate(t *testing.T, match string, tmpl faultFile) {
	t.Helper()
	orig := fsCreate
	t.Cleanup(func() { fsCreate = orig })
	fsCreate = func(name string) (osFile, error) {
		f, err := orig(name)
		if err != nil {
			return nil, err
		}
		if match == "" || contains(name, match) {
			ff := tmpl
			ff.osFile = f
			return &ff, nil
		}
		return f, nil
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// openTestStore opens a small MmapStore in a temp dir and guarantees it is
// closed at test end (so no test leaks mmaps/fds, which on Windows would block
// the t.TempDir cleanup from deleting the files).
func openTestStore(t *testing.T) *MmapStore {
	t.Helper()
	s, err := OpenMmapStore(t.TempDir(), MmapStoreOptions{Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// --- init / endianness guard ---

func TestCheckEndianProbeLittleEndian(t *testing.T) {
	checkEndianProbe(0x01020304) // low byte 0x04 on little-endian: must not panic
}

func TestCheckEndianProbeNonLittleEndianPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for non-0x04 low byte")
		}
	}()
	checkEndianProbe(0x00000000) // low byte 0x00 != 0x04: panics
}

// --- writeMetaHeader IO error paths ---

func TestWriteMetaHeaderWriteError(t *testing.T) {
	wrapNextCreate(t, "meta.bin.tmp", faultFile{failWrite: true})
	if err := writeMetaHeader(t.TempDir(), &MetaHeader{Dim: 4, M: 4}); err == nil {
		t.Fatal("expected write error")
	}
}

func TestWriteMetaHeaderSyncError(t *testing.T) {
	wrapNextCreate(t, "meta.bin.tmp", faultFile{failSync: true})
	if err := writeMetaHeader(t.TempDir(), &MetaHeader{Dim: 4, M: 4}); err == nil {
		t.Fatal("expected sync error")
	}
}

func TestWriteMetaHeaderCloseError(t *testing.T) {
	wrapNextCreate(t, "meta.bin.tmp", faultFile{failClose: true})
	if err := writeMetaHeader(t.TempDir(), &MetaHeader{Dim: 4, M: 4}); err == nil {
		t.Fatal("expected close error")
	}
}

// --- writeDataFileHeader IO error paths ---

func TestWriteDataFileHeaderWriteError(t *testing.T) {
	wrapNextCreate(t, "", faultFile{failWrite: true})
	path := t.TempDir() + "/x.dat"
	if err := writeDataFileHeader(path, magicVectors, &VectorsHeader{Magic: magicVectors, Dim: 4, Capacity: 1}, int64(pageSize)); err == nil {
		t.Fatal("expected write error")
	}
}

func TestWriteDataFileHeaderTruncateError(t *testing.T) {
	wrapNextCreate(t, "", faultFile{failTruncate: true})
	path := t.TempDir() + "/x.dat"
	if err := writeDataFileHeader(path, magicVectors, &VectorsHeader{Magic: magicVectors, Dim: 4, Capacity: 1}, int64(pageSize)); err == nil {
		t.Fatal("expected truncate error")
	}
}

// --- syncAll msync error paths ---

func TestSyncAllMsyncErrors(t *testing.T) {
	for n := 1; n <= 4; n++ {
		s := openTestStore(t)
		orig := mmapSync
		count := 0
		mmapSync = func(data []byte) error {
			count++
			if count == n {
				return errInjected
			}
			return orig(data)
		}
		err := s.syncAll()
		mmapSync = orig
		if err == nil {
			t.Fatalf("expected msync error on call %d", n)
		}
		_ = s.Close()
	}
}

// --- WAL Close flush error ---

func TestWALCloseFlushError(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Point the buffered writer at a file that fails writes, then buffer some
	// bytes so the Flush inside Close must write them (and fail).
	w.file = &faultFile{osFile: w.file, failWrite: true}
	w.buf.Reset(w.file)
	if _, err := w.buf.WriteString("pending"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err == nil {
		t.Fatal("expected flush error from Close")
	}
}

// --- OpenWAL scanLSN error (Truncate fails) ---

func TestOpenWALScanError(t *testing.T) {
	orig := fsOpenFile
	defer func() { fsOpenFile = orig }()
	fsOpenFile = func(name string, flag int, perm os.FileMode) (osFile, error) {
		f, err := orig(name, flag, perm)
		if err != nil {
			return nil, err
		}
		return &faultFile{osFile: f, failTruncate: true}, nil
	}
	if _, err := OpenWAL(t.TempDir()); err == nil {
		t.Fatal("expected scanLSN truncate error")
	}
}

// --- store Sync WAL flush/sync errors ---

func TestStoreSyncWALErrors(t *testing.T) {
	s := openTestStore(t)
	s.wal.file = &faultFile{osFile: s.wal.file, failSync: true}
	if err := s.Sync(); err == nil {
		t.Fatal("expected WAL sync error from store Sync")
	}
}
