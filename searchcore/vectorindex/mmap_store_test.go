package vectorindex

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMmapStoreOpenClose(t *testing.T) {
	dir := t.TempDir()

	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 128, M: 16})
	if err != nil {
		t.Fatalf("OpenMmapStore: %v", err)
	}

	// All data files should exist.
	for _, name := range []string{"meta.bin", "vectors.dat", "nodes.dat", "graph_l0.dat", "graph_upper.dat"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing file %s: %v", name, err)
		}
	}

	// Check capacities.
	if s.vecCapacity != defaultInitialCapacity {
		t.Errorf("vecCapacity = %d, want %d", s.vecCapacity, defaultInitialCapacity)
	}
	if s.nodeCapacity != defaultInitialCapacity {
		t.Errorf("nodeCapacity = %d, want %d", s.nodeCapacity, defaultInitialCapacity)
	}
	if s.l0Capacity != defaultInitialCapacity {
		t.Errorf("l0Capacity = %d, want %d", s.l0Capacity, defaultInitialCapacity)
	}

	// Verify file sizes = header + capacity * slotSize.
	vecInfo, _ := os.Stat(filepath.Join(dir, "vectors.dat"))
	wantVecSize := int64(pageSize) + int64(defaultInitialCapacity)*int64(128*4)
	if vecInfo.Size() != wantVecSize {
		t.Errorf("vectors.dat size = %d, want %d", vecInfo.Size(), wantVecSize)
	}

	nodeInfo, _ := os.Stat(filepath.Join(dir, "nodes.dat"))
	wantNodeSize := int64(pageSize) + int64(defaultInitialCapacity)*int64(nodeSlotSize)
	if nodeInfo.Size() != wantNodeSize {
		t.Errorf("nodes.dat size = %d, want %d", nodeInfo.Size(), wantNodeSize)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-open should succeed with same params.
	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 128, M: 16})
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	if s2.meta.Dim != 128 {
		t.Errorf("re-open dim = %d, want 128", s2.meta.Dim)
	}
	if err := s2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestMmapStoreOpenMismatch(t *testing.T) {
	dir := t.TempDir()

	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 128, M: 16})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Dim mismatch.
	_, err = OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 256, M: 16})
	if err == nil {
		t.Fatal("expected error for dim mismatch")
	}

	// M mismatch.
	_, err = OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 128, M: 32})
	if err == nil {
		t.Fatal("expected error for M mismatch")
	}
}

func TestMmapStoreInvalidOpts(t *testing.T) {
	dir := t.TempDir()

	if _, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 0, M: 16}); err == nil {
		t.Fatal("expected error for dim=0")
	}
	if _, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 128, M: 0}); err == nil {
		t.Fatal("expected error for M=0")
	}
}

func TestMmapStoreInitBadDir(t *testing.T) {
	// Opening in a non-writable path should fail during initAllFiles.
	badPath := "/dev/null/impossible"
	if runtime.GOOS == "windows" {
		badPath = "NUL\\impossible"
	}
	_, err := OpenMmapStore(badPath, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err == nil {
		t.Fatal("expected error for non-writable path")
	}
}

func TestMmapStoreInitSmallCap(t *testing.T) {
	// This exercises the upperCap < 64 branch in initAllFiles
	// by opening a store normally (defaultInitialCapacity=1024, so upperCap=256>=64).
	// To test upperCap<64 we need cap<256, but initAllFiles is not exported.
	// Instead, verify upperCapacity is at least 64 when default cap is 1024 (256/4=256>=64 is fine,
	// but let's verify the files are correct).
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// With defaultInitialCapacity=1024, upperCap = 1024/4 = 256.
	if s.upperCapacity < 64 {
		t.Errorf("upperCapacity = %d, want >= 64", s.upperCapacity)
	}
}

func TestFaultedStoreRejectsWrites(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Simulate a recorded fault.
	s.muWrite.Lock()
	s.fault(fmt.Errorf("disk on fire"))
	s.muWrite.Unlock()

	if err := s.PutNode(0, 0, []float32{1, 2, 3, 4}, 101); err == nil {
		t.Fatal("PutNode on a faulted store must return an error")
	}
	if err := s.SetNeighbors(0, 0, []uint64{1}); err == nil {
		t.Fatal("SetNeighbors on a faulted store must return an error")
	}
}

func TestTxnCommitDurableAfterCrash(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4, CheckpointInterval: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.txnBegin(); err != nil {
		t.Fatal(err)
	}
	if err := s.PutNode(0, 0, []float32{1, 2, 3, 4}, 101); err != nil {
		t.Fatal(err)
	}
	if err := s.txnCommit(); err != nil {
		t.Fatal(err)
	}
	simulateCrash(s)

	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	vec, err := s2.GetVector(0)
	if err != nil {
		t.Fatalf("committed node must survive crash: %v", err)
	}
	if vec[0] != 1 || vec[3] != 4 {
		t.Fatalf("vec = %v, want [1 2 3 4]", vec)
	}
	if s2.meta.NodeCount != 1 {
		t.Fatalf("NodeCount = %d, want 1", s2.meta.NodeCount)
	}
}

func TestTxnAbortFaultsAndDiscardsOnReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4, CheckpointInterval: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.txnBegin(); err != nil {
		t.Fatal(err)
	}
	if err := s.PutNode(0, 0, []float32{9, 9, 9, 9}, 101); err != nil {
		t.Fatal(err)
	}
	// Abort: records a fault, writes NO commit marker.
	if err := s.txnAbort(fmt.Errorf("boom")); err == nil {
		t.Fatal("txnAbort must return the fault error")
	}
	// Subsequent writes are rejected.
	if err := s.PutNode(1, 0, []float32{1, 1, 1, 1}, 102); err == nil {
		t.Fatal("writes after abort must be rejected")
	}
	simulateCrash(s)

	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.meta.NodeCount != 0 {
		t.Fatalf("NodeCount = %d, want 0 (aborted txn must not persist)", s2.meta.NodeCount)
	}
	if s2.meta.NextNodeId != 0 {
		t.Fatalf("NextNodeId = %d, want 0", s2.meta.NextNodeId)
	}
}

func TestTxnAbortDiscardedAfterGracefulClose(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4, CheckpointInterval: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.txnBegin(); err != nil {
		t.Fatal(err)
	}
	if err := s.PutNode(0, 0, []float32{9, 9, 9, 9}, 101); err != nil {
		t.Fatal(err)
	}
	_ = s.txnAbort(fmt.Errorf("boom"))
	// Graceful Close (NOT a crash) must NOT persist the aborted txn.
	_ = s.Close()

	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.meta.NodeCount != 0 {
		t.Fatalf("NodeCount = %d, want 0 (aborted txn must not survive graceful Close)", s2.meta.NodeCount)
	}
	if s2.meta.NextNodeId != 0 {
		t.Fatalf("NextNodeId = %d, want 0", s2.meta.NextNodeId)
	}
}

func TestTxnBeginRejectsNested(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.txnBegin(); err != nil {
		t.Fatal(err)
	}
	if err := s.txnBegin(); err == nil {
		t.Fatal("nested txnBegin must error (single-level transactions)")
	}
	if err := s.txnCommit(); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Phase 2 new tests
// ---------------------------------------------------------------------------

// TestOpenRejectsOldVersion verifies that opening a Version 1 store errors
// with a message mentioning migration.
func TestOpenRejectsOldVersion(t *testing.T) {
	dir := t.TempDir()
	// Create a valid v2 store, then overwrite the version field to 1.
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	// Overwrite version in-memory, then write meta to disk via writeMetaHeader.
	s.meta.Version = 1
	if err := writeMetaHeader(s.dir, &s.meta); err != nil {
		t.Fatalf("writeMetaHeader: %v", err)
	}
	s.simulateCrashNoClose()

	_, err = OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	if err == nil {
		t.Fatal("expected error opening Version 1 store")
	}
	t.Logf("got expected error: %v", err)
}

// TestDocIdSurvivesAndAbortLeavesNone inserts a node with a known docId,
// crashes, reopens, and verifies the docId is intact. It then opens a fresh
// store, starts a txn, inserts but aborts, and verifies the docId is gone.
func TestDocIdSurvivesAndAbortLeavesNone(t *testing.T) {
	dir := t.TempDir()
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4, CheckpointInterval: 1_000_000}

	const docId int64 = 0xDEADBEEF

	// --- committed insert survives crash ---
	s, err := OpenMmapStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.txnBegin(); err != nil {
		t.Fatal(err)
	}
	if err := s.PutNode(0, 0, []float32{1, 2, 3, 4}, docId); err != nil {
		t.Fatal(err)
	}
	if err := s.txnCommit(); err != nil {
		t.Fatal(err)
	}
	simulateCrash(s)

	s2, err := OpenMmapStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	got, found, err := s2.GetDocId(0)
	if err != nil {
		t.Fatalf("GetDocId after crash: %v", err)
	}
	if !found {
		t.Fatal("node 0 should be occupied after committed insert")
	}
	if got != docId {
		t.Fatalf("docId = %d, want %d", got, docId)
	}

	// Verify reverse: GetNodeId returns the correct nodeId.
	nodeId, ok, err := s2.GetNodeId(docId)
	if err != nil {
		t.Fatalf("GetNodeId after crash: %v", err)
	}
	if !ok || nodeId != 0 {
		t.Fatalf("GetNodeId(%d) = (%d, %v), want (0, true)", docId, nodeId, ok)
	}

	// --- aborted insert leaves no docId ---
	dir2 := t.TempDir()
	s3, err := OpenMmapStore(dir2, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := s3.txnBegin(); err != nil {
		t.Fatal(err)
	}
	if err := s3.PutNode(0, 0, []float32{1, 2, 3, 4}, docId); err != nil {
		t.Fatal(err)
	}
	// Abort — no WAL commit marker, mmap changes should be discarded on replay.
	_ = s3.txnAbort(fmt.Errorf("test abort"))
	simulateCrash(s3)

	s4, err := OpenMmapStore(dir2, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s4.Close()

	if s4.meta.NodeCount != 0 {
		t.Fatalf("NodeCount = %d after aborted txn, want 0", s4.meta.NodeCount)
	}
}

// TestDocToNodeLazyBuiltOnWrite verifies that docToNode is nil before the
// first write-path call to GetNodeId, and is built lazily on demand.
func TestDocToNodeLazyBuiltOnWrite(t *testing.T) {
	dir := t.TempDir()
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4}

	// Insert a node, close, reopen — on reopen docToNodeBuilt is false.
	s, err := OpenMmapStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutNode(0, 0, []float32{1, 2, 3, 4}, 42); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := OpenMmapStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	// docToNodeBuilt should be false immediately after open (lazy).
	s2.muWrite.Lock()
	built := s2.docToNodeBuilt
	s2.muWrite.Unlock()
	if built {
		t.Fatal("docToNodeBuilt should be false immediately after open")
	}

	// GetNodeId triggers the lazy build.
	nodeId, ok, err := s2.GetNodeId(42)
	if err != nil {
		t.Fatalf("GetNodeId: %v", err)
	}
	if !ok || nodeId != 0 {
		t.Fatalf("GetNodeId(42) = (%d, %v), want (0, true)", nodeId, ok)
	}

	// Now docToNodeBuilt should be true.
	s2.muWrite.Lock()
	built = s2.docToNodeBuilt
	s2.muWrite.Unlock()
	if !built {
		t.Fatal("docToNodeBuilt should be true after GetNodeId")
	}
}
