package vectorindex

import (
	"os"
	"testing"
)

// --- OpenMmapStore + initAllFiles + mmapAll: failures during open ---

func TestOpenMmapStoreInitMetaError(t *testing.T) {
	wrapNextCreate(t, "meta.bin.tmp", faultFile{failWrite: true})
	if _, err := OpenMmapStore(t.TempDir(), MmapStoreOptions{Dim: 4, M: 4}); err == nil {
		t.Fatal("expected init error (meta write)")
	}
}

func TestOpenMmapStoreInitDataFileError(t *testing.T) {
	wrapNextCreate(t, "vectors.dat", faultFile{failWrite: true})
	if _, err := OpenMmapStore(t.TempDir(), MmapStoreOptions{Dim: 4, M: 4}); err == nil {
		t.Fatal("expected init error (vectors.dat write)")
	}
}

func TestOpenMmapStoreInitNodesError(t *testing.T) {
	wrapNextCreate(t, "nodes.dat", faultFile{failWrite: true})
	if _, err := OpenMmapStore(t.TempDir(), MmapStoreOptions{Dim: 4, M: 4}); err == nil {
		t.Fatal("expected init error (nodes.dat write)")
	}
}

func TestOpenMmapStoreInitL0Error(t *testing.T) {
	wrapNextCreate(t, "graph_l0.dat", faultFile{failWrite: true})
	if _, err := OpenMmapStore(t.TempDir(), MmapStoreOptions{Dim: 4, M: 4}); err == nil {
		t.Fatal("expected init error (graph_l0.dat write)")
	}
}

func TestOpenMmapStoreInitUpperError(t *testing.T) {
	wrapNextCreate(t, "graph_upper.dat", faultFile{failWrite: true})
	if _, err := OpenMmapStore(t.TempDir(), MmapStoreOptions{Dim: 4, M: 4}); err == nil {
		t.Fatal("expected init error (graph_upper.dat write)")
	}
}

func TestOpenMmapStoreMmapError(t *testing.T) {
	orig := mmapAlloc
	defer func() { mmapAlloc = orig }()
	mmapAlloc = func(fd uintptr, off int64, length, flags int) ([]byte, error) {
		return nil, errInjected
	}
	if _, err := OpenMmapStore(t.TempDir(), MmapStoreOptions{Dim: 4, M: 4}); err == nil {
		t.Fatal("expected mmap error")
	}
}

func TestMmapAllStatError(t *testing.T) {
	orig := fsOpenFile
	defer func() { fsOpenFile = orig }()
	fsOpenFile = func(name string, flag int, perm os.FileMode) (osFile, error) {
		f, err := orig(name, flag, perm)
		if err != nil {
			return nil, err
		}
		if contains(name, ".dat") {
			return &faultFile{osFile: f, failStat: true}, nil
		}
		return f, nil
	}
	if _, err := OpenMmapStore(t.TempDir(), MmapStoreOptions{Dim: 4, M: 4}); err == nil {
		t.Fatal("expected stat error in mmapAll")
	}
}

func TestMmapAllOpenError(t *testing.T) {
	orig := fsOpenFile
	defer func() { fsOpenFile = orig }()
	fsOpenFile = func(name string, flag int, perm os.FileMode) (osFile, error) {
		if contains(name, "nodes.dat") {
			return nil, errInjected
		}
		return orig(name, flag, perm)
	}
	if _, err := OpenMmapStore(t.TempDir(), MmapStoreOptions{Dim: 4, M: 4}); err == nil {
		t.Fatal("expected open error in mmapAll")
	}
}

func TestOpenMmapStoreWALError(t *testing.T) {
	orig := fsOpenFile
	defer func() { fsOpenFile = orig }()
	fsOpenFile = func(name string, flag int, perm os.FileMode) (osFile, error) {
		if contains(name, "wal.bin") {
			return nil, errInjected
		}
		return orig(name, flag, perm)
	}
	if _, err := OpenMmapStore(t.TempDir(), MmapStoreOptions{Dim: 4, M: 4}); err == nil {
		t.Fatal("expected WAL open error")
	}
}

// --- remapFile grow error paths ---

func TestRemapFileMunmapError(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	orig := mmapFree
	defer func() { mmapFree = orig }()
	mmapFree = func([]byte) error { return errInjected }
	// munmap fails first; remapFile returns the error and leaves *data/*cap
	// untouched, so the store stays usable on the old mapping.
	if err := s.remapFile(s.vecFile, &s.vectors, &s.vecCapacity, s.vecCapacity+1, s.vecSlotSize, 8); err == nil {
		t.Fatal("expected munmap error")
	}
}

func TestRemapFileTruncateError(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	// Operate on a throwaway file+mapping so the store's real vectors region is
	// left intact: remapFile unmaps + nils its slice before truncating, so a
	// truncate failure on the real region would zero the store's capacity.
	f, err := fsCreate(t.TempDir() + "/throw.dat")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(int64(pageSize) + 16); err != nil {
		t.Fatal(err)
	}
	data, err := mmapAlloc(f.Fd(), 0, pageSize+16, mmapRead|mmapWrite)
	if err != nil {
		t.Fatal(err)
	}
	c := uint64(1)
	ff := &faultFile{osFile: f, failTruncate: true}
	if err := s.remapFile(ff, &data, &c, 2, 16, 8); err == nil {
		t.Fatal("expected truncate error")
	}
}

func TestRemapFileMmapError(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	f, err := fsCreate(t.TempDir() + "/throw.dat")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(int64(pageSize) + 16); err != nil {
		t.Fatal(err)
	}
	data, err := mmapAlloc(f.Fd(), 0, pageSize+16, mmapRead|mmapWrite)
	if err != nil {
		t.Fatal(err)
	}
	c := uint64(1)
	orig := mmapAlloc
	mmapAlloc = func(uintptr, int64, int, int) ([]byte, error) { return nil, errInjected }
	err = s.remapFile(f, &data, &c, 2, 16, 8)
	mmapAlloc = orig
	if err == nil {
		t.Fatal("expected mmap error")
	}
}
