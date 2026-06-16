package vectorstore

import (
	"errors"
	"testing"
)

func TestStore_OpenClose(t *testing.T) {
	s := openTestStore(t, Cosine)
	if s.Metric() != Cosine {
		t.Fatalf("metric = %v, want cosine", s.Metric())
	}
}

func TestStore_OpenRequiresKV(t *testing.T) {
	if _, err := Open(Options{Dir: t.TempDir(), Metric: Cosine}); err == nil {
		t.Fatal("Open without KV should error")
	}
}

func TestStore_OpenRequiresDir(t *testing.T) {
	if _, err := Open(Options{KV: newTestKV(t), Metric: Cosine}); err == nil {
		t.Fatal("Open without Dir should error")
	}
}

func TestStore_PutThenGet(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.Put("a", []float32{3, 4}, []byte("payA")))
	v, pl, found, err := s.Get("a")
	requireNoError(t, err)
	if !found {
		t.Fatal("Get(a) not found after Put")
	}
	if !approxEqual(v[0], 3, 1e-4) || !approxEqual(v[1], 4, 1e-4) {
		t.Fatalf("restored vector = %v, want [3 4]", v)
	}
	if string(pl) != "payA" {
		t.Fatalf("payload = %q, want payA", pl)
	}
}

func TestStore_GetUnknownDoesNotAllocate(t *testing.T) {
	s := openTestStore(t, Cosine)
	_, _, found, err := s.Get("never")
	requireNoError(t, err)
	if found {
		t.Fatal("Get of never-put id should be not-found")
	}
	// A subsequent Put must get docId 1 (Get did not burn an id).
	requireNoError(t, s.Put("a", []float32{1, 0}, nil))
	if got := s.idToDoc["a"]; got != 1 {
		t.Fatalf("first Put docId = %d, want 1 (Get must not allocate)", got)
	}
}

func TestStore_GetReturnsCopy_NonCosine(t *testing.T) {
	s := openTestStore(t, DotProduct)
	requireNoError(t, s.Put("a", []float32{1, 2, 3}, nil))
	v, _, _, err := s.Get("a")
	requireNoError(t, err)
	v[0] = 999 // mutating the returned slice must NOT corrupt the segment
	v2, _, _, err := s.Get("a")
	requireNoError(t, err)
	if v2[0] != 1 {
		t.Fatalf("Get must return a copy; segment was corrupted: %v", v2)
	}
}

func TestStore_PutUpsertReplaces(t *testing.T) {
	s := openTestStore(t, DotProduct)
	requireNoError(t, s.Put("a", []float32{1, 0}, nil))
	requireNoError(t, s.Put("a", []float32{0, 9}, []byte("v2")))
	v, pl, found, err := s.Get("a")
	requireNoError(t, err)
	if !found || v[0] != 0 || v[1] != 9 || string(pl) != "v2" {
		t.Fatalf("after upsert Get = %v,%q,%v, want [0 9],v2,true", v, pl, found)
	}
	live := 0
	s.seg.eachLive(func(int, int64, []float32, float32) { live++ })
	if live != 1 {
		t.Fatalf("live slots = %d, want 1 (old slot tombstoned)", live)
	}
}

func TestStore_PutRejectsBadVector(t *testing.T) {
	s := openTestStore(t, Cosine)
	if err := s.Put("a", []float32{}, nil); err == nil {
		t.Fatal("empty vector should be rejected")
	}
	requireNoError(t, s.Put("a", []float32{1, 2}, nil))
	if err := s.Put("b", []float32{1, 2, 3}, nil); err == nil {
		t.Fatal("dim mismatch should be rejected")
	}
}

func TestStore_Delete(t *testing.T) {
	s := openTestStore(t, DotProduct)
	requireNoError(t, s.Put("a", []float32{1, 2}, []byte("x")))
	requireNoError(t, s.Delete("a"))
	_, _, found, err := s.Get("a")
	requireNoError(t, err)
	if found {
		t.Fatal("Get(a) should be not-found after Delete")
	}
}

func TestStore_DeleteMissingIsPureNoOp(t *testing.T) {
	s := openTestStore(t, DotProduct)
	if err := s.Delete("never-put"); err != nil {
		t.Fatalf("Delete of missing id should be nil, got %v", err)
	}
	// Must not have allocated an id: first real Put gets docId 1.
	requireNoError(t, s.Put("a", []float32{1, 0}, nil))
	if s.idToDoc["a"] != 1 {
		t.Fatalf("Delete of unknown id must not allocate a docId")
	}
}

func TestStore_Search_MatchesOracle_Cosine(t *testing.T) {
	s := openTestStore(t, Cosine)
	raw := map[string][]float32{
		"a": {1, 0, 0, 0},
		"b": {0, 1, 0, 0},
		"c": {0.9, 0.1, 0, 0},
		"d": {0, 0, 1, 0},
	}
	for id, v := range raw {
		requireNoError(t, s.Put(id, v, nil))
	}
	q := []float32{1, 0, 0, 0}
	res, err := s.Search(q, 2)
	requireNoError(t, err)
	if len(res) != 2 {
		t.Fatalf("len(res) = %d, want 2", len(res))
	}
	vecs := map[int64][]float32{}
	for id, v := range raw {
		vecs[s.idToDoc[id]] = v
	}
	want := bruteForceKNN(Cosine, q, vecs, 2)
	if res[0].DocID != want[0] || res[1].DocID != want[1] {
		t.Fatalf("search docIds = [%d %d], want %v", res[0].DocID, res[1].DocID, want)
	}
	if res[0].Distance > res[1].Distance {
		t.Fatalf("results not ascending by distance: %+v", res)
	}
}

func TestStore_Search_MatchesOracle_Euclidean(t *testing.T) {
	s := openTestStore(t, Euclidean)
	raw := map[string][]float32{
		"a": {0, 0},
		"b": {3, 4},   // dist 5 from origin
		"c": {1, 1},   // dist ~1.41
		"d": {10, 10}, // far
	}
	for id, v := range raw {
		requireNoError(t, s.Put(id, v, nil))
	}
	q := []float32{0, 0}
	res, err := s.Search(q, 3)
	requireNoError(t, err)
	vecs := map[int64][]float32{}
	for id, v := range raw {
		vecs[s.idToDoc[id]] = v
	}
	want := bruteForceKNN(Euclidean, q, vecs, 3)
	for i := range want {
		if res[i].DocID != want[i] {
			t.Fatalf("euclidean search[%d] = %d, want %d (full=%v)", i, res[i].DocID, want[i], want)
		}
	}
}

func TestStore_Search_SkipsTombstoned(t *testing.T) {
	s := openTestStore(t, DotProduct)
	requireNoError(t, s.Put("a", []float32{1, 0}, nil))
	requireNoError(t, s.Put("b", []float32{0, 1}, nil))
	requireNoError(t, s.Delete("a"))
	res, err := s.Search([]float32{1, 0}, 5)
	requireNoError(t, err)
	da := s.idToDoc["a"]
	for _, r := range res {
		if r.DocID == da {
			t.Fatal("tombstoned docId must not appear in results")
		}
	}
}

func TestStore_Search_EmptyReturnsNil(t *testing.T) {
	s := openTestStore(t, Cosine)
	res, err := s.Search([]float32{1, 0}, 3)
	requireNoError(t, err)
	if res != nil {
		t.Fatalf("search on empty store = %v, want nil", res)
	}
}

func TestStore_Search_RejectsBadVectorAndK(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.Put("a", []float32{1, 0}, nil))
	if _, err := s.Search([]float32{}, 3); err == nil {
		t.Fatal("empty query should be rejected")
	}
	if _, err := s.Search([]float32{1, 0}, 0); err == nil {
		t.Fatal("k<=0 should be rejected")
	}
}

func TestStore_Reopen_AfterClose(t *testing.T) {
	dir := t.TempDir()
	store := newTestKV(t) // shared across both opens, closed at cleanup

	s1, err := Open(Options{Dir: dir, KV: store, Metric: Cosine})
	requireNoError(t, err)
	requireNoError(t, s1.Put("a", []float32{1, 0, 0}, []byte("pa")))
	requireNoError(t, s1.Put("b", []float32{0, 1, 0}, []byte("pb")))
	requireNoError(t, s1.Put("a", []float32{0, 0, 1}, []byte("pa2"))) // upsert
	requireNoError(t, s1.Delete("b"))
	requireNoError(t, s1.Close()) // graceful: commits idtable + flushes WAL

	s2, err := Open(Options{Dir: dir, KV: store, Metric: Cosine})
	requireNoError(t, err)
	defer s2.Close()

	v, pl, found, err := s2.Get("a")
	requireNoError(t, err)
	if !found || string(pl) != "pa2" || !approxEqual(v[2], 1, 1e-4) {
		t.Fatalf("after reopen Get(a) = %v,%q,%v, want [0 0 1],pa2,true", v, pl, found)
	}
	if _, _, found, _ := s2.Get("b"); found {
		t.Fatal("deleted id b must stay deleted after reopen")
	}
	res, err := s2.Search([]float32{0, 0, 1}, 5)
	requireNoError(t, err)
	if len(res) != 1 {
		t.Fatalf("live results after reopen = %d, want 1", len(res))
	}
}

func TestStore_CrashRecovery_NoClose_WALIsSourceOfTruth(t *testing.T) {
	dir := t.TempDir()
	store := newTestKV(t) // shared; NOT closed between opens

	s1, err := Open(Options{Dir: dir, KV: store, Metric: Cosine})
	requireNoError(t, err)
	requireNoError(t, s1.Put("a", []float32{1, 0, 0}, []byte("pa")))
	requireNoError(t, s1.Put("b", []float32{0, 1, 0}, []byte("pb")))
	requireNoError(t, s1.Put("a", []float32{0, 0, 1}, []byte("pa2"))) // upsert
	requireNoError(t, s1.Delete("b"))
	docA := s1.idToDoc["a"]

	// CRASH: do NOT call s1.Close(). idtable's id→docId batch and nextId were
	// never committed to KV. Close ONLY the WAL fd so the OS releases the file;
	// the allocator is deliberately left uncommitted to mimic a kill.
	requireNoError(t, s1.wal.Close())

	// Reopen over the SAME dir + SAME KV. Recovery must come entirely from the
	// WAL: the segment, the id→docId map, and a consistent allocator nextId.
	s2, err := Open(Options{Dir: dir, KV: store, Metric: Cosine})
	requireNoError(t, err)
	defer s2.Close()

	// The string id "a" must still resolve to the SAME docId and the upserted
	// vector/payload — proving the WAL, not idtable, carried the mapping.
	if got := s2.idToDoc["a"]; got != docA {
		t.Fatalf("recovered docId for a = %d, want %d (WAL must carry the mapping)", got, docA)
	}
	v, pl, found, err := s2.Get("a")
	requireNoError(t, err)
	if !found || string(pl) != "pa2" || !approxEqual(v[2], 1, 1e-4) {
		t.Fatalf("crash-recovered Get(a) = %v,%q,%v, want [0 0 1],pa2,true", v, pl, found)
	}
	if _, _, found, _ := s2.Get("b"); found {
		t.Fatal("deleted id b must stay deleted after crash recovery")
	}
	res, err := s2.Search([]float32{0, 0, 1}, 5)
	requireNoError(t, err)
	if len(res) != 1 {
		t.Fatalf("live results after crash recovery = %d, want 1", len(res))
	}

	// A fresh Put after recovery must get a NEW, non-colliding docId (nextId was
	// resynced by replay re-driving the allocator).
	requireNoError(t, s2.Put("c", []float32{1, 0, 0}, nil))
	if s2.idToDoc["c"] == docA {
		t.Fatalf("new id c collided with recovered docId %d — nextId not resynced", docA)
	}
}

func TestStore_CloseSurfacesWALError(t *testing.T) {
	dir := t.TempDir()
	store := newTestKV(t)
	withOpenFileFault(t, func(f *faultFile) { f.failClose = true })
	s, err := Open(Options{Dir: dir, KV: store, Metric: Cosine})
	requireNoError(t, err)
	if err := s.Close(); err == nil {
		t.Fatal("Close should surface the injected WAL close error")
	}
}

func TestStore_OpenWALScanError(t *testing.T) {
	dir := t.TempDir()
	store := newTestKV(t)
	withOpenFileFault(t, func(f *faultFile) { f.failTruncate = true }) // scanLSN truncates
	if _, err := Open(Options{Dir: dir, KV: store, Metric: Cosine}); err == nil {
		t.Fatal("Open should fail when WAL scan truncate fails")
	}
}

func TestStore_PutSyncError(t *testing.T) {
	dir := t.TempDir()
	store := newTestKV(t)
	s, err := Open(Options{Dir: dir, KV: store, Metric: Cosine})
	requireNoError(t, err)
	defer s.Close()
	// Replace the WAL's file with a Sync-faulting wrapper after open so the
	// Put's fsync fails.
	s.wal.file = &faultFile{osFile: s.wal.file, failSync: true}
	if err := s.Put("a", []float32{1, 0}, nil); err == nil {
		t.Fatal("Put should surface a WAL Sync failure")
	}
}

func TestStore_DeleteSyncError(t *testing.T) {
	dir := t.TempDir()
	store := newTestKV(t)
	s, err := Open(Options{Dir: dir, KV: store, Metric: Cosine})
	requireNoError(t, err)
	defer s.Close()
	requireNoError(t, s.Put("a", []float32{1, 0}, nil))
	s.wal.file = &faultFile{osFile: s.wal.file, failSync: true}
	if err := s.Delete("a"); err == nil {
		t.Fatal("Delete should surface a WAL Sync failure")
	}
}

func TestStore_OpenIdtableError(t *testing.T) {
	dir := t.TempDir()
	kvStore := &faultKV{Store: newTestKV(t), getErr: errors.New("kv get boom")}
	// idtable.New issues a startup Get for nextId; faulting it fails Open before
	// the WAL is even opened.
	if _, err := Open(Options{Dir: dir, KV: kvStore, Metric: Cosine}); err == nil {
		t.Fatal("Open should fail when the idtable startup read fails")
	}
}

func TestStore_PutAllocError(t *testing.T) {
	dir := t.TempDir()
	kvStore := &faultKV{Store: newTestKV(t)}
	s, err := Open(Options{Dir: dir, KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	defer s.Close()
	// Now make GetId fail (IsClosed) so docIDForAlloc errors on the Put path.
	kvStore.isClosed = true
	if err := s.Put("a", []float32{1, 0}, nil); err == nil {
		t.Fatal("Put should surface a docId-allocation failure")
	}
}

func TestStore_OpenReplayError(t *testing.T) {
	dir := t.TempDir()
	base := newTestKV(t)
	kvStore := &faultKV{Store: base}

	// Seed the WAL with one record so replay has work to do.
	s1, err := Open(Options{Dir: dir, KV: kvStore, Metric: Cosine})
	requireNoError(t, err)
	requireNoError(t, s1.Put("a", []float32{1, 0}, nil))
	requireNoError(t, s1.wal.Close()) // leave the record on disk; do not commit alloc

	// Reopen: idtable.New's startup Get and OpenWAL/scanLSN succeed, but replay
	// drives docIDForAlloc -> GetId, which fails because the KV reports closed.
	kvStore.isClosed = true
	if _, err := Open(Options{Dir: dir, KV: kvStore, Metric: Cosine}); err == nil {
		t.Fatal("Open should fail when replay's docId re-allocation fails")
	}
}
