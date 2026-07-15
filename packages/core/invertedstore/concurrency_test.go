package invertedstore

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codetrek/haystack/core/queue"
)

// concurrency_test.go — P9 (design §6 Concurrency model; task T8) acceptance tests.
//
// The MUST-PASS cases this file proves:
//
//   - go test -race is clean under concurrent Search + Update + (forced) merge;
//   - a reader mid-scan on a segment being merged away COMPLETES with no use-after-free (the
//     merged-away file is unlinked only after the last reader of it releases its ref);
//   - no Search blocks on a writer (Search returns while a long worker task is in flight).
//
// These are exactly the hazards the pre-P9 immediate-deletion path had: installMerge used to close +
// unlink an input segment's fd the instant the MANIFEST swapped, so a reader that had copied the seg
// slice and was still mid-scan read a closed fd and panicked in mustReadAt. The refcount + deferred
// deletion (concurrency.go) fixes that; these tests would panic / hang against the old code.

// newConcStore opens a store with the given options + one table for the concurrency tests.
func newConcStore(t *testing.T, opts Options) (*Store, int) {
	t.Helper()
	dir := t.TempDir()
	q := queue.NewMpsc("invconc")
	q.Start()
	s, err := Open(dir, q, opts)
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}
	return s, tbl
}

// segFileExists reports whether the on-disk segment file for id still exists in the store dir.
func (s *Store) segFileExists(id uint64) bool {
	_, err := os.Stat(filepath.Join(s.dir, segFileName(id)))
	return err == nil
}

// --- 1. Deferred deletion: a reader mid-scan on a merged-away segment is safe ---

// TestConcurrency_ReaderMidScanSurvivesMerge is the deterministic "no use-after-free" proof. A reader
// acquires the live snapshot (taking a ref on every segment) and is therefore "mid-scan". A covering
// merge then runs on the worker and retires (merges away) those exact segments. Because the reader
// still holds refs, the merged-away files MUST NOT be unlinked yet, and the reader MUST still read
// correct data from them (the open fd stays valid). Only after the reader releases its snapshot are
// the files unlinked. Against the pre-P9 immediate-unlink path this reader would read a closed fd and
// panic in mustReadAt.
func TestConcurrency_ReaderMidScanSurvivesMerge(t *testing.T) {
	// High Fanout so only the explicit covering merge fires (no incidental tiered merge mid-test).
	s, tbl := newConcStore(t, Options{Fanout: 100})
	defer s.CloseAndWait()

	// Two sealed segments, each a distinct doc under keyword "alpha".
	s.applyForTest(tbl, 10, []string{"alpha"})
	s.forceSpill(tbl)
	s.applyForTest(tbl, 20, []string{"alpha"})
	s.forceSpill(tbl)
	if len(s.segs) != 2 {
		t.Fatalf("expected 2 segments before merge, got %d", len(s.segs))
	}
	inputIds := []uint64{s.segs[0].id, s.segs[1].id}

	// A reader acquires the snapshot and holds it (== mid-scan): refs on both input segments are up.
	held := s.acquireSnapshot()
	if len(held) != 2 {
		t.Fatalf("snapshot should hold 2 segments, got %d", len(held))
	}

	// The worker runs a covering merge that retires both inputs into one new segment.
	s.coveringMergeForTest(t)
	if len(s.segs) != 1 {
		t.Fatalf("covering merge must compact to 1 segment, got %d", len(s.segs))
	}

	// DEFERRED DELETION: the inputs are merged away from the live set, but the reader still holds
	// refs, so their files MUST still exist (POSIX: an open fd survives unlink anyway, but the design
	// defers the unlink itself to refcount 0).
	for _, id := range inputIds {
		if !s.segFileExists(id) {
			t.Fatalf("merged-away segment %d unlinked while a reader still holds it (use-after-free)", id)
		}
	}

	// The held reader scans the retired segments — this is the "mid-scan completes" case. It must read
	// the original docids with no panic (the fds are alive). Against pre-P9 (immediate close+unlink)
	// this scanPrefix would panic on a closed fd.
	lo := invertedKey(uint32(tbl), "alpha")
	hi := prefixUpper(lo)
	got := map[int64]bool{}
	for _, seg := range held {
		seg.scanPrefix(lo, hi, func(_ []byte, v []byte) {
			ab, _ := splitInvertedValue(v)
			decodeDocs(ab, func(d int64) { got[d] = true })
		})
	}
	if !got[10] || !got[20] {
		t.Fatalf("reader mid-scan on retired segments got %v, want {10,20}", got)
	}

	// Release the snapshot: now the last ref drops and the deferred deletion fires.
	s.releaseSnapshot(held)
	for _, id := range inputIds {
		if s.segFileExists(id) {
			t.Fatalf("merged-away segment %d NOT unlinked after the last reader released it", id)
		}
	}

	// The live merged segment still serves the union through the normal Search path.
	r := s.Search(tbl, "alpha", 0, nil)
	if !hasDoc(r, 10) || !hasDoc(r, 20) {
		t.Fatalf("post-merge Search got %v, want both 10 and 20", r.DocIds)
	}
}

// --- 2. -race clean under concurrent Search + Update + merge ------------------

// TestConcurrency_SearchUpdateMergeRaceClean drives many concurrent Searchers + Updaters while the
// worker forces merges (AutoMerge on, small Fanout, tiny CapBytes so spills + merges churn). It is
// the race-detector gate: the test passes if it completes with no race, no panic (a use-after-free on
// a merged-away fd would panic in mustReadAt), and the final state is correct after the writers drain.
func TestConcurrency_SearchUpdateMergeRaceClean(t *testing.T) {
	// Tiny cap + small fanout + AutoMerge so the worker spills and merges constantly under load — the
	// background merger races real concurrent Search/Update calls, which is the whole point of T8.
	s, tbl := newConcStore(t, Options{CapBytes: 1 << 12, Fanout: 2, AutoMerge: true})
	defer s.CloseAndWait()

	const nDocs = 400
	const nReaders = 8
	const dur = 700 * time.Millisecond

	// Baseline the machinery counters so we can assert spills + merges ACTUALLY fired during the
	// churn (not just that the final state is correct). NextSegId advances once per sealed segment;
	// mergeAckSeq advances once per completed background merge pass. If a future refactor silently
	// disabled spills/merges, the final-state assertion alone would still pass while testing nothing
	// of the merge/deferred-deletion path under concurrency — the very thing T8 is about.
	s.mu.RLock()
	startSegId := s.man.NextSegId
	s.mu.RUnlock()
	startMergeAck := s.mergeAckSeq.Load()

	var stop atomic.Bool
	var wg sync.WaitGroup

	// Writers: continuously Update docs (full keyword sets that overlap so postings churn + tombstone).
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := 0; !stop.Load(); i++ {
				d := int64((base*nDocs + i) % nDocs)
				kw := []string{
					"alpha",
					fmt.Sprintf("doc%d", d%17),
					fmt.Sprintf("w%d", base),
				}
				s.Update(tbl, d, kw)
			}
		}(w)
	}

	// Readers: continuously Search the shared "alpha" prefix + a per-doc keyword. Must never panic on
	// a merged-away segment and must always return a (possibly partial) consistent result.
	for r := 0; r < nReaders; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				_ = s.Search(tbl, "alpha", 0, nil)
				_ = s.Search(tbl, "doc", 0, nil)
				_ = s.GetDocs(tbl, "alpha")
			}
		}()
	}

	time.Sleep(dur)
	stop.Store(true)
	wg.Wait()

	// Drain all queued async Updates, then assert the final state is sane: every doc that was last
	// written with "alpha" is searchable. We re-Update a known set so the final state is deterministic.
	for d := int64(0); d < nDocs; d++ {
		s.Update(tbl, d, []string{"alpha", fmt.Sprintf("final%d", d)})
	}
	s.sync()          // drain the worker so every async Update (and the spills they triggered) has run
	s.waitMergeIdle() // let the background merger settle (it runs on its own goroutine now, P9)

	r := s.Search(tbl, "alpha", 0, nil)
	for d := int64(0); d < nDocs; d++ {
		if !hasDoc(r, d) {
			t.Fatalf("after concurrent churn + final re-Update, doc %d missing from alpha search", d)
		}
	}

	// Assert the churn actually drove the machinery T8 hardens: at least one segment was sealed
	// (spills fired) AND at least one background merge pass acked (the merger ran concurrently with
	// the readers). Without these, the race test could pass green while exercising none of the
	// concurrent merge / deferred-deletion path.
	s.mu.RLock()
	endSegId := s.man.NextSegId
	s.mu.RUnlock()
	if endSegId <= startSegId {
		t.Fatalf("expected spills to seal segments during the churn (NextSegId %d -> %d)", startSegId, endSegId)
	}
	if s.mergeAckSeq.Load() <= startMergeAck {
		t.Fatalf("expected the background merger to run a pass during the churn (mergeAckSeq %d -> %d)", startMergeAck, s.mergeAckSeq.Load())
	}
}

// --- 3. No Search blocks on a writer's I/O ------------------------------------

// TestConcurrency_SearchDoesNotBlockOnWriter proves the design §6 invariant that a writer holds s.mu
// ONLY for the O(1) snapshot swap, NEVER across its slow MANIFEST fsync — so a concurrent Search does
// not block on the writer's I/O. It drives a REAL spill (the actual writer path) and wedges it INSIDE
// writeManifestBytes (via the beforeManifestFsync test hook, simulating a slow fsync). At that moment
// the spill is mid-I/O. We then assert a concurrent Search returns promptly. If the writer held s.mu
// across writeManifestBytes (the pre-fix bug), Search's acquireSnapshotLocked() RLock would block for
// the whole fsync and this test would time out. The hook holds NO lock, so the test fails ONLY if the
// CODE holds the lock across I/O — exactly the invariant under test.
func TestConcurrency_SearchDoesNotBlockOnWriter(t *testing.T) {
	s, tbl := newConcStore(t, Options{Fanout: 100})
	defer s.CloseAndWait()

	// Seed a sealed segment so Search has something to scan (and so its RLock acquires real refs).
	s.applyForTest(tbl, 1, []string{"alpha"})
	s.forceSpill(tbl)

	// Buffer a second doc in the head, then force a spill whose MANIFEST fsync we wedge mid-flight.
	s.applyForTest(tbl, 2, []string{"alpha"})

	atFsync := make(chan struct{})
	releaseFsync := make(chan struct{})
	beforeManifestFsync = func() {
		close(atFsync)
		<-releaseFsync // hold the spill INSIDE writeManifestBytes (mid-I/O), holding NO lock
	}
	defer func() { beforeManifestFsync = nil }()

	// Drive the real spill on the worker; it will block in the fsync hook (so RunFunc would not return
	// until we release). Run it in a goroutine so the test can race a Search against the held fsync.
	spillDone := make(chan struct{})
	go func() {
		s.spillForTest(tbl)
		close(spillDone)
	}()
	<-atFsync // the spill is now wedged mid-MANIFEST-fsync, holding no lock per the invariant

	// A Search MUST return promptly even though a writer is mid-fsync. With the fix (lock released
	// across the fsync) the RLock is free; against the bug (lock held across the fsync) this blocks.
	done := make(chan SearchResult, 1)
	go func() { done <- s.Search(tbl, "alpha", 0, nil) }()
	select {
	case r := <-done:
		// Doc 1 is in the sealed segment; doc 2 is still in the (un-spilled) head — either is fine,
		// the point is the call RETURNED while the writer was mid-fsync.
		if !hasDoc(r, 1) {
			t.Fatalf("Search returned but missing the sealed doc 1: %v", r.DocIds)
		}
	case <-time.After(2 * time.Second):
		close(releaseFsync)
		<-spillDone
		t.Fatal("Search blocked on a writer mid-MANIFEST-fsync (the lock is held across I/O)")
	}

	// Let the spill's fsync complete and the worker drain.
	close(releaseFsync)
	<-spillDone

	// The just-spilled doc 2 is now visible too — the spill completed correctly after the fsync.
	if r := s.Search(tbl, "alpha", 0, nil); !hasDoc(r, 1) || !hasDoc(r, 2) {
		t.Fatalf("after the spill completed, alpha must contain both docs 1 and 2: %v", r.DocIds)
	}
}

// TestConcurrency_SearchSurvivesCloseRace proves Close honors the P9 refcount path: a Search in
// flight when CloseAndWait runs must NOT read a closed fd. Many readers loop Searching while Close
// drops the published segment set; with the refcount path each segment's fd is closed only after the
// last in-flight reader releases its ref, so no reader ever reads a closed fd (no panic in
// mustReadAt). Against the pre-fix force-close path (seg.close() inline under the lock) a reader
// mid-scan after RUnlock would read a closed fd and panic. Run under -race.
func TestConcurrency_SearchSurvivesCloseRace(t *testing.T) {
	s, tbl := newConcStore(t, Options{Fanout: 100})

	// A few sealed segments so a Search scans real fds across the Close.
	for d := int64(1); d <= 5; d++ {
		s.applyForTest(tbl, d, []string{"alpha"})
		s.forceSpill(tbl)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// A Search after Close returns empty (emptySnapshot / absent table) — that is fine; it
				// must just never PANIC reading a closed fd. The refcount path guarantees that.
				_ = s.Search(tbl, "alpha", 0, nil)
				_ = s.GetDocs(tbl, "alpha")
			}
		}()
	}

	// Let readers ramp, then Close concurrently with them.
	time.Sleep(20 * time.Millisecond)
	s.CloseAndWait()
	close(stop)
	wg.Wait()
}

// --- 4. chunk-LRU entries are purged for a merged-away segment ----------------

// TestConcurrency_ChunkLRUPurgedOnMerge proves the chunk-LRU drops a retired segment's cached dict
// chunks on the MANIFEST swap (design §6: "entries for a merged-away segment are purged on the swap").
// A forward read warms the LRU with a segment's dict chunk; after a covering merge retires that
// segment, the LRU must hold zero chunks for its id.
func TestConcurrency_ChunkLRUPurgedOnMerge(t *testing.T) {
	s, tbl := newConcStore(t, Options{Fanout: 100})
	defer s.CloseAndWait()

	s.applyForTest(tbl, 1, []string{"alpha", "beta"})
	s.forceSpill(tbl)
	s.applyForTest(tbl, 2, []string{"gamma"})
	s.forceSpill(tbl)
	segIds := []uint64{s.segs[0].id, s.segs[1].id}

	// Warm the LRU: a forward read resolves doc 1's ordinals through seg 0's term-dict region, which
	// reads + caches its dict chunk. forwardKeywords runs on the worker.
	s.q.RunFunc(func() error {
		_, _ = s.forwardKeywords(tbl, 1)
		_, _ = s.forwardKeywords(tbl, 2)
		return nil
	})
	cachedBefore := 0
	for _, id := range segIds {
		cachedBefore += s.dictCache.countForSeg(id)
	}
	if cachedBefore == 0 {
		t.Fatal("expected the forward read to warm the chunk LRU, but it is empty")
	}

	// Covering merge retires both segments; installMerge purges their LRU entries on the swap.
	s.coveringMergeForTest(t)
	for _, id := range segIds {
		if n := s.dictCache.countForSeg(id); n != 0 {
			t.Fatalf("chunk LRU still holds %d chunks for merged-away segment %d (not purged on swap)", n, id)
		}
	}
}
