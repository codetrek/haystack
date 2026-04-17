package vectorindex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMmapAllocFree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.dat")

	// Create a file with some content.
	data := make([]byte, 4096)
	for i := range data {
		data[i] = byte(i % 251)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// mmap the file.
	mapped, err := mmapAlloc(f.Fd(), 0, 4096, mmapRead|mmapWrite)
	if err != nil {
		t.Fatalf("mmapAlloc: %v", err)
	}

	// Verify content matches.
	for i := 0; i < len(data); i++ {
		if mapped[i] != data[i] {
			t.Fatalf("byte %d: got %d, want %d", i, mapped[i], data[i])
		}
	}

	// Write through mmap.
	mapped[0] = 0xAB
	mapped[4095] = 0xCD

	// Unmap.
	if err := mmapFree(mapped); err != nil {
		t.Fatalf("mmapFree: %v", err)
	}

	// Read back from file to verify write-through.
	readBack, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if readBack[0] != 0xAB {
		t.Fatalf("byte 0: got %d, want 0xAB", readBack[0])
	}
	if readBack[4095] != 0xCD {
		t.Fatalf("byte 4095: got %d, want 0xCD", readBack[4095])
	}
}

func TestMmapAllocZeroLength(t *testing.T) {
	_, err := mmapAlloc(0, 0, 0, mmapRead)
	if err == nil {
		t.Fatal("expected error for zero length")
	}
}

func TestMmapFreeEmpty(t *testing.T) {
	if err := mmapFree(nil); err != nil {
		t.Fatalf("mmapFree(nil): %v", err)
	}
	if err := mmapFree([]byte{}); err != nil {
		t.Fatalf("mmapFree(empty): %v", err)
	}
}
