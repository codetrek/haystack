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
