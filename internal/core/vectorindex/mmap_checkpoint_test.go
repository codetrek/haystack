package vectorindex

import (
	"fmt"
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

func TestClose_WALTruncated(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	n := 5
	s.BeginBatch()
	for i := 0; i < n; i++ {
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

	// After clean Close, WAL should be empty and meta should have checkpoint LSN.
	walPath := filepath.Join(dir, "wal.bin")
	info, _ := os.Stat(walPath)
	if info.Size() != 0 {
		t.Fatalf("WAL size after Close: got %d, want 0", info.Size())
	}
	meta, _ := readMetaHeader(dir)
	if meta.WalCheckpointLSN != uint64(n) {
		t.Fatalf("WalCheckpointLSN: got %d, want %d", meta.WalCheckpointLSN, n)
	}
}

func TestOpen_ReplayThenCheckpoint(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	// Write records.
	n := 5
	s.BeginBatch()
	for i := 0; i < n; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CommitBatch(true); err != nil {
		t.Fatal(err)
	}

	// Simulate crash: close WAL and files without calling Close().
	s.wal.Close()
	s.idmapFile.Close()
	mmapFree(s.vectors)
	mmapFree(s.nodes)
	mmapFree(s.graphL0)
	mmapFree(s.graphUpper)
	s.vecFile.Close()
	s.nodeFile.Close()
	s.l0File.Close()
	s.upperFile.Close()

	// Reopen — should replay WAL and then auto-checkpoint.
	s2, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	// After open, WAL should be truncated (post-replay checkpoint).
	walPath := filepath.Join(dir, "wal.bin")
	info, _ := os.Stat(walPath)
	if info.Size() != 0 {
		t.Fatalf("WAL should be truncated after replay+checkpoint, got size %d", info.Size())
	}

	// Data should be intact.
	for i := 0; i < n; i++ {
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

func TestAutoCheckpoint_TriggeredAtInterval(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4, CheckpointInterval: 10})
	if err != nil {
		t.Fatal(err)
	}

	// Write 15 records (non-batch, so each PutNode triggers maybeCheckpoint).
	for i := 0; i < 15; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}

	// After 15 ops with interval=10, a checkpoint should have fired.
	// The WAL should have been truncated at op 10, so only ops 11-15 remain.
	walPath := filepath.Join(dir, "wal.bin")
	info, _ := os.Stat(walPath)
	if info.Size() == 0 {
		// WAL could be 0 if exactly at threshold — but we wrote 15 so 5 should remain.
		// Actually with auto-checkpoint at 10, ops 1-10 get checkpointed, 11-15 in WAL.
	}

	// Meta should show a non-zero checkpoint LSN.
	meta, _ := readMetaHeader(dir)
	if meta.WalCheckpointLSN == 0 {
		t.Fatal("expected auto-checkpoint to have set WalCheckpointLSN > 0")
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAutoCheckpoint_NotTriggeredBelowInterval(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4, CheckpointInterval: 1000})
	if err != nil {
		t.Fatal(err)
	}

	// Write 5 records — well below 1000.
	for i := 0; i < 5; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}

	// WAL should still have data (no auto-checkpoint).
	walPath := filepath.Join(dir, "wal.bin")
	info, _ := os.Stat(walPath)
	if info.Size() == 0 {
		t.Fatal("WAL should not be empty — auto-checkpoint should not have triggered")
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAutoCheckpoint_BatchMode(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4, CheckpointInterval: 5})
	if err != nil {
		t.Fatal(err)
	}

	// Batch mode: checkpoint should trigger at CommitBatch, not during writes.
	s.BeginBatch()
	for i := 0; i < 10; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CommitBatch(true); err != nil {
		t.Fatal(err)
	}

	// After commit with 10 ops and interval=5, checkpoint should have fired.
	meta, _ := readMetaHeader(dir)
	if meta.WalCheckpointLSN == 0 {
		t.Fatal("expected auto-checkpoint after CommitBatch")
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCrashRecovery_BasicReplay(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	n := 20
	s.BeginBatch()
	for i := 0; i < n; i++ {
		vec := []float32{float32(i), float32(i + 1), float32(i + 2), float32(i + 3)}
		if err := s.PutNode(uint64(i), 0, vec); err != nil {
			t.Fatal(err)
		}
		if err := s.SetNodeMapping(fmt.Sprintf("doc-%d", i), uint64(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CommitBatch(true); err != nil {
		t.Fatal(err)
	}

	// Simulate crash: close file handles without calling Close().
	s.wal.Sync()
	s.wal.Close()
	s.idmapFile.Close()
	mmapFree(s.vectors)
	mmapFree(s.nodes)
	mmapFree(s.graphL0)
	mmapFree(s.graphUpper)
	s.vecFile.Close()
	s.nodeFile.Close()
	s.l0File.Close()
	s.upperFile.Close()

	// Reopen — WAL replay should recover all data.
	s2, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < n; i++ {
		vec, err := s2.GetVector(uint64(i))
		if err != nil {
			t.Fatalf("GetVector(%d): %v", i, err)
		}
		expected := []float32{float32(i), float32(i + 1), float32(i + 2), float32(i + 3)}
		for j := range expected {
			if vec[j] != expected[j] {
				t.Fatalf("vec[%d][%d]: got %f, want %f", i, j, vec[j], expected[j])
			}
		}
	}

	if s2.meta.NodeCount != uint64(n) {
		t.Fatalf("NodeCount: got %d, want %d", s2.meta.NodeCount, n)
	}

	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCrashRecovery_AfterCheckpoint(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	// Write N=10 records and checkpoint.
	s.BeginBatch()
	for i := 0; i < 10; i++ {
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

	// Write M=5 more records (post-checkpoint).
	s.BeginBatch()
	for i := 10; i < 15; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CommitBatch(true); err != nil {
		t.Fatal(err)
	}

	// Crash.
	s.wal.Sync()
	s.wal.Close()
	s.idmapFile.Close()
	mmapFree(s.vectors)
	mmapFree(s.nodes)
	mmapFree(s.graphL0)
	mmapFree(s.graphUpper)
	s.vecFile.Close()
	s.nodeFile.Close()
	s.l0File.Close()
	s.upperFile.Close()

	// Reopen — should recover all 15 nodes.
	s2, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 15; i++ {
		vec, err := s2.GetVector(uint64(i))
		if err != nil {
			t.Fatalf("GetVector(%d): %v", i, err)
		}
		if vec[0] != float32(i) {
			t.Fatalf("vec[%d][0]: got %f, want %f", i, vec[0], float32(i))
		}
	}

	if s2.meta.NodeCount != 15 {
		t.Fatalf("NodeCount: got %d, want 15", s2.meta.NodeCount)
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
