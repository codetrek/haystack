package vectorstore

import "testing"

// TestSealedSegment_GetVectorRef_DecodesStoredExactly is the correctness guard for
// getVectorRef. It aliases the mmap via unsafe, so it must surface the little-endian
// on-disk floats exactly. DotProduct stores raw vectors verbatim, so the stored form
// equals the input and the assertion is exact (no metric reconstruction involved).
func TestSealedSegment_GetVectorRef_DecodesStoredExactly(t *testing.T) {
	s := openTestStore(t, DotProduct)
	want := []float32{1.5, -2.25, 3.0, 4.5, -0.125, 7.75}
	requireNoError(t, s.Put("a", want, nil))
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	if len(s.sealed) == 0 {
		t.Fatal("no sealed segment after Seal")
	}
	got := s.sealed[0].getVectorRef(0) // slot 0 = the only doc
	if len(got) != len(want) {
		t.Fatalf("dim = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("getVectorRef[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}
