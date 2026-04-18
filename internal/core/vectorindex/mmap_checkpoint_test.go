package vectorindex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpoint_MetaAndWALTruncated(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	// Write N records via batch.
	n := 5
	s.BeginBatch()
	for i := 0; i < n; i++ {
		vec := []float32{float32(i), 0, 0, 1}
		if err := s.PutNode(uint64(i), 0, vec); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CommitBatch(true); err != nil {
		t.Fatal(err)
	}

	// WAL should have data before checkpoint.
	walPath := filepath.Join(dir, "wal.bin")
	info, _ := os.Stat(walPath)
	if info.Size() == 0 {
		t.Fatal("WAL should not be empty before checkpoint")
	}

	if err := s.Checkpoint(); err != nil {
		t.Fatal(err)
	}

	// Verify meta.bin WalCheckpointLSN = n.
	meta, err := readMetaHeader(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.WalCheckpointLSN != uint64(n) {
		t.Fatalf("WalCheckpointLSN: got %d, want %d", meta.WalCheckpointLSN, n)
	}

	// Verify WAL is truncated to 0.
	info, _ = os.Stat(walPath)
	if info.Size() != 0 {
		t.Fatalf("WAL size after checkpoint: got %d, want 0", info.Size())
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpoint_ContinueWriteAndReplay(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	// Write 3 records, checkpoint.
	s.BeginBatch()
	for i := 0; i < 3; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CommitBatch(true); err != nil {
		t.Fatal(err)
	}
	if err := s.Checkpoint(); err != nil {
		t.Fatal(err)
	}

	// Write 2 more records after checkpoint.
	s.BeginBatch()
	for i := 3; i < 5; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CommitBatch(true); err != nil {
		t.Fatal(err)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen — replay should only see the 2 new records.
	s2, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	// All 5 nodes should be present.
	for i := 0; i < 5; i++ {
		vec, err := s2.GetVector(uint64(i))
		if err != nil {
			t.Fatalf("GetVector(%d): %v", i, err)
		}
		if vec[0] != float32(i) {
			t.Fatalf("vec[%d][0]: got %f, want %f", i, vec[0], float32(i))
		}
	}

	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpoint_LSNMonotonic(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	// Write 3 records.
	for i := 0; i < 3; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	lsnBefore := s.wal.LSN()

	if err := s.Checkpoint(); err != nil {
		t.Fatal(err)
	}

	// Write after checkpoint: LSN must be > checkpoint LSN.
	if err := s.PutNode(10, 0, []float32{10, 0, 0, 1}); err != nil {
		t.Fatal(err)
	}
	lsnAfter := s.wal.LSN()

	if lsnAfter <= lsnBefore {
		t.Fatalf("LSN after reset not monotonic: before=%d, after=%d", lsnBefore, lsnAfter)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
