package vectorstore

import (
	"reflect"
	"testing"
)

func TestStore_Put_Get_StructuredPayload_RoundTrip(t *testing.T) {
	s := openTestStore(t, Cosine)
	p := Payload{"color": StringValue("red"), "size": Int64Value(7), "hot": BoolValue(true)}
	requireNoError(t, s.Put("a", []float32{1, 0, 0}, p))
	v, got, found, err := s.Get("a")
	requireNoError(t, err)
	if !found {
		t.Fatal("Get(a) not found")
	}
	if len(v) != 3 {
		t.Fatalf("vector len = %d, want 3", len(v))
	}
	if !reflect.DeepEqual(got, p) {
		t.Fatalf("payload mismatch:\n got=%#v\nwant=%#v", got, p)
	}
}

func TestStore_Put_NilPayload_GetEmpty(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.Put("a", []float32{1, 0, 0}, nil))
	_, got, found, err := s.Get("a")
	requireNoError(t, err)
	if !found || len(got) != 0 {
		t.Fatalf("nil payload → Get got=%#v found=%v", got, found)
	}
}

func TestStore_Payload_SurvivesSealAndMerge(t *testing.T) {
	s := openTestStore(t, Cosine)
	for i := 0; i < 30; i++ {
		requireNoError(t, s.Put("k"+itoa(i), []float32{float32(i), 0, 0},
			Payload{"n": Int64Value(int64(i))}))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	_, got, found, err := s.Get("k5")
	requireNoError(t, err)
	if !found || got["n"].Int != 5 {
		t.Fatalf("post-seal payload for k5 = %#v found=%v", got, found)
	}
}

// TestStore_Payload_SurvivesRecovery proves the structured payload survives a WAL
// replay (Put, then reopen before any seal): the version-gated blob round-trips
// through encodePut/decodePut and decodePayload on recovery.
func TestStore_Payload_SurvivesRecovery(t *testing.T) {
	dir := t.TempDir()
	kvs := newTestKV(t)
	s, err := Open(Options{Dir: dir, KV: kvs, Metric: Cosine})
	requireNoError(t, err)
	p := Payload{"tag": StringValue("x"), "rank": Float64Value(2.5)}
	requireNoError(t, s.Put("a", []float32{1, 0, 0}, p))
	requireNoError(t, s.Close())

	s2, err := Open(Options{Dir: dir, KV: kvs, Metric: Cosine})
	requireNoError(t, err)
	defer s2.Close()
	_, got, found, err := s2.Get("a")
	requireNoError(t, err)
	if !found || !reflect.DeepEqual(got, p) {
		t.Fatalf("recovered payload = %#v found=%v, want %#v", got, found, p)
	}
}
