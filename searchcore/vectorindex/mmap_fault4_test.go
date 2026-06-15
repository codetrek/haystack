package vectorindex

import (
	"testing"
)

// --- normal out-of-range id branches (read + L0 write) ---

func TestReadOutOfRangeIds(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	const huge = uint64(1) << 40
	if _, err := s.GetVectorRef(huge); err == nil {
		t.Fatal("expected GetVectorRef out-of-range error")
	}
	if _, err := s.GetVector(huge); err == nil {
		t.Fatal("expected GetVector out-of-range error")
	}
	if _, err := s.GetNeighbors(huge, 0); err == nil {
		t.Fatal("expected getNeighborsL0 out-of-range error")
	}
	if _, err := s.GetNeighbors(huge, 1); err == nil {
		t.Fatal("expected getNeighborsUpper out-of-range error")
	}
	if _, err := s.GetNorm(huge); err == nil {
		t.Fatal("expected GetNorm out-of-range error")
	}
	if _, err := s.GetNodeLevel(huge); err == nil {
		t.Fatal("expected GetNodeLevel out-of-range error")
	}
}

func TestSetNeighborsL0OutOfRange(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	// WAL append succeeds, then setNeighborsL0 rejects the out-of-range id.
	if err := s.SetNeighbors(uint64(1)<<40, 0, []uint64{1}); err == nil {
		t.Fatal("expected setNeighborsL0 out-of-range error")
	}
}

// --- checkpointLocked fault branches (via Checkpoint) ---

func TestCheckpointMsyncError(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	orig := mmapSync
	defer func() { mmapSync = orig }()
	mmapSync = func([]byte) error { return errInjected }
	if err := s.Checkpoint(); err == nil {
		t.Fatal("expected msync error from Checkpoint")
	}
}

func TestCheckpointMetaError(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	wrapNextCreate(t, "meta.bin.tmp", faultFile{failWrite: true})
	if err := s.Checkpoint(); err == nil {
		t.Fatal("expected meta error from Checkpoint")
	}
}

func TestCheckpointWALResetError(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	s.wal.file = &faultFile{osFile: s.wal.file, failTruncate: true}
	if err := s.Checkpoint(); err == nil {
		t.Fatal("expected WAL reset error from Checkpoint")
	}
}

func TestCheckpointCompactError(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	orig := fsCreate
	defer func() { fsCreate = orig }()
	fsCreate = func(name string) (osFile, error) {
		if contains(name, "idmap.dat.tmp") {
			return nil, errInjected
		}
		return orig(name)
	}
	if err := s.Checkpoint(); err == nil {
		t.Fatal("expected idmap compact error from Checkpoint")
	}
}

// --- txnCommit fault: msync error surfaces as faulted store ---

func TestStoreSyncCheckpointError(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	requireNoError(t, s.txnBegin())
	requireNoError(t, s.PutNode(0, 0, []float32{1, 2, 3, 4}))
	orig := mmapSync
	defer func() { mmapSync = orig }()
	mmapSync = func([]byte) error { return errInjected }
	if err := s.txnCommit(); err == nil {
		t.Fatal("expected msync error from txnCommit")
	}
	if s.faulted == nil {
		t.Fatal("store must be faulted after txnCommit msync error")
	}
}

// --- txnCommit error branches ---

func TestTxnCommitNestedRejected(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	requireNoError(t, s.txnBegin())
	// A second txnBegin while one is open must fail.
	if err := s.txnBegin(); err == nil {
		t.Fatal("expected txnBegin to reject nesting")
	}
	requireNoError(t, s.txnCommit())
}

func TestTxnCommitSyncError(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	requireNoError(t, s.txnBegin())
	s.wal.file = &faultFile{osFile: s.wal.file, failSync: true}
	if err := s.txnCommit(); err == nil {
		t.Fatal("expected WAL sync error from txnCommit")
	}
}

func TestTxnCommitMsyncError(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	requireNoError(t, s.txnBegin())
	orig := mmapSync
	defer func() { mmapSync = orig }()
	mmapSync = func([]byte) error { return errInjected }
	if err := s.txnCommit(); err == nil {
		t.Fatal("expected msync error from txnCommit")
	}
}

// --- AC6: Batch.Commit I/O fault → store faulted → reopen recovers pre-batch state ---

func TestBatchCommitIOFaultReopensClean(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}

	// Build an index with one doc so NodeCount == 1 before the faulted batch.
	idx := NewHNSWIndex(s)
	if err := idx.Insert("pre-existing", []float32{1, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	preCount := s.meta.NodeCount // should be 1

	// Inject a WAL write fault so txnBegin's WalTxnBegin append fails,
	// causing the store to fault before any new data is committed.
	failWALNextWrite(s)

	// A batch with a new doc: Commit must return an error.
	b := idx.NewBatch()
	b.Put("new-doc", []float32{0, 1, 0, 0})
	commitErr := b.Commit()
	if commitErr == nil {
		t.Fatal("expected Batch.Commit to return an error after WAL write fault")
	}
	// Store must now be faulted.
	if s.faulted == nil {
		t.Fatal("store must be faulted after failed Batch.Commit")
	}
	// A subsequent write must be rejected.
	if err := idx.Insert("another", []float32{0, 0, 1, 0}); err == nil {
		t.Fatal("expected faulted store to reject further writes")
	}

	// Simulate a crash (force WAL flush / close files) so the reopen sees disk state.
	simulateCrash(s)

	// Reopen and verify the pre-batch state is intact.
	s2, err := OpenMmapStore(dir, MmapStoreOptions{Dim: 4, M: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.meta.NodeCount != preCount {
		t.Fatalf("NodeCount after reopen = %d, want %d (pre-batch state)", s2.meta.NodeCount, preCount)
	}
}
