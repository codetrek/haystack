package vectorstore

import "math/bits"

// pickDeleteDriven returns the ids of sealed segments whose live ratio is below
// mergeFloor — heavy-tombstone "deflated" segments whose live docs should be
// bin-packed into fresh ~maxSegSize buckets, reclaiming tombstone space and
// consolidating count (architecture §4.9, delete driver). Order follows the input
// (attach order). A segment with no rows is never picked (nothing to reclaim).
func pickDeleteDriven(stats []segLiveStats, cfg mergeConfig) []segID {
	var picks []segID
	for _, st := range stats {
		if st.count == 0 {
			continue
		}
		if st.liveRatio() < cfg.MergeFloor {
			picks = append(picks, st.id)
		}
	}
	return picks
}

// sizeTier maps a segment row count to a power-of-two size tier: tier t holds
// counts in [2^t, 2^(t+1)). Counts 0 and 1 share tier 0. Size-tiered merge groups
// like-sized segments so a merge roughly doubles size each level, bounding the
// number of tiers (hence total segment count) logarithmically in the corpus size
// (architecture §4.9, growth driver; NOT leveled — vectors have no key range).
//
// NOTE (deviation from plan Task 4c): the plan's body was bits.Len(uint(count-1))
// — that is ceil(log2(count)), which contradicts the [2^t,2^(t+1)) tier definition
// stated in the same comment and fails the plan's OWN TestSizeTier cases
// {3→1} and {7→2} (it returns 2 and 3). The tier definition is floor(log2(count)),
// i.e. bits.Len(uint(count))-1 for count>=1. Fixed minimally to keep tests green.
func sizeTier(count int) int {
	if count < 2 {
		return 0
	}
	return bits.Len(uint(count)) - 1 // floor(log2(count)) for count>=2
}

// pickGrowthTiered returns the ids of the FIRST size tier (lowest first) that has
// accumulated >= Fanout segments AND whose combined live rows fit MaxMergedSize.
// Merging that tier folds K small segments into one larger one in the next tier,
// bounding total segment count as the corpus grows. Returns nil if no tier
// qualifies. Only the live-row sum is checked against the cap (the output holds
// only live docs).
func pickGrowthTiered(stats []segLiveStats, cfg mergeConfig) []segID {
	// Group ids by tier (preserve attach order within a tier).
	tiers := make(map[int][]segLiveStats)
	var order []int
	for _, st := range stats {
		t := sizeTier(st.count)
		if _, seen := tiers[t]; !seen {
			order = append(order, t)
		}
		tiers[t] = append(tiers[t], st)
	}
	// Lowest tier first → keeps merges small/cheap and drains the long tail.
	sortIntsAsc(order)
	for _, t := range order {
		group := tiers[t]
		if len(group) < cfg.Fanout {
			continue
		}
		liveSum := 0
		for _, st := range group {
			liveSum += st.live
		}
		if liveSum > cfg.MaxMergedSize {
			continue
		}
		ids := make([]segID, len(group))
		for i, st := range group {
			ids[i] = st.id
		}
		return ids
	}
	return nil
}

func sortIntsAsc(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}
