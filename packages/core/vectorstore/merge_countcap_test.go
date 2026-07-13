package vectorstore

import "testing"

// TestPickGrowthMerge_CountCapFiresOnOverTarget red-proofs Phase-4 finding #2: the
// count-cap fallback must bound total segment count even when NO single tier has
// reached Fanout AND no tier has >= 2 like-sized segments (the all-singleton-tier
// case). §4.9's core invariant is "k-NN searches EVERY segment → read amplification
// = total segment count → bound it". The tier+fanout pick alone leaves an over-
// target store stranded forever if its segments are spread one-per-tier, which the
// old fallback (which required a tier with >= 2 members) could not break.
//
// Setup: five segments at counts {2,4,8,16,32} → tiers {1,2,3,4,5}, every tier a
// SINGLETON. With TargetSegCount=2 the store is over target. No tier reaches Fanout,
// no tier has 2 members. pickGrowthMerge MUST still return a non-nil merge of >= 2
// segments (bounded by MaxMergedSize) so the count strictly drops toward target.
func TestPickGrowthMerge_CountCapFiresOnOverTarget(t *testing.T) {
	cfg := mergeConfig{
		Fanout:         8,
		MaxMergedSize:  1000,
		TargetSegCount: 2,
		MergeFloor:     0.5,
	}.withDefaults()
	stats := []segLiveStats{
		{id: 1, count: 2, live: 2},
		{id: 2, count: 4, live: 4},
		{id: 3, count: 8, live: 8},
		{id: 4, count: 16, live: 16},
		{id: 5, count: 32, live: 32},
	}

	got := pickGrowthMerge(stats, cfg)
	if got == nil {
		t.Fatal("pickGrowthMerge returned nil for an over-target all-singleton-tier set: " +
			"the count cap never bounds total segment count (finding #2)")
	}
	if len(got) < 2 {
		t.Fatalf("pickGrowthMerge = %v (len %d), want a merge of >= 2 segments to reduce count", got, len(got))
	}
	// The pick must reduce the segment count toward target: merging len(got) inputs
	// into <= 1 output (live sum fits MaxMergedSize) drops the count by len(got)-1.
	live := map[segID]int{}
	for _, st := range stats {
		live[st.id] = st.live
	}
	sum := 0
	for _, id := range got {
		sum += live[id]
	}
	if sum > cfg.MaxMergedSize {
		t.Fatalf("pickGrowthMerge picked a group whose live sum %d exceeds MaxMergedSize %d", sum, cfg.MaxMergedSize)
	}
	newCount := len(stats) - len(got) + 1 // inputs collapse into one output
	if newCount >= len(stats) {
		t.Fatalf("pick does not reduce count: %d -> %d", len(stats), newCount)
	}
}

// TestPickGrowthMerge_PrefersSmallestSegments: when greedily breaking an over-target
// all-singleton set, the fallback should consume the SMALLEST segments first (cheap
// merges, drains the long tail) rather than the largest. The two smallest here
// (count 2 + 4 = live 6) fit MaxMergedSize; assert they are the ones chosen.
func TestPickGrowthMerge_PrefersSmallestSegments(t *testing.T) {
	cfg := mergeConfig{Fanout: 8, MaxMergedSize: 16, TargetSegCount: 2}.withDefaults()
	stats := []segLiveStats{
		{id: 10, count: 2, live: 2},
		{id: 20, count: 4, live: 4},
		{id: 30, count: 32, live: 32}, // alone too big to pair under MaxMergedSize=16
		{id: 40, count: 64, live: 64},
	}
	got := pickGrowthMerge(stats, cfg)
	if got == nil {
		t.Fatal("pickGrowthMerge returned nil over target (finding #2 all-singleton gap)")
	}
	// Must pick the two smallest (10,20): live 2+4=6 <= 16; any pair including 30/40
	// would exceed MaxMergedSize.
	picked := map[segID]bool{}
	for _, id := range got {
		picked[id] = true
	}
	if !picked[10] || !picked[20] {
		t.Fatalf("pickGrowthMerge = %v, want the two smallest segments {10,20}", got)
	}
	for _, id := range got {
		if id == 30 || id == 40 {
			t.Fatalf("pickGrowthMerge = %v, must not include an over-cap large segment", got)
		}
	}
}

// TestPickGrowthMerge_NilWhenAtTarget: a store already at/under TargetSegCount with
// no fanout-ready tier is healthy — the count cap must NOT force a merge.
func TestPickGrowthMerge_NilWhenAtTarget(t *testing.T) {
	cfg := mergeConfig{Fanout: 8, MaxMergedSize: 1000, TargetSegCount: 3}.withDefaults()
	stats := []segLiveStats{
		{id: 1, count: 2, live: 2},
		{id: 2, count: 8, live: 8},
		{id: 3, count: 32, live: 32},
	}
	if got := pickGrowthMerge(stats, cfg); got != nil {
		t.Fatalf("pickGrowthMerge = %v, want nil (at target, no fanout-ready tier)", got)
	}
}

// TestPickGrowthMerge_NilWhenEverySmallestPairOverCap: over target but the two
// smallest segments together already exceed MaxMergedSize → no admissible greedy
// pick exists, so the cap returns nil rather than violate the size bound.
func TestPickGrowthMerge_NilWhenEverySmallestPairOverCap(t *testing.T) {
	cfg := mergeConfig{Fanout: 8, MaxMergedSize: 10, TargetSegCount: 1}.withDefaults()
	stats := []segLiveStats{
		{id: 1, count: 8, live: 8},
		{id: 2, count: 16, live: 16},
		{id: 3, count: 32, live: 32},
	}
	// Smallest pair live sum 8+16=24 > MaxMergedSize 10 → no admissible merge.
	if got := pickGrowthMerge(stats, cfg); got != nil {
		t.Fatalf("pickGrowthMerge = %v, want nil (no pair fits MaxMergedSize)", got)
	}
}
