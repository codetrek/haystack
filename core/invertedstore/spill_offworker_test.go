package invertedstore

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/codetrek/haystack/core/queue"
)

// newSpillOffworkerStore opens a store with the given Options and one table (mirrors
// newForwardSkipStore), so a test can drive the async detach/encode/install path of F (v5).
func newSpillOffworkerStore(t *testing.T, opts Options) (*Store, int) {
	t.Helper()
	q := queue.NewMpsc("spilloffworker")
	q.Start()
	s, err := Open(t.TempDir(), q, opts)
	if err != nil {
		t.Fatal(err)
	}
	// Drain any in-flight off-worker spill goroutine before t.TempDir() is removed, so a leaked encode
	// never panics writing into the deleted dir. Registered before CreateTable so it runs (LIFO) after
	// the test body's own cleanups (e.g. closing a parked encode's release channel).
	t.Cleanup(s.WaitSpillsForTest)
	tid, err := s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}
	return s, tid
}

// drain blocks until every task enqueued on the worker so far has run (a no-op RunFunc returns only
// after all earlier-enqueued closures completed).
func (s *Store) drainForTest() { s.q.RunFunc(func() error { return nil }) }

// parkEncode installs an encodeSpillBlock that signals `entered` (buffered, every entry) and blocks on
// `release`. unpark clears the hook and closes release (idempotent), so a Close-time drain is not
// re-parked. The encodeSpillBlock package global means NO test using this may call t.Parallel.
func parkEncode(t *testing.T) (entered chan struct{}, release chan struct{}, unpark func()) {
	t.Helper()
	entered = make(chan struct{}, 64)
	release = make(chan struct{})
	var once sync.Once
	unpark = func() {
		encodeSpillBlock = nil
		once.Do(func() { close(release) })
	}
	encodeSpillBlock = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	return entered, release, unpark
}

// TestSpillF_B1_RepostAfterDetachTombstonesDropped is THE B1 zero-concurrency silent-corruption gate.
// With a tiny CapBytes the first Update over-caps the head, so F detaches it for an OFF-WORKER encode
// (parked here via encodeSpillBlock) and pushes it onto s.spilling. A SECOND Update for the SAME doc
// that drops a keyword must diff against the doc's OLD keyword set — which now lives ONLY in the
// detached (spilling) head — so the dropped keyword is tombstoned. If forwardKeywords did not consult
// the spilling tier, the re-post would diff against an empty `old`, drop no tombstone, and the dropped
// keyword would resurrect (silent corruption, ZERO concurrency). Run -count=20 for determinism.
func TestSpillF_B1_RepostAfterDetachTombstonesDropped(t *testing.T) {
	s, tbl := newSpillOffworkerStore(t, Options{CapBytes: 64})

	// Park the off-worker encode so the detached head STAYS in s.spilling across the re-post.
	release := make(chan struct{})
	parked := make(chan struct{}, 1)
	encodeSpillBlock = func() {
		select {
		case parked <- struct{}{}:
		default:
		}
		<-release
	}
	// DRAIN-FIRST cleanup (-race): under H's +8/op accounting the head re-caps sooner, so a re-dispatched
	// spill goroutine may still be reading encodeSpillBlock at head.go:346. Drain every in-flight spill
	// FIRST (the test body has close(release)d, so a re-dispatched encode returns immediately), THEN nil
	// the hook — never nil it while a spill goroutine reads it. Registering this in the SAME t.Cleanup
	// keeps the two steps ordered (a bare `encodeSpillBlock = nil` cleanup runs LIFO before the store's
	// own WaitSpillsForTest drain and races the live read).
	t.Cleanup(func() {
		s.WaitSpillsForTest()
		encodeSpillBlock = nil
	})

	// First post: [alpha, beta]. The tiny cap over-caps the head ⇒ async detach (encode parks).
	s.Update(tbl, 1, []string{"alpha", "beta"})
	select {
	case <-parked:
	case <-time.After(5 * time.Second):
		t.Fatal("encode never parked — the over-cap head was not detached for an off-worker encode")
	}
	// Assert the ASYNC branch was taken (fail loud if it silently took a sync spill path).
	if n := s.SpillingLenForTest(); n != 1 {
		t.Fatalf("len(s.spilling) = %d after detach, want 1 (the async detach branch must be taken)", n)
	}

	// Second post for the SAME doc, dropping beta. forwardKeywords must read doc 1's old [alpha,beta]
	// from the spilling tier so beta is tombstoned.
	s.Update(tbl, 1, []string{"alpha"})
	s.drainForTest()

	// Release the parked encode and let the install complete.
	close(release)
	s.drainForTest()
	// Bounce the worker once more so a re-dispatched install RunFunc lands.
	s.drainForTest()

	if got := searchDocidsForTest(t, s, tbl, "beta"); len(got) != 0 {
		t.Fatalf("doc still searchable under dropped keyword 'beta' = %v, want [] (forwardKeywords must read the detached head via spilling)", got)
	}
	if got := searchDocidsForTest(t, s, tbl, "alpha"); len(got) != 1 || got[0] != 1 {
		t.Fatalf("doc 1 not searchable under retained keyword 'alpha' = %v, want [1]", got)
	}
}

// maxSegIdForTest returns the highest live segment id (the newest segment), or 0 if none.
func (s *Store) maxSegIdForTest() uint64 {
	var mx uint64
	for _, sm := range s.SegmentsForTest() {
		if sm.Id > mx {
			mx = sm.Id
		}
	}
	return mx
}

// TestSpillF_InstallTimeId_NewestWins proves the v5 install-time id: a head detached for an off-worker
// encode is the NEWEST data, so when it installs AFTER a concurrent merge that ran while it was parked,
// it must get a HIGHER id (newest) and its newer tombstone must NOT be resurrected by the older merge
// output. With detach-time ids (the v4 defect) the merge would reserve a higher id than the parked
// spill and outrank it, resurrecting the dropped keyword.
func TestSpillF_InstallTimeId_NewestWins(t *testing.T) {
	s, tbl := newSpillOffworkerStore(t, Options{CapBytes: 1 << 20, Fanout: 2, AutoMerge: true})
	t.Cleanup(func() { s.CloseAndWait() })

	// Seal two L0 segments where doc 1 has keyword "alpha" (so an older tier holds the add). Fanout=2
	// makes these two eligible for a tiered merge.
	s.applyForTest(tbl, 1, []string{"alpha"})
	s.spillForTest(tbl)
	s.applyForTest(tbl, 2, []string{"alpha"})
	s.spillForTest(tbl)
	if n := len(s.SegmentsForTest()); n != 2 {
		t.Fatalf("want 2 sealed segments, got %d", n)
	}
	idBeforeSpill := s.maxSegIdForTest()

	// Re-post doc 1 dropping "alpha" (tombstone), then detach that head for an off-worker encode (parked).
	encEntered, _, unparkEnc := parkEncode(t)
	t.Cleanup(unparkEnc)
	s.q.RunFunc(func() error {
		s.mu.Lock()
		h := s.head[tbl]
		if h == nil {
			h = newHeadTable()
			s.head[tbl] = h
		}
		h.tombstonePosting("alpha", 1)
		h.setForward(1, nil)
		e := s.detachHeadLocked(tbl)
		s.mu.Unlock()
		s.dispatchSpill(e)
		return nil
	})
	select {
	case <-encEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("spill encode never parked")
	}
	if s.SpillingLenForTest() != 1 {
		t.Fatalf("want exactly 1 parked spill, got %d", s.SpillingLenForTest())
	}

	// While the spill is parked, run a tiered merge of the two L0 segments ON the worker (the merge
	// carries "alpha" for doc 1 forward; it reserves+assigns an id NOW). Its install-time id is lower
	// than the spill's, which installs LATER.
	s.mergeOneLevelForTest(t)
	idAfterMerge := s.maxSegIdForTest()
	if idAfterMerge <= idBeforeSpill {
		t.Fatalf("merge should have produced a new segment id > %d, got %d", idBeforeSpill, idAfterMerge)
	}

	// Release the parked encode and let the spill install.
	unparkEnc()
	s.WaitSpillsForTest()

	// The spill (newest data) must have the HIGHEST id (installed after the merge).
	idAfterSpill := s.maxSegIdForTest()
	if idAfterSpill <= idAfterMerge {
		t.Fatalf("parked spill must install with the highest id (newest): merge id=%d, spill id=%d", idAfterMerge, idAfterSpill)
	}
	// The spill's newer tombstone of "alpha" for doc 1 wins over the merge's older add ⇒ doc 1 is NOT
	// resurrected. (Doc 2 still legitimately holds "alpha", so the result is [2], never containing 1.)
	got := searchDocidsForTest(t, s, tbl, "alpha")
	for _, d := range got {
		if d == 1 {
			t.Fatalf("doc 1 resurrected under 'alpha' (result %v) — an older merge output outranked the newer spill (install-time id broken)", got)
		}
	}
}

// TestSpillF_GateBoundAndLiveness_SingleTable drives a fast producer against an artificially-slowed
// encode (parked, released in stages): at most ONE spill is ever in flight (peak len(s.spilling) ≤ 1),
// the producer parks at the blockProducer gate, and once each install lands the over-cap head is
// re-dispatched so the build CONVERGES (no wedge) — all timeout-guarded.
func TestSpillF_GateBoundAndLiveness_SingleTable(t *testing.T) {
	s, tbl := newSpillOffworkerStore(t, Options{CapBytes: 64})
	t.Cleanup(func() { s.CloseAndWait() })

	// Encodes signal entry (buffered) and block until released one at a time, so we can hold a spill in
	// flight while the producer keeps posting.
	gate := make(chan struct{}) // one token per release
	entered := make(chan struct{}, 256)
	encodeSpillBlock = func() {
		entered <- struct{}{}
		<-gate
	}
	t.Cleanup(func() { encodeSpillBlock = nil })

	// A producer goroutine floods distinct over-cap docs; each Update may park at the gate.
	const n = 40
	prodDone := make(chan struct{})
	go func() {
		for d := int64(1); d <= n; d++ {
			s.Update(tbl, d, []string{uniqWord(int(d)), "shared"})
		}
		close(prodDone)
	}()

	// Release encodes one at a time; after EACH, assert ≤ 1 spill is ever in flight, until the producer
	// finishes and all spills drain.
	deadline := time.After(20 * time.Second)
	for {
		select {
		case <-entered:
			// A spill parked: there must be at most one detached head outstanding.
			if got := s.SpillingLenForTest(); got > 1 {
				t.Fatalf("peak len(s.spilling) = %d, want <= 1 (one-in-flight violated)", got)
			}
			gate <- struct{}{} // let this encode proceed to install
		case <-deadline:
			t.Fatal("build did not converge — producer wedged at the gate (no re-dispatch / lost wakeup)")
		default:
			select {
			case <-prodDone:
				// Producer done; drain any remaining in-flight spill (release pending encodes).
				go func() {
					for {
						select {
						case <-entered:
							gate <- struct{}{}
						case <-time.After(200 * time.Millisecond):
							return
						}
					}
				}()
				s.WaitSpillsForTest()
				// Every doc is searchable under "shared" (across installed segments + head) ⇒ converged.
				if got := searchDocidsForTest(t, s, tbl, "shared"); len(got) != n {
					t.Fatalf("after convergence, 'shared' has %d docs, want %d", len(got), n)
				}
				return
			default:
				time.Sleep(time.Millisecond)
			}
		}
	}
}

// TestSpillF_GateBoundAndLiveness_MultiTable is the multi-table liveness gate: a table-B head that goes
// over-cap WHILE a table-A spill is in flight must be found + re-dispatched by installSpill's
// re-check-ALL-tables (not just the just-installed table), or the multi-table workload wedges.
func TestSpillF_GateBoundAndLiveness_MultiTable(t *testing.T) {
	s, tblA := newSpillOffworkerStore(t, Options{CapBytes: 64})
	t.Cleanup(func() { s.CloseAndWait() })
	tblB, err := s.CreateTable("filesB")
	if err != nil {
		t.Fatal(err)
	}

	gate := make(chan struct{})
	entered := make(chan struct{}, 256)
	encodeSpillBlock = func() {
		entered <- struct{}{}
		<-gate
	}
	t.Cleanup(func() { encodeSpillBlock = nil })

	const n = 20
	prodDone := make(chan struct{})
	go func() {
		// Interleave the two tables so a B head fills while an A spill is in flight (and vice-versa).
		for d := int64(1); d <= n; d++ {
			s.Update(tblA, d, []string{uniqWord(1000 + int(d)), "shA"})
			s.Update(tblB, d, []string{uniqWord(2000 + int(d)), "shB"})
		}
		close(prodDone)
	}()

	deadline := time.After(20 * time.Second)
	drained := false
	for !drained {
		select {
		case <-entered:
			if got := s.SpillingLenForTest(); got > 1 {
				t.Fatalf("peak len(s.spilling) = %d across two tables, want <= 1", got)
			}
			gate <- struct{}{}
		case <-deadline:
			t.Fatal("multi-table build wedged — a different-table over-cap head was not re-dispatched")
		default:
			select {
			case <-prodDone:
				go func() {
					for {
						select {
						case <-entered:
							gate <- struct{}{}
						case <-time.After(200 * time.Millisecond):
							return
						}
					}
				}()
				s.WaitSpillsForTest()
				drained = true
			default:
				time.Sleep(time.Millisecond)
			}
		}
	}
	if got := searchDocidsForTest(t, s, tblA, "shA"); len(got) != n {
		t.Fatalf("table A 'shA' has %d docs after convergence, want %d", len(got), n)
	}
	if got := searchDocidsForTest(t, s, tblB, "shB"); len(got) != n {
		t.Fatalf("table B 'shB' has %d docs after convergence, want %d", len(got), n)
	}
}

// TestSpillF_CloseAndWaitDrainsInFlightEncode: with the in-flight encode blocked then released,
// CloseAndWait returns within a timeout (the drain is OFF the worker, no v4 self-deadlock) and the doc
// is durable on reopen.
func TestSpillF_CloseAndWaitDrainsInFlightEncode(t *testing.T) {
	dir := t.TempDir()
	q := queue.NewMpsc("spillclose")
	q.Start()
	s, err := Open(dir, q, Options{CapBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}

	entered, _, unpark := parkEncode(t)
	// Over-cap the head so it detaches + the encode parks.
	s.Update(tbl, 1, []string{"alpha", "beta", "gamma"})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("encode never parked")
	}

	// Release the encode shortly, then CloseAndWait must return within a timeout (off-worker drain).
	go func() {
		time.Sleep(50 * time.Millisecond)
		unpark()
	}()
	closed := make(chan struct{})
	go func() { s.CloseAndWait(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("CloseAndWait did not return — in-flight encode drain self-deadlocked (v4 regression)")
	}
	q.Stop()

	// Reopen: the doc the in-flight spill installed must be durable.
	q2 := queue.NewMpsc("spillclose-reopen")
	q2.Start()
	t.Cleanup(q2.Stop)
	s2, err := Open(dir, q2, Options{CapBytes: 64})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { s2.CloseAndWait() })
	if got := searchDocidsForTest(t, s2, tbl, "alpha"); len(got) != 1 || got[0] != 1 {
		t.Fatalf("doc 1 not durable on reopen under 'alpha' = %v, want [1]", got)
	}
}

// TestSpillF_InstallFailureGiveUpBound forces writeManifestBytes to fail persistently (MANIFEST.tmp as
// a directory) so installSpill never succeeds: the dispatch retries a BOUNDED number of times, then
// gives up — dropping the detached head (crash-volatile), removing its temp file, clearing
// spillInFlight/blockProducer, and re-dispatching. No spin, no stuck producer, len(s.spilling) bounded.
func TestSpillF_InstallFailureGiveUpBound(t *testing.T) {
	dir := t.TempDir()
	q := queue.NewMpsc("spillgiveup")
	q.Start()
	s, err := Open(dir, q, Options{CapBytes: 64, MaxInstallRetries: 3})
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.CloseAndWait() })

	// Make MANIFEST.tmp a directory so os.Create(MANIFEST.tmp) in writeManifestBytes fails persistently.
	tmpAsDir := filepath.Join(dir, "MANIFEST.tmp")
	if err := os.Mkdir(tmpAsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Over-cap the head ⇒ detach ⇒ async encode ⇒ install retries then gives up.
	s.Update(tbl, 1, []string{"alpha", "beta", "gamma"})
	s.WaitSpillsForTest()

	// Give-up: no detached head left in flight, the gate is clear, and no temp file orphan remains.
	if got := s.SpillingLenForTest(); got != 0 {
		t.Fatalf("after give-up len(s.spilling) = %d, want 0 (the head was dropped)", got)
	}
	if s.SpillInFlightForTest() {
		t.Fatal("spillInFlight still set after give-up")
	}
	if s.BlockProducerForTest() {
		t.Fatal("blockProducer still set after give-up — the producer would be wedged")
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if isSegTempFileName(e.Name()) {
			t.Fatalf("temp file %q left after give-up", e.Name())
		}
	}
	// No segment was installed (the MANIFEST write never succeeded); the lost head is crash-volatile.
	if got := len(s.SegmentsForTest()); got != 0 {
		t.Fatalf("no segment should have installed under a failing MANIFEST write, got %d", got)
	}

	// Remove the blocker; the store still accepts writes and a clean Close is durable for the next doc.
	if err := os.Remove(tmpAsDir); err != nil {
		t.Fatal(err)
	}
}

// TestSpillF_CrashLosesDetachedHeadNoOrphan: a crash with a detached-but-not-installed head loses it
// (volatile, like today's unspilled head) AND leaves no seg-tmp-* orphan after reopen (G sweeps it).
func TestSpillF_CrashLosesDetachedHeadNoOrphan(t *testing.T) {
	dir := t.TempDir()
	q := queue.NewMpsc("spillcrash")
	q.Start()
	s, err := Open(dir, q, Options{CapBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}

	entered, _, unpark := parkEncode(t)
	// Detach a head and park its encode (so it is detached-but-not-installed at the "crash"). Two
	// keywords push the head over the tiny CapBytes so the over-cap detach fires.
	s.Update(tbl, 1, []string{"alpha", "beta"})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("encode never parked")
	}
	if s.SpillingLenForTest() != 1 {
		t.Fatalf("want 1 detached head, got %d", s.SpillingLenForTest())
	}

	// Crash: abandon the store + the parked encode goroutine without installing. The temp file (if the
	// parked encode already created it) is an orphan; nothing is in the MANIFEST.
	unpark() // let the parked goroutine proceed so it isn't leaked into the next test (it will fail
	q.Stop() // to install against the stopped queue, harmlessly)
	time.Sleep(100 * time.Millisecond)

	// Reopen on a fresh queue: the detached head is GONE (no segment), and G removed any seg-tmp-* orphan.
	q2 := queue.NewMpsc("spillcrash-reopen")
	q2.Start()
	t.Cleanup(q2.Stop)
	s2, err := Open(dir, q2, Options{CapBytes: 64})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { s2.CloseAndWait() })
	if got := len(s2.SegmentsForTest()); got != 0 {
		t.Fatalf("detached-but-not-installed head should be lost on crash, got %d segments", got)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if isSegTempFileName(e.Name()) {
			t.Fatalf("seg-tmp-* orphan %q survived reopen (G did not sweep it)", e.Name())
		}
	}
}

// TestSpillF_SyncSpillManifestWriteFailureRollsBack exercises the SYNCHRONOUS spill's write-failure
// rollback (the spillForTest / Close-flush path, distinct from the off-worker installSpill): a failing
// MANIFEST write must roll the in-memory manifest back and seal NO segment, leaving the head readable.
func TestSpillF_SyncSpillManifestWriteFailureRollsBack(t *testing.T) {
	s, tbl := newSpillOffworkerStore(t, Options{CapBytes: 1 << 20}) // large cap: no async detach
	s.applyForTest(tbl, 1, []string{"alpha", "beta"})

	// Make MANIFEST.tmp a directory so writeManifestBytes' os.Create fails — the synchronous spill must
	// roll back (no segment appended, NextSegId restored, head preserved).
	tmpAsDir := filepath.Join(s.dir, "MANIFEST.tmp")
	if err := os.Mkdir(tmpAsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s.spillForTest(tbl) // spillForTest discards the error; the rollback branch still runs

	if got := len(s.SegmentsForTest()); got != 0 {
		t.Fatalf("failed spill must seal no segment, got %d", got)
	}
	// The head was preserved (rollback did not reset it): doc 1 is still searchable from the head tier.
	if got := searchDocidsForTest(t, s, tbl, "alpha"); len(got) != 1 || got[0] != 1 {
		t.Fatalf("doc 1 lost after a rolled-back spill = %v, want [1] (head preserved)", got)
	}

	// Removing the blocker lets a subsequent spill succeed (the store is not wedged).
	if err := os.Remove(tmpAsDir); err != nil {
		t.Fatal(err)
	}
	s.spillForTest(tbl)
	if got := len(s.SegmentsForTest()); got != 1 {
		t.Fatalf("spill after clearing the blocker must seal 1 segment, got %d", got)
	}
}
