package invertedstore

import (
	"testing"

	"github.com/codetrek/haystack/core/queue"
)

// TestDrainMerge_PendingSignalMergesAtStop deterministically covers drainMerge's signal-pending
// branch. drainMerge runs on the merge goroutine when it observes mergeStop, and processes a merge
// trigger that was raised just before Close (so a last-second spill's segments still get merged).
// Via the normal stop path this branch is TIMING-FLAKY — mergeLoop's select{<-mergeStop; <-mergeSignal}
// picks randomly, so a pending signal is only ~50% observed by drainMerge (the rest are consumed by
// the main loop first). So exercise it directly: stop the background loop, inject a pending trigger,
// then call drainMerge and assert it collapses the >=Fanout L0 segments.
func TestDrainMerge_PendingSignalMergesAtStop(t *testing.T) {
	dir := t.TempDir()
	q := queue.NewMpsc("drainmerge")
	q.Start()
	defer q.Stop()
	s, err := Open(dir, q, Options{AutoMerge: true, Fanout: 2})
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := s.CreateTable("files")

	// Stop the background merger so the trigger we raise below has no concurrent consumer, and
	// neutralize mergeStop so CloseAndWait's stopMergeLoop is a no-op (stopMergeLoop is not
	// idempotent — a second close(mergeStop) would panic).
	s.stopMergeLoop()
	s.mergeStop = nil

	// Two L0 segments (>= Fanout=2); each spill's triggerMerge coalesces into mergeSignal (cap 1),
	// leaving exactly one pending trigger.
	s.applyForTest(tid, 1, []string{"alpha"})
	s.spillForTest(tid)
	s.applyForTest(tid, 2, []string{"alpha"})
	s.spillForTest(tid)
	before := len(s.SegmentsForTest())
	if before < 2 {
		t.Fatalf("want >= 2 L0 segments before drain, got %d", before)
	}

	// A trigger is pending; drainMerge must take the <-mergeSignal branch and run the merge.
	s.drainMerge()

	if after := len(s.SegmentsForTest()); after >= before {
		t.Fatalf("drainMerge did not merge the pending L0 segments: before=%d after=%d", before, after)
	}
	// The merged index still resolves the posting written to both docs.
	if res := s.Search(tid, "alpha", -1, nil); len(res.DocIds) != 2 {
		t.Fatalf("after drain-merge Search(alpha) = %d docids, want 2", len(res.DocIds))
	}
	s.CloseAndWait()
}
