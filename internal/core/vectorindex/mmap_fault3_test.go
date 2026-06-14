package vectorindex

import (
	"bufio"
	"testing"
)

// failWALNextWrite makes the store's WAL fail its next buffered flush, so the
// next Append (and the write method calling it) returns an error. The existing
// buffer is flushed first, then replaced with a 1-byte buffer backed by a
// fault-injecting file so that the very next buf.Write triggers a flush/error.
func failWALNextWrite(s *MmapStore) {
	_ = s.wal.buf.Flush() // drain any pending bytes
	ff := &faultFile{osFile: s.wal.file, failWrite: true}
	s.wal.file = ff
	s.wal.buf = bufio.NewWriterSize(ff, 1) // 1-byte buffer: any Write triggers immediate flush
}

// --- grow dispatch / re-check / newCap==0 ---

func TestGrowFileUnknownType(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	if err := s.growFile(fileType(99), 1); err == nil {
		t.Fatal("expected unknown file type error")
	}
}

func TestGrowReCheckNoOp(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	// requiredCap below current capacity: each grow re-checks and returns nil.
	if err := s.growVectors(0); err != nil {
		t.Fatal(err)
	}
	if err := s.growNodes(0); err != nil {
		t.Fatal(err)
	}
	if err := s.growL0(0); err != nil {
		t.Fatal(err)
	}
	if err := s.growUpper(0); err != nil {
		t.Fatal(err)
	}
}

func TestGrowFromZeroCapacity(t *testing.T) {
	// Drive the newCap==0 → default seeding branch in each grow path by forcing
	// the tracked capacity to 0 before growing.
	s := openTestStore(t)
	defer s.Close()
	s.vecCapacity, s.nodeCapacity, s.l0Capacity, s.upperCapacity = 0, 0, 0, 0
	if err := s.growVectors(1); err != nil {
		t.Fatal(err)
	}
	if err := s.growNodes(1); err != nil {
		t.Fatal(err)
	}
	if err := s.growL0(1); err != nil {
		t.Fatal(err)
	}
	if err := s.growUpper(1); err != nil {
		t.Fatal(err)
	}
}

// --- write methods: WAL append failure branch ---

func TestPutNodeWALError(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	failWALNextWrite(s)
	if err := s.PutNode(0, 0, []float32{1, 0, 0, 0}); err == nil {
		t.Fatal("expected WAL error from PutNode")
	}
}

func TestSetNormWALError(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	failWALNextWrite(s)
	if err := s.SetNorm(0, 1.0); err == nil {
		t.Fatal("expected WAL error from SetNorm")
	}
}

func TestSetNeighborsWALError(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	failWALNextWrite(s)
	if err := s.SetNeighbors(0, 0, []uint64{1}); err == nil {
		t.Fatal("expected WAL error from SetNeighbors")
	}
}

func TestSetEntryPointWALError(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	failWALNextWrite(s)
	if err := s.SetEntryPoint(0, 0); err == nil {
		t.Fatal("expected WAL error from SetEntryPoint")
	}
}

func TestDeleteNodeWALError(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()
	failWALNextWrite(s)
	if err := s.DeleteNode(0); err == nil {
		t.Fatal("expected WAL error from DeleteNode")
	}
}

// --- WAL method error branches ---

func TestWALSyncFlushError(t *testing.T) {
	w, err := OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.file = &faultFile{osFile: w.file, failWrite: true}
	w.buf.Reset(w.file)
	if _, err := w.buf.WriteString("pending"); err != nil {
		t.Fatal(err)
	}
	if err := w.Sync(); err == nil {
		t.Fatal("expected flush error from Sync")
	}
}

func TestWALResetFlushError(t *testing.T) {
	w, err := OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.file = &faultFile{osFile: w.file, failWrite: true}
	w.buf.Reset(w.file)
	if _, err := w.buf.WriteString("pending"); err != nil {
		t.Fatal(err)
	}
	if err := w.Reset(); err == nil {
		t.Fatal("expected flush error from Reset")
	}
}

func TestWALResetTruncateError(t *testing.T) {
	w, err := OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.file = &faultFile{osFile: w.file, failTruncate: true}
	if err := w.Reset(); err == nil {
		t.Fatal("expected truncate error from Reset")
	}
}

func TestWALReplaySeekError(t *testing.T) {
	w, err := OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.file = &faultFile{osFile: w.file, failSeek: true}
	if err := w.Replay(0, nil); err == nil {
		t.Fatal("expected seek error from Replay")
	}
}

func TestWALReplayTruncateError(t *testing.T) {
	w, err := OpenWAL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Append(WalInsert, []byte("x")); err != nil {
		t.Fatal(err)
	}
	w.file = &faultFile{osFile: w.file, failTruncate: true}
	if err := w.Replay(0, func(uint64, WalRecordType, []byte) error { return nil }); err == nil {
		t.Fatal("expected truncate error from Replay")
	}
}
