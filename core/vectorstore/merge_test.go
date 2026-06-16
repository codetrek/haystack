package vectorstore

import "testing"

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
