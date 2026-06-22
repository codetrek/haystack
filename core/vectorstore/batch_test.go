package vectorstore

import (
	"math/rand"
	"testing"
)

func batchRandVec(rng *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	for d := range v {
		v[d] = rng.Float32()
	}
	return v
}

// TestBatch_DurableSearchableReopen: a committed batch is durable (survives an
// unclean reopen), Get-able, and searchable.
func TestBatch_DurableSearchableReopen(t *testing.T) {
	kvStore := newTestKV(t)
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, Metric: Cosine})
	requireNoError(t, err)
	rng := rand.New(rand.NewSource(5))
	dim := 16

	b := s.NewBatch()
	for i := 0; i < 200; i++ {
		b.Put("d-"+itoa(i), batchRandVec(rng, dim), nil)
	}
	if b.Len() != 200 {
		t.Fatalf("Len = %d, want 200", b.Len())
	}
	requireNoError(t, b.Commit())
	if b.Len() != 0 {
		t.Fatalf("batch not emptied after Commit: Len = %d", b.Len())
	}

	for i := 0; i < 200; i++ {
		if _, _, found, _ := s.Get("d-" + itoa(i)); !found {
			t.Fatalf("d-%d not found after batch commit", i)
		}
	}
	got, err := s.Search("default", batchRandVec(rng, dim), 10, nil)
	requireNoError(t, err)
	if len(got) != 10 {
		t.Fatalf("search returned %d, want 10", len(got))
	}

	s2 := reopenStore(t, s, kvStore)
	for i := 0; i < 200; i++ {
		if _, _, found, _ := s2.Get("d-" + itoa(i)); !found {
			t.Fatalf("d-%d lost after reopen (batch not durable)", i)
		}
	}
}

// TestBatch_EquivalentToSinglePut: a batch of N Puts yields the same per-id state
// as N single Puts over identical data.
func TestBatch_EquivalentToSinglePut(t *testing.T) {
	const n = 60 // enough to prove batch ≡ single-Put; the single-Put control pays n fsyncs
	build := func(useBatch bool) *Store {
		s := openTestStore(t, Cosine)
		rng := rand.New(rand.NewSource(7))
		if useBatch {
			b := s.NewBatch()
			for i := 0; i < n; i++ {
				b.Put("d-"+itoa(i), batchRandVec(rng, 16), Payload{"k": StringValue(itoa(i))})
			}
			requireNoError(t, b.Commit())
		} else {
			for i := 0; i < n; i++ {
				requireNoError(t, s.Put("d-"+itoa(i), batchRandVec(rng, 16), Payload{"k": StringValue(itoa(i))}))
			}
		}
		return s
	}
	sb, ss := build(true), build(false)
	for i := 0; i < n; i++ {
		id := "d-" + itoa(i)
		vb, _, fb, _ := sb.Get(id)
		vs, _, fs, _ := ss.Get(id)
		if fb != fs || !fb {
			t.Fatalf("%s found: batch=%v single=%v", id, fb, fs)
		}
		for d := range vb {
			if vb[d] != vs[d] {
				t.Fatalf("%s vector mismatch batch vs single", id)
			}
		}
	}
}

// TestBatch_CoalesceLastWins: within one batch, a later op for an id supersedes the
// earlier (Put→Delete = gone; Put→Put = last value).
func TestBatch_CoalesceLastWins(t *testing.T) {
	s := openTestStore(t, DotProduct) // raw storage → exact value check
	b := s.NewBatch()
	b.Put("a", []float32{1, 1, 1, 1}, nil)
	b.Delete("a") // supersedes the Put
	b.Put("x", []float32{1, 1, 1, 1}, nil)
	b.Put("x", []float32{2, 2, 2, 2}, nil) // supersedes
	if b.Len() != 2 {
		t.Fatalf("Len = %d, want 2 (coalesced)", b.Len())
	}
	requireNoError(t, b.Commit())

	if _, _, found, _ := s.Get("a"); found {
		t.Fatal("a should be gone (Put then Delete coalesced to Delete)")
	}
	v, _, found, _ := s.Get("x")
	if !found || v[0] != 2 {
		t.Fatalf("x = %v found=%v, want last value {2,...}", v, found)
	}
}

// TestBatch_DeleteHeadAndSealed: a batch can delete both a head doc and a sealed doc
// in one commit.
func TestBatch_DeleteHeadAndSealed(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(9))
	b0 := s.NewBatch()
	for i := 0; i < 40; i++ {
		b0.Put("seal-"+itoa(i), batchRandVec(rng, 16), nil)
	}
	requireNoError(t, b0.Commit())
	requireNoError(t, s.Seal()) // seal-* now in a sealed segment
	requireNoError(t, s.Put("head-0", batchRandVec(rng, 16), nil))

	b := s.NewBatch()
	b.Delete("seal-3") // sealed
	b.Delete("head-0") // head
	requireNoError(t, b.Commit())

	if _, _, found, _ := s.Get("seal-3"); found {
		t.Fatal("seal-3 not deleted")
	}
	if _, _, found, _ := s.Get("head-0"); found {
		t.Fatal("head-0 not deleted")
	}
	if _, _, found, _ := s.Get("seal-4"); !found {
		t.Fatal("seal-4 wrongly affected")
	}
}

// TestBatch_ValidationRejects_NothingApplied: a malformed vector fails Commit before
// any apply, the batch is left intact, and no record landed.
func TestBatch_ValidationRejects_NothingApplied(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.Put("base", []float32{1, 2, 3, 4}, nil))
	b := s.NewBatch()
	b.Put("good", []float32{1, 2, 3, 4}, nil)
	b.Put("bad", []float32{1, 2, 3}, nil) // dim mismatch (base is dim 4)
	if err := b.Commit(); err == nil {
		t.Fatal("Commit should reject the dim-mismatch batch")
	}
	if b.Len() != 2 {
		t.Fatalf("batch should be intact for retry: Len = %d", b.Len())
	}
	if _, _, found, _ := s.Get("good"); found {
		t.Fatal("'good' must NOT be applied when the batch failed validation")
	}
}

// TestBatch_SealsWithinCommit: a batch that overflows maxSegSize seals into a sealed
// segment during Commit; everything stays Get-able.
func TestBatch_SealsWithinCommit(t *testing.T) {
	s := openTestStore(t, Cosine)
	s.maxSegSize = 50 // white-box: force a small head
	rng := rand.New(rand.NewSource(11))
	b := s.NewBatch()
	for i := 0; i < 120; i++ {
		b.Put("d-"+itoa(i), batchRandVec(rng, 16), nil)
	}
	requireNoError(t, b.Commit())
	requireNoError(t, s.WaitForIndex())
	if len(s.sealed) == 0 {
		t.Fatal("batch over maxSegSize should have sealed a segment")
	}
	for i := 0; i < 120; i++ {
		if _, _, found, _ := s.Get("d-" + itoa(i)); !found {
			t.Fatalf("d-%d lost across in-batch seal", i)
		}
	}
}

// TestBatch_CrossSegPutOverwrite: a batch Put of an id currently in a sealed
// segment tombstones the old sealed slot (in the same commit) and the new value
// wins; a Delete of an unknown id in the batch is a no-op.
func TestBatch_CrossSegPutOverwrite(t *testing.T) {
	s := openTestStore(t, DotProduct) // raw storage → exact value check
	requireNoError(t, s.Put("x", []float32{1, 1, 1, 1}, nil))
	requireNoError(t, s.Seal()) // x now lives in a sealed segment
	requireNoError(t, s.WaitForIndex())

	b := s.NewBatch()
	b.Put("x", []float32{9, 9, 9, 9}, nil) // cross-segment overwrite
	b.Delete("nonexistent")                // unknown id → no-op
	requireNoError(t, b.Commit())

	v, _, found, _ := s.Get("x")
	if !found || v[0] != 9 {
		t.Fatalf("x = %v found=%v, want overwritten {9,...}", v, found)
	}
	// The old sealed slot is tombstoned, so a search returns x exactly once.
	got, err := s.Search("default", []float32{9, 9, 9, 9}, 5, nil)
	requireNoError(t, err)
	seen := 0
	for _, h := range got {
		if h.DocID == s.idToDoc["x"] {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("x appears %d times in search, want exactly 1 (stale sealed slot not tombstoned)", seen)
	}
}
