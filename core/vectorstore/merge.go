package vectorstore

import (
	"os"
	"path/filepath"
)

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

// mergePlan is the under-lock snapshot a merge builds before releasing s.mu for
// the slow write+graph-build. It captures everything the off-lock phase needs and
// nothing that the live store can mutate underneath it.
type mergePlan struct {
	inputs  []segID
	inputSS []*sealedSegment // parallel to inputs; kept mmap'd until the swap
	buckets []*segment       // packed live docs (≤ maxSegSize each)
	outIDs  []segID          // fresh segIds, one per bucket (allocated under lock)
	outDirs []string
	moved   map[int64]bool // docs whose global segId the swap rehomes
}

// planMergeLocked resolves inputIDs, packs their live docs into buckets, and
// allocates a fresh segId + dir per bucket. Returns (nil, nil) if any input id is
// already gone (a concurrent merge won the race) OR is not yet indexed — the
// caller treats that as a no-op. Caller holds s.mu. Allocating the output ids here
// (under the lock) keeps s.nextSeg monotonic and collision-free against concurrent
// seals.
//
// Why skip an input whose graph is not yet installed (appendix #8): a sealed
// segment that is still pending has a background buildAndPublish goroutine reading
// its mmap via eachLive OFF the store lock. The merge swap (step 2b) close()s the
// input — mmapFree on vecMap/tombMap/plMap — which would unmap memory the builder
// is mid-read, a SIGSEGV-on-free. A graph is installed (s.graphs[id] != nil) only
// as the builder's LAST action under s.mu, so requiring it proves no builder is
// in flight for that input. This also satisfies the "do not merge a just-sealed
// pending segment before its graph is built" fidelity point (appendix #3).
func (s *Store) planMergeLocked(inputIDs []segID) (*mergePlan, error) {
	inputSS := make([]*sealedSegment, 0, len(inputIDs))
	for _, id := range inputIDs {
		ss := s.sealedByID(id)
		if ss == nil {
			return nil, nil // already merged/swept; nothing to do
		}
		if s.graphs[id] == nil {
			return nil, nil // still pending/building — defer (avoids close-during-build, appendix #8)
		}
		inputSS = append(inputSS, ss)
	}
	buckets, moved := packLiveDocs(inputSS, s.metric, s.maxSegSize)
	if len(buckets) == 1 && len(buckets[0].slotDoc) == 0 {
		// All inputs fully tombstoned: no output, but the inputs must still be
		// dropped + their dirs deleted. Represent that as a plan with zero buckets.
		buckets = nil
	}
	p := &mergePlan{inputs: inputIDs, inputSS: inputSS, buckets: buckets, moved: moved}
	for range buckets {
		id := s.nextSeg
		s.nextSeg++
		p.outIDs = append(p.outIDs, id)
		p.outDirs = append(p.outDirs, filepath.Join(s.dir, segDirName(id, 0)))
	}
	return p, nil
}

// mergeAndPublish runs the SLOW phase off the store lock: write each output bucket
// to disk (fsync via writeSealedSegment) and reopen it. It then re-takes buildMu +
// s.mu for the atomic swap: reconcile any tombstones that arrived on the inputs
// during the off-lock window, mutate the segment set (drop inputs, add outputs,
// rehome moved docs), write the manifest ONCE (the commit point), then delete the
// old input dirs and spawn the background graph builds. Mirrors buildAndPublish's
// lock discipline (build off-lock → buildMu → s.mu → install + writeManifestLocked)
// and sealLocked's commit order (data durable → manifest swap → delete old).
//
// WaitGroup discipline (appendix #1): merges.Done fires when this returns. Every
// output's builds.Add(1) happens at step 4 BEFORE the return, so when Close's
// merges.Wait() passes, every spawned build is already counted in s.builds and
// the subsequent builds.Wait() drains them. New merges are gated by s.closing at
// the launch sites, so no merges.Add ever races a zero-counter merges.Wait.
func (s *Store) mergeAndPublish(p *mergePlan) error {
	defer s.merges.Done()
	if p == nil {
		return nil
	}

	// (1) Off-lock: write + reopen every output bucket. Data files fsync inside
	// writeSealedSegment (+ dir fsync) BEFORE the manifest will reference them.
	outSS := make([]*sealedSegment, len(p.buckets))
	for i, bk := range p.buckets {
		if err := writeSealedSegment(p.outDirs[i], bk); err != nil {
			s.abortMerge(p, outSS, i)
			return err
		}
		ss, err := openSealedSegment(p.outDirs[i], s.metric)
		if err != nil {
			s.abortMerge(p, outSS, i)
			return err
		}
		outSS[i] = ss
	}

	// (2) Swap under buildMu (serializes manifest rewrites vs builders) + s.mu.
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	s.mu.Lock()

	// (2a) Reconcile the off-lock window: a concurrent Delete/Put may have
	// tombstoned (or rehomed to head) an input doc AFTER the pack snapshot. Such a
	// doc must NOT be live in the output. For every doc we moved, if it is no
	// longer mapped to ITS INPUT segment in docToSeg, tombstone it in whatever
	// output bucket carries it. docToSeg is the single source of truth for which
	// segment owns a live doc (§4.6), so this is the exact liveness gate. The
	// tombstoneSlot persists+msyncs the bit into the output's tomb.dat, so the
	// reconciliation is durable independent of the manifest write below.
	inputSet := make(map[segID]bool, len(p.inputs))
	for _, id := range p.inputs {
		inputSet[id] = true
	}
	for _, ss := range outSS {
		for slot := 0; slot < ss.count(); slot++ {
			doc := ss.slotDoc(slot)
			owner, ok := s.docToSeg[doc]
			if !ok || !inputSet[owner] {
				// Deleted, or rehomed to head/another seg during the merge window.
				_ = ss.tombstoneSlot(slot)
			}
		}
	}

	// (2b) Drop inputs from the parallel sealed slices (delete by INDEX to keep
	// s.sealed and s.sealedID aligned — gotcha 6), closing + scheduling dir delete.
	// close()'s mmapFree is safe here: planMergeLocked required every input to be
	// indexed, so no background builder is reading the input mmap (appendix #8).
	for _, id := range p.inputs {
		for i := 0; i < len(s.sealedID); i++ {
			if s.sealedID[i] == id {
				s.sealed[i].close()
				s.sealed = append(s.sealed[:i], s.sealed[i+1:]...)
				s.sealedID = append(s.sealedID[:i], s.sealedID[i+1:]...)
				break
			}
		}
		delete(s.graphs, id)
	}

	// (2c) Append outputs (pending) and rehome the surviving moved docs. A doc
	// tombstoned in 2a is no longer live in the output, so it is not (re)mapped.
	for i, ss := range outSS {
		s.sealed = append(s.sealed, ss)
		s.sealedID = append(s.sealedID, p.outIDs[i])
		for slot := 0; slot < ss.count(); slot++ {
			if !ss.tombGet(slot) {
				s.docToSeg[ss.slotDoc(slot)] = p.outIDs[i]
			}
		}
	}

	// (2d) ONE atomic manifest swap — the commit point replacing N inputs with M
	// outputs. A crash before this leaves the outputs unreferenced (swept on
	// recover); a crash after leaves the inputs unreferenced (swept). No
	// alloc.Commit / wal.Reset: idtable mappings for moved docs are already durable
	// and the head/WAL is untouched (gotcha 4).
	if err := s.writeManifestLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	inputDirs := make([]string, len(p.inputs))
	for i, id := range p.inputs {
		inputDirs[i] = filepath.Join(s.dir, segDirName(id, 0))
	}
	s.mu.Unlock()

	// (3) Delete old input dirs AFTER the swap committed (now orphans).
	for _, dir := range inputDirs {
		_ = os.RemoveAll(dir)
	}

	// (4) Background-build each output's HNSW (off-lock, like seal). Reuse the
	// builds WaitGroup so Close() drains them; buildAndPublish flips pending→indexed.
	// Every Add(1) here precedes this function's return (and thus the deferred
	// merges.Done), so Close's merges.Wait()→builds.Wait() ordering is sound.
	for i, ss := range outSS {
		s.builds.Add(1)
		go s.buildAndPublish(p.outIDs[i], p.outDirs[i], ss)
	}
	return nil
}

// abortMerge cleans up partially-written output dirs when an off-lock write fails
// before the swap. The inputs are untouched (still referenced by the live
// manifest), so the store stays consistent; the half-written outputs are removed
// here and would also be swept on the next recover (defense in depth).
func (s *Store) abortMerge(p *mergePlan, outSS []*sealedSegment, upto int) {
	for i := 0; i < upto; i++ {
		if outSS[i] != nil {
			outSS[i].close()
		}
		_ = os.RemoveAll(p.outDirs[i])
	}
}

// mergeNow is the synchronous-launch test helper: plan under the lock, then run
// mergeAndPublish on a tracked goroutine. WaitForMerge() awaits completion. It
// refuses to launch once the store is closing (appendix #1: a merges.Add must
// never race a zero-counter merges.Wait in Close).
func (s *Store) mergeNow(inputIDs []segID) error {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil
	}
	p, err := s.planMergeLocked(inputIDs)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if p == nil {
		s.mu.Unlock()
		return nil
	}
	s.merges.Add(1)
	s.mu.Unlock()
	go func() { _ = s.mergeAndPublish(p) }()
	return nil
}

// WaitForMerge blocks until every in-flight merge has published (or aborted). It
// does NOT wait for the merged segments' background graph builds — use
// WaitForIndex for that. Mirrors WaitForIndex.
func (s *Store) WaitForMerge() error {
	s.merges.Wait()
	return nil
}
