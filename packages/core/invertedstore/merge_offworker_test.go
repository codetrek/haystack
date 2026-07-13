package invertedstore

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codetrek/haystack/packages/core/queue"
)

// merge_offworker_test.go — item A (merge COMPUTE off the worker; spec Task 4 Steps 5–6) acceptance
// tests. The off-worker change runs the heavy mergeSegments on the merge goroutine and installs back
// ON the worker (one MANIFEST writer). These tests guard the exact hazards that move introduces:
//
//   - Step 5 (this file's first test): a parked off-worker compute must NOT block the worker — an
//     Update task enqueued while mergeSegments is parked still drains promptly.
//   - Step 5 race + hit-identity: while the compute is parked, concurrent Updates + Searches run
//     -race clean and the post-drain live hit set equals a SERIAL reference build (a second store fed
//     the same net state with AutoMerge off) — the off-worker path has an equivalence net under load.
//   - Step 5 waitMergeIdle convergence: with a deliberately slow install (beforeManifestFsync delay),
//     waitMergeIdle returns ONLY after the install lands (mergeAckSeq is stored AFTER the last
//     runMergePlan, which awaits its install RunFunc) — proven, not assumed.
//   - Step 6 ref-held-during-compute: while parked, each input segment's refs.Load() >= 2 (the
//     published snapshot ref + the plan's incref), so a concurrent retire cannot free an input
//     mid-read; after release the merged-away inputs are torn down (files removed) and a reopened
//     MANIFEST lists only the merged output.
//   - Step 6 liveTables staleness: a DeleteTable racing an in-flight covering compute is benign — the
//     reclaim is still correct and a follow-up covering pass cleans the now-deleted table.
//
// The merge observer hooks (mergeComputeBlock, beforeManifestFsync, coveringMergeHook) are package
// globals on the merge hot path, so NO test in this file may call t.Parallel.

// parkAt installs a mergeComputeBlock that signals `entered` the first time a compute reaches it and
// then blocks on `release`. The returned `unpark` clears the hook (so a later drain does not re-park)
// and closes `release` (idempotent), letting any parked compute proceed. parkAt does NOT register a
// Cleanup itself — the caller MUST arrange for unpark to run BEFORE its store's CloseAndWait (so the
// drain at Close is not re-parked into a deadlock); the standard pattern is to register CloseAndWait
// FIRST (runs last, LIFO) then call parkAt and register unpark (runs first).
func parkAt(t *testing.T) (entered chan struct{}, release chan struct{}, unpark func()) {
	t.Helper()
	entered = make(chan struct{}, 1)
	release = make(chan struct{})
	var releaseOnce sync.Once
	unpark = func() {
		mergeComputeBlock = nil
		releaseOnce.Do(func() { close(release) })
	}
	mergeComputeBlock = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}
	return entered, release, unpark
}

// waitEntered blocks until a parked compute has signaled entry, failing the test on timeout.
func waitEntered(t *testing.T, entered chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("merge compute never started off the worker (parked block never reached)")
	}
}

// With the merge COMPUTE off the worker, a parked compute must NOT block the worker: an Update
// enqueued while mergeSegments is blocked still completes promptly.
func TestMergeOffWorker_ComputeDoesNotBlockWorker(t *testing.T) {
	q := queue.NewMpsc("offworker")
	q.Start()
	s, err := Open(t.TempDir(), q, Options{AutoMerge: true, Fanout: 2, CapBytes: 1 << 12})
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := s.CreateTable("files")

	// Register CloseAndWait FIRST so it runs LAST (LIFO) — after unpark has cleared the hook + released
	// the parked compute, so the Close-time drain is not re-parked into a deadlock and the merge output
	// is settled before t.TempDir's RemoveAll.
	t.Cleanup(func() { s.CloseAndWait() })
	entered, _, unpark := parkAt(t)
	t.Cleanup(unpark)

	// Seal >= Fanout segments so the background merger fires a tiered pass (compute will park).
	for i := 0; i < 4; i++ {
		s.applyForTest(tbl, int64(1000+i), []string{uniqWord(1000 + i)})
		s.spillForTest(tbl)
	}
	waitEntered(t, entered)

	// The compute is parked. A worker task (RunFunc) MUST still run — proving the compute is off-worker.
	done := make(chan struct{})
	go func() { s.q.RunFunc(func() error { return nil }); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker blocked behind the off-worker merge compute (compute is ON the worker)")
	}
}

// docSet maps docid -> live keyword set; the empty/absent entry is a deleted doc. It is the net state
// fed identically to the store-under-test and the serial reference build so a hit-identity assertion
// compares the off-worker merge path against a deterministic, AutoMerge-off oracle.
type docSet map[int64][]string

// buildSerialReference builds a fresh store with AutoMerge OFF, applies net `state` for each doc via
// the public Update path, drains, and returns it. With AutoMerge off the off-worker merge path is
// never exercised, so this is the equivalence oracle: same net data, no background merge. The caller
// owns Close.
func buildSerialReference(t *testing.T, state docSet) (*Store, int) {
	t.Helper()
	q := queue.NewMpsc("offworker-ref")
	q.Start()
	s, err := Open(t.TempDir(), q, Options{}) // AutoMerge defaults OFF
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := s.CreateTable("files")
	if err != nil {
		t.Fatal(err)
	}
	for d, kws := range state {
		s.Update(tbl, d, kws)
	}
	s.sync()
	return s, tbl
}

// searchDocSet returns the live docid set Search yields for `query` (prefix) on tableId.
func searchDocSet(s *Store, tableId int, query string) map[int64]bool {
	r := s.Search(tableId, query, 0, nil)
	out := make(map[int64]bool, len(r.DocIds))
	for d := range r.DocIds {
		out[d] = true
	}
	return out
}

// assertSameDocSet fails the test unless a == b (as docid sets), reporting the symmetric difference.
func assertSameDocSet(t *testing.T, query string, got, want map[int64]bool) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("query %q: hit set size %d != reference %d (got=%v want=%v)", query, len(got), len(want), got, want)
	}
	for d := range want {
		if !got[d] {
			t.Fatalf("query %q: doc %d in reference but missing from off-worker store (got=%v)", query, d, got)
		}
	}
	for d := range got {
		if !want[d] {
			t.Fatalf("query %q: doc %d in off-worker store but not the reference (want=%v)", query, d, want)
		}
	}
}

// TestMergeOffWorker_ConcurrentUpdateSearchUnderParkedCompute is the Step 5 adversarial equivalence
// net. While the off-worker mergeSegments is held OPEN (parked via mergeComputeBlock), it fires
// concurrent Updates + Searches against the parked compute, then unparks, drains, and asserts the
// live hit set equals a SERIAL reference build (a second store fed the same net state with AutoMerge
// off). The concurrent Updates re-write each doc to its OWN final keyword set (idempotent), so the
// net state is deterministic regardless of interleaving with the parked compute; the Searches prove
// readers do not panic on a segment being merged away and -race stays clean. Run under -race this is
// the off-worker path's "hits identical under concurrent load + inputs not torn down mid-compute"
// guard the single off-worker-ness test does not provide.
func TestMergeOffWorker_ConcurrentUpdateSearchUnderParkedCompute(t *testing.T) {
	q := queue.NewMpsc("offworker-stress")
	q.Start()
	// Small Fanout + tiny CapBytes so sealing a handful of docs reaches Fanout and the background
	// merger fires a tiered pass (whose compute we park). AutoMerge on drives the off-worker path.
	s, err := Open(t.TempDir(), q, Options{AutoMerge: true, Fanout: 2, CapBytes: 1 << 11})
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := s.CreateTable("files")

	// The deterministic NET state: each doc has "alpha" (a shared prefix that fans out) + a per-doc
	// keyword. This is fed to BOTH the off-worker store and the serial reference.
	const nDocs = 24
	state := docSet{}
	for d := int64(1); d <= nDocs; d++ {
		state[d] = []string{"alpha", fmt.Sprintf("doc%d", d)}
	}

	t.Cleanup(func() { s.CloseAndWait() })
	entered, _, unpark := parkAt(t)
	t.Cleanup(unpark)

	// Establish the net state, then seal enough segments to fire a tiered merge (the parked compute).
	for d := int64(1); d <= nDocs; d++ {
		s.applyForTest(tbl, d, state[d])
		s.spillForTest(tbl) // one segment per doc => well past Fanout=2 => tiered passes fire
	}
	waitEntered(t, entered)

	// The compute is PARKED. Fire concurrent Updates (idempotent re-writes of the same net state) +
	// Searches against the parked compute. Run them for a short, bounded burst so they overlap the
	// parked window; the idempotent re-writes keep the final net state deterministic.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for d := int64(1); d <= nDocs; d++ {
					s.Update(tbl, d, state[d]) // idempotent: net state unchanged
				}
			}
		}()
	}
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = s.Search(tbl, "alpha", 0, nil)
				_ = s.Search(tbl, "doc", 0, nil)
				_ = s.GetDocs(tbl, "alpha")
			}
		}()
	}

	// Let the concurrent load overlap the parked compute, then unpark and stop the load.
	time.Sleep(100 * time.Millisecond)
	unpark() // clear the hook + release the parked compute (subsequent passes do not re-park)
	close(stop)
	wg.Wait()

	// Drain: every async Update applied, the background merger settled, the off-worker compute installed.
	s.sync()
	s.waitMergeIdle()

	// Hit-identity: the off-worker store's live hits equal a serial reference fed the same net state.
	ref, refTbl := buildSerialReference(t, state)
	defer ref.CloseAndWait()
	for _, q := range []string{"alpha", "doc"} {
		assertSameDocSet(t, q, searchDocSet(s, tbl, q), searchDocSet(ref, refTbl, q))
	}
	// Per-doc keyword equivalence too (each doc resolvable under its own keyword).
	for d := int64(1); d <= nDocs; d++ {
		q := fmt.Sprintf("doc%d", d)
		assertSameDocSet(t, q, searchDocSet(s, tbl, q), searchDocSet(ref, refTbl, q))
	}
}

// TestMergeOffWorker_WaitMergeIdleWaitsForInstall is the Step 5 convergence guard: with a deliberately
// SLOW install (a beforeManifestFsync delay armed for exactly the merge install), waitMergeIdle must
// return ONLY after the install lands. mergeAckSeq is stored in runScheduledMerge AFTER the last
// runMergePlan returns, and runMergePlan awaits its install RunFunc — so a waitMergeIdle that sampled
// the merge's reqSeq cannot observe ackSeq catch up until the (slow) install has completed. We pin the
// arming window deterministically by PARKING the off-worker compute (which runs strictly BEFORE the
// install): while parked, the spills' manifest writes are already done, so arming the slow hook now
// targets only the upcoming install. After unpark, we assert waitMergeIdle blocks across the slow
// install and the merged output is live by the time it returns.
func TestMergeOffWorker_WaitMergeIdleWaitsForInstall(t *testing.T) {
	q := queue.NewMpsc("offworker-idle")
	q.Start()
	s, err := Open(t.TempDir(), q, Options{AutoMerge: true, Fanout: 2, CapBytes: 1 << 12})
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := s.CreateTable("files")

	t.Cleanup(func() { s.CloseAndWait() })
	entered, _, unpark := parkAt(t)
	t.Cleanup(unpark)
	t.Cleanup(func() { beforeManifestFsync = nil })

	// Seal exactly Fanout L0 segments => one tiered merge fires; its compute parks at the hook. Drive
	// the PUBLIC Update path so liveByTable is maintained (deadFraction ~0 => a TIERED pass, not the
	// bogus-deadFraction covering merge the bare test seam would force).
	s.Update(tbl, 1, []string{"alpha"})
	s.sync()
	s.forceSpill(tbl)
	s.Update(tbl, 2, []string{"alpha"})
	s.sync()
	s.forceSpill(tbl)
	waitEntered(t, entered)

	if len(s.SegmentsForTest()) != 2 {
		t.Fatalf("expected 2 L0 inputs before the merge installs, got %d", len(s.SegmentsForTest()))
	}

	// Arm the slow install NOW (the spills' manifest writes already completed before the compute parked,
	// so the next manifest write is the merge install). installDone is closed by the hook AFTER its
	// delay, so we can prove waitMergeIdle did not return before the (slow) install ran.
	const installDelay = 250 * time.Millisecond
	var installStartedAt atomic.Int64
	installDone := make(chan struct{})
	beforeManifestFsync = func() {
		installStartedAt.CompareAndSwap(0, time.Now().UnixNano())
		time.Sleep(installDelay)
		select {
		case <-installDone:
		default:
			close(installDone)
		}
	}

	// Unpark: the compute finishes, then the (slow) install runs on the worker, then mergeAckSeq is
	// stored. A reader thread calls waitMergeIdle concurrently; it must NOT return until the install has
	// completed.
	unpark()

	idleReturned := make(chan time.Time, 1)
	go func() {
		s.waitMergeIdle()
		idleReturned <- time.Now()
	}()

	// waitMergeIdle must still be blocked while the slow install is in flight. Give the install time to
	// start but not finish: assert idle has NOT returned before the install delay elapses.
	select {
	case <-installDone:
	case <-time.After(5 * time.Second):
		t.Fatal("slow install never ran (merge install's manifest write was not reached)")
	}
	installFinishedAt := time.Now()

	var idleAt time.Time
	select {
	case idleAt = <-idleReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("waitMergeIdle never returned after the slow install completed")
	}

	// Convergence: waitMergeIdle returned only AFTER the install's manifest write finished — proving
	// mergeAckSeq is stored after the install RunFunc, not before it.
	if idleAt.Before(installFinishedAt) {
		t.Fatalf("waitMergeIdle returned at %v, BEFORE the slow install finished at %v — ackSeq stored before install",
			idleAt, installFinishedAt)
	}
	if installStartedAt.Load() == 0 {
		t.Fatal("the install's manifest write never started — the merge did not install")
	}

	// And the merged output is live by the time waitMergeIdle returned: the 2 L0 inputs collapsed.
	if n := len(s.SegmentsForTest()); n >= 2 {
		t.Fatalf("expected the tiered merge to collapse the 2 L0 inputs once installed, got %d segments", n)
	}
	if r := s.Search(tbl, "alpha", 0, nil); !hasDoc(r, 1) || !hasDoc(r, 2) {
		t.Fatalf("after the slow install, alpha must contain both docs 1 and 2: %v", r.DocIds)
	}
}

// TestMergeOffWorker_InputsRefHeldAcrossCompute is the Step 6 lifecycle guard. While the off-worker
// mergeSegments is PARKED, each input segment must be ref-held with refs.Load() >= 2 — the published
// snapshot ref (1, set at seal) plus the plan's incref (segsByIdsLocked bumps it to 2) — so a
// concurrent retire cannot free an input mid-read across the off-worker compute. After unpark + drain,
// the merged-away inputs must be torn down (files removed) and a REOPENED MANIFEST must list only the
// merged output. A regression that dropped the plan incref early (re-opening the load-then-retire race
// the plan closes) would leave an input at refs==1 while parked and fail this directly — the present
// off-worker-ness test would not catch it.
func TestMergeOffWorker_InputsRefHeldAcrossCompute(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, dir, Options{AutoMerge: true, Fanout: 3, CapBytes: 1 << 12})
	tbl, _ := s.CreateTable("files")

	entered, _, unpark := parkAt(t)
	t.Cleanup(unpark) // clear the hook on any early t.Fatal so a later drain does not re-park

	// Seal exactly Fanout L0 segments => one tiered merge fires; its compute parks at the hook with the
	// plan's input refs already taken (selectTieredMergePlan ran on the worker before mergeSegments).
	// Drive the PUBLIC Update path (not applyForTest) so liveByTable is maintained and deadFraction
	// stays ~0 — otherwise the bogus deadFraction=1.0 of the bare test seam would fire a covering merge
	// instead of the tiered pass this test pins.
	const fanout = 3
	for d := int64(1); d <= fanout; d++ {
		s.Update(tbl, d, []string{"alpha"})
		s.sync()
		s.forceSpill(tbl)
	}
	waitEntered(t, entered)

	// Capture the input ids while parked. All Fanout L0 segments are the tiered inputs.
	inputs := s.SegmentsForTest()
	if len(inputs) != fanout {
		t.Fatalf("expected %d L0 inputs while the compute is parked, got %d", fanout, len(inputs))
	}
	inputIds := make([]uint64, 0, len(inputs))
	for _, sm := range inputs {
		inputIds = append(inputIds, sm.Id)
	}

	// REF-HELD ACROSS COMPUTE: each input is held at refs >= 2 (published snapshot + plan incref) while
	// the off-worker compute is parked, so a concurrent retire cannot free it mid-read.
	for _, id := range inputIds {
		if got := s.segRefsByIdForTest(id); got < 2 {
			t.Fatalf("input segment %d held at refs=%d while the off-worker compute is parked, want >= 2 "+
				"(published snapshot + plan incref) — the plan must keep the input pinned across the compute", id, got)
		}
	}

	// Unpark + drain: the compute finishes, installs on the worker, and releases the plan refs. The
	// merged-away inputs then drop to zero and are torn down (close + unlink).
	unpark()
	s.sync()
	s.waitMergeIdle()

	// Post-release teardown: every merged-away input file is removed, and the live set is the single
	// merged output (the inputs collapsed).
	live := s.SegmentsForTest()
	if len(live) != 1 {
		t.Fatalf("expected the tiered merge to collapse %d inputs into 1 output, got %d live segments", fanout, len(live))
	}
	outId := live[0].Id
	for _, id := range inputIds {
		if id == outId {
			continue
		}
		if s.segFileExists(id) {
			t.Fatalf("merged-away input segment %d file still exists after install + release (not torn down)", id)
		}
		if got := s.segRefsByIdForTest(id); got != -1 {
			t.Fatalf("merged-away input segment %d still has a live handle (refs=%d) after release", id, got)
		}
	}

	// A REOPENED MANIFEST lists ONLY the merged output — the inputs were removed durably, not just
	// dropped from the in-memory set.
	s.CloseAndWait()
	s2 := openAt(t, dir, Options{AutoMerge: false})
	reSegs := s2.SegmentsForTest()
	if len(reSegs) != 1 {
		t.Fatalf("reopened MANIFEST lists %d segments, want only the merged output", len(reSegs))
	}
	if reSegs[0].Id != outId {
		t.Fatalf("reopened MANIFEST names segment %d, want the merged output %d", reSegs[0].Id, outId)
	}
	if r := s2.Search(tbl, "alpha", 0, nil); !hasDoc(r, 1) || !hasDoc(r, 2) || !hasDoc(r, 3) {
		t.Fatalf("after reopen, alpha must contain docs 1,2,3 from the merged output: %v", r.DocIds)
	}
	s2.CloseAndWait()
}

// segHasTableKeys reports whether the live single segment holds any [I] record for tableId — used to
// observe the liveTables-staleness window: a dead table's keys are over-retained for ONE covering
// pass that snapshotted the table as live, then reclaimed by the follow-up pass.
func segHasTableKeys(t *testing.T, s *Store, tableId int) bool {
	t.Helper()
	segs := s.acquireSnapshot()
	defer s.releaseSnapshot(segs)
	for _, seg := range segs {
		if len(segInvRecords(seg, tableId)) > 0 {
			return true
		}
	}
	return false
}

// TestMergeOffWorker_DeleteTableRacesCoveringCompute is the Step 6 liveTables-staleness guard.
// selectCoveringMergePlan snapshots liveTables under the lock, but the off-worker compute + install
// run later; a DeleteTable in that window changes the catalog AFTER selection. This must be benign:
// the racing covering pass over-retains the now-deleted table's keys for ONE pass (it merged with the
// stale liveTables that still listed the table), Search/GetDocs for the deleted table read empty
// immediately regardless, and the follow-up covering pass the DeleteTable scheduled reclaims the dead
// table's bytes. The surviving table's data is correct throughout.
func TestMergeOffWorker_DeleteTableRacesCoveringCompute(t *testing.T) {
	q := queue.NewMpsc("offworker-staletables")
	q.Start()
	// High Fanout so NO tiered pass qualifies — the only mergeSegments is the covering one we park, so
	// the parked compute is deterministically the covering pass that snapshotted liveTables = {A, B}.
	s, err := Open(t.TempDir(), q, Options{AutoMerge: true, Fanout: 100, CapBytes: 1 << 12})
	if err != nil {
		t.Fatal(err)
	}
	tblA, _ := s.CreateTable("A")
	tblB, _ := s.CreateTable("B")

	t.Cleanup(func() { s.CloseAndWait() })
	entered, _, unpark := parkAt(t)
	t.Cleanup(unpark)

	// Two sealed segments holding both tables' data (covering needs the bottom + everything above).
	s.applyForTest(tblA, 1, []string{"alpha"})
	s.applyForTest(tblB, 1, []string{"beta"})
	s.spillForTest(tblA)
	s.spillForTest(tblB)
	s.applyForTest(tblA, 2, []string{"alpha"})
	s.applyForTest(tblB, 2, []string{"beta"})
	s.spillForTest(tblA)
	s.spillForTest(tblB)

	// Force a covering pass; its compute parks AFTER selectCoveringMergePlan snapshotted liveTables =
	// {A, B} (both still in the catalog at selection time).
	s.triggerMerge(true)
	waitEntered(t, entered)

	// RACE: delete table B while the covering compute is parked — the catalog changes AFTER the plan's
	// liveTables snapshot. DeleteTable runs on the (free) worker and schedules a follow-up covering pass.
	if err := s.DeleteTable(tblB); err != nil {
		t.Fatalf("DeleteTable raced with the parked covering compute: %v", err)
	}
	// B reads empty IMMEDIATELY after delete, regardless of whether its bytes are reclaimed yet
	// (Search/GetDocs gate on the catalog).
	if r := s.Search(tblB, "beta", 0, nil); len(r.DocIds) != 0 {
		t.Fatalf("deleted table B must read empty immediately, got %v", r.DocIds)
	}

	// Unpark: the parked covering pass installs with the STALE liveTables (B still listed live) — B's
	// keys are over-retained for this one pass (the benign staleness window). Then the follow-up pass
	// (scheduled by DeleteTable) runs with a FRESH liveTables = {A} and reclaims B.
	unpark()
	s.sync()
	s.waitMergeIdle()

	// CORRECTNESS: the surviving table A is intact and complete after the racing merges.
	rA := s.Search(tblA, "alpha", 0, nil)
	if !hasDoc(rA, 1) || !hasDoc(rA, 2) {
		t.Fatalf("surviving table A must keep docs 1,2 across the DeleteTable-racing covering merge: %v", rA.DocIds)
	}
	// B is gone from the catalog, so it reads empty.
	if r := s.Search(tblB, "beta", 0, nil); len(r.DocIds) != 0 {
		t.Fatalf("deleted table B must read empty after the merges settle, got %v", r.DocIds)
	}
	// RECLAIM: the follow-up covering pass dropped B's [I] keys from the live segment — the dead table's
	// bytes are reclaimed even though the racing pass snapshotted B as live.
	if segHasTableKeys(t, s, tblB) {
		t.Fatal("deleted table B's keys were not reclaimed by the follow-up covering pass (stale liveTables not cleaned)")
	}
	// And A's keys are still present (not collaterally reclaimed).
	if !segHasTableKeys(t, s, tblA) {
		t.Fatal("surviving table A's keys were dropped by the covering reclaim (over-reclaim bug)")
	}
}

// TestMergeOffWorker_PersistentInstallFailureDoesNotSpin guards the error-path regression the
// off-worker tiered loop introduced: when installMerge fails persistently (here the MANIFEST.tmp path
// is a directory so writeManifestBytes can never create the temp file), installMerge rolls the live
// set back to the pre-merge inputs — so the SAME level stays >= Fanout. If runMergePlan swallowed the
// install error and the loop only broke on plan==nil, runScheduledMerge would re-select the same
// level and re-run the heavy mergeSegments compute forever (a hot CPU/IO livelock), AND
// runScheduledMerge would never reach mergeAckSeq.Store, so waitMergeIdle would hang. The fix makes
// runMergePlan RETURN the install error and the tiered loop break on it (drop the pass; the next
// trigger retries). This test bounds the mergeSegments invocation count per pass and proves a later
// trigger (after the failure is cleared) succeeds.
func TestMergeOffWorker_PersistentInstallFailureDoesNotSpin(t *testing.T) {
	dir := t.TempDir()
	q := queue.NewMpsc("offworker-spin")
	q.Start()
	s, err := Open(dir, q, Options{AutoMerge: true, Fanout: 2, CapBytes: 1 << 12})
	if err != nil {
		t.Fatal(err)
	}
	tbl, _ := s.CreateTable("files")
	t.Cleanup(func() { s.CloseAndWait() })

	// Count mergeSegments invocations AND park the FIRST one. mergeComputeBlock fires at the START of
	// each mergeSegments: every call bumps computeCount (a spinning tiered loop would re-run it
	// unboundedly; the fix runs it once per pass then breaks on the failed install), and the FIRST call
	// blocks on `release` after signalling `entered`. Parking the first compute is what makes the
	// failure injection deterministic: it lets the two spills finish their OWN manifest writes (so two
	// L0 segments go live and the tiered merge actually has inputs) and pins the merge at the compute,
	// BEFORE any install runs — so when we then make MANIFEST.tmp a directory it fails ONLY the
	// install(s), never the spills. (Blocking MANIFEST.tmp before the spills would fail the spills too,
	// publishing no segments, so no merge would ever fire — the compute would never run.)
	//
	// unpark only RELEASES the parked compute; the hook stays installed as a pure counter so the later
	// retry's compute is still counted (the retry assertion reads computeCount). t.Cleanup clears it.
	var computeCount atomic.Int64
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var parkOnce sync.Once
	mergeComputeBlock = func() {
		computeCount.Add(1)
		parkOnce.Do(func() {
			entered <- struct{}{}
			<-release
		})
	}
	t.Cleanup(func() { mergeComputeBlock = nil })
	var releaseOnce sync.Once
	unpark := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unpark) // release any still-parked compute on an early t.Fatal so Close's drain proceeds

	// Seal Fanout L0 segments => a tiered merge fires; its compute parks at the hook (the two spills'
	// own manifest writes have already completed). Drive the PUBLIC Update path so liveByTable is
	// maintained (deadFraction ~0 => the TIERED loop under test, not the one-shot covering merge the
	// bare test seam's bogus deadFraction=1.0 would fire instead).
	for d := int64(1); d <= 2; d++ {
		s.Update(tbl, d, []string{"alpha"})
		s.sync()
		s.forceSpill(tbl)
	}
	waitEntered(t, entered) // the two spills are durable; the merge is parked at the compute, pre-install

	// NOW make MANIFEST.tmp un-creatable as a file (it is a directory) so EVERY installMerge
	// writeManifest fails — a persistent install failure that hits ONLY the install, not the (already
	// done) spills.
	tmpBlock := filepath.Join(dir, "MANIFEST.tmp")
	if err := os.Mkdir(tmpBlock, 0o755); err != nil {
		t.Fatal(err)
	}

	// Unpark: the compute finishes, the install fails persistently. With the fix the pass runs
	// mergeSegments once, the install fails, the loop breaks, and runScheduledMerge reaches
	// mergeAckSeq.Store — so waitMergeIdle CONVERGES (a spin would hang it forever).
	unpark()

	idleDone := make(chan struct{})
	go func() { s.waitMergeIdle(); close(idleDone) }()
	select {
	case <-idleDone:
	case <-time.After(5 * time.Second):
		t.Fatal("waitMergeIdle never converged under a persistent install failure — the tiered loop is spinning " +
			"(runScheduledMerge never reached mergeAckSeq.Store)")
	}

	// The compute ran a BOUNDED number of times (the fix: ~one mergeSegments per qualifying pass, then
	// break on the failed install). A spin would have run it hundreds/thousands of times in the window.
	// Allow a small margin for an extra coalesced trigger, but assert it is nowhere near a spin.
	if n := computeCount.Load(); n == 0 {
		t.Fatal("expected the tiered merge to run mergeSegments at least once")
	} else if n > 8 {
		t.Fatalf("mergeSegments ran %d times under a persistent install failure — the tiered loop is spinning "+
			"(re-selecting the rolled-back level and recomputing the merge)", n)
	}

	// The inputs are still live (install rolled back), so the store is fully usable.
	if r := s.Search(tbl, "alpha", 0, nil); !hasDoc(r, 1) || !hasDoc(r, 2) {
		t.Fatalf("data must survive the failed installs: alpha = %v", r.DocIds)
	}
	if n := len(s.SegmentsForTest()); n != 2 {
		t.Fatalf("after the failed install the 2 inputs must still be live (rolled back), got %d segments", n)
	}

	// Clear the failure; a LATER trigger must succeed (the pass was dropped, not poisoned).
	if err := os.Remove(tmpBlock); err != nil {
		t.Fatal(err)
	}
	before := computeCount.Load()
	s.triggerMerge(false) // re-trigger the (now-installable) tiered merge
	s.waitMergeIdle()
	if computeCount.Load() <= before {
		t.Fatal("the retry trigger did not run a fresh merge compute")
	}
	if n := len(s.SegmentsForTest()); n != 1 {
		t.Fatalf("after the failure is cleared and a retry trigger, the 2 inputs must merge to 1, got %d segments", n)
	}
	if r := s.Search(tbl, "alpha", 0, nil); !hasDoc(r, 1) || !hasDoc(r, 2) {
		t.Fatalf("after the successful retry, alpha must contain both docs: %v", r.DocIds)
	}
}
