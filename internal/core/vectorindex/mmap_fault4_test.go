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

// --- store Sync: checkpoint error branch ---

func TestStoreSyncCheckpointError(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	orig := mmapSync
	defer func() { mmapSync = orig }()
	mmapSync = func([]byte) error { return errInjected }
	if err := s.Sync(); err == nil {
		t.Fatal("expected checkpoint error from Sync")
	}
}

// --- CommitBatch branches ---

func TestCommitBatchNestedNoCommit(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	s.BeginBatch()
	s.BeginBatch()
	// depth still > 0: returns without flushing/syncing.
	if err := s.CommitBatch(true); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitBatch(true); err != nil {
		t.Fatal(err)
	}
}

func TestCommitBatchSyncError(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	s.BeginBatch()
	s.wal.file = &faultFile{osFile: s.wal.file, failSync: true}
	if err := s.CommitBatch(true); err == nil {
		t.Fatal("expected WAL sync error from CommitBatch")
	}
}

func TestCommitBatchMsyncError(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	s.BeginBatch()
	orig := mmapSync
	defer func() { mmapSync = orig }()
	mmapSync = func([]byte) error { return errInjected }
	if err := s.CommitBatch(true); err == nil {
		t.Fatal("expected msync error from CommitBatch")
	}
}
