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
