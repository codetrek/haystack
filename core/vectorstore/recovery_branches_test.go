package vectorstore

import (
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// makeRecoveredStore seals one indexed segment + leaves a few head docs, then
// reopens. It returns the recovered store and its kv so further reopens can share
// the same idtable. The sealed docs' string ids are absent from the reopened
// idToDoc (the head WAL was truncated at seal), so Get/Delete of them must resolve
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

	// Simulate a crash mid-build: drop graph-default.dat and rewrite the manifest
	// marking segment 1 pending (the state recover() must resume from).
	segDir := filepath.Join(dir, segDirName(segID(1), 0))
	requireNoError(t, os.Remove(filepath.Join(segDir, "graph-default.dat")))
	m, err := readManifest(dir)
	requireNoError(t, err)
	for i := range m.Segments {
		m.Segments[i].State = segPending
	}
	m.Version++
	requireNoError(t, writeManifest(dir, m))

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

// TestRecovery_CrashAfterManifestSwapBeforeWALReset simulates the crash window
// the appendix #8/#19 guard defends: the sealed records + manifest are durable
// but the head WAL was NOT yet truncated, so it still carries every pre-seal Put.
// recover() must treat the manifest as authoritative — each doc must live in
// exactly ONE segment (the sealed one), the head must be empty, and Search must
// return no duplicate docIds. Re-homing the pre-seal records into the head (the
// un-guarded behavior) would double-store every doc and corrupt the sealed tomb
// bitmap.
func TestRecovery_CrashAfterManifestSwapBeforeWALReset(t *testing.T) {
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

	// Hand-build the post-manifest-swap, pre-WAL-reset crash state on disk: dump
	// the head to a sealed segment and write a manifest listing it as pending —
	// WITHOUT calling Seal (so the WAL is NOT reset and still holds all 40 Puts).
	segDir := filepath.Join(dir, segDirName(segID(1), 0))
	s.mu.Lock()
	requireNoError(t, writeSealedSegment(segDir, s.seg, nil))
	m := &manifest{
		Version: 1,
		Head:    headSegID,
		Segments: []segmentEntry{
			{SegID: 1, Gen: 0, VecCount: uint64(s.seg.dim), TombCount: 0, State: segPending},
		},
	}
	requireNoError(t, writeManifest(dir, m))
	s.mu.Unlock()
	// Abandon s WITHOUT Close (Close would flush/reset); leak its WAL fd — the OS
	// reclaims it. Reopen over the same dir+KV: the durable manifest + the
	// un-truncated WAL are exactly the crash state.
	s2, err := Open(Options{Dir: dir, KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	requireNoError(t, s2.WaitForIndex())

	// Head must be empty — no pre-seal record was re-homed into it.
	s2.mu.RLock()
	headLive := 0
	s2.seg.eachLive(func(int, int64, []float32, float32) { headLive++ })
	nSealed := len(s2.sealed)
	s2.mu.RUnlock()
	if headLive != 0 {
		t.Fatalf("head live = %d after crash-window recovery, want 0 (pre-seal records re-homed = double-store)", headLive)
	}
	if nSealed != 1 {
		t.Fatalf("sealed segments = %d, want 1", nSealed)
	}

	// Every doc is present and Search returns no duplicate docIds.
	for i := 0; i < n; i++ {
		if _, _, found, _ := s2.Get("c-" + itoa(i)); !found {
			t.Fatalf("doc c-%d lost after crash-window recovery", i)
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
