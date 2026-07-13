package vectorstore

import (
	"math/rand"
	"path/filepath"
	"testing"
)

func TestSegGraphStore_BuildOverSealedSegment_Recall(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	dim := 16
	n := 250
	rows := make([]struct {
		doc int64
		v   []float32
		pl  Payload
	}, 0, n)
	vecs := make(map[int64][]float32, n)
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		doc := int64(1000 + i)
		rows = append(rows, struct {
			doc int64
			v   []float32
			pl  Payload
		}{doc, v, nil})
		vecs[doc] = v
	}
	head := buildHeadSeg(Cosine, rows)
	head.tombstone(5) // a hole — must be skipped by the dense build
	delete(vecs, int64(1005))

	segDir := filepath.Join(t.TempDir(), "seg-1-0")
	requireNoError(t, writeSealedSegment(segDir, head, nil))
	ss, err := openSealedSegment(segDir, Cosine, 1, nil)
	requireNoError(t, err)
	defer ss.close()

	gs := newSegGraphStore(ss)
	idx := newHNSWIndex(gs, withGraphM(16), withGraphEfConstruction(100),
		withGraphRand(rand.New(rand.NewSource(8))))
	b := idx.newBatch()
	// Build over LIVE slots only, feeding stored form + segment docId.
	ss.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		gs.bindSlot(docID, slot) // teach the store nodeId↔(docId,slot)
		b.put(docID, stored)
	})
	requireNoError(t, b.commit())

	q := make([]float32, dim)
	for d := range q {
		q[d] = rng.Float32()
	}
	got, err := idx.search(q, 10)
	requireNoError(t, err)
	want := bruteForceKNN(Cosine, q, vecs, 10)
	if r := recallAtK(got, want); r < 0.8 {
		t.Fatalf("recall@10 over sealed segment = %.2f, want >= 0.8", r)
	}
	// Returned docIds must be real segment docIds (>=1000), never the tombstoned 1005.
	for _, r := range got {
		if r.DocID < 1000 {
			t.Fatalf("bogus docId %d", r.DocID)
		}
		if r.DocID == 1005 {
			t.Fatal("tombstoned docId 1005 leaked into graph results")
		}
	}
}

// TestSegGraphStore_InteriorTombstones_DenseIdBoundary applies adversarial-review
// appendix #4: the design's "nodeId == slot" claim is FALSE — nodeId is a dense
// live-only build index, and a segment with INTERIOR tombstone gaps would alias
// or overflow the graph's visitedSet if nodeId were the raw slot. Sealing with
// several pre-seal tombstones and then building+searching proves the dense
// build-id model: (a) no panic, (b) recall holds over the live set, (c) no
// tombstoned docId leaks out. Task 5's original test only tombstones ONE slot
// and never exercises the dense-id boundary this guards.
func TestSegGraphStore_InteriorTombstones_DenseIdBoundary(t *testing.T) {
	rng := rand.New(rand.NewSource(31))
	dim := 16
	n := 200
	rows := make([]struct {
		doc int64
		v   []float32
		pl  Payload
	}, 0, n)
	vecs := make(map[int64][]float32, n)
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		doc := int64(2000 + i)
		rows = append(rows, struct {
			doc int64
			v   []float32
			pl  Payload
		}{doc, v, nil})
		vecs[doc] = v
	}
	head := buildHeadSeg(Cosine, rows)
	// Interior holes scattered through the slot space (not just slot 0/end).
	tombSlots := []int{5, 17, 40, 41, 88, 150, 199}
	tombDocs := make(map[int64]bool)
	for _, slot := range tombSlots {
		head.tombstone(slot)
		doc := int64(2000 + slot)
		tombDocs[doc] = true
		delete(vecs, doc)
	}

	segDir := filepath.Join(t.TempDir(), "seg-9-0")
	requireNoError(t, writeSealedSegment(segDir, head, nil))
	ss, err := openSealedSegment(segDir, Cosine, 1, nil)
	requireNoError(t, err)
	defer ss.close()

	gs := newSegGraphStore(ss)
	idx := newHNSWIndex(gs, withGraphM(16), withGraphEfConstruction(100),
		withGraphRand(rand.New(rand.NewSource(32))))
	b := idx.newBatch()
	ss.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		gs.bindSlot(docID, slot)
		b.put(docID, stored)
	})
	requireNoError(t, b.commit())

	q := make([]float32, dim)
	for d := range q {
		q[d] = rng.Float32()
	}
	got, err := idx.search(q, 10) // must not panic in visitedSet
	requireNoError(t, err)
	want := bruteForceKNN(Cosine, q, vecs, 10)
	if r := recallAtK(got, want); r < 0.8 {
		t.Fatalf("recall@10 over interior-tombstoned segment = %.2f, want >= 0.8", r)
	}
	for _, r := range got {
		if tombDocs[r.DocID] {
			t.Fatalf("tombstoned docId %d leaked into graph results", r.DocID)
		}
	}
}

// TestSegGraphStore_MemEquivalence applies adversarial-review appendix #10/#21:
// the migrated graph is parity-tested against memGraphStore (Task 4), but the
// thing actually shipped is segGraphStore. With both stores now 0-based, feeding
// the SAME vectors through both with the SAME seeded RNG must yield identical
// search output — proving the seg path is covered, not just the mem path.
func TestSegGraphStore_MemEquivalence(t *testing.T) {
	rng := rand.New(rand.NewSource(101))
	dim := 12
	n := 150
	rows := make([]struct {
		doc int64
		v   []float32
		pl  Payload
	}, 0, n)
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		rows = append(rows, struct {
			doc int64
			v   []float32
			pl  Payload
		}{int64(i), v, nil})
	}
	head := buildHeadSeg(Cosine, rows)
	segDir := filepath.Join(t.TempDir(), "seg-1-0")
	requireNoError(t, writeSealedSegment(segDir, head, nil))
	ss, err := openSealedSegment(segDir, Cosine, 1, nil)
	requireNoError(t, err)
	defer ss.close()

	// Build through memGraphStore (reference) and segGraphStore (shipped), same
	// RNG seed, same live-slot order.
	mem := newMemGraphStore(Cosine)
	memIdx := newHNSWIndex(mem, withGraphM(16), withGraphEfConstruction(100),
		withGraphRand(rand.New(rand.NewSource(202))))
	mb := memIdx.newBatch()

	seg := newSegGraphStore(ss)
	segIdx := newHNSWIndex(seg, withGraphM(16), withGraphEfConstruction(100),
		withGraphRand(rand.New(rand.NewSource(202))))
	sb := segIdx.newBatch()

	ss.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		mb.put(docID, stored)
		seg.bindSlot(docID, slot)
		sb.put(docID, stored)
	})
	requireNoError(t, mb.commit())
	requireNoError(t, sb.commit())

	q := make([]float32, dim)
	for d := range q {
		q[d] = rng.Float32()
	}
	memRes, err := memIdx.search(q, 10)
	requireNoError(t, err)
	segRes, err := segIdx.search(q, 10)
	requireNoError(t, err)

	if len(memRes) != len(segRes) {
		t.Fatalf("result count mem=%d seg=%d", len(memRes), len(segRes))
	}
	for i := range memRes {
		if memRes[i].DocID != segRes[i].DocID {
			t.Fatalf("result[%d] mem docId=%d seg docId=%d (0-based id divergence)", i, memRes[i].DocID, segRes[i].DocID)
		}
	}
}

// TestSegGraphStore_getNeighborsRef_Guard covers getNeighborsRef's guard branch
// (out-of-range id / nil-neighbors node) that the search tests never hit, since
// search only ever passes valid node ids.
func TestSegGraphStore_getNeighborsRef_Guard(t *testing.T) {
	g := &segGraphStore{neighbors: []map[int][]uint32{nil}} // node 0 present but nil

	if got := g.getNeighborsRef(5, 0); got != nil { // id >= len(neighbors)
		t.Errorf("getNeighborsRef(out-of-range) = %v, want nil", got)
	}
	if got := g.getNeighborsRef(0, 0); got != nil { // neighbors[0] == nil
		t.Errorf("getNeighborsRef(nil node) = %v, want nil", got)
	}

	g.neighbors[0] = map[int][]uint32{0: {7, 8}}
	got := g.getNeighborsRef(0, 0)
	if len(got) != 2 || got[0] != 7 || got[1] != 8 {
		t.Errorf("getNeighborsRef(valid) = %v, want [7 8]", got)
	}
}
