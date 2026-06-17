package vectorstore

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEncodeDecodePut(t *testing.T) {
	rec := putRecord{
		ID:      "doc-a",
		DocID:   42,
		OldSlot: 7,
		Stored:  []float32{0.6, 0.8},
		Norm:    5,
		Payload: []byte("meta"),
	}
	got := decodePut(encodePut(rec))
	if !reflect.DeepEqual(got, rec) {
		t.Fatalf("decodePut(encodePut) = %+v, want %+v", got, rec)
	}
}

func TestEncodeDecodePut_NoOldSlotEmptyPayload(t *testing.T) {
	rec := putRecord{ID: "x", DocID: 1, OldSlot: -1, Stored: []float32{1, 2, 3}, Norm: 0, Payload: nil}
	got := decodePut(encodePut(rec))
	if got.ID != "x" || got.DocID != 1 || got.OldSlot != -1 || got.Norm != 0 {
		t.Fatalf("scalars wrong: %+v", got)
	}
	if !reflect.DeepEqual(got.Stored, rec.Stored) {
		t.Fatalf("stored = %v, want %v", got.Stored, rec.Stored)
	}
	if len(got.Payload) != 0 {
		t.Fatalf("payload = %v, want empty", got.Payload)
	}
}

func TestEncodeDecodeDelete(t *testing.T) {
	got := decodeDelete(encodeDelete("doc-b", 123, 4))
	if got.ID != "doc-b" || got.DocID != 123 || got.Slot != 4 {
		t.Fatalf("decodeDelete = %+v, want {ID:doc-b DocID:123 Slot:4}", got)
	}
}

// TestEncodePut_PayloadVersionGate locks the WAL putRecord contract: encodePut
// frames a serialized Payload blob behind walRecVersion, decodePut round-trips
// the blob (which decodePayload then decodes), a record written under the current
// walRecVersion decodes cleanly, and a pre-Phase-5 frame (no version byte) is
// flagged badVersion so an old WAL cannot be silently mis-applied. A raw opaque
// payload is likewise rejected by decodePayload (the gate that makes the bump
// safe end-to-end).
func TestEncodePut_PayloadVersionGate(t *testing.T) {
	pl, err := encodePayload(Payload{"k": StringValue("v")})
	requireNoError(t, err)
	r := putRecord{ID: "a", DocID: 1, OldSlot: -1, Stored: []float32{1, 0}, Norm: 1, Payload: pl}
	enc := encodePut(r)
	got := decodePut(enc)
	if got.badVersion {
		t.Fatalf("a current-version record was flagged badVersion: %+v", got)
	}
	if got.ID != "a" || got.DocID != 1 {
		t.Fatalf("decodePut header mismatch: %+v", got)
	}
	p, err := decodePayload(got.Payload)
	requireNoError(t, err)
	if p["k"].Str != "v" {
		t.Fatalf("payload round-trip via WAL record mismatch: %#v", p)
	}
	// A pre-Phase-5 frame begins with a byte that is not walRecVersion (here the
	// ascii 'l'); decodePut must flag it so replay rejects rather than mis-decodes.
	if !decodePut([]byte("legacy-opaque-record-bytes")).badVersion {
		t.Fatal("a pre-Phase-5 WAL putRecord frame must be flagged badVersion")
	}
	if !decodePut(nil).badVersion {
		t.Fatal("an empty frame must be flagged badVersion")
	}
	// And an OLD-format opaque payload must be rejected by decodePayload.
	if _, err := decodePayload([]byte("legacy-opaque-bytes")); err == nil {
		t.Fatal("a pre-Phase-5 opaque payload must be rejected by decodePayload")
	}
}

// TestReplay_RejectsPrePhase5WAL is the load-bearing recovery guard: a records
// WAL holding a pre-Phase-5 putRecord frame (no version byte) must fail Open
// rather than silently mis-decode the opaque bytes into a bogus head segment.
func TestReplay_RejectsPrePhase5WAL(t *testing.T) {
	dir := t.TempDir()
	// Hand-write one putRecord frame whose body lacks the walRecVersion prefix
	// (a Phase-1 encodePut layout: idLen(4)|id|docId|oldSlot|norm|vecLen|...).
	w, err := OpenWAL(dir)
	requireNoError(t, err)
	body := legacyEncodePut("a", 1, -1, []float32{1, 0}, 0, nil)
	_, err = w.Append(recPut, body)
	requireNoError(t, err)
	requireNoError(t, w.Sync())
	requireNoError(t, w.Close())

	kvs := newTestKV(t)
	if _, err := Open(Options{Dir: dir, KV: kvs, Metric: DotProduct}); err == nil {
		t.Fatal("Open should reject a pre-Phase-5 WAL putRecord frame")
	}
}

// legacyEncodePut reproduces the Phase-1 (pre-version-byte) putRecord body layout
// so the recovery guard can feed the replayer a genuine old-format frame.
func legacyEncodePut(id string, docID, oldSlot int64, stored []float32, norm float32, payload []byte) []byte {
	size := 4 + len(id) + 8 + 8 + 4 + 4 + len(stored)*4 + 4 + len(payload)
	buf := make([]byte, size)
	off := putString(buf, 0, id)
	binary.LittleEndian.PutUint64(buf[off:], uint64(docID))
	off += 8
	binary.LittleEndian.PutUint64(buf[off:], uint64(oldSlot))
	off += 8
	binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(norm))
	off += 4
	binary.LittleEndian.PutUint32(buf[off:], uint32(len(stored)))
	off += 4
	for _, v := range stored {
		binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(v))
		off += 4
	}
	binary.LittleEndian.PutUint32(buf[off:], uint32(len(payload)))
	off += 4
	copy(buf[off:], payload)
	return buf
}

func TestWAL_AppendReplayRoundTrip(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	requireNoError(t, err)
	put := encodePut(putRecord{ID: "a", DocID: 1, OldSlot: -1, Stored: []float32{1, 0}, Norm: 0, Payload: []byte("p")})
	del := encodeDelete("a", 1, 0)
	_, err = w.Append(recPut, put)
	requireNoError(t, err)
	_, err = w.Append(recDelete, del)
	requireNoError(t, err)
	requireNoError(t, w.Sync())
	requireNoError(t, w.Close())

	w2, err := OpenWAL(dir)
	requireNoError(t, err)
	defer w2.Close()
	var types []recType
	requireNoError(t, w2.Replay(func(typ recType, payload []byte) error {
		types = append(types, typ)
		return nil
	}))
	if len(types) != 2 || types[0] != recPut || types[1] != recDelete {
		t.Fatalf("replayed types = %v, want [recPut recDelete]", types)
	}
}

func TestWAL_ResetClearsRecords(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	requireNoError(t, err)
	defer w.Close()
	_, err = w.Append(recDelete, encodeDelete("a", 1, 0))
	requireNoError(t, err)
	requireNoError(t, w.Reset())
	n := 0
	requireNoError(t, w.Replay(func(recType, []byte) error { n++; return nil }))
	if n != 0 {
		t.Fatalf("after Reset replay saw %d records, want 0", n)
	}
}

func TestWAL_SyncFault(t *testing.T) {
	dir := t.TempDir()
	withOpenFileFault(t, func(f *faultFile) { f.failSync = true })
	w, err := OpenWAL(dir)
	requireNoError(t, err)
	defer w.Close()
	if _, err := w.Append(recDelete, encodeDelete("a", 1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := w.Sync(); err == nil {
		t.Fatal("expected Sync to surface injected fsync failure")
	}
}

func TestWAL_AppendWriteFault(t *testing.T) {
	dir := t.TempDir()
	withOpenFileFault(t, func(f *faultFile) { f.failWrite = true })
	w, err := OpenWAL(dir)
	requireNoError(t, err)
	defer w.Close()
	// bufio buffers small writes, so force a flush via Sync to surface the
	// injected write error on the underlying file.
	_, _ = w.Append(recDelete, encodeDelete("a", 1, 0))
	if err := w.Sync(); err == nil {
		t.Fatal("expected the injected write failure to surface on flush")
	}
}

func TestWAL_OpenTruncateFault(t *testing.T) {
	dir := t.TempDir()
	withOpenFileFault(t, func(f *faultFile) { f.failTruncate = true }) // scanLSN truncates
	if _, err := OpenWAL(dir); err == nil {
		t.Fatal("OpenWAL should fail when scanLSN truncate fails")
	}
}

func TestWAL_ResetTruncateFault(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	requireNoError(t, err)
	defer w.Close()
	// Swap in a Truncate-faulting file AFTER open so Reset's own Truncate fails
	// (not scanLSN's).
	w.file = &faultFile{osFile: w.file, failTruncate: true}
	if err := w.Reset(); err == nil {
		t.Fatal("Reset should surface a Truncate failure")
	}
}

func TestWAL_ResetFlushFault(t *testing.T) {
	dir := t.TempDir()
	// Open with a Write-faulting file so the WAL's buffered writer wraps it from
	// the start; scanLSN only reads/truncates/seeks (no Write), so open succeeds.
	withOpenFileFault(t, func(f *faultFile) { f.failWrite = true })
	w, err := OpenWAL(dir)
	requireNoError(t, err)
	defer w.Close()
	// Leave buffered, unflushed bytes, then Reset's buf.Flush() surfaces the write
	// fault.
	_, _ = w.Append(recDelete, encodeDelete("a", 1, 0))
	if err := w.Reset(); err == nil {
		t.Fatal("Reset should surface a flush (write) failure")
	}
}

func TestWAL_ReplayFnError(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	requireNoError(t, err)
	defer w.Close()
	_, err = w.Append(recDelete, encodeDelete("a", 1, 0))
	requireNoError(t, err)
	requireNoError(t, w.Sync())
	if err := w.Replay(func(recType, []byte) error { return errInjected }); err == nil {
		t.Fatal("Replay should surface an error returned by the apply callback")
	}
}

func TestWAL_ReplayTruncateFault(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenWAL(dir)
	requireNoError(t, err)
	defer w.Close()
	_, err = w.Append(recDelete, encodeDelete("a", 1, 0))
	requireNoError(t, err)
	requireNoError(t, w.Sync())
	// Fault the post-scan Truncate that Replay performs after the loop.
	w.file = &faultFile{osFile: w.file, failTruncate: true}
	if err := w.Replay(func(recType, []byte) error { return nil }); err == nil {
		t.Fatal("Replay should surface its post-loop Truncate failure")
	}
}

func TestWAL_ReplayRejectsOversizedFrame(t *testing.T) {
	dir := t.TempDir()
	// Hand-write a frame whose declared payload length exceeds the max, which
	// scanLSN tolerates (treats as a torn tail) but Replay must reject.
	path := filepath.Join(dir, "records.wal")
	header := make([]byte, walHeaderSize)
	binary.LittleEndian.PutUint64(header[0:8], 1)
	binary.LittleEndian.PutUint32(header[8:12], maxWalPayloadSize+1)
	header[12] = byte(recPut)
	requireNoError(t, os.WriteFile(path, header, 0644))

	w, err := OpenWAL(dir)
	requireNoError(t, err)
	defer w.Close()
	// OpenWAL's scanLSN truncated the oversized torn tail, so re-append it raw to
	// the file the WAL holds, bypassing the buffer, then replay.
	if _, err := w.file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := w.file.Write(header); err != nil {
		t.Fatal(err)
	}
	if err := w.Replay(func(recType, []byte) error { return nil }); err == nil {
		t.Fatal("Replay should reject an oversized payload length")
	}
}
