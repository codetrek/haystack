package vectorstore

import "testing"

func TestBitmap(t *testing.T) {
	var b bitmap
	if b.get(0) || b.get(100) {
		t.Fatal("fresh bitmap must be all-zero")
	}
	if b.count() != 0 {
		t.Fatalf("count = %d, want 0", b.count())
	}
	b.set(0)
	b.set(63)
	b.set(64) // forces growth into a second word
	b.set(200)
	for _, i := range []int{0, 63, 64, 200} {
		if !b.get(i) {
			t.Fatalf("bit %d should be set", i)
		}
	}
	for _, i := range []int{1, 62, 65, 199, 201} {
		if b.get(i) {
			t.Fatalf("bit %d should be clear", i)
		}
	}
	if b.count() != 4 {
		t.Fatalf("count = %d, want 4", b.count())
	}
	b.set(0) // idempotent
	if b.count() != 4 {
		t.Fatalf("count after re-set = %d, want 4", b.count())
	}
}
