package vectorstore

import (
	"math/rand"
	"path/filepath"
	"sort"
	"testing"
)

func TestStore_Search_MergesHeadAndIndexedSealed(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(31))
	dim := 16
	vecs := make(map[int64][]float32)

	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	// putBatch Puts n vectors in one batch commit (one fsync), then resolves the
	// docId-keyed oracle AFTER commit (idToDoc is populated by Commit, not before).
	putBatch := func(prefix string, n int) {
		b := s.NewBatch()
		ids := make([]string, n)
		vs := make([][]float32, n)
		for i := 0; i < n; i++ {
			ids[i] = prefix + itoa(i)
			vs[i] = randVec()
			b.Put(ids[i], vs[i], nil)
		}
		requireNoError(t, b.Commit())
		for i := 0; i < n; i++ {
			vecs[s.idToDoc[ids[i]]] = append([]float32(nil), vs[i]...)
		}
	}

	// Batch 1 → seal → build graph (indexed sealed segment).
	putBatch("s1-", 120)
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	// Batch 2 stays in the head (brute leg).
	putBatch("h-", 40)

	q := randVec()
	got, err := s.Search("default", q, 10, nil)
	requireNoError(t, err)
	want := bruteForceKNN(Cosine, q, vecs, 10)
	if r := recallAtK(got, want); r < 0.8 {
		t.Fatalf("merged recall@10 = %.2f, want >= 0.8", r)
	}
	// Results ascending by distance.
	if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].Distance <= got[j].Distance }) {
		t.Fatalf("results not ascending by distance: %v", got)
	}
}

func TestStore_Search_IndexedSegmentTombstoneFiltered(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(32))
	dim := 8
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	b := s.NewBatch()
	for i := 0; i < 80; i++ {
		b.Put("x-"+itoa(i), randVec(), nil)
	}
	requireNoError(t, b.Commit())
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	// Delete one doc that now lives in the indexed sealed segment.
	requireNoError(t, s.Delete("x-0"))
	deletedDoc := s.idToDoc["x-0"]

	// Search many times; the deleted doc must never appear (graph would return it
	// without the tombstone post-filter).
	for iter := 0; iter < 20; iter++ {
		q := randVec()
		got, err := s.Search("default", q, 20, nil)
		requireNoError(t, err)
		for _, r := range got {
			if r.DocID == deletedDoc {
				t.Fatalf("tombstoned docId %d leaked through indexed graph leg", deletedDoc)
			}
		}
	}
}

// TestStore_Search_TombstoneFilterMustFire is the deterministic version of the
// tombstone-leak test (appendix #17): the deleted doc is the EXACT nearest to the
// query, so the indexed graph WILL return it and the tombstone post-filter MUST
// fire. The random test above can false-green if the deleted doc never enters the
// top-k; this one cannot.
func TestStore_Search_TombstoneFilterMustFire(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(99))
	dim := 8
	target := make([]float32, dim)
	for d := range target {
		target[d] = rng.Float32() + 0.5
	}
	// Insert the target vector plus a cloud of others, all sealed + indexed.
	b := s.NewBatch()
	b.Put("target", append([]float32(nil), target...), nil)
	for i := 0; i < 120; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		b.Put("o-"+itoa(i), v, nil)
	}
	requireNoError(t, b.Commit())
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	targetDoc := s.idToDoc["target"]

	// Sanity: with the target live, querying its own vector returns it first.
	got, err := s.Search("default", target, 5, nil)
	requireNoError(t, err)
	if len(got) == 0 || got[0].DocID != targetDoc {
		t.Fatalf("pre-delete: target should be the nearest to itself, got %v", docIDs(got))
	}

	// Delete the target (now tombstoned in the indexed sealed segment) and query
	// its exact vector again: the graph still has the node, so the post-filter is
	// the only thing keeping it out of the results.
	requireNoError(t, s.Delete("target"))
	got, err = s.Search("default", target, 5, nil)
	requireNoError(t, err)
	if len(got) == 0 {
		t.Fatal("post-delete: expected the 2nd-nearest to be returned, got nothing")
	}
	for _, r := range got {
		if r.DocID == targetDoc {
			t.Fatalf("post-delete: tombstoned target %d leaked through indexed graph leg", targetDoc)
		}
	}
}

// TestStore_Search_MergesPendingSealedBrute exercises the pending-sealed brute
// leg: a sealed-but-not-yet-indexed segment must still be searched (brute over
// its live slots) and merged with the head.
func TestStore_Search_MergesPendingSealedBrute(t *testing.T) {
	s := openTestStore(t, DotProduct)
	// Three orthogonal docs into the head.
	requireNoError(t, s.Put("a", []float32{1, 0, 0}, nil))
	requireNoError(t, s.Put("b", []float32{0, 1, 0}, nil))
	requireNoError(t, s.Put("c", []float32{0, 0, 1}, nil))

	// Seal but DO NOT build a graph: attach as a pending sealed segment.
	segDir := filepath.Join(s.dir, "seg-7-0")
	requireNoError(t, writeSealedSegment(segDir, s.seg, nil))
	ss, err := openSealedSegment(segDir, DotProduct, 7, nil)
	requireNoError(t, err)
	s.attachSealedForTest(ss, 7) // no graph for seg 7 in any index → pending → brute leg

	// One more doc into the fresh head.
	requireNoError(t, s.Put("d", []float32{0, 1, 1}, nil))

	got, err := s.Search("default", []float32{0, 1, 0}, 4, nil)
	requireNoError(t, err)
	docs := map[int64]bool{}
	for _, r := range got {
		docs[r.DocID] = true
	}
	// "b" (sealed, exact match) and "d" (head) must both surface.
	if !docs[s.idToDoc["b"]] {
		t.Fatal("pending sealed brute leg did not return b")
	}
	if !docs[s.idToDoc["d"]] {
		t.Fatal("head leg did not return d")
	}
}

// TestStore_Get_LiveSealedDoc covers the Get happy path through a sealed segment
// (the docToSeg→sealedByID→read branch added in Task 8): after Seal, a live doc
// is fetched from the sealed segment, not the (now empty) head.
func TestStore_Get_LiveSealedDoc(t *testing.T) {
	s := openTestStore(t, DotProduct)
	requireNoError(t, s.Put("a", []float32{1, 2, 3}, Payload{"p": StringValue("pa")}))
	requireNoError(t, s.Put("b", []float32{4, 5, 6}, nil))
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())

	// Both docs now live in the sealed segment; the head is empty.
	v, pl, found, err := s.Get("a")
	requireNoError(t, err)
	if !found {
		t.Fatal("Get(a) should be found in the sealed segment")
	}
	if len(v) != 3 || v[0] != 1 || v[2] != 3 || pl["p"].Str != "pa" {
		t.Fatalf("Get(a) = v=%v pl=%#v, want {1,2,3} {p:pa}", v, pl)
	}
	// Deleting then Get returns not-found via the sealed path.
	requireNoError(t, s.Delete("b"))
	if _, _, found, _ := s.Get("b"); found {
		t.Fatal("Get(b) should be not-found after sealed delete")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
