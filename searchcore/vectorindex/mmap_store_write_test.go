package vectorindex

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func openTestMmapStore(t *testing.T) *MmapStore {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	requireNoError(t, err)
	return s
}

func TestMmapStorePutNodeAndGetVector(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	vec := []float32{1.0, 2.0, 3.0, 4.0}
	requireNoError(t, s.PutNode(0, 0, vec, 42))

	got, err := s.GetVector(0)
	requireNoError(t, err)
	assert.Equal(t, vec, got)

	// Verify docId stored on the node slot.
	docId, ok, err := s.GetDocId(0)
	requireNoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int64(42), docId)
}

func TestMmapStorePutNodeAndGetNorm(t *testing.T) {
	// Only cosine persists a norm (to restore the original scale); the raw
	// metrics store the vector verbatim and skip the norm computation.
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: Cosine, Dim: 4, M: 4})
	requireNoError(t, err)
	defer s.Close()

	vec := []float32{3.0, 4.0, 0.0, 0.0} // norm = 5.0
	requireNoError(t, s.PutNode(0, 0, vec, 0))

	norm, err := s.GetNorm(0)
	requireNoError(t, err)
	assert.InDelta(t, float32(5.0), norm, 0.01)
}

func TestMmapStorePutNodeRawMetricNoNorm(t *testing.T) {
	// A raw metric (dot/euclidean) stores the original vector and reports norm 0,
	// since the norm is never needed to restore or compare it.
	s := openTestMmapStore(t) // DotProduct
	defer s.Close()

	requireNoError(t, s.PutNode(0, 0, []float32{3.0, 4.0, 0.0, 0.0}, 0))

	norm, err := s.GetNorm(0)
	requireNoError(t, err)
	assert.Equal(t, float32(0), norm)
}

func TestMmapStorePutNodeWithLevel(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	vec := []float32{1.0, 2.0, 3.0, 4.0}
	requireNoError(t, s.PutNode(0, 2, vec, 0))

	level, err := s.GetNodeLevel(0)
	requireNoError(t, err)
	assert.Equal(t, 2, level)
}

func TestMmapStoreSetNeighborsL0(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	vec := []float32{1.0, 2.0, 3.0, 4.0}
	requireNoError(t, s.PutNode(0, 0, vec, 0))
	requireNoError(t, s.PutNode(1, 0, vec, 1))
	requireNoError(t, s.PutNode(2, 0, vec, 2))

	nbs := []uint64{1, 2}
	requireNoError(t, s.SetNeighbors(0, 0, nbs))

	got, err := s.GetNeighbors(0, 0)
	requireNoError(t, err)
	assert.Equal(t, nbs, got)
}

func TestMmapStoreSetNeighborsUpper(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	vec := []float32{1.0, 2.0, 3.0, 4.0}
	requireNoError(t, s.PutNode(0, 3, vec, 0)) // level=3 → allocates upper slot

	nbs := []uint64{1, 2}
	requireNoError(t, s.SetNeighbors(0, 2, nbs))

	got, err := s.GetNeighbors(0, 2)
	requireNoError(t, err)
	assert.Equal(t, nbs, got)
}

func TestMmapStoreSetNorm(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	vec := []float32{1.0, 0.0, 0.0, 0.0}
	requireNoError(t, s.PutNode(0, 0, vec, 0))

	requireNoError(t, s.SetNorm(0, 99.5))

	norm, err := s.GetNorm(0)
	requireNoError(t, err)
	assert.InDelta(t, float32(99.5), norm, 0.01)
}

func TestMmapStoreSetEntryPoint(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	requireNoError(t, s.SetEntryPoint(42, 3))

	id, level, err := s.GetEntryPoint()
	requireNoError(t, err)
	assert.Equal(t, uint64(42), id)
	assert.Equal(t, 3, level)
}

// TestMmapStoreDocIdRoundtrip verifies that a docId written via PutNode is
// readable via GetDocId and via the lazy docToNode map via GetNodeId.
func TestMmapStoreDocIdRoundtrip(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	requireNoError(t, s.PutNode(42, 0, []float32{1, 0, 0, 0}, int64(999)))

	// GetDocId reads from the mmap node slot.
	docId, ok, err := s.GetDocId(42)
	requireNoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int64(999), docId)

	// GetNodeId triggers lazy docToNode build, then does a map lookup.
	nodeId, ok, err := s.GetNodeId(int64(999))
	requireNoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, uint64(42), nodeId)

	// Non-existent docId returns ok=false.
	_, ok, err = s.GetNodeId(int64(12345))
	requireNoError(t, err)
	assert.False(t, ok)
}

// TestMmapStoreDocIdPersistence verifies that the docId survives close → reopen.
func TestMmapStoreDocIdPersistence(t *testing.T) {
	dir := t.TempDir()
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4}

	s, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	requireNoError(t, s.PutNode(0, 0, []float32{1, 0, 0, 0}, int64(100)))
	requireNoError(t, s.PutNode(1, 0, []float32{0, 1, 0, 0}, int64(200)))
	requireNoError(t, s.Close())

	// Reopen and verify docIds are readable from mmap.
	s2, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	defer s2.Close()

	docId0, ok0, err := s2.GetDocId(0)
	requireNoError(t, err)
	assert.True(t, ok0)
	assert.Equal(t, int64(100), docId0)

	docId1, ok1, err := s2.GetDocId(1)
	requireNoError(t, err)
	assert.True(t, ok1)
	assert.Equal(t, int64(200), docId1)

	// GetNodeId must rebuild docToNode from mmap scan.
	nodeId, ok, err := s2.GetNodeId(int64(100))
	requireNoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, uint64(0), nodeId)

	nodeId2, ok2, err := s2.GetNodeId(int64(200))
	requireNoError(t, err)
	assert.True(t, ok2)
	assert.Equal(t, uint64(1), nodeId2)
}

func TestMmapStoreBatchWriteAndRead(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	requireNoError(t, s.txnBegin())
	for i := uint64(0); i < 10; i++ {
		vec := []float32{float32(i), 0, 0, 0}
		requireNoError(t, s.PutNode(i, 0, vec, int64(i)))
		requireNoError(t, s.SetNeighbors(i, 0, []uint64{(i + 1) % 10}))
	}
	requireNoError(t, s.txnCommit())

	// Verify all data is readable.
	for i := uint64(0); i < 10; i++ {
		got, err := s.GetVector(i)
		requireNoError(t, err)
		assert.Equal(t, []float32{float32(i), 0, 0, 0}, got)

		nbs, err := s.GetNeighbors(i, 0)
		requireNoError(t, err)
		assert.Equal(t, []uint64{(i + 1) % 10}, nbs)
	}
}

func TestMmapStoreNextNodeId(t *testing.T) {
	s := openTestMmapStore(t)
	defer s.Close()

	id0, err := s.NextNodeId()
	requireNoError(t, err)
	assert.Equal(t, uint64(0), id0)

	id1, err := s.NextNodeId()
	requireNoError(t, err)
	assert.Equal(t, uint64(1), id1)

	id2, err := s.NextNodeId()
	requireNoError(t, err)
	assert.Equal(t, uint64(2), id2)
}

func TestMmapStoreNextNodeIdPersistence(t *testing.T) {
	dir := t.TempDir()
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4}

	s, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)

	for i := 0; i < 5; i++ {
		_, err := s.NextNodeId()
		requireNoError(t, err)
	}
	requireNoError(t, s.Close())

	// Reopen — NextNodeId should continue from 5.
	s2, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	defer s2.Close()

	id, err := s2.NextNodeId()
	requireNoError(t, err)
	assert.Equal(t, uint64(5), id)
}

func TestTxnInsertSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4}

	s, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)

	const n = 100
	requireNoError(t, s.txnBegin())
	for i := 0; i < n; i++ {
		v := []float32{float32(i), float32(i + 1), float32(i + 2), float32(i + 3)}
		requireNoError(t, s.PutNode(uint64(i), 0, v, int64(i)))
	}
	requireNoError(t, s.txnCommit())
	requireNoError(t, s.Close())

	s2, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	defer s2.Close()

	for i := 0; i < n; i++ {
		v := []float32{float32(i), float32(i + 1), float32(i + 2), float32(i + 3)}
		got, err := s2.GetVector(uint64(i))
		requireNoError(t, err)
		assert.Equal(t, v, got, "vector %d mismatch after reopen", i)
	}
	// Verify docId=50 maps back to nodeId=50.
	nodeId, ok, err := s2.GetNodeId(int64(50))
	requireNoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, uint64(50), nodeId)
}

func TestDocIdDiscardedOnAbortReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4, CheckpointInterval: 1_000_000})
	requireNoError(t, err)
	requireNoError(t, s.txnBegin())
	requireNoError(t, s.PutNode(0, 0, []float32{1, 2, 3, 4}, int64(999)))
	_ = s.txnAbort(nil)
	_ = s.Close() // graceful close of a faulted store must not persist

	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	requireNoError(t, err)
	defer s2.Close()
	// The aborted node must not exist (WAL txn was never committed).
	if _, ok, _ := s2.GetNodeId(int64(999)); ok {
		t.Fatal("aborted txn's docId=999 must NOT survive reopen")
	}
}

func TestDocIdCommittedSurvivesCrash(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4, CheckpointInterval: 1_000_000})
	requireNoError(t, err)
	requireNoError(t, s.txnBegin())
	requireNoError(t, s.PutNode(0, 0, []float32{1, 2, 3, 4}, int64(77)))
	requireNoError(t, s.txnCommit())
	simulateCrash(s)

	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4})
	requireNoError(t, err)
	defer s2.Close()
	nodeId, ok, _ := s2.GetNodeId(int64(77))
	if !ok || nodeId != 0 {
		t.Fatalf("committed docId=77 must survive crash: got (%d,%v)", nodeId, ok)
	}
}

// TestZombieNodeReachableViaGetDocId proves that a node slot written by an
// aborted/crashed transaction (a "zombie") is never surfaced as a real
// document, even though a committed node holds a graph edge to it and its mmap
// bytes leaked to disk.
//
// Scenario: a committed node (entry point, docId 100) is durable. A second
// transaction then allocates a zombie node (docId 999) and rewrites the
// committed node's L0 neighbor list to point at the zombie — exactly what an
// HNSW insert does to existing neighbors. The transaction never commits; the
// dirty mmap pages are synced to disk and the store crashes. On reopen, WAL
// replay discards the unterminated transaction, so meta.TotalSlots covers only
// the committed node, leaving the zombie slot at id >= TotalSlots.
//
// Without the TotalSlots bound in GetDocId, Search traverses the leaked edge to
// the zombie and GetDocId returns 999 (aborted-txn data). With the bound, the
// zombie is reported not-found and never appears in results.
func TestZombieNodeReachableViaGetDocId(t *testing.T) {
	dir := t.TempDir()
	opts := MmapStoreOptions{Metric: DotProduct, Dim: 4, M: 4, CheckpointInterval: 1_000_000}
	s, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)

	const (
		committedID = uint64(0)
		zombieID    = uint64(1)
		committedDc = int64(100)
		zombieDc    = int64(999)
	)

	// Durable committed node — becomes the search entry point.
	requireNoError(t, s.txnBegin())
	requireNoError(t, s.PutNode(committedID, 0, []float32{1, 0, 0, 0}, committedDc))
	requireNoError(t, s.SetEntryPoint(committedID, 0))
	requireNoError(t, s.txnCommit())

	// Uncommitted transaction: write the zombie and graft a committed→zombie
	// edge, then crash before COMMIT. The zombie's vector is closer to the query
	// below than the committed node's, so an unfiltered Search would prefer it.
	requireNoError(t, s.txnBegin())
	zid, err := s.NextNodeId()
	requireNoError(t, err)
	if zid != zombieID {
		t.Fatalf("expected zombie node id %d, got %d", zombieID, zid)
	}
	requireNoError(t, s.PutNode(zombieID, 0, []float32{0, 1, 0, 0}, zombieDc))
	requireNoError(t, s.SetNeighbors(committedID, 0, []uint64{zombieID}))
	// Force the leaked mmap writes to disk, then crash with no COMMIT.
	requireNoError(t, s.syncAll())
	simulateCrash(s)

	// Reopen: WAL replay drops the unterminated txn; meta.TotalSlots == 1.
	s2, err := OpenMmapStore(dir, opts)
	requireNoError(t, err)
	defer s2.Close()

	if s2.meta.TotalSlots != 1 {
		t.Fatalf("TotalSlots = %d, want 1 (zombie txn must be discarded)", s2.meta.TotalSlots)
	}

	// Direct GetDocId on the zombie slot must report not-found.
	docId, ok, err := s2.GetDocId(zombieID)
	requireNoError(t, err)
	if ok {
		t.Fatalf("GetDocId(zombie) returned (%d, ok=true); aborted-txn data leaked", docId)
	}

	// The committed node is still fully readable.
	cDoc, ok, err := s2.GetDocId(committedID)
	requireNoError(t, err)
	if !ok || cDoc != committedDc {
		t.Fatalf("GetDocId(committed) = (%d, %v), want (%d, true)", cDoc, ok, committedDc)
	}

	// End-to-end: Search must not surface the zombie's docId even though the
	// committed entry node holds a leaked edge to it.
	idx := NewHNSWIndex(s2)
	results, err := idx.Search([]float32{0, 1, 0, 0}, 10)
	requireNoError(t, err)
	for _, r := range results {
		if r.DocID == zombieDc {
			t.Fatalf("Search returned zombie docId %d from an aborted transaction", zombieDc)
		}
	}
}
