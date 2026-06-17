package vectorstore

import (
	"reflect"
	"testing"
)

func TestPickDeleteDriven_SelectsBelowFloor(t *testing.T) {
	cfg := mergeConfig{MergeFloor: 0.5}.withDefaults()
	stats := []segLiveStats{
		{id: 1, count: 100, live: 30}, // 0.30 < 0.5 → pick
		{id: 2, count: 100, live: 90}, // 0.90 → skip
		{id: 3, count: 100, live: 49}, // 0.49 < 0.5 → pick
		{id: 4, count: 100, live: 50}, // 0.50 not < 0.5 → skip
	}
	got := pickDeleteDriven(stats, cfg)
	want := []segID{1, 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pickDeleteDriven = %v, want %v", got, want)
	}
}

func TestPickDeleteDriven_EmptyWhenAllHealthy(t *testing.T) {
	cfg := mergeConfig{MergeFloor: 0.5}.withDefaults()
	stats := []segLiveStats{{id: 1, count: 10, live: 10}, {id: 2, count: 10, live: 8}}
	if got := pickDeleteDriven(stats, cfg); got != nil {
		t.Fatalf("pickDeleteDriven = %v, want nil (all healthy)", got)
	}
}

func TestPickDeleteDriven_SkipsEmptySegments(t *testing.T) {
	// A fully-tombstoned segment (live 0) is still bait, but a zero-row segment is
	// not (nothing to reclaim) — liveRatio of count==0 is defined as 1.
	cfg := mergeConfig{MergeFloor: 0.5}.withDefaults()
	stats := []segLiveStats{{id: 1, count: 0, live: 0}, {id: 2, count: 10, live: 0}}
	got := pickDeleteDriven(stats, cfg)
	want := []segID{2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pickDeleteDriven = %v, want %v", got, want)
	}
}

func TestSizeTier_PowerOfTwoBuckets(t *testing.T) {
	cases := []struct {
		count int
		tier  int
	}{
		{0, 0}, {1, 0}, {2, 1}, {3, 1}, {4, 2}, {7, 2}, {8, 3}, {16, 4},
	}
	for _, c := range cases {
		if got := sizeTier(c.count); got != c.tier {
			t.Fatalf("sizeTier(%d) = %d, want %d", c.count, got, c.tier)
		}
	}
}

func TestPickGrowthTiered_MergesWhenTierReachesFanout(t *testing.T) {
	cfg := mergeConfig{Fanout: 3, MaxMergedSize: 1000}.withDefaults()
	// Three segments in the same tier (count 4,5,6 → tier 2) → fanout reached.
	stats := []segLiveStats{
		{id: 1, count: 4, live: 4},
		{id: 2, count: 5, live: 5},
		{id: 3, count: 6, live: 6},
		{id: 4, count: 64, live: 64}, // tier 6, alone → not picked
	}
	got := pickGrowthTiered(stats, cfg)
	want := []segID{1, 2, 3}
	if !equalSegIDs(got, want) {
		t.Fatalf("pickGrowthTiered = %v, want %v", got, want)
	}
}

func TestPickGrowthTiered_NoneWhenBelowFanout(t *testing.T) {
	cfg := mergeConfig{Fanout: 3, MaxMergedSize: 1000}.withDefaults()
	stats := []segLiveStats{{id: 1, count: 4, live: 4}, {id: 2, count: 5, live: 5}}
	if got := pickGrowthTiered(stats, cfg); got != nil {
		t.Fatalf("pickGrowthTiered = %v, want nil (tier below fanout)", got)
	}
}

func TestPickGrowthTiered_RespectsMaxMergedSize(t *testing.T) {
	// Fanout reached but the live sum would exceed MaxMergedSize → do not merge
	// (the cap protects against an un-mergeable giant; §4.9).
	cfg := mergeConfig{Fanout: 2, MaxMergedSize: 10}.withDefaults()
	stats := []segLiveStats{{id: 1, count: 8, live: 8}, {id: 2, count: 9, live: 9}}
	if got := pickGrowthTiered(stats, cfg); got != nil {
		t.Fatalf("pickGrowthTiered = %v, want nil (would exceed MaxMergedSize)", got)
	}
}

func equalSegIDs(a, b []segID) bool {
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
