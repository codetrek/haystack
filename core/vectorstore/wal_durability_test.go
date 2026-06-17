package vectorstore

import (
	"os"
	"strings"
	"testing"
)

// blocker 2: a WAL record whose payload exceeds maxWalPayloadSize must be
// rejected at Append. Otherwise scanLSN/Replay treat its length field as a torn
// tail on reopen and silently drop it AND every record written after it.
func TestWALAppendRejectsOversizePayload(t *testing.T) {
	w, err := OpenWAL(t.TempDir())
	requireNoError(t, err)
	defer w.Close()
	if _, err := w.Append(recPut, make([]byte, maxWalPayloadSize+1)); err == nil {
		t.Fatal("Append accepted an oversize payload; reopen would silently drop it + the tail")
	}
}

// blocker 2 end-to-end: an oversize Put must return an error (not silent
// success), and must NOT cause cascading loss of records written after it.
func TestPutOversizeRejectedNoCascadeLoss(t *testing.T) {
	dir := t.TempDir()
	kvs := newTestKV(t)
	s, err := Open(Options{Dir: dir, KV: kvs, Metric: DotProduct})
	requireNoError(t, err)
	requireNoError(t, s.Put("a", []float32{1, 2, 3, 4}, nil))
	// A structured payload whose serialized blob exceeds maxWalPayloadSize: the
	// encoded putRecord frame is oversize, so WAL.Append must reject it (the blob
	// body is a Payload now, not raw []byte — the oversize check is on the frame).
	oversize := Payload{"k": StringValue(strings.Repeat("x", maxWalPayloadSize))}
	if err := s.Put("big", []float32{1, 2, 3, 4}, oversize); err == nil {
		t.Fatal("oversize Put returned success; data will be silently lost on reopen")
	}
	requireNoError(t, s.Put("c", []float32{5, 6, 7, 8}, nil))
	requireNoError(t, s.Close())

	s2, err := Open(Options{Dir: dir, KV: kvs, Metric: DotProduct})
	requireNoError(t, err)
	defer s2.Close()
	if _, _, found, _ := s2.Get("a"); !found {
		t.Fatal("record 'a' lost")
	}
	if _, _, found, _ := s2.Get("c"); !found {
		t.Fatal("record 'c' (written after the oversize Put) was cascade-dropped")
	}
	if _, _, found, _ := s2.Get("big"); found {
		t.Fatal("oversize record 'big' should not exist")
	}
}

// blocker 1: a Put whose fsync fails returns an error and must NOT be applied;
// its CRC-valid WAL frame must not be silently persisted (by a later Sync or
// Close) and resurrected on reopen (crash-atomicity, decision #7).
func TestErroredPutNotResurrectedOnReopen(t *testing.T) {
	dir := t.TempDir()
	kvs := newTestKV(t)
	orig := fsOpenFile
	fsOpenFile = func(name string, flag int, perm os.FileMode) (osFile, error) {
		f, err := orig(name, flag, perm)
		if err != nil {
			return nil, err
		}
		return &faultFile{osFile: f, failSync: true}, nil
	}
	s, err := Open(Options{Dir: dir, KV: kvs, Metric: DotProduct})
	requireNoError(t, err)
	if err := s.Put("x", []float32{1, 2, 3, 4}, nil); err == nil {
		t.Fatal("expected Put to fail when fsync fails")
	}
	requireNoError(t, s.Close())
	fsOpenFile = orig // clean reopen, no fault

	s2, err := Open(Options{Dir: dir, KV: kvs, Metric: DotProduct})
	requireNoError(t, err)
	defer s2.Close()
	if _, _, found, _ := s2.Get("x"); found {
		t.Fatal("errored Put was resurrected on reopen (crash-atomicity violation)")
	}
}

// rollback coverage: a frame larger than the bufio buffer (4096) forces an
// auto-flush mid-Append; the injected write failure surfaces as an Append error
// and the WAL rolls back to the last durable state.
func TestAppendFlushErrorRollsBack(t *testing.T) {
	orig := fsOpenFile
	fsOpenFile = func(name string, flag int, perm os.FileMode) (osFile, error) {
		f, err := orig(name, flag, perm)
		if err != nil {
			return nil, err
		}
		return &faultFile{osFile: f, failWrite: true}, nil
	}
	defer func() { fsOpenFile = orig }()
	w, err := OpenWAL(t.TempDir())
	requireNoError(t, err)
	defer w.Close()
	if _, err := w.Append(recPut, make([]byte, 8192)); err == nil {
		t.Fatal("expected Append to fail when the buffered flush errors")
	}
}

// rollback coverage: a small frame buffers cleanly in Append, then Sync's
// buf.Flush hits the injected write failure and rolls back.
func TestSyncFlushErrorRollsBack(t *testing.T) {
	orig := fsOpenFile
	fsOpenFile = func(name string, flag int, perm os.FileMode) (osFile, error) {
		f, err := orig(name, flag, perm)
		if err != nil {
			return nil, err
		}
		return &faultFile{osFile: f, failWrite: true}, nil
	}
	defer func() { fsOpenFile = orig }()
	w, err := OpenWAL(t.TempDir())
	requireNoError(t, err)
	defer w.Close()
	if _, err := w.Append(recPut, []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("small Append should buffer cleanly: %v", err)
	}
	if err := w.Sync(); err == nil {
		t.Fatal("expected Sync to fail when buf.Flush errors")
	}
}
