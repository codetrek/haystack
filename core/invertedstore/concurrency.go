package invertedstore

// concurrency.go — P9 (design §6 Concurrency model; task T8).
//
// Harden the Store for one-writer / many-reader. The pieces this file adds:
//
//   - segSnapshot + atomic.Pointer[segSnapshot]: the live segment set is PUBLISHED via an
//     atomic pointer that the worker swaps on every seal/merge/table change (spill, installMerge).
//     Search/GetDocs/forwardKeywords load that pointer ONCE per call to get a consistent set,
//     so a reader never observes a half-applied swap (some-old + some-new segments).
//
//   - per-segment refcount + deferred deletion: a merge swaps the MANIFEST to DROP input segments,
//     but a reader may be mid-scan on one. Each segment carries an atomic refcount; the published
//     snapshot holds ONE ref per live segment, and a reader that acquires a snapshot bumps an extra
//     ref on each of its segments for the duration of the scan. installMerge marks a retired segment
//     and drops its published ref; the file is close()d + os.Remove()d only when the refcount reaches
//     zero — i.e. only after the last in-flight reader that still references it has finished. POSIX
//     keeps an already-open fd valid across unlink, so in-flight reads complete with no use-after-free.
//
//   - the acquire/swap handoff is serialized by the Store RWMutex (already guarding the head): a
//     reader acquires its extra refs under s.mu.RLock(); the worker swaps + releases under
//     s.mu.Lock(). That closes the load-then-incref window (a reader either takes its ref before the
//     worker can drop the published ref, OR observes the already-swapped new snapshot) WITHOUT a
//     reader ever blocking on the writer's I/O — the lock is held only for the O(1) pointer swap, ref
//     bookkeeping, and a cheap in-memory MANIFEST marshal, NEVER for the slow MANIFEST fsync or any
//     segment file I/O (spill/installMerge do the two fsyncs via writeManifestBytes OUTSIDE the lock;
//     design §6 "Locks only for the brief mutation"; "no Search blocks on a writer").
//
// The chunk-LRU is already its own mutex-guarded structure (dictcache.go) and is purged of a retired
// segment's entries on the swap (installMerge), so the cache never serves bytes for a gone segment.
//
//   - background merger on its OWN goroutine (design §6 "Background merger (own goroutine, off the
//     critical path)"). The pre-P9 P8 shortcut re-enqueued maybeMerge onto the SAME mpsc worker via
//     s.q.AddFunc from inside a worker task (spill). That self-enqueue DEADLOCKS the moment the queue
//     fills: the worker blocks sending to its own queue while it is the only consumer (observed: a
//     race-stress run wedged with the worker parked in AddFunc's chan-send). P9 moves the merger to a
//     dedicated goroutine triggered by a NON-BLOCKING signal: the worker never sends to its own queue;
//     the merge goroutine drives merges back onto the worker via RunFunc (a DIFFERENT goroutine, so a
//     blocking wait on the worker is safe and progresses), preserving the single-mutator invariant.

import (
	"os"
)

// segSnapshot is an immutable, atomically-published view of the live sealed segment set, OLDEST ->
// NEWEST by seal-sequence id (Search scans it in reverse for newest-wins). It is replaced wholesale
// on every change; a reader loads the pointer once and holds that exact slice for its whole call.
type segSnapshot struct {
	segs []*segment
}

// emptySnapshot is the initial published value (no segments) so Search can Load() without a nil check.
var emptySnapshot = &segSnapshot{}

// publishSnapshotLocked installs s.segs as the new live segment set. The caller MUST hold s.mu (it
// is the worker mutating the segment set) and MUST have already taken the published ref on every NEW
// segment and arranged to retire (drop the published ref on) every REMOVED segment. It only swaps the
// atomic pointer from a private copy of s.segs; ref bookkeeping is the caller's (spill increfs the new
// seg; installMerge increfs the merged output and retires the inputs). Search loads this pointer once.
func (s *Store) publishSnapshotLocked() {
	cp := make([]*segment, len(s.segs))
	copy(cp, s.segs)
	s.snap.Store(&segSnapshot{segs: cp})
}

// acquireSnapshot loads the published segment set and bumps a reader ref on each of its segments, so
// none can be torn down (close + unlink) while this reader is mid-scan — even if a concurrent merge
// retires it. It takes s.mu.RLock() so the load + the incref loop cannot interleave with the worker's
// swap-then-retire (which holds s.mu.Lock()): the reader either takes its refs on the pre-swap
// segments before the worker drops their published ref, or it loads the post-swap snapshot. The
// caller MUST releaseSnapshot the returned slice when done (release decrefs + tears down at 0).
func (s *Store) acquireSnapshot() []*segment {
	s.mu.RLock()
	segs := s.acquireSnapshotLocked()
	s.mu.RUnlock()
	return segs
}

// acquireSnapshotLocked is acquireSnapshot for a caller that ALREADY holds s.mu.RLock() — it loads
// the published set and increfs each segment. Search uses it so the head copy and the segment-ref
// acquisition happen in one RLock window (a single consistent point). The caller still owns the
// returned slice and MUST releaseSnapshot it. The incref must be inside the RLock so it cannot race
// the worker's retire (which decrefs under s.mu.Lock()).
func (s *Store) acquireSnapshotLocked() []*segment {
	snap := s.snap.Load()
	segs := snap.segs
	for _, seg := range segs {
		seg.refs.Add(1)
	}
	return segs
}

// releaseSnapshot drops the reader ref this scan held on each segment. A segment whose refcount
// reaches zero AND that has been retired by a merge is torn down here (close fd + unlink file) — so a
// merged-away file outlives every in-flight reader and is removed only once the last one is done.
func (s *Store) releaseSnapshot(segs []*segment) {
	for _, seg := range segs {
		seg.release()
	}
}

// release drops one ref on the segment. When the count reaches zero and the segment has been retired
// (a merge dropped it from the live set), it is closed and its file unlinked — the deferred deletion
// that makes a merged-away segment safe to read until the last reader finishes. A still-live segment
// (refs never hits zero while it is published, since the published ref keeps it >= 1) is never torn
// down here. The retired flag is set by the worker BEFORE it drops the published ref, so the worker's
// own drop-to-zero (no readers) tears the segment down immediately, and a reader's drop-to-zero (the
// worker dropped the published ref first, the reader was the last holder) tears it down then.
func (seg *segment) release() {
	if seg.refs.Add(-1) == 0 && seg.retired.Load() {
		seg.teardown()
	}
}

// retire marks the segment as merged-away and drops its PUBLISHED ref. If no reader currently holds
// it (refcount falls to zero) it is torn down immediately; otherwise the last reader's release tears
// it down. MUST be called by the worker AFTER the new snapshot (without this segment) is published,
// so a reader that loaded the old snapshot has already taken its own ref under the RLock. Setting
// retired before the decref ensures release() observes it.
func (seg *segment) retire() {
	seg.retired.Store(true)
	if seg.refs.Add(-1) == 0 {
		seg.teardown()
	}
}

// retireKeepFile is retire for a CLEAN CLOSE: it drops the published ref and tears the segment down
// when the last reader releases, but teardown only CLOSES the fd — it does NOT unlink the file (the
// segment is still live in the on-disk MANIFEST and must survive for the next Open). It is the same
// deferred path retire() uses, so a Search in flight during CloseAndWait that holds a ref keeps the
// fd valid until it releases — no use-after-free on a closed fd (P9/T8). Setting keepFile + retired
// before the decref ensures teardown() (which may run on this drop or a later reader's release)
// observes both.
func (seg *segment) retireKeepFile() {
	seg.keepFile.Store(true)
	seg.retired.Store(true)
	if seg.refs.Add(-1) == 0 {
		seg.teardown()
	}
}

// teardown closes the segment's fd and (unless keepFile is set) unlinks its file. Idempotent via the
// atomic done flag so a concurrent reader-release and worker-retire racing to zero only tear down
// once. Called only when the refcount has reached zero on a retired segment, so no one can read it
// afterwards. keepFile (a clean-close retire) keeps the file so the next Open still finds it.
func (seg *segment) teardown() {
	if seg.tornDown.CompareAndSwap(false, true) {
		seg.close()
		if !seg.keepFile.Load() {
			os.Remove(seg.path)
		}
	}
}

// ---- background merge scheduler (design §6 "own goroutine") -----------------
//
// The merge goroutine decouples merge SCHEDULING from the mpsc worker so the worker never blocks
// sending to its own queue. A spill (or DeleteTable) raises a non-blocking trigger; the goroutine
// coalesces triggers (a buffered-1 channel + a "force covering" atomic) and, for each, runs the merge
// passes ON the worker via q.RunFunc — serialized with applies/spills so the segment set keeps a
// single mutator, but driven from a separate goroutine so the RunFunc wait can never self-deadlock.
//
// Quiescence (for tests) is tracked with two monotonic counters: triggerMerge bumps mergeReqSeq; the
// loop, after each completed pass, stores the reqSeq it observed into mergeAckSeq. waitMergeIdle waits
// for mergeAckSeq >= the mergeReqSeq it sampled, which a coalesced follow-up pass always reaches — no
// "consumed the signal but not yet marked busy" window to fall through.

// startMergeLoop launches the background merge goroutine (idempotent; only when AutoMerge is on). It
// is started in Open and stopped in CloseAndWait.
func (s *Store) startMergeLoop() {
	if !s.opts.AutoMerge {
		return
	}
	s.mergeSignal = make(chan struct{}, 1)
	s.mergeStop = make(chan struct{})
	s.mergeDone = make(chan struct{})
	go s.mergeLoop()
}

// mergeLoop is the background merge goroutine. It waits for a trigger, then runs maybeMerge (and, if a
// DeleteTable forced it, a covering merge) on the worker via RunFunc. It exits on mergeStop after a
// final drain so a merge in flight at Close completes. Errors from a merge are dropped: a background
// merge failing must not crash the process (it runs off any caller), and the live set is left
// consistent by installMerge's persist-then-publish on every failure path.
func (s *Store) mergeLoop() {
	defer close(s.mergeDone)
	for {
		select {
		case <-s.mergeStop:
			s.drainMerge()
			return
		case <-s.mergeSignal:
			s.runScheduledMerge()
		}
	}
}

// runScheduledMerge executes the tiered + covering merge passes on the worker. It snapshots the
// current request sequence FIRST, runs the pass, then publishes that sequence as acked — so a
// waitMergeIdle that sampled any reqSeq <= the snapshot sees it satisfied. forceCovering (set by
// DeleteTable) guarantees a covering merge even when the dead-fraction trigger would not fire. The
// whole pass runs inside ONE RunFunc so it is a single serialized worker task.
func (s *Store) runScheduledMerge() {
	req := s.mergeReqSeq.Load()
	force := s.forceCovering.Swap(false)
	_ = s.q.RunFunc(func() error {
		if err := s.maybeMerge(); err != nil {
			return err
		}
		if force {
			return s.coveringMerge()
		}
		return nil
	})
	s.mergeAckSeq.Store(req)
}

// drainMerge runs one final scheduled merge if a trigger is pending at stop, so a spill that signaled
// right before Close is not silently dropped (the head is already flushed by CloseAndWait's spill;
// this collapses any segments that spill produced). Non-blocking: only if a signal is queued.
func (s *Store) drainMerge() {
	select {
	case <-s.mergeSignal:
		s.runScheduledMerge()
	default:
	}
}

// triggerMerge raises a non-blocking merge trigger. Called from the worker (spill) or any goroutine
// (DeleteTable). It NEVER blocks: it bumps the request sequence and does a coalescing non-blocking
// send (if a trigger is already pending the new one is folded in — the next pass re-reads the live
// segment set, so one pass covers all spills since the last). This is the fix for the worker
// self-enqueue deadlock — the worker raises a flag instead of sending a task to its own queue.
// covering=true also sets forceCovering so the scheduled pass runs a covering merge.
func (s *Store) triggerMerge(covering bool) {
	if !s.opts.AutoMerge {
		return
	}
	if covering {
		s.forceCovering.Store(true)
	}
	s.mergeReqSeq.Add(1)
	select {
	case s.mergeSignal <- struct{}{}:
	default: // a trigger is already pending; coalesce (the next pass sees the latest reqSeq + state)
	}
}

// stopMergeLoop signals the merge goroutine to drain and exit, and waits for it. Idempotent. Called by
// CloseAndWait AFTER the final head spill (so the drain collapses the just-spilled segments) but
// BEFORE the segment fds are closed (so a merge in flight does not read a closed fd).
func (s *Store) stopMergeLoop() {
	if !s.opts.AutoMerge || s.mergeStop == nil {
		return
	}
	close(s.mergeStop)
	<-s.mergeDone
}

// waitMergeIdle blocks until every merge trigger raised so far has been processed (mergeAckSeq has
// caught up to the mergeReqSeq sampled here AND no signal is still pending). Test-only — it lets a
// test deterministically wait for AutoMerge to settle instead of sleeping. It terminates because
// spills (the only trigger source) have stopped before a test calls this, so reqSeq is stable and a
// coalesced pass drives ackSeq up to it.
func (s *Store) waitMergeIdle() {
	if !s.opts.AutoMerge {
		return
	}
	target := s.mergeReqSeq.Load()
	for s.mergeAckSeq.Load() < target || len(s.mergeSignal) != 0 {
		target = s.mergeReqSeq.Load()
		// Bounce off the worker (the merge executor) to give an in-flight pass room to finish, then
		// re-check. A no-op RunFunc returns only after every earlier-enqueued worker task — including a
		// merge pass's RunFunc — has run, so this both yields and synchronizes.
		_ = s.q.RunFunc(func() error { return nil })
	}
}
