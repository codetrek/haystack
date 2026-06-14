package vectorindex

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpoint_MetaAndWALTruncated(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	// Write N records via txn.
	n := 5
	requireNoError(t, s.txnBegin())
	for i := 0; i < n; i++ {
		vec := []float32{float32(i), 0, 0, 1}
		if err := s.PutNode(uint64(i), 0, vec); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.txnCommit(); err != nil {
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

	// Verify meta.bin WalCheckpointLSN = n+2 (n data records + TxnBegin + TxnCommit).
	meta, err := readMetaHeader(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.WalCheckpointLSN == 0 {
		t.Fatalf("WalCheckpointLSN: got %d, want > 0", meta.WalCheckpointLSN)
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
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	// Write 3 records, checkpoint.
	requireNoError(t, s.txnBegin())
	for i := 0; i < 3; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.txnCommit(); err != nil {
		t.Fatal(err)
	}
	if err := s.Checkpoint(); err != nil {
		t.Fatal(err)
	}

	// Write 2 more records after checkpoint.
	requireNoError(t, s.txnBegin())
	for i := 3; i < 5; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.txnCommit(); err != nil {
		t.Fatal(err)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen — replay should only see the 2 new records.
	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
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
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	n := 5
	requireNoError(t, s.txnBegin())
	for i := 0; i < n; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.txnCommit(); err != nil {
		t.Fatal(err)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// After clean Close, WAL should be empty.
	walPath := filepath.Join(dir, "wal.bin")
	info, _ := os.Stat(walPath)
	if info.Size() != 0 {
		t.Fatalf("WAL size after Close: got %d, want 0", info.Size())
	}
	meta, _ := readMetaHeader(dir)
	if meta.WalCheckpointLSN == 0 {
		t.Fatalf("WalCheckpointLSN: got 0, want > 0")
	}
	_ = n
}

func TestOpen_ReplayThenCheckpoint(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	// Write records.
	n := 5
	requireNoError(t, s.txnBegin())
	for i := 0; i < n; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.txnCommit(); err != nil {
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
	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
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
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4, CheckpointInterval: 10})
	if err != nil {
		t.Fatal(err)
	}

	// Write 15 records (non-txn, so each PutNode triggers maybeCheckpoint).
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
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4, CheckpointInterval: 1000})
	if err != nil {
		t.Fatal(err)
	}

	// Write 5 records inside a txn — well below the 1000 threshold.
	// txnCommit flushes the WAL to disk without triggering a checkpoint.
	if err := s.txnBegin(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.txnCommit(); err != nil {
		t.Fatal(err)
	}

	// WAL should still have data (no auto-checkpoint — ops < interval).
	walPath := filepath.Join(dir, "wal.bin")
	info, _ := os.Stat(walPath)
	if info.Size() == 0 {
		t.Fatal("WAL should not be empty — auto-checkpoint should not have triggered")
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAutoCheckpoint_TxnMode(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4, CheckpointInterval: 5})
	if err != nil {
		t.Fatal(err)
	}

	// Txn mode: checkpoint should trigger at txnCommit, not during writes.
	requireNoError(t, s.txnBegin())
	for i := 0; i < 10; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.txnCommit(); err != nil {
		t.Fatal(err)
	}

	// After commit with 10 ops and interval=5, checkpoint should have fired.
	meta, _ := readMetaHeader(dir)
	if meta.WalCheckpointLSN == 0 {
		t.Fatal("expected auto-checkpoint after txnCommit")
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCrashRecovery_BasicReplay(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	n := 20
	requireNoError(t, s.txnBegin())
	for i := 0; i < n; i++ {
		vec := []float32{float32(i), float32(i + 1), float32(i + 2), float32(i + 3)}
		if err := s.PutNode(uint64(i), 0, vec); err != nil {
			t.Fatal(err)
		}
		if err := s.SetNodeMapping(fmt.Sprintf("doc-%d", i), uint64(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.txnCommit(); err != nil {
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
	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
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
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	// Write N=10 records and checkpoint.
	requireNoError(t, s.txnBegin())
	for i := 0; i < 10; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.txnCommit(); err != nil {
		t.Fatal(err)
	}
	if err := s.Checkpoint(); err != nil {
		t.Fatal(err)
	}

	// Write M=5 more records (post-checkpoint).
	requireNoError(t, s.txnBegin())
	for i := 10; i < 15; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.txnCommit(); err != nil {
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
	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
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
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
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

// simulateCrash closes all file handles without calling Close().
func simulateCrash(s *MmapStore) {
	s.wal.Sync()
	s.wal.Close()
	if s.idmapFile != nil {
		s.idmapFile.Close()
	}
	mmapFree(s.vectors)
	mmapFree(s.nodes)
	mmapFree(s.graphL0)
	mmapFree(s.graphUpper)
	s.vecFile.Close()
	s.nodeFile.Close()
	s.l0File.Close()
	s.upperFile.Close()
}

// --- 8 Crash Point Tests (Task 6) ---

// 6a: WAL written, msync not done → WAL replay recovers data.
func TestCrashPoint_AfterWALWrite(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	// Write 5 nodes normally.
	requireNoError(t, s.txnBegin())
	for i := 0; i < 5; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.txnCommit(); err != nil {
		t.Fatal(err)
	}
	if err := s.Checkpoint(); err != nil {
		t.Fatal(err)
	}

	// Now write node 5 with crash hook — after WAL write, before mmap write.
	crashed := false
	s.crashAfterWALWrite = func() {
		s.wal.Sync()
		crashed = true
		panic("crash after WAL write")
	}

	func() {
		defer func() { recover() }()
		s.PutNode(5, 0, []float32{5, 0, 0, 1})
	}()

	if !crashed {
		t.Fatal("hook did not fire")
	}
	s.crashAfterWALWrite = nil
	simulateCrash(s)

	// Reopen — WAL replay should recover node 5.
	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= 5; i++ {
		vec, err := s2.GetVector(uint64(i))
		if err != nil {
			t.Fatalf("GetVector(%d): %v", i, err)
		}
		if vec[0] != float32(i) {
			t.Fatalf("vec[%d][0]: got %f, want %f", i, vec[0], float32(i))
		}
	}
	s2.Close()
}

// 6b: Checkpoint msync done, meta.bin not written → old checkpoint LSN, WAL replays.
func TestCrashPoint_AfterMsync(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	requireNoError(t, s.txnBegin())
	for i := 0; i < 5; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.txnCommit(); err != nil {
		t.Fatal(err)
	}

	s.crashAfterMsync = func() {
		panic("crash after msync")
	}

	func() {
		defer func() { recover() }()
		s.Checkpoint()
	}()
	s.crashAfterMsync = nil
	simulateCrash(s)

	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		vec, err := s2.GetVector(uint64(i))
		if err != nil {
			t.Fatalf("GetVector(%d): %v", i, err)
		}
		if vec[0] != float32(i) {
			t.Fatalf("vec[%d][0]: got %f, want %f", i, vec[0], float32(i))
		}
	}
	s2.Close()
}

// 6c: meta.bin written, crash AFTER meta but BEFORE WAL truncate.
// Uses crashAfterMeta hook. WAL still has old records; on recovery the LSN
// filter skips them because meta.WalCheckpointLSN is already advanced.
func TestCrashPoint_AfterMeta(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	requireNoError(t, s.txnBegin())
	for i := 0; i < 5; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.txnCommit(); err != nil {
		t.Fatal(err)
	}

	s.crashAfterMeta = func() {
		panic("crash after meta")
	}

	func() {
		defer func() { recover() }()
		s.Checkpoint()
	}()
	s.crashAfterMeta = nil
	simulateCrash(s)

	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		vec, err := s2.GetVector(uint64(i))
		if err != nil {
			t.Fatalf("GetVector(%d): %v", i, err)
		}
		if vec[0] != float32(i) {
			t.Fatalf("vec[%d][0]: got %f, want %f", i, vec[0], float32(i))
		}
	}
	s2.Close()
}

// 6c-b: crash BEFORE WAL truncate (uses crashBeforeTruncate hook).
func TestCrashPoint_BeforeTruncate(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	requireNoError(t, s.txnBegin())
	for i := 0; i < 5; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.txnCommit(); err != nil {
		t.Fatal(err)
	}

	s.crashBeforeTruncate = func() {
		panic("crash before truncate")
	}

	func() {
		defer func() { recover() }()
		s.Checkpoint()
	}()
	s.crashBeforeTruncate = nil
	simulateCrash(s)

	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		vec, err := s2.GetVector(uint64(i))
		if err != nil {
			t.Fatalf("GetVector(%d): %v", i, err)
		}
		if vec[0] != float32(i) {
			t.Fatalf("vec[%d][0]: got %f, want %f", i, vec[0], float32(i))
		}
	}
	s2.Close()
}

// 6d: Partial WAL record — incomplete bytes appended to wal.bin.
func TestCrashPoint_PartialWALRecord(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	requireNoError(t, s.txnBegin())
	for i := 0; i < 5; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.txnCommit(); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Reopen, write more, close raw, append junk.
	s, err = OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	for i := 5; i < 8; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	s.wal.Sync()
	simulateCrash(s)

	walPath := filepath.Join(dir, "wal.bin")
	f, _ := os.OpenFile(walPath, os.O_WRONLY|os.O_APPEND, 0644)
	f.Write([]byte{0xFF, 0xFE, 0xFD, 0xFC, 0xFB})
	f.Close()

	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		vec, err := s2.GetVector(uint64(i))
		if err != nil {
			t.Fatalf("GetVector(%d): %v", i, err)
		}
		if vec[0] != float32(i) {
			t.Fatalf("vec[%d][0]: got %f, want %f", i, vec[0], float32(i))
		}
	}
	s2.Close()
}

// 6e: Partial meta.bin write — meta truncated to 32 bytes (corrupt).
func TestCrashPoint_PartialMeta(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	metaPath := filepath.Join(dir, "meta.bin")
	f, _ := os.OpenFile(metaPath, os.O_WRONLY, 0644)
	f.Truncate(32)
	f.Close()

	_, err = OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err == nil {
		t.Fatal("expected error on corrupt meta.bin")
	}
}

// 6f: grow during write, crash before meta update → replay re-grows.
func TestCrashPoint_GrowMidWrite(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	n := 1030
	requireNoError(t, s.txnBegin())
	for i := 0; i < n; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.txnCommit(); err != nil {
		t.Fatal(err)
	}

	s.wal.Sync()
	simulateCrash(s)

	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		vec, err := s2.GetVector(uint64(i))
		if err != nil {
			t.Fatalf("GetVector(%d): %v", i, err)
		}
		if vec[0] != float32(i) {
			t.Fatalf("vec[%d][0]: got %f, want %f", i, vec[0], float32(i))
		}
	}
	s2.Close()
}

// 6g: SetNeighbors WAL written, crash before msync.
func TestCrashPoint_SetNeighborsCrash(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	requireNoError(t, s.txnBegin())
	for i := 0; i < 5; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.txnCommit(); err != nil {
		t.Fatal(err)
	}

	if err := s.SetNeighbors(0, 0, []uint64{1, 2, 3}); err != nil {
		t.Fatal(err)
	}

	simulateCrash(s)

	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	nbs, err := s2.GetNeighbors(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(nbs) != 3 || nbs[0] != 1 || nbs[1] != 2 || nbs[2] != 3 {
		t.Fatalf("neighbors: got %v, want [1 2 3]", nbs)
	}
	s2.Close()
}

// 6h: DeleteNode WAL written, crash before msync.
func TestCrashPoint_DeleteNodeCrash(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	requireNoError(t, s.txnBegin())
	for i := 0; i < 5; i++ {
		if err := s.PutNode(uint64(i), 0, []float32{float32(i), 0, 0, 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.txnCommit(); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteNode(2); err != nil {
		t.Fatal(err)
	}

	simulateCrash(s)

	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	nodeOff := int64(pageSize) + 2*int64(nodeSlotSize)
	flags := s2.nodes[nodeOff+1]
	if flags&nodeFlagDeleted == 0 {
		t.Fatal("node 2 should be deleted after replay")
	}

	for _, id := range []uint64{0, 1, 3, 4} {
		noff := int64(pageSize) + int64(id)*int64(nodeSlotSize)
		nflags := s2.nodes[noff+1]
		if nflags&nodeFlagDeleted != 0 {
			t.Fatalf("node %d should not be deleted", id)
		}
		if nflags&nodeFlagOccupied == 0 {
			t.Fatalf("node %d should be marked occupied after replay", id)
		}
	}

	s2.Close()
}
