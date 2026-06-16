package vectorstore

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestMergeConfig_Defaults(t *testing.T) {
	c := mergeConfig{}.withDefaults()
	if c.MergeFloor <= 0 || c.MergeFloor >= 1 {
		t.Fatalf("MergeFloor default = %v, want in (0,1)", c.MergeFloor)
	}
	if c.Fanout < 2 {
		t.Fatalf("Fanout default = %d, want >= 2", c.Fanout)
	}
	if c.MaxMergedSize <= 0 {
		t.Fatalf("MaxMergedSize default = %d, want > 0", c.MaxMergedSize)
	}
	if c.TargetSegCount <= 0 {
		t.Fatalf("TargetSegCount default = %d, want > 0", c.TargetSegCount)
	}
	// withDefaults must not clobber an operator-set value.
	got := mergeConfig{MergeFloor: 0.25, Fanout: 4}.withDefaults()
	if got.MergeFloor != 0.25 || got.Fanout != 4 {
		t.Fatalf("withDefaults clobbered set values: %+v", got)
	}
}

func TestStore_HasMergeConfig(t *testing.T) {
	s := openTestStore(t, Cosine)
	if s.mcfg.Fanout < 2 {
		t.Fatalf("store mergeConfig not initialized: %+v", s.mcfg)
	}
}

func TestPackLiveDocs_BinPacksAndCarriesPayload(t *testing.T) {
	s := openTestStore(t, DotProduct)
	// Build two sealed segments with known live docs.
	mk := func(ids []string, vecs [][]float32) *sealedSegment {
		seg := newSegment(DotProduct)
		for i, id := range ids {
			doc, err := s.docIDForAlloc(id)
			requireNoError(t, err)
			st, nrm := DotProduct.prepare(vecs[i])
			seg.append(doc, st, nrm, []byte(id)) // payload = id bytes (asserted below)
		}
		dir := t.TempDir()
		requireNoError(t, writeSealedSegment(dir, seg))
		ss, err := openSealedSegment(dir, DotProduct)
		requireNoError(t, err)
		return ss
	}
	a := mk([]string{"a0", "a1", "a2"}, [][]float32{{1, 0}, {0, 1}, {1, 1}})
	b := mk([]string{"b0", "b1"}, [][]float32{{2, 0}, {0, 2}})

	// maxSegSize 2 → 5 live docs pack into 3 buckets (2,2,1).
	buckets, moved := packLiveDocs([]*sealedSegment{a, b}, DotProduct, 2)
	if len(buckets) != 3 {
		t.Fatalf("buckets = %d, want 3", len(buckets))
	}
	total := 0
	seen := map[int64]bool{}
	for _, bk := range buckets {
		if len(bk.slotDoc) > 2 {
			t.Fatalf("bucket overflow: %d rows > maxSegSize 2", len(bk.slotDoc))
		}
		bk.eachLive(func(slot int, doc int64, stored []float32, norm float32) {
			total++
			seen[doc] = true
			// payload travels with the doc.
			_, _, pl, _ := bk.read(slot)
			if len(pl) == 0 {
				t.Fatalf("doc %d lost its payload during pack", doc)
			}
		})
	}
	if total != 5 {
		t.Fatalf("packed live docs = %d, want 5", total)
	}
	if len(moved) != 5 {
		t.Fatalf("moved set = %d, want 5", len(moved))
	}
	for d := range seen {
		if !moved[d] {
			t.Fatalf("doc %d packed but not in moved set", d)
		}
	}
}

func TestPackLiveDocs_ExcludesTombstoned(t *testing.T) {
	s := openTestStore(t, DotProduct)
	seg := newSegment(DotProduct)
	for _, id := range []string{"x", "y", "z"} {
		doc, err := s.docIDForAlloc(id)
		requireNoError(t, err)
		st, nrm := DotProduct.prepare([]float32{1, 0})
		seg.append(doc, st, nrm, nil)
	}
	dir := t.TempDir()
	requireNoError(t, writeSealedSegment(dir, seg))
	ss, err := openSealedSegment(dir, DotProduct)
	requireNoError(t, err)
	requireNoError(t, ss.tombstoneSlot(1)) // tombstone "y"

	buckets, moved := packLiveDocs([]*sealedSegment{ss}, DotProduct, 50)
	got := 0
	for _, bk := range buckets {
		got += len(bk.slotDoc)
	}
	if got != 2 || len(moved) != 2 {
		t.Fatalf("packed %d docs (moved %d), want 2 (tombstoned excluded)", got, len(moved))
	}
}

func TestStore_segStatsLocked(t *testing.T) {
	s := openTestStore(t, DotProduct)
	requireNoError(t, s.Put("a", []float32{1, 0, 0}, nil))
	requireNoError(t, s.Put("b", []float32{0, 1, 0}, nil))
	requireNoError(t, s.Put("c", []float32{0, 0, 1}, nil))
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	requireNoError(t, s.Delete("b")) // tombstone one of three → live 2 / count 3

	s.mu.RLock()
	stats := s.segStatsLocked()
	s.mu.RUnlock()

	if len(stats) != 1 {
		t.Fatalf("segStatsLocked len = %d, want 1", len(stats))
	}
	st := stats[0]
	if st.id != segID(1) || st.count != 3 || st.live != 2 {
		t.Fatalf("stats = %+v, want {id:1 count:3 live:2}", st)
	}
	if r := st.liveRatio(); r < 0.66 || r > 0.67 {
		t.Fatalf("liveRatio = %v, want ~0.666", r)
	}
}

// TestMerge_CompactOfOne reclaims a heavy-tombstone single segment: a "merge of
// one" rewrites only the live docs into a fresh segment, the old dir is deleted,
// docIds survive, and Search still returns the live set. (architecture §4.9
// "单段 compact = merge 1 个".)
func TestMerge_CompactOfOne(t *testing.T) {
	s := openTestStore(t, Cosine)
	rng := rand.New(rand.NewSource(3))
	dim := 8
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	live := map[int64][]float32{}
	for i := 0; i < 40; i++ {
		id := "d-" + itoa(i)
		v := randVec()
		requireNoError(t, s.Put(id, v, nil))
		live[s.idToDoc[id]] = v
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	oldDir := filepath.Join(s.dir, "seg-1-0")

	// Delete 60% → liveRatio 0.4 < mergeFloor; merge reclaims it.
	for i := 0; i < 40; i++ {
		if i%5 < 3 { // 60% deleted
			id := "d-" + itoa(i)
			requireNoError(t, s.Delete(id))
			delete(live, s.idToDoc[id])
		}
	}

	requireNoError(t, s.mergeNow([]segID{1}))
	requireNoError(t, s.WaitForMerge())

	// Old input dir gone (space reclaimed); a NEW segId replaced it.
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatal("merge did not delete the old input segment dir")
	}
	s.mu.RLock()
	nSealed := len(s.sealed)
	newID := s.sealedID[0]
	newCount := s.sealed[0].count()
	newTomb := s.sealed[0].tombCount()
	s.mu.RUnlock()
	if nSealed != 1 {
		t.Fatalf("sealed segments after merge = %d, want 1", nSealed)
	}
	if newID == segID(1) {
		t.Fatal("merge reused old segId; multi-id model requires a fresh segId")
	}
	if newCount != 16 || newTomb != 0 {
		t.Fatalf("merged seg count=%d tomb=%d, want 16 live / 0 tomb (repacked)", newCount, newTomb)
	}

	// docToSeg rehomed every surviving doc to the new segment.
	s.mu.RLock()
	for doc := range live {
		if s.docToSeg[doc] != newID {
			s.mu.RUnlock()
			t.Fatalf("doc %d not rehomed to merged segId %d (got %d)", doc, newID, s.docToSeg[doc])
		}
	}
	s.mu.RUnlock()

	// Survivors readable, deleted gone, recall holds.
	if _, _, found, _ := s.Get("d-0"); found { // d-0: i%5==0 → deleted
		t.Fatal("deleted d-0 resurrected by merge")
	}
	if _, _, found, _ := s.Get("d-3"); !found { // d-3: i%5==3 → live
		t.Fatal("live d-3 lost by merge")
	}
	var sum float64
	for it := 0; it < 20; it++ {
		q := randVec()
		got, err := s.Search(q, 5)
		requireNoError(t, err)
		sum += recallAtK(got, bruteForceKNN(Cosine, q, live, 5))
	}
	if avg := sum / 20; avg < 0.8 {
		t.Fatalf("post-merge recall@5 = %.3f, want >= 0.8", avg)
	}
}

// TestMerge_MultiInputRehomesOnlyMovedDocs proves the two-level id invariant: a
// 2-input merge updates docToSeg ONLY for the merged docs; a third untouched
// segment keeps its segId and its docs' mapping (architecture §4.6).
func TestMerge_MultiInputRehomesOnlyMovedDocs(t *testing.T) {
	s := openTestStore(t, DotProduct)
	mkSeg := func(ids []string) {
		for _, id := range ids {
			requireNoError(t, s.Put(id, []float32{float32(len(id)), 1, 0}, nil))
		}
		requireNoError(t, s.Seal())
	}
	mkSeg([]string{"a", "aa"}) // seg 1
	mkSeg([]string{"b", "bb"}) // seg 2
	mkSeg([]string{"c", "cc"}) // seg 3 (the bystander)
	requireNoError(t, s.WaitForIndex())

	cDoc, ccDoc := s.idToDoc["c"], s.idToDoc["cc"]

	requireNoError(t, s.mergeNow([]segID{1, 2}))
	requireNoError(t, s.WaitForMerge())

	s.mu.RLock()
	defer s.mu.RUnlock()
	// Bystander seg 3 still present with its original segId.
	if s.sealedByID(3) == nil {
		t.Fatal("untouched segment 3 vanished after merging 1+2")
	}
	if s.docToSeg[cDoc] != segID(3) || s.docToSeg[ccDoc] != segID(3) {
		t.Fatalf("bystander docs c/cc wrongly rehomed: %d,%d (want 3)", s.docToSeg[cDoc], s.docToSeg[ccDoc])
	}
	// Merged docs point at a brand-new segId that is not 1, 2, or 3.
	aDoc := s.idToDoc["a"]
	newID := s.docToSeg[aDoc]
	if newID == 1 || newID == 2 || newID == 3 || newID == headSegID {
		t.Fatalf("merged doc 'a' segId = %d, want a fresh sealed id", newID)
	}
	// Inputs 1 and 2 are gone from the set.
	if s.sealedByID(1) != nil || s.sealedByID(2) != nil {
		t.Fatal("merge inputs 1/2 still in the sealed set after swap")
	}
}
