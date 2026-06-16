package vectorstore

import "testing"

func TestStore_OpenClose(t *testing.T) {
	s := openTestStore(t, Cosine)
	if s.Metric() != Cosine {
		t.Fatalf("metric = %v, want cosine", s.Metric())
	}
}

func TestStore_OpenRequiresKV(t *testing.T) {
	if _, err := Open(Options{Dir: t.TempDir(), Metric: Cosine}); err == nil {
		t.Fatal("Open without KV should error")
	}
}

func TestStore_OpenRequiresDir(t *testing.T) {
	if _, err := Open(Options{KV: newTestKV(t), Metric: Cosine}); err == nil {
		t.Fatal("Open without Dir should error")
	}
}
