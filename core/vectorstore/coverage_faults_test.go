package vectorstore

import (
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// --- Coverage gate closers (Phase 2 final-review) ---------------------------
//
// These tests drive the per-function floor for the IO-fault error branches and
// the migrated graph-delete machinery the insert-only seal pipeline never
// exercises, via the fsCreate/fsOpen/fsOpenFile overrides and direct
// graph-store manipulation.

// withCreateFault overrides fsCreate so the returned file injects faults per cfg.
func withCreateFault(t *testing.T, cfg func(*faultFile)) {
	t.Helper()
	orig := fsCreate
	t.Cleanup(func() { fsCreate = orig })
	fsCreate = func(name string) (osFile, error) {
		f, err := orig(name)
		if err != nil {
			return nil, err
		}
		ff := &faultFile{osFile: f}
		cfg(ff)
		return ff, nil
	}
}

// withOpenFault overrides fsOpen so the returned file injects faults per cfg.
func withOpenFault(t *testing.T, cfg func(*faultFile)) {
	t.Helper()
	orig := fsOpen
	t.Cleanup(func() { fsOpen = orig })
	fsOpen = func(name string) (osFile, error) {
		f, err := orig(name)
		if err != nil {
			return nil, err
		}
		ff := &faultFile{osFile: f}
		cfg(ff)
		return ff, nil
	}
}

func sampleHead(t *testing.T) *segment {
	t.Helper()
	rng := rand.New(rand.NewSource(5))
	rows := make([]struct {
		doc int64
		v   []float32
		pl  Payload
	}, 0, 12)
	for i := 0; i < 12; i++ {
		v := make([]float32, 4)
		for d := range v {
			v[d] = rng.Float32()
		}
		rows = append(rows, struct {
			doc int64
			v   []float32
			pl  Payload
		}{int64(i + 1), v, Payload{"p": StringValue("p")}})
	}
	return buildHeadSeg(Cosine, rows)
}

// TestWriteSealedSegment_WriteFault drives the write-error branches of each
// per-file writer (writeSealedSegment via writeVectorsFile's first Write).
func TestWriteSealedSegment_WriteFault(t *testing.T) {
	head := sampleHead(t)
	withCreateFault(t, func(f *faultFile) { f.failWrite = true })
	dir := filepath.Join(t.TempDir(), "seg-1-0")
	if err := writeSealedSegment(dir, head, nil); err == nil {
		t.Fatal("writeSealedSegment should fail when Write is injected to fail")
	}
}

// TestWriteSealedSegment_SyncFault drives the f.Sync() error branch of the
// per-file writers.
func TestWriteSealedSegment_SyncFault(t *testing.T) {
	head := sampleHead(t)
	withCreateFault(t, func(f *faultFile) { f.failSync = true })
	dir := filepath.Join(t.TempDir(), "seg-1-0")
	if err := writeSealedSegment(dir, head, nil); err == nil {
		t.Fatal("writeSealedSegment should fail when Sync is injected to fail")
	}
}

// TestOpenSealedSegment_FileSizeFault drives fileSize's Stat-error path (and the
// openSealedSegment error branch that propagates it) by failing Stat on the
// read-open of vectors.dat.
func TestOpenSealedSegment_StatFault(t *testing.T) {
	head := sampleHead(t)
	dir := filepath.Join(t.TempDir(), "seg-1-0")
	requireNoError(t, writeSealedSegment(dir, head, nil))

	// Fail Stat on the next fsOpenFile-opened file (vectors.dat is first).
	orig := fsOpenFile
	t.Cleanup(func() { fsOpenFile = orig })
	fsOpenFile = func(name string, flag int, perm os.FileMode) (osFile, error) {
		f, err := orig(name, flag, perm)
		if err != nil {
			return nil, err
		}
		return &statFaultFile{osFile: f}, nil
	}
	if _, err := openSealedSegment(dir, Cosine, 1, nil); err == nil {
		t.Fatal("openSealedSegment should fail when Stat (fileSize) is injected to fail")
	}
}

// TestOpenSealedSegment_ReadWholeFileFault drives readWholeFile's ReadAt error
// path: vectors.dat opens fine (fsOpenFile) but slotdoc.dat goes through fsOpen
// → readWholeFile, where we fail ReadAt.
func TestOpenSealedSegment_ReadWholeFileFault(t *testing.T) {
	head := sampleHead(t)
	dir := filepath.Join(t.TempDir(), "seg-1-0")
	requireNoError(t, writeSealedSegment(dir, head, nil))

	withOpenFault(t, func(f *faultFile) { f.failReadAt = true })
	if _, err := openSealedSegment(dir, Cosine, 1, nil); err == nil {
		t.Fatal("openSealedSegment should fail when readWholeFile's ReadAt fails")
	}
}

// TestFsyncDir_OpenFault drives fsyncDir's open-error branch (a non-existent dir).
func TestFsyncDir_OpenFault(t *testing.T) {
	if err := fsyncDir(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("fsyncDir on a missing directory should error")
	}
}

// TestFsyncDir_SyncFault drives fsyncDir's directory-Sync error branch: the open
// succeeds, then Sync fails. On a non-Windows OS the error must propagate (POSIX
// directory fsync is real); on Windows fsyncDir documents Sync as a no-op and
// swallows the fault, so it returns nil there. (Both branches were formerly
// covered by the now-removed manifest-rewrite dir-fsync test.)
func TestFsyncDir_SyncFault(t *testing.T) {
	withOpenFault(t, func(f *faultFile) { f.failSync = true })
	err := fsyncDir(t.TempDir())
	if runtime.GOOS == "windows" {
		if err != nil {
			t.Fatalf("fsyncDir Sync fault must be swallowed on Windows (no-op), got %v", err)
		}
		return
	}
	if err == nil {
		t.Fatal("fsyncDir must surface a directory Sync fault on POSIX")
	}
}

// TestStore_SealedByID_NotFound drives sealedByID's nil (not-found) return.
func TestStore_SealedByID_NotFound(t *testing.T) {
	s := openTestStore(t, Cosine)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if ss := s.sealedByID(segID(98765)); ss != nil {
		t.Fatal("sealedByID for an unknown id should return nil")
	}
}

// --- migrated graph-delete machinery (segGraphStore variants) ----------------

// faultSegGraphStore wraps segGraphStore and fails PutNode to drive the seg
// store's runInTxnLocked abort path (txnAbort returning the cause).
type faultSegGraphStore struct {
	*segGraphStore
	failPut bool
}

func (f *faultSegGraphStore) PutNode(id uint64, level int, vector []float32, docId int64) error {
	if f.failPut {
		return errInjected
	}
	return f.segGraphStore.PutNode(id, level, vector, docId)
}

func sealedFromRandom(t *testing.T, n, dim int) *sealedSegment {
	t.Helper()
	rng := rand.New(rand.NewSource(21))
	rows := make([]struct {
		doc int64
		v   []float32
		pl  Payload
	}, 0, n)
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		rows = append(rows, struct {
			doc int64
			v   []float32
			pl  Payload
		}{int64(4000 + i), v, nil})
	}
	head := buildHeadSeg(Cosine, rows)
	dir := filepath.Join(t.TempDir(), "seg-1-0")
	requireNoError(t, writeSealedSegment(dir, head, nil))
	ss, err := openSealedSegment(dir, Cosine, 1, nil)
	requireNoError(t, err)
	t.Cleanup(func() { ss.close() })
	return ss
}

// TestSegGraphStore_TxnAbort drives the seg store through runInTxnLocked's abort
// branch (PutNode fault → txnAbort returns the cause), covering segGraphStore's
// txnBegin/txnAbort.
func TestSegGraphStore_TxnAbort(t *testing.T) {
	ss := sealedFromRandom(t, 20, 4)
	gs := newSegGraphStore(ss)
	fs := &faultSegGraphStore{segGraphStore: gs, failPut: true}
	idx := newHNSWIndex(fs, withGraphM(16))
	gs.bindSlot(4000, 0)
	if err := idx.insert(4000, ss.getVectorRef(0)); err == nil {
		t.Fatal("seg insert with failing PutNode should error (txnAbort path)")
	}
}

// TestSegGraphStore_DeleteEveryNodeClearsEntry deletes every node from a seg
// graph, driving HighestLiveNodeExcluding across its ranking branches and finally
// ClearEntryPoint when none remain, plus GetDocId's not-found return on a deleted
// node id.
func TestSegGraphStore_DeleteEveryNodeClearsEntry(t *testing.T) {
	ss := sealedFromRandom(t, 30, 6)
	gs := newSegGraphStore(ss)
	idx := newHNSWIndex(gs, withGraphM(16), withGraphEfConstruction(100),
		withGraphRand(rand.New(rand.NewSource(7))))
	b := idx.newBatch()
	var docs []int64
	ss.eachLive(func(slot int, docID int64, stored []float32, norm float32) {
		gs.bindSlot(docID, slot)
		b.put(docID, stored)
		docs = append(docs, docID)
	})
	requireNoError(t, b.commit())

	// Capture a node id BEFORE deletion so GetDocId's not-found path can be hit
	// after that node is removed.
	ep, _, err := gs.GetEntryPoint()
	requireNoError(t, err)

	for _, doc := range docs {
		requireNoError(t, idx.delete(doc))
	}
	// All nodes gone → entry point cleared (ClearEntryPoint).
	if _, _, err := gs.GetEntryPoint(); err == nil {
		t.Fatal("seg entry point not cleared after deleting every node")
	}
	// GetDocId on the (now deleted) former entry-point node returns found=false.
	if _, ok, _ := gs.GetDocId(ep); ok {
		t.Fatal("GetDocId on a deleted node should report not-found")
	}
	// Search over the emptied seg graph returns nil.
	got, err := idx.search(make([]float32, 6), 3)
	requireNoError(t, err)
	if got != nil {
		t.Fatalf("emptied seg graph search = %v, want nil", got)
	}
}

// TestMemGraphStore_HighestLiveTieBreak drives memGraphStore.
// HighestLiveNodeExcluding's tie-break (equal level, lower id wins) and the
// not-found return, which the random delete tests do not deterministically hit.
func TestMemGraphStore_HighestLiveTieBreak(t *testing.T) {
	m := newMemGraphStore(Cosine)
	// Three nodes, all level 0 (a tie); lowest id must win when none excluded.
	requireNoError(t, m.PutNode(5, 0, []float32{1, 0}, 105))
	requireNoError(t, m.PutNode(2, 0, []float32{0, 1}, 102))
	requireNoError(t, m.PutNode(9, 0, []float32{1, 1}, 109))
	id, lvl, ok, err := m.HighestLiveNodeExcluding(999) // exclude none present
	requireNoError(t, err)
	if !ok || id != 2 || lvl != 0 {
		t.Fatalf("HighestLiveNodeExcluding = id=%d lvl=%d ok=%v, want id=2 lvl=0 ok=true", id, lvl, ok)
	}
	// Excluding the winner promotes the next-lowest id.
	id, _, ok, err = m.HighestLiveNodeExcluding(2)
	requireNoError(t, err)
	if !ok || id != 5 {
		t.Fatalf("HighestLiveNodeExcluding(exclude=2) = id=%d ok=%v, want id=5", id, ok)
	}
	// Excluding the only candidates → not found.
	requireNoError(t, m.DeleteNode(5))
	requireNoError(t, m.DeleteNode(9))
	if _, _, ok, _ := m.HighestLiveNodeExcluding(2); ok {
		t.Fatal("HighestLiveNodeExcluding should report not-found when no live node remains")
	}
}

// TestSegGraphStore_HighestLiveBranches drives segGraphStore.
// HighestLiveNodeExcluding directly across its branches: a deleted node
// (nodeSlot < 0 → skipped), a tie-break by lower id, an excluded id, and the
// not-found return.
func TestSegGraphStore_HighestLiveBranches(t *testing.T) {
	ss := sealedFromRandom(t, 8, 4)
	gs := newSegGraphStore(ss)
	// Three live nodes at equal level 0 plus one we delete.
	gs.bindSlot(4000, 0)
	requireNoError(t, gs.PutNode(0, 0, ss.getVectorRef(0), 4000))
	gs.bindSlot(4001, 1)
	requireNoError(t, gs.PutNode(1, 0, ss.getVectorRef(1), 4001))
	gs.bindSlot(4002, 2)
	requireNoError(t, gs.PutNode(2, 0, ss.getVectorRef(2), 4002))
	gs.bindSlot(4003, 3)
	requireNoError(t, gs.PutNode(3, 0, ss.getVectorRef(3), 4003))
	requireNoError(t, gs.DeleteNode(3)) // node 3 → nodeSlot[3] = -1 (skip branch)

	// Lowest id wins on a tie; the deleted node 3 is skipped.
	id, lvl, ok, err := gs.HighestLiveNodeExcluding(math.MaxUint64)
	requireNoError(t, err)
	if !ok || id != 0 || lvl != 0 {
		t.Fatalf("HighestLiveNodeExcluding = id=%d lvl=%d ok=%v, want id=0 lvl=0", id, lvl, ok)
	}
	// Excluding 0 promotes 1.
	id, _, ok, err = gs.HighestLiveNodeExcluding(0)
	requireNoError(t, err)
	if !ok || id != 1 {
		t.Fatalf("HighestLiveNodeExcluding(exclude=0) = id=%d ok=%v, want id=1", id, ok)
	}
	// Delete the rest → not found.
	requireNoError(t, gs.DeleteNode(0))
	requireNoError(t, gs.DeleteNode(1))
	requireNoError(t, gs.DeleteNode(2))
	if _, _, ok, _ := gs.HighestLiveNodeExcluding(math.MaxUint64); ok {
		t.Fatal("HighestLiveNodeExcluding should be not-found when all nodes deleted")
	}
}

// TestRandomLevel_ClampAndZero drives randomLevel's defaultMaxLayers clamp (a
// tiny uniform draw yields a huge raw level) and confirms levels stay in range.
// A small M widens mL so the clamp is reachable within a bounded sample budget.
func TestRandomLevel_ClampAndZero(t *testing.T) {
	gs := newMemGraphStore(Cosine)
	idx := newHNSWIndex(gs, withGraphM(2), withGraphRand(rand.New(rand.NewSource(1))))
	clamped := false
	for i := 0; i < 200000; i++ {
		lvl := idx.randomLevel()
		if lvl < 0 || lvl > defaultMaxLayers {
			t.Fatalf("randomLevel out of range: %d", lvl)
		}
		if lvl == defaultMaxLayers {
			clamped = true
		}
	}
	if !clamped {
		t.Fatal("randomLevel never hit the defaultMaxLayers clamp in 200k draws")
	}
}

// TestVisitedSet_BeginEpochOverflow drives visitedSet.begin's epoch-overflow
// reset branch (epoch == MaxUint32 → zero the backing array, wrap to 0, then ++).
func TestVisitedSet_BeginEpochOverflow(t *testing.T) {
	v := &visitedSet{}
	v.mark(3) // grow the backing array and stamp a mark in the current epoch
	v.epoch = math.MaxUint32
	v.versions[3] = math.MaxUint32 // a stale stamp at the about-to-wrap epoch
	v.begin()                      // overflow → zero versions, epoch wraps to 1
	if v.epoch != 1 {
		t.Fatalf("epoch after overflow begin = %d, want 1", v.epoch)
	}
	if v.seen(3) {
		t.Fatal("stale mark must not survive the epoch-overflow reset")
	}
}

// panicStore is a graphNodeStore whose PutNode panics, to drive
// runInTxnLocked's panic-recover-then-re-panic branch (which aborts the txn to
// clear the in-txn flag before re-raising).
type panicStore struct {
	*memGraphStore
}

func (p *panicStore) PutNode(id uint64, level int, vector []float32, docId int64) error {
	panic("boom in apply")
}

func TestRunInTxnLocked_PanicRecovers(t *testing.T) {
	ps := &panicStore{memGraphStore: newMemGraphStore(Cosine)}
	idx := newHNSWIndex(ps, withGraphM(16))
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("insert with a panicking PutNode must re-panic after aborting the txn")
		}
	}()
	_ = idx.insert(1, []float32{1, 0, 0, 0}) // panics; runInTxnLocked aborts + re-panics
	t.Fatal("unreachable: insert should have panicked")
}

// TestWriteSealedSegment_LaterFileWriteFault fails the Write on the SECOND
// fsCreate'd file (slotdoc.dat), so writeSealedSegment's error propagation from
// a non-first per-file writer is covered.
func TestWriteSealedSegment_LaterFileWriteFault(t *testing.T) {
	head := sampleHead(t)
	orig := fsCreate
	t.Cleanup(func() { fsCreate = orig })
	calls := 0
	fsCreate = func(name string) (osFile, error) {
		f, err := orig(name)
		if err != nil {
			return nil, err
		}
		calls++
		ff := &faultFile{osFile: f}
		if calls == 2 { // slotdoc.dat
			ff.failWrite = true
		}
		return ff, nil
	}
	dir := filepath.Join(t.TempDir(), "seg-1-0")
	if err := writeSealedSegment(dir, head, nil); err == nil {
		t.Fatal("writeSealedSegment should fail when a later per-file Write fails")
	}
}

// TestOpenSealedSegment_PayloadOpenFault fails the SECOND fsOpenFile call
// (payload.dat), covering openSealedSegment's payload open-error branch. Since
// incr 3 a sealed segment has no tomb.dat: vectors.dat opens via fsOpenFile #1,
// slotdoc.dat via fsOpen, payload.dat via fsOpenFile #2 (the tombstone bitmap is
// seeded from the bbolt tomb bucket, not a mmap'd file).
func TestOpenSealedSegment_PayloadOpenFault(t *testing.T) {
	head := sampleHead(t)
	dir := filepath.Join(t.TempDir(), "seg-1-0")
	requireNoError(t, writeSealedSegment(dir, head, nil))

	orig := fsOpenFile
	t.Cleanup(func() { fsOpenFile = orig })
	calls := 0
	fsOpenFile = func(name string, flag int, perm os.FileMode) (osFile, error) {
		calls++
		if calls == 2 { // payload.dat is the 2nd fsOpenFile (vectors is the 1st)
			return nil, errInjected
		}
		return orig(name, flag, perm)
	}
	if _, err := openSealedSegment(dir, Cosine, 1, nil); err == nil {
		t.Fatal("openSealedSegment should fail when payload.dat open fails")
	}
}

// TestMmapSyncPlatform_EmptyNoop covers mmapSyncPlatform's len==0 early return.
// (The non-empty happy path is covered by TestMmap_RoundTrip's mmapSync over a
// real read-write mmap; since incr 3 a sealed segment carries no RW mmap.)
func TestMmapSyncPlatform_EmptyNoop(t *testing.T) {
	if err := mmapSyncPlatform(nil); err != nil {
		t.Fatalf("mmapSyncPlatform(nil) = %v, want nil", err)
	}
}

// TestWriteSealedSegment_MkdirFault drives writeSealedSegment's os.MkdirAll
// error branch by targeting a path whose parent is a regular file.
func TestWriteSealedSegment_MkdirFault(t *testing.T) {
	base := filepath.Join(t.TempDir(), "afile")
	requireNoError(t, os.WriteFile(base, []byte("x"), 0644))
	// base is a file, so MkdirAll(base/seg-1-0) cannot create the parent.
	if err := writeSealedSegment(filepath.Join(base, "seg-1-0"), sampleHead(t), nil); err == nil {
		t.Fatal("writeSealedSegment should fail when MkdirAll cannot create the dir")
	}
}

// TestWriteSealedSegment_PayloadWriteFault fails the Write on the THIRD fsCreate'd
// file (payload.dat), covering writeSealedSegment's payload writer error
// propagation. Since incr 3 there is no tomb.dat: the files are vectors.dat (#1),
// slotdoc.dat (#2), payload.dat (#3). (The slotdoc writer branch is covered by
// TestWriteSealedSegment_LaterFileWriteFault.)
func TestWriteSealedSegment_PayloadWriteFault(t *testing.T) {
	head := sampleHead(t)
	orig := fsCreate
	t.Cleanup(func() { fsCreate = orig })
	calls := 0
	fsCreate = func(name string) (osFile, error) {
		f, err := orig(name)
		if err != nil {
			return nil, err
		}
		calls++
		ff := &faultFile{osFile: f}
		if calls == 3 { // payload.dat
			ff.failWrite = true
		}
		return ff, nil
	}
	dir := filepath.Join(t.TempDir(), "seg-1-0")
	if err := writeSealedSegment(dir, head, nil); err == nil {
		t.Fatal("writeSealedSegment should fail when the payload.dat Write fails")
	}
}

// faultMemSetNeighbors wraps memGraphStore and fails SetNeighbors on demand, to
// drive deleteNodeLocked's SetNeighbors error-return branch.
type faultMemSetNeighbors struct {
	*memGraphStore
	fail bool
}

func (f *faultMemSetNeighbors) SetNeighbors(id uint64, layer int, neighbors []uint64) error {
	if f.fail {
		return errInjected
	}
	return f.memGraphStore.SetNeighbors(id, layer, neighbors)
}

// TestDeleteNodeLocked_SetNeighborsFault builds a connected graph, then deletes
// an interior node with the store rigged to fail SetNeighbors — the rewrite of a
// neighbor's list — so deleteNodeLocked's error-return on SetNeighbors fires.
func TestDeleteNodeLocked_SetNeighborsFault(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	dim := 8
	fs := &faultMemSetNeighbors{memGraphStore: newMemGraphStore(Cosine)}
	idx := newHNSWIndex(fs, withGraphM(16), withGraphEfConstruction(100),
		withGraphRand(rand.New(rand.NewSource(4))))
	b := idx.newBatch()
	for i := 0; i < 60; i++ {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		b.put(int64(i), v)
	}
	requireNoError(t, b.commit())

	// Now fail SetNeighbors and delete an interior (non-EP) node: deleteNodeLocked
	// must surface the SetNeighbors error.
	fs.fail = true
	if err := idx.delete(10); err == nil {
		t.Fatal("delete should surface a SetNeighbors fault from deleteNodeLocked")
	}
}
