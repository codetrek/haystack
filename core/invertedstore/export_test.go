package invertedstore

import (
	"strconv"
	"sync/atomic"
	"testing"
)

// This file provides test-only accessors (compiled only under `go test`) that drive the real
// worker-side apply/spill so store_test.go can exercise the head buffer + spill path WITHOUT the
// full Update path (P7). They run their work on the mpsc worker, exactly as the production write
// path does, so the concurrency contract (head mutated only on the worker) is preserved.

// applyForTest is a minimal stand-in for the P7 Update apply: for a cold doc (no diff against an
// older forward) it sets the doc's forward keyword set and adds a posting for each keyword. It
// runs on the worker and is synchronous (blocks until applied).
func (s *Store) applyForTest(tableId int, docid int64, keywords []string) {
	s.q.RunFunc(func() error {
		s.mu.Lock()
		h := s.head[tableId]
		if h == nil {
			h = newHeadTable()
			s.head[tableId] = h
		}
		if len(keywords) == 0 {
			h.deleteForward(docid)
		} else {
			h.setForward(docid, keywords)
			for _, kw := range keywords {
				h.addPosting(kw, docid)
			}
		}
		s.mu.Unlock()
		return nil
	})
}

// spillForTest forces a spill of the table's head on the worker (synchronous).
func (s *Store) spillForTest(tableId int) {
	s.q.RunFunc(func() error { return s.spill(tableId) })
}

// SegmentsForTest returns a copy of the live segMeta set (the MANIFEST's segment list).
func (s *Store) SegmentsForTest() []segMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]segMeta(nil), s.man.Segments...)
}

// LiveByTableForTest returns a copy of the per-table live-pair counter.
func (s *Store) LiveByTableForTest() map[int]int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[int]int64, len(s.liveByTable))
	for k, v := range s.liveByTable {
		out[k] = v
	}
	return out
}

// RecomputeLiveForTest re-runs the Open-time live recompute on the worker (zeroes liveByTable first
// so the test verifies the recompute reproduces the value, not that it merely left it alone).
func (s *Store) RecomputeLiveForTest() {
	s.q.RunFunc(func() error {
		s.mu.Lock()
		s.liveByTable = map[int]int64{}
		s.recomputeLive()
		s.mu.Unlock()
		return nil
	})
}

// dropHeadCloseSegmentsForTest simulates a process crash for the recovery tests (T11/§9): it discards
// the volatile in-memory head (every apply that has NOT yet spilled to a sealed segment is LOST) and
// closes the open segment fds WITHOUT spilling the head, keeping the on-disk files so the next Open
// finds exactly the durable, MANIFEST-named segments. This is deliberately NOT CloseAndWait —
// CloseAndWait spills the head first (a clean close), which is the opposite of a crash. It mirrors
// CloseAndWait's segment teardown (stop the merge loop, publish emptySnapshot so no late reader
// acquires a ref, retireKeepFile each segment) but drops the head map instead of flushing it, leaving
// the store in the design §9 crash-consistency state: sealed segments durable, head volatile/lost.
func (s *Store) dropHeadCloseSegmentsForTest() {
	s.stopMergeLoop() // drain + stop the background merger before any fd is closed
	s.mu.Lock()
	s.head = map[int]*headTable{} // the crash: the volatile head is simply gone
	segs := s.segs
	s.segs = nil
	s.snap.Store(emptySnapshot) // drop the published set first so no late reader acquires a ref
	s.mu.Unlock()
	for _, seg := range segs {
		seg.retireKeepFile() // close the fd, keep the file (still live in the on-disk MANIFEST)
	}
}

// DeadFractionForTest exposes the covering-merge trigger value.
func (s *Store) DeadFractionForTest() float64 { return s.deadFraction() }

// uniqWord makes a per-index distinct keyword (so each doc adds a unique posting).
func uniqWord(n int) string { return "w" + strconv.Itoa(n) }

// installCoveringCounter installs the package coveringMergeHook to count covering merges (covering
// BOTH the dead-fraction-triggered and the DeleteTable/orphan forced paths). Read via .Load(). The
// hook runs on the worker, so the atomic keeps it -race clean. Cleared on test cleanup.
func installCoveringCounter(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	coveringMergeHook = func() { n.Add(1) }
	t.Cleanup(func() { coveringMergeHook = nil })
	return &n
}

// assertCounterInvariantForTest checks the spec §5 invariant: live (catalog-gated) never goes
// negative and exceeds sealed `written` by at most a head's worth of postings (≤ CapBytes, since a
// posting is ≥ 1 byte). A larger excess signals a counter over-count bug the deadFraction clamp would
// otherwise silently swallow.
func (s *Store) assertCounterInvariantForTest(t *testing.T) {
	t.Helper()
	s.mu.RLock()
	defer s.mu.RUnlock()
	var written, live int64
	for _, sm := range s.man.Segments {
		written += sm.Postings
	}
	for tid, v := range s.liveByTable {
		if _, ok := s.man.Tables[tid]; ok {
			live += v
		}
	}
	if live < 0 {
		t.Fatalf("counter invariant: live=%d went negative", live)
	}
	if live-written > int64(s.opts.CapBytes) {
		t.Fatalf("counter invariant: live(%d) - written(%d) = %d exceeds headCap(%d) — over-count bug",
			live, written, live-written, s.opts.CapBytes)
	}
}

// installForwardProbeCounter counts segment forward PROBES (non-skipped lookupForward calls). The
// hook runs on the worker; the atomic keeps it -race clean. Cleared on cleanup.
func (s *Store) installForwardProbeCounter(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	s.onForwardProbe = func() { n.Add(1) }
	t.Cleanup(func() { s.onForwardProbe = nil })
	return &n
}

// forwardKeywordsForTest runs forwardKeywords on the worker (synchronous), so a test can drive the
// "read old keyword set" path directly and observe the probe counter.
func (s *Store) forwardKeywordsForTest(tableId int, docid int64) (words []string, deleted bool) {
	s.q.RunFunc(func() error {
		words, deleted = s.forwardKeywords(tableId, docid)
		return nil
	})
	return
}
