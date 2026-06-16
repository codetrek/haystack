package vectorstore

// mergeConfig holds the space-reclamation tunables (architecture §4.9). All are
// measure-don't-assert placeholders the operator can override; defaults are safe
// for production-scale corpora and are deliberately shrinkable in tests so a
// handful of Puts can trigger a merge.
type mergeConfig struct {
	// MergeFloor: a sealed segment whose live ratio (live/count) is below this is
	// delete-driven merge bait (heavy tombstones). ~0.5 (§4.9 "段 live 占比 < ~50%").
	MergeFloor float32
	// Fanout K: a size tier with >= K segments is growth-driven merged up. ~8-10.
	Fanout int
	// MaxMergedSize caps a merge output's row count so the top tier never makes one
	// giant un-mergeable segment (§4.9 "封顶 maxMergedSize ~1M").
	MaxMergedSize int
	// TargetSegCount: the growth driver works to keep the live sealed-segment count
	// near this so the N-way Search loop stays cheap (§4.9 "目标活段数 ~几十").
	TargetSegCount int
}

const (
	defaultMergeFloor     = float32(0.5)
	defaultFanout         = 8
	defaultMaxMergedSize  = 1 << 20 // ~1M rows
	defaultTargetSegCount = 32
)

func (c mergeConfig) withDefaults() mergeConfig {
	if c.MergeFloor == 0 {
		c.MergeFloor = defaultMergeFloor
	}
	if c.Fanout == 0 {
		c.Fanout = defaultFanout
	}
	if c.MaxMergedSize == 0 {
		c.MaxMergedSize = defaultMaxMergedSize
	}
	if c.TargetSegCount == 0 {
		c.TargetSegCount = defaultTargetSegCount
	}
	return c
}

// segLiveStats is an immutable snapshot of one sealed segment's reclamation
// signal, taken under s.mu so the pure driver/selector logic never touches the
// live segment set. count includes tombstoned rows; live excludes them.
type segLiveStats struct {
	id    segID
	count int // total rows (incl. tombstoned)
	live  int // non-tombstoned rows
}

func (s segLiveStats) liveRatio() float32 {
	if s.count == 0 {
		return 1
	}
	return float32(s.live) / float32(s.count)
}

// segStatsLocked snapshots every live sealed segment's (id, count, live). Caller
// holds s.mu (R or W). It reads ss.count()/ss.tombCount(), which take the
// segment's own tomb RLock, so the snapshot is internally consistent per segment.
func (s *Store) segStatsLocked() []segLiveStats {
	out := make([]segLiveStats, len(s.sealed))
	for i, ss := range s.sealed {
		cnt := ss.count()
		out[i] = segLiveStats{
			id:    s.sealedID[i],
			count: cnt,
			live:  cnt - ss.tombCount(),
		}
	}
	return out
}

// packLiveDocs streams the live (non-tombstoned) docs of the input sealed
// segments through eachLive and bin-packs them into in-memory *segment buckets of
// at most maxSegSize rows each, returning the buckets and the set of moved docIds.
//
// Vectors from eachLive are already in metric-natural stored form (cosine = unit
// + separate norm); segment.append stores them VERBATIM — do NOT re-run
// metric.prepare (would double-normalize, gotcha 1). append copies the slice and
// payload, so aliasing the input mmap is safe. eachLive holds each input's tomb
// RLock for a consistent per-segment snapshot. The returned moved set is the
// authoritative list of docs whose global segId the swap must rehome.
func packLiveDocs(inputs []*sealedSegment, metric Metric, maxSegSize int) (buckets []*segment, moved map[int64]bool) {
	moved = make(map[int64]bool)
	cur := newSegment(metric)
	buckets = append(buckets, cur)
	for _, ss := range inputs {
		ss.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
			if len(cur.slotDoc) >= maxSegSize {
				cur = newSegment(metric)
				buckets = append(buckets, cur)
			}
			cur.append(docID, stored, norm, ss.payload(slot))
			moved[docID] = true
		})
	}
	// Drop a trailing empty bucket (all inputs were fully tombstoned).
	if len(buckets) > 1 && len(buckets[len(buckets)-1].slotDoc) == 0 {
		buckets = buckets[:len(buckets)-1]
	}
	return buckets, moved
}
