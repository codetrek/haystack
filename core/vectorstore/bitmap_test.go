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

func TestBitmap_SetAlgebra(t *testing.T) {
	var a, b bitmap
	for _, i := range []int{1, 3, 5, 64, 65, 130} {
		a.set(i)
	}
	for _, i := range []int{3, 5, 64, 200} {
		b.set(i)
	}
	a.and(&b) // a ← a ∩ b
	got := a.collect()
	want := []int{3, 5, 64}
	if !intsEqual(got, want) {
		t.Fatalf("and = %v, want %v", got, want)
	}
	if a.count() != 3 {
		t.Fatalf("count after and = %d, want 3", a.count())
	}

	// Receiver wider than operand: words past the operand's length are zeroed.
	var wide, narrow bitmap
	for _, i := range []int{1, 200} { // bit 200 lives in word 3
		wide.set(i)
	}
	narrow.set(1) // only word 0 exists in the operand
	wide.and(&narrow)
	if !intsEqual(wide.collect(), []int{1}) {
		t.Fatalf("and (wide∩narrow) = %v, want [1]", wide.collect())
	}
}

func TestBitmap_AndNotWords(t *testing.T) {
	var a bitmap
	for _, i := range []int{1, 2, 3, 70} {
		a.set(i)
	}
	// tomb words: bit 2 and bit 70 are tombstoned.
	tomb := make([]uint64, 2)
	tomb[0] = 1 << 2
	tomb[1] = 1 << (70 - 64)
	a.andNotWords(tomb)
	if !intsEqual(a.collect(), []int{1, 3}) {
		t.Fatalf("andNotWords = %v, want [1 3]", a.collect())
	}
}

func TestBitmap_Clone(t *testing.T) {
	var a bitmap
	for _, i := range []int{0, 70, 130} {
		a.set(i)
	}
	cp := a.clone()
	// Mutating the clone must not touch the original (deep copy).
	cp.set(5)
	if a.get(5) {
		t.Fatal("clone is not a deep copy: mutation leaked back to original")
	}
	if !intsEqual(a.collect(), []int{0, 70, 130}) {
		t.Fatalf("original after clone-mutate = %v, want [0 70 130]", a.collect())
	}
	if !intsEqual(cp.collect(), []int{0, 5, 70, 130}) {
		t.Fatalf("clone = %v, want [0 5 70 130]", cp.collect())
	}
}

func intsEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
