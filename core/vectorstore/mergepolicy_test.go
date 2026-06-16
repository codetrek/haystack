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
