package vectorstore

import (
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// makeRecoveredStore seals one indexed segment + leaves a few head docs, then
// reopens. It returns the recovered store and its kv so further reopens can share
// the same idtable. The sealed docs' string ids are absent from the reopened
// idToDoc (the head bucket was cleared at seal), so Get/Delete of them must resolve
// via the durable idtable (lookupDocID's KV fallback).
func TestRecovery_DeleteSealedDocAfterReopen(t *testing.T) {
	kvStore := newTestKV(t)
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	rng := rand.New(rand.NewSource(101))
	dim := 12
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	for i := 0; i < 60; i++ {
		requireNoError(t, s.Put("s-"+itoa(i), randVec(), Payload{"p": StringValue("p")}))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex()) // segment becomes indexed → recover reopens graph.dat
	for i := 0; i < 5; i++ {
		requireNoError(t, s.Put("h-"+itoa(i), randVec(), nil))
	}

	s2 := reopenStore(t, s, kvStore)

	// The sealed doc s-3 is recovered (in docToSeg) but its STRING id is not in the
	// reopened idToDoc — Get must resolve it through the idtable KV fallback.
	if _, _, found, err := s2.Get("s-3"); err != nil || !found {
		t.Fatalf("Get(s-3) after reopen: found=%v err=%v (KV-fallback lookup failed)", found, err)
	}
	// Delete a sealed doc post-reopen: exercises lookupDocID KV-fallback + the
	// Delete sealed-segment branch (persisted tombstone in the mmap'd bitmap).
	requireNoError(t, s2.Delete("s-3"))
	if _, _, found, _ := s2.Get("s-3"); found {
		t.Fatal("sealed doc s-3 still found after post-reopen Delete")
	}
	// An unknown id resolves to not-found WITHOUT allocating (KV miss path).
	if _, _, found, err := s2.Get("never-put"); err != nil || found {
		t.Fatalf("Get(never-put) = found=%v err=%v, want false/nil", found, err)
	}
	if err := s2.Delete("never-put"); err != nil {
		t.Fatalf("Delete(never-put) = %v, want nil no-op", err)
	}

	// Survives a second reopen: the sealed delete is durable in tomb.dat.
	s3 := reopenStore(t, s2, kvStore)
	if _, _, found, _ := s3.Get("s-3"); found {
		t.Fatal("sealed delete of s-3 did not survive a second reopen")
	}
}

// TestRecovery_ResumesPendingBuild crafts an on-disk PENDING segment (durable
// records + a manifest marking it pending, NO graph.dat) and reopens. recover()
// must spawn a background builder for it; WaitForIndex blocks until it indexes;
// Search must return results throughout (pending brute leg, then indexed graph).
func TestRecovery_ResumesPendingBuild(t *testing.T) {
	kvStore := newTestKV(t)
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	rng := rand.New(rand.NewSource(102))
	dim := 12
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	for i := 0; i < 40; i++ {
		requireNoError(t, s.Put("p-"+itoa(i), randVec(), nil))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	requireNoError(t, s.Close())

	// Simulate a crash mid-build: drop graph-default.dat and rewrite the control
	// store marking the default index's (default, seg) state pending (the state
	// recover() must resume from — build state lives per-(index,segment) in the
	// indexsegs bucket). s is closed, so we open the control DB directly.
	segDir := filepath.Join(dir, segDirName(segID(1), 0))
	requireNoError(t, os.Remove(filepath.Join(segDir, "graph-default.dat")))
	cs, err := openControlStore(dir)
	requireNoError(t, err)
	requireNoError(t, cs.update(func(tx *bolt.Tx) error {
		entries, lerr := listIndexSegs(tx)
		if lerr != nil {
			return lerr
		}
		for _, is := range entries {
			if is.Index == defaultIndexName {
				is.State = segPending
				if perr := putIndexSeg(tx, is); perr != nil {
					return perr
				}
			}
		}
		ver, head, met, _, gerr := getMeta(tx)
		if gerr != nil {
			return gerr
		}
		return putMeta(tx, ver+1, head, met)
	}))
	requireNoError(t, cs.Close())

	// Reopen: recover() loads the pending segment and resumes its build.
	s2, err := Open(Options{Dir: dir, KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })

	// Searchable immediately (pending brute leg).
	q := randVec()
	got, err := s2.Search("default", q, 5, nil)
	requireNoError(t, err)
	if len(got) == 0 {
		t.Fatal("search returned nothing on a resumed pending segment")
	}
	// After the resumed build finishes, the segment is indexed and the default
	// index's graph file exists.
	requireNoError(t, s2.WaitForIndex())
	if !s2.isIndexedForTest(1) {
		t.Fatal("resumed segment 1 not indexed after WaitForIndex")
	}
	if _, err := os.Stat(filepath.Join(segDir, "graph-default.dat")); err != nil {
		t.Fatalf("graph-default.dat not rebuilt by resumed build: %v", err)
	}
}

// TestRecovery_CrossSegmentUpdateTombstoneNotFlushed covers rebuildHead's
// cross-segment durability branch (appendix #7). A Put that overwrites a doc living
// in a SEALED segment commits the durable head row, THEN tombstones the sealed slot
// via an mmap msync that is less crash-atomic than the bbolt commit. We hand-build
// the crash state where the head row IS durable but the sealed slot is STILL LIVE
// (the msync lost): a sealed segment with doc X live + a committed control snapshot +
// a head-bucket row for X carrying a NEW vector. On reopen the durable head row is
// authoritative, so the rebuild must tombstone the stale sealed slot and re-home X
// to the head — X must read back as its NEW value and appear in Search exactly once.
func TestRecovery_CrossSegmentUpdateTombstoneNotFlushed(t *testing.T) {
	kvStore := newTestKV(t)
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	rng := rand.New(rand.NewSource(7777))
	dim := 8
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	// Seal a batch so doc "x" lives in a sealed segment (slot 0).
	const n = 6
	requireNoError(t, s.Put("x", randVec(), Payload{"v": StringValue("old")}))
	for i := 0; i < n; i++ {
		requireNoError(t, s.Put("f-"+itoa(i), randVec(), nil))
	}
	requireNoError(t, s.Seal())
	requireNoError(t, s.WaitForIndex())
	xDoc := s.idToDoc["x"]

	// Hand-build the crash window: commit a durable head row for "x" with a NEW
	// vector WITHOUT tombstoning the sealed slot (simulating the lost msync). The
	// sealed segment on disk still has "x" live. Commit through s's open control DB.
	newVec, newNorm := Cosine.prepare([]float32{1, 0, 0, 0, 0, 0, 0, 0})
	plNew, err := encodePayload(Payload{"v": StringValue("new")})
	requireNoError(t, err)
	s.mu.Lock()
	requireNoError(t, s.cs.update(func(tx *bolt.Tx) error {
		return putHeadRecord(tx, headRecord{ID: "x", DocID: xDoc, Stored: newVec, Norm: newNorm, Payload: plNew})
	}))
	s.mu.Unlock()

	// Crash-reopen: the sealed slot for "x" is still live on disk, but the durable
	// head row owns "x" — rebuildHead must tombstone the stale sealed slot.
	s2 := reopenUnclean(t, s, kvStore)
	requireNoError(t, s2.WaitForIndex())

	// "x" reads back as the NEW value (from the head, not the stale sealed slot).
	v, pl, found, err := s2.Get("x")
	requireNoError(t, err)
	if !found || pl["v"].Str != "new" || !approxEqual(v[0], 1, 1e-3) {
		t.Fatalf("Get(x) = %v,%#v,%v, want new value in head", v, pl, found)
	}
	// "x" appears exactly once in Search (not double-stored across head + sealed).
	got, err := s2.Search("default", []float32{1, 0, 0, 0, 0, 0, 0, 0}, n+2, nil)
	requireNoError(t, err)
	seen := 0
	for _, h := range got {
		if h.DocID == xDoc {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("docId x appears %d times in Search results, want exactly 1 (no double-store)", seen)
	}
}

// TestRecovery_SealHeadClearIsAtomicNoDoubleStore proves the bbolt seal commit is
// atomic: the head rows leave the head bucket in the SAME write-txn that adds the
// new sealed segment, so a crash-reopen can never find a doc in BOTH the sealed
// segment and the rebuilt head. (Under the former WAL design this was the appendix
// #8/#19 crash window — manifest committed but the WAL not yet truncated, so it
// still carried every pre-seal Put — and recover() needed an explicit
// skip-already-sealed guard. With the head in bbolt that window is structurally
// impossible: there is no separate truncate step to crash between.) After a real
// Seal + unclean reopen, the head must be empty, the sealed segment present, every
// doc readable exactly once, and Search must return no duplicate docIds.
func TestRecovery_SealHeadClearIsAtomicNoDoubleStore(t *testing.T) {
	kvStore := newTestKV(t)
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	rng := rand.New(rand.NewSource(103))
	dim := 10
	randVec := func() []float32 {
		v := make([]float32, dim)
		for d := range v {
			v[d] = rng.Float32()
		}
		return v
	}
	const n = 40
	for i := 0; i < n; i++ {
		requireNoError(t, s.Put("c-"+itoa(i), randVec(), nil))
	}

	// Real Seal: dumps the head to a sealed segment AND clears the head bucket in
	// one atomic control-store commit. alloc.Commit (inside Seal) makes the
	// string→docId mappings durable in the KV before the head bucket is cleared.
	requireNoError(t, s.Seal())

	// Crash-reopen WITHOUT Close (no quiesce, no extra idtable commit). The durable
	// control store (segment present, head bucket empty) is exactly the post-seal
	// state.
	s2 := reopenUnclean(t, s, kvStore)
	requireNoError(t, s2.WaitForIndex())

	// Head must be empty — the seal commit cleared it atomically with the segment add.
	s2.mu.RLock()
	headLive := 0
	s2.seg.eachLive(func(int, int64, []float32, float32) { headLive++ })
	nSealed := len(s2.sealed)
	s2.mu.RUnlock()
	if headLive != 0 {
		t.Fatalf("head live = %d after seal+crash recovery, want 0 (head not cleared atomically = double-store)", headLive)
	}
	if nSealed != 1 {
		t.Fatalf("sealed segments = %d, want 1", nSealed)
	}

	// Every doc is present and Search returns no duplicate docIds.
	for i := 0; i < n; i++ {
		if _, _, found, _ := s2.Get("c-" + itoa(i)); !found {
			t.Fatalf("doc c-%d lost after seal+crash recovery", i)
		}
	}
	got, err := s2.Search("default", randVec(), n, nil)
	requireNoError(t, err)
	seen := make(map[int64]bool, len(got))
	for _, h := range got {
		if seen[h.DocID] {
			t.Fatalf("duplicate docId %d in Search results (doc lives in two segments)", h.DocID)
		}
		seen[h.DocID] = true
	}
}

// TestLookupDocID_KVErrorPropagates makes the KV fail on a cache-miss read, so
// lookupDocID's direct idtable read errors. Get and Delete must surface that
// error rather than silently treating the id as not-found.
func TestLookupDocID_KVErrorPropagates(t *testing.T) {
	fkv := &faultKV{Store: newTestKV(t)}
	s, err := Open(Options{Dir: t.TempDir(), KV: fkv, Metric: Cosine})
	requireNoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	// Arm the fault AFTER open (idtable.New already read its counter).
	fkv.getErr = errors.New("kv get boom")

	// "ghost" was never Put → idToDoc cache miss → lookupDocID reads the KV → errors.
	if _, _, _, err := s.Get("ghost"); err == nil {
		t.Fatal("Get should propagate the KV lookup error")
	}
	if err := s.Delete("ghost"); err == nil {
		t.Fatal("Delete should propagate the KV lookup error")
	}
}
