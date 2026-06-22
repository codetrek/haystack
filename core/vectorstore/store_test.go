package vectorstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStore_OpenClose(t *testing.T) {
	s := openTestStore(t, Cosine)
	if s.Metric() != Cosine {
		t.Fatalf("metric = %v, want cosine", s.Metric())
	}
}

func TestStore_OpenRequiresDir(t *testing.T) {
	if _, err := Open(Options{Metric: Cosine}); err == nil {
		t.Fatal("Open without Dir should error")
	}
}

func TestStore_PutThenGet(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.Put("a", []float32{3, 4}, Payload{"p": StringValue("payA")}))
	v, pl, found, err := s.Get("a")
	requireNoError(t, err)
	if !found {
		t.Fatal("Get(a) not found after Put")
	}
	if !approxEqual(v[0], 3, 1e-4) || !approxEqual(v[1], 4, 1e-4) {
		t.Fatalf("restored vector = %v, want [3 4]", v)
	}
	if pl["p"].Str != "payA" {
		t.Fatalf("payload = %#v, want {p:payA}", pl)
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
	requireNoError(t, s.Put("a", []float32{0, 9}, Payload{"p": StringValue("v2")}))
	v, pl, found, err := s.Get("a")
	requireNoError(t, err)
	if !found || v[0] != 0 || v[1] != 9 || pl["p"].Str != "v2" {
		t.Fatalf("after upsert Get = %v,%#v,%v, want [0 9],{p:v2},true", v, pl, found)
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
	requireNoError(t, s.Put("a", []float32{1, 2}, Payload{"p": StringValue("x")}))
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
	rb := s.NewBatch()
	for id, v := range raw {
		rb.Put(id, v, nil)
	}
	requireNoError(t, rb.Commit())
	q := []float32{1, 0, 0, 0}
	res, err := s.Search("default", q, 2, nil)
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
	rb := s.NewBatch()
	for id, v := range raw {
		rb.Put(id, v, nil)
	}
	requireNoError(t, rb.Commit())
	q := []float32{0, 0}
	res, err := s.Search("default", q, 3, nil)
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
	res, err := s.Search("default", []float32{1, 0}, 5, nil)
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
	res, err := s.Search("default", []float32{1, 0}, 3, nil)
	requireNoError(t, err)
	if res != nil {
		t.Fatalf("search on empty store = %v, want nil", res)
	}
}

func TestStore_Search_RejectsBadVectorAndK(t *testing.T) {
	s := openTestStore(t, Cosine)
	requireNoError(t, s.Put("a", []float32{1, 0}, nil))
	if _, err := s.Search("default", []float32{}, 3, nil); err == nil {
		t.Fatal("empty query should be rejected")
	}
	if _, err := s.Search("default", []float32{1, 0}, 0, nil); err == nil {
		t.Fatal("k<=0 should be rejected")
	}
}

func TestStore_Reopen_AfterClose(t *testing.T) {
	dir := t.TempDir()

	s1, err := Open(Options{Dir: dir, Metric: Cosine})
	requireNoError(t, err)
	requireNoError(t, s1.Put("a", []float32{1, 0, 0}, Payload{"p": StringValue("pa")}))
	requireNoError(t, s1.Put("b", []float32{0, 1, 0}, Payload{"p": StringValue("pb")}))
	requireNoError(t, s1.Put("a", []float32{0, 0, 1}, Payload{"p": StringValue("pa2")})) // upsert
	requireNoError(t, s1.Delete("b"))
	requireNoError(t, s1.Close()) // graceful: commits idtable + closes control store

	s2, err := Open(Options{Dir: dir, Metric: Cosine})
	requireNoError(t, err)
	defer s2.Close()

	v, pl, found, err := s2.Get("a")
	requireNoError(t, err)
	if !found || pl["p"].Str != "pa2" || !approxEqual(v[2], 1, 1e-4) {
		t.Fatalf("after reopen Get(a) = %v,%#v,%v, want [0 0 1],{p:pa2},true", v, pl, found)
	}
	if _, _, found, _ := s2.Get("b"); found {
		t.Fatal("deleted id b must stay deleted after reopen")
	}
	res, err := s2.Search("default", []float32{0, 0, 1}, 5, nil)
	requireNoError(t, err)
	if len(res) != 1 {
		t.Fatalf("live results after reopen = %d, want 1", len(res))
	}
}

func TestStore_CrashRecovery_NoClose_HeadBucketIsSourceOfTruth(t *testing.T) {
	dir := t.TempDir()

	s1, err := Open(Options{Dir: dir, Metric: Cosine})
	requireNoError(t, err)
	requireNoError(t, s1.Put("a", []float32{1, 0, 0}, Payload{"p": StringValue("pa")}))
	requireNoError(t, s1.Put("b", []float32{0, 1, 0}, Payload{"p": StringValue("pb")}))
	requireNoError(t, s1.Put("a", []float32{0, 0, 1}, Payload{"p": StringValue("pa2")})) // upsert
	requireNoError(t, s1.Delete("b"))
	docA := s1.idToDoc["a"]

	// CRASH: do NOT call s1.Close(). idtable's id→docId batch and nextId were
	// never committed to KV. Drop only the OS-held file handle a real kill would
	// release (the bbolt control-DB lock); the allocator is deliberately left
	// uncommitted to mimic a kill.
	crashRelease(t, s1)

	// Reopen over the SAME dir + SAME KV. Recovery must come entirely from the
	// durable head bucket: the segment, the id→docId map, and a consistent
	// allocator nextId.
	s2, err := Open(Options{Dir: dir, Metric: Cosine})
	requireNoError(t, err)
	defer s2.Close()

	// The string id "a" must still resolve to the SAME docId and the upserted
	// vector/payload — proving the head bucket, not idtable, carried the mapping.
	if got := s2.idToDoc["a"]; got != docA {
		t.Fatalf("recovered docId for a = %d, want %d (head bucket must carry the mapping)", got, docA)
	}
	v, pl, found, err := s2.Get("a")
	requireNoError(t, err)
	if !found || pl["p"].Str != "pa2" || !approxEqual(v[2], 1, 1e-4) {
		t.Fatalf("crash-recovered Get(a) = %v,%#v,%v, want [0 0 1],{p:pa2},true", v, pl, found)
	}
	if _, _, found, _ := s2.Get("b"); found {
		t.Fatal("deleted id b must stay deleted after crash recovery")
	}
	res, err := s2.Search("default", []float32{0, 0, 1}, 5, nil)
	requireNoError(t, err)
	if len(res) != 1 {
		t.Fatalf("live results after crash recovery = %d, want 1", len(res))
	}

	// A fresh Put after recovery must get a NEW, non-colliding docId (nextId was
	// resynced by the head rebuild re-driving the allocator).
	requireNoError(t, s2.Put("c", []float32{1, 0, 0}, nil))
	if s2.idToDoc["c"] == docA {
		t.Fatalf("new id c collided with recovered docId %d — nextId not resynced", docA)
	}
}

func TestStore_CloseSurfacesControlStoreError(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, Metric: Cosine})
	requireNoError(t, err)
	// Force cs.Close to surface an error so Store.Close propagates it (the
	// control store is now the only Close that returns an error).
	s.cs.failClose = errInjected
	if err := s.Close(); err == nil {
		t.Fatal("Close should surface the control-store close error")
	}
}

func TestStore_PutControlStoreCommitError(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, Metric: Cosine})
	requireNoError(t, err)
	defer s.Close()
	// Force the next control-store commit (the Put's durable head-row write) to fail.
	s.cs.failNextUpdate = errInjected
	if err := s.Put("a", []float32{1, 0}, nil); err == nil {
		t.Fatal("Put should surface a control-store commit failure")
	}
}

func TestStore_DeleteControlStoreCommitError(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, Metric: Cosine})
	requireNoError(t, err)
	defer s.Close()
	requireNoError(t, s.Put("a", []float32{1, 0}, nil))
	s.cs.failNextUpdate = errInjected
	if err := s.Delete("a"); err == nil {
		t.Fatal("Delete should surface a control-store commit failure")
	}
}

// TestErroredPutNotResurrectedOnReopen is the crash-atomicity durability property
// (formerly proved against the WAL): a Put whose durable head-row commit FAILS
// returns an error and must NOT be applied — its record must not exist on a clean
// reopen. With the head in bbolt the commit is one db.Update, so a failed commit
// rolls back fully (copy-on-write page swap is never applied) and nothing is
// resurrected. A second, surviving Put proves the failed Put caused no cascade loss.
func TestErroredPutNotResurrectedOnReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, Metric: DotProduct})
	requireNoError(t, err)
	// Force the durable commit for "x" to fail.
	s.cs.failNextUpdate = errInjected
	if err := s.Put("x", []float32{1, 2, 3, 4}, nil); err == nil {
		t.Fatal("expected Put to fail when the control-store commit fails")
	}
	// A later Put must still succeed (no cascade) and be durable.
	requireNoError(t, s.Put("y", []float32{5, 6, 7, 8}, nil))
	requireNoError(t, s.Close())

	s2, err := Open(Options{Dir: dir, Metric: DotProduct})
	requireNoError(t, err)
	defer s2.Close()
	if _, _, found, _ := s2.Get("x"); found {
		t.Fatal("errored Put was resurrected on reopen (crash-atomicity violation)")
	}
	if _, _, found, _ := s2.Get("y"); !found {
		t.Fatal("record 'y' (written after the errored Put) was lost")
	}
}

func TestStore_OpenIdtableError(t *testing.T) {
	dir := t.TempDir()
	// Occupy the idtable's path with a directory so idtable.Open (bbolt) fails,
	// which must fail Store.Open.
	requireNoError(t, os.MkdirAll(filepath.Join(dir, "idtable.db"), 0o755))
	if _, err := Open(Options{Dir: dir, Metric: Cosine}); err == nil {
		t.Fatal("Open should fail when the idtable cannot be opened")
	}
}

func TestStore_PutAllocError(t *testing.T) {
	s, err := Open(Options{Dir: t.TempDir(), Metric: Cosine})
	requireNoError(t, err)
	defer s.Close()
	// Close the idtable out from under the store so GetId fails, making
	// docIDForAlloc error on the Put path.
	s.alloc.Close()
	if err := s.Put("a", []float32{1, 0}, nil); err == nil {
		t.Fatal("Put should surface a docId-allocation failure")
	}
}
