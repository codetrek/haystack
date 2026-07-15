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
