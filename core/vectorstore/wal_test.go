package vectorstore

import (
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
