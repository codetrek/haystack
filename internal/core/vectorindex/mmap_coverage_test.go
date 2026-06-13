package vectorindex

import (
	"encoding/binary"
	"path/filepath"
	"testing"
)

// TestMmapStoreCloseMmaps exercises the closeMmaps cleanup helper directly.
// In production it is only reached from OpenMmapStore error paths, so normal
// tests never cover it; calling it on a healthy store covers the unmap/close
// logic without needing fault injection.
func TestMmapStoreCloseMmaps(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	s.closeMmaps()
	// closeMmaps does not touch the WAL or idmap handles; release them so the
	// test leaks no descriptors.
	if s.wal != nil {
		_ = s.wal.Close()
	}
	if s.idmapFile != nil {
		_ = s.idmapFile.Close()
	}
}

// TestOpenWALMissingParentDir covers OpenWAL's open-error branch by pointing it
// at a directory whose parents do not exist.
func TestOpenWALMissingParentDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no", "such", "dir")
	if _, err := OpenWAL(missing); err == nil {
		t.Fatal("expected error opening WAL in a nonexistent directory")
	}
}

// TestWALReplayPayloadTooLarge covers Replay's corruption guard: a record whose
// declared payload length exceeds maxWalPayloadSize must be rejected rather than
// triggering a huge allocation.
func TestWALReplayPayloadTooLarge(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if _, err := w.Append(WalInsert, []byte("x"), false); err != nil {
		t.Fatal(err)
	}

	// Overwrite the record's Length field (bytes 8..12) with an over-large value.
	huge := make([]byte, 4)
	binary.LittleEndian.PutUint32(huge, uint32(maxWalPayloadSize+1))
	if _, err := w.file.WriteAt(huge, 8); err != nil {
		t.Fatal(err)
	}

	err = w.Replay(0, func(uint64, WalRecordType, []byte) error { return nil })
	if err == nil {
		t.Fatal("expected corruption error for oversized payload length")
	}
}

// TestSetNeighborsUpperInvalidArgs covers setNeighborsUpper's argument-validation
// branches: a node with no upper slot, and a node id beyond capacity.
func TestSetNeighborsUpperInvalidArgs(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	vec := []float32{1, 0, 0, 0}
	if err := s.PutNode(0, 0, vec); err != nil { // level-0 node => no upper slot
		t.Fatal(err)
	}

	// Upper-layer write on a node that has no upper slot allocated.
	if err := s.SetNeighbors(0, 1, []uint64{1}); err == nil {
		t.Fatal("expected error: node has no upper slot")
	}

	// readUpperSlot rejects an id beyond node capacity.
	if err := s.SetNeighbors(1<<40, 1, []uint64{1}); err == nil {
		t.Fatal("expected error: node id out of range")
	}
}
