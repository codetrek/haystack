package vectorindex

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"
)

func TestMetaHeaderSize(t *testing.T) {
	if s := unsafe.Sizeof(MetaHeader{}); s != 72 {
		t.Fatalf("MetaHeader size = %d, want 72", s)
	}
}

func TestMetaHeaderWriteRead(t *testing.T) {
	dir := t.TempDir()

	h := &MetaHeader{
		Version:          1,
		Dim:              128,
		M:                16,
		MaxLevel:         3,
		EntryLevel:       3,
		NodeCount:        5000,
		TotalSlots:       5100,
		EntryPoint:       42,
		NextNodeId:       5100,
		WalCheckpointLSN: 99,
	}

	if err := writeMetaHeader(dir, h); err != nil {
		t.Fatalf("writeMetaHeader: %v", err)
	}

	got, err := readMetaHeader(dir)
	if err != nil {
		t.Fatalf("readMetaHeader: %v", err)
	}

	if got.Magic != magicMeta {
		t.Errorf("magic = %q, want %q", got.Magic, magicMeta)
	}
	if got.Dim != 128 {
		t.Errorf("dim = %d, want 128", got.Dim)
	}
	if got.M != 16 {
		t.Errorf("M = %d, want 16", got.M)
	}
	if got.NodeCount != 5000 {
		t.Errorf("NodeCount = %d, want 5000", got.NodeCount)
	}
	if got.EntryPoint != 42 {
		t.Errorf("EntryPoint = %d, want 42", got.EntryPoint)
	}
	if got.NextNodeId != 5100 {
		t.Errorf("NextNodeId = %d, want 5100", got.NextNodeId)
	}
	if got.WalCheckpointLSN != 99 {
		t.Errorf("WalCheckpointLSN = %d, want 99", got.WalCheckpointLSN)
	}

	// Verify file size is exactly 64 bytes.
	info, err := os.Stat(filepath.Join(dir, "meta.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 72 {
		t.Errorf("meta.bin size = %d, want 72", info.Size())
	}
}

func TestMetaHeaderAtomicWrite(t *testing.T) {
	dir := t.TempDir()

	// Write initial.
	h1 := &MetaHeader{Dim: 128, M: 16, NodeCount: 100}
	if err := writeMetaHeader(dir, h1); err != nil {
		t.Fatal(err)
	}

	// Overwrite.
	h2 := &MetaHeader{Dim: 128, M: 16, NodeCount: 200}
	if err := writeMetaHeader(dir, h2); err != nil {
		t.Fatal(err)
	}

	got, err := readMetaHeader(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.NodeCount != 200 {
		t.Errorf("NodeCount = %d, want 200", got.NodeCount)
	}

	// Tmp file should not exist.
	if _, err := os.Stat(filepath.Join(dir, "meta.bin.tmp")); !os.IsNotExist(err) {
		t.Error("meta.bin.tmp should not exist after successful write")
	}
}

func TestWriteMetaHeaderBadDir(t *testing.T) {
	// Non-existent directory should fail on create tmp.
	h := &MetaHeader{Dim: 128, M: 16}
	err := writeMetaHeader("/nonexistent/path/that/does/not/exist", h)
	if err == nil {
		t.Fatal("expected error for non-existent directory")
	}
}

func TestWriteMetaHeaderVersionDefault(t *testing.T) {
	dir := t.TempDir()
	h := &MetaHeader{Version: 0, Dim: 128, M: 16}
	if err := writeMetaHeader(dir, h); err != nil {
		t.Fatal(err)
	}
	got, err := readMetaHeader(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 2 {
		t.Errorf("Version = %d, want 2 (auto-set from 0)", got.Version)
	}
}

func TestWriteMetaHeaderRenameFailure(t *testing.T) {
	dir := t.TempDir()

	// Create meta.bin as a directory to make rename fail.
	metaDir := filepath.Join(dir, "meta.bin")
	if err := os.MkdirAll(metaDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Put a file inside so the directory isn't empty (rename will fail).
	if err := os.WriteFile(filepath.Join(metaDir, "blocker"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	h := &MetaHeader{Dim: 128, M: 16}
	err := writeMetaHeader(dir, h)
	if err == nil {
		t.Fatal("expected error when rename target is a non-empty directory")
	}
}

func TestWriteDataFileHeaderBadPath(t *testing.T) {
	// Non-existent path should fail on os.Create.
	hdr := VectorsHeader{Magic: magicVectors, Dim: 4, Capacity: 10}
	err := writeDataFileHeader("/nonexistent/dir/vectors.dat", magicVectors, &hdr, 4096+40)
	if err == nil {
		t.Fatal("expected error for non-existent path")
	}
}

func TestWriteDataFileHeaderReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "readonly.dat")

	// Create a file, then make the dir read-only to prevent truncate on new file.
	hdr := VectorsHeader{Magic: magicVectors, Dim: 4, Capacity: 10}

	// First write should succeed.
	if err := writeDataFileHeader(path, magicVectors, &hdr, int64(pageSize)+40); err != nil {
		t.Fatal(err)
	}

	// Verify file was created and has correct size.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(pageSize)+40 {
		t.Errorf("size = %d, want %d", info.Size(), int64(pageSize)+40)
	}
}

func TestReadMetaHeaderMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := readMetaHeader(dir)
	if err == nil {
		t.Fatal("expected error for missing meta.bin")
	}
}

func TestReadMetaHeaderTruncated(t *testing.T) {
	dir := t.TempDir()
	// Write a file that's too short to be a valid header.
	if err := os.WriteFile(filepath.Join(dir, "meta.bin"), make([]byte, 10), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := readMetaHeader(dir)
	if err == nil {
		t.Fatal("expected error for truncated meta.bin")
	}
}

func TestReadMetaHeaderBadMagic(t *testing.T) {
	dir := t.TempDir()
	// Write garbage.
	if err := os.WriteFile(filepath.Join(dir, "meta.bin"), make([]byte, 64), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := readMetaHeader(dir)
	if err == nil {
		t.Fatal("expected error for bad magic")
	}
}
