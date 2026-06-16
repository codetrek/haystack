package vectorstore

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
