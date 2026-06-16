package vectorindex

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// VEC-007: HNSWIndex.deleteNodeLocked reselected the entry point ONLY from the
// deleted node's own neighbor lists. When the deleted node IS the entry point
// and its neighbor lists are empty, newLevel stayed -1, the `if newLevel >= 0`
// guard was skipped, and nothing happened — leaving meta.EntryPoint pointing at
// the tombstoned node. Every subsequent Search then did GetVectorRef(epId) ->
// error -> returned nil for the WHOLE index even though live nodes remained
// (availability/recall cliff), or, when truly empty, left the EP dangling on
// disk. These tests assert the fix: reseat the EP to a live node when one
// remains, clear the EP marker (consistently + durably) when none do.

// buildIsolatedMemNode drives the store directly to create a node with NO edges.
func buildIsolatedMemNode(t *testing.T, store *MemNodeStore, level int, vec []float32, docId int64) uint64 {
	t.Helper()
	id, err := store.NextNodeId()
	require.NoError(t, err)
	require.NoError(t, store.PutNode(id, level, vec, docId))
	for layer := 0; layer <= level; layer++ {
		require.NoError(t, store.SetNeighbors(id, layer, nil))
	}
	return id
}

// (1) NON-EMPTY index, EP deleted with empty neighbor lists, >=1 other live node.
// BEFORE FIX: stale EP -> Search returns nil. AFTER FIX: EP reseated to B.
func TestStaleEntryPointReseat_MemStore(t *testing.T) {
	store := NewMemNodeStore(Euclidean) // Euclidean so distance == raw, no cosine noise
	idx := NewHNSWIndex(store)

	vecA := []float32{1, 0, 0, 0}
	vecB := []float32{0, 1, 0, 0}
	idA := buildIsolatedMemNode(t, store, 0, vecA, 100)
	idB := buildIsolatedMemNode(t, store, 0, vecB, 200)
	require.NoError(t, store.SetEntryPoint(idA, 0)) // A is the EP, A has NO neighbors

	// Sanity: Search finds the EP (A) before deletion.
	res, err := idx.Search(vecA, 5)
	require.NoError(t, err)
	require.NotEmpty(t, res)

	// Delete docId 100 (== node A == the EP). Its neighbor list is empty: the bug.
	require.NoError(t, idx.Delete(100))

	// ASSERT: Search STILL returns the surviving live node B.
	res2, err := idx.Search(vecB, 5)
	require.NoError(t, err)
	require.Len(t, res2, 1)
	require.Equal(t, int64(200), res2[0].DocID)

	// The EP is now the live node B, not the tombstone.
	ep, _, err := store.GetEntryPoint()
	require.NoError(t, err)
	require.Equal(t, idB, ep)
}

// (2) Highest-level preference: A(EP, level0), B(level0), C(level2).
// Delete A; reseated EP must be C (highest level).
func TestStaleEntryPointReseat_HighestLevel_MemStore(t *testing.T) {
	store := NewMemNodeStore(Euclidean)
	idx := NewHNSWIndex(store)

	idA := buildIsolatedMemNode(t, store, 0, []float32{1, 0, 0, 0}, 100)
	_ = buildIsolatedMemNode(t, store, 0, []float32{0, 1, 0, 0}, 200)
	idC := buildIsolatedMemNode(t, store, 2, []float32{0, 0, 1, 0}, 300)
	require.NoError(t, store.SetEntryPoint(idA, 0))

	require.NoError(t, idx.Delete(100)) // delete the EP (empty neighbors)

	ep, lvl, err := store.GetEntryPoint()
	require.NoError(t, err)
	require.Equal(t, idC, ep, "highest-level live node must be chosen as EP")
	require.Equal(t, 2, lvl)

	// Search still returns results (availability restored). Note: the reseated
	// EP (C) is isolated from B, so Search from C reaches only C — full graph
	// connectivity repair is OUT OF SCOPE (VEC-007). The load-bearing assertion
	// is that the EP is a live node and Search is no longer empty.
	res, err := idx.Search([]float32{0, 0, 1, 0}, 5)
	require.NoError(t, err)
	require.NotEmpty(t, res)
	require.Equal(t, int64(300), res[0].DocID)
}

// (3) Truly-empty case + durability: single node deleted, EP must be CLEARED,
// and the cleared marker must survive a Close/reopen of the MmapStore.
func TestStaleEntryPointClear_MmapReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: Euclidean, Dim: 4, M: 16})
	require.NoError(t, err)
	idx := NewHNSWIndex(s)

	require.NoError(t, idx.Insert(100, []float32{1, 0, 0, 0})) // single node, becomes EP
	require.NoError(t, idx.Delete(100))                        // last node deleted

	// EP must be cleared now (no live nodes): GetEntryPoint returns an error.
	_, _, err = s.GetEntryPoint()
	require.Error(t, err, "EP must be cleared after the last node is deleted")

	// Search returns empty, no panic.
	res, err := idx.Search([]float32{1, 0, 0, 0}, 5)
	require.NoError(t, err)
	require.Empty(t, res)
	require.NoError(t, s.Close())

	// Reopen: the cleared marker must be durable (WalSetEntry sentinel replayed).
	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: Euclidean, Dim: 4, M: 16})
	require.NoError(t, err)
	defer s2.Close()
	_, _, err = s2.GetEntryPoint()
	require.Error(t, err, "cleared EP must survive reopen")
}

// (4) MmapStore RESEAT durability: two isolated nodes, delete the EP with empty
// neighbor lists; the reseated EP must survive a Close/reopen and a Search on
// the reopened index must return the survivor.
func TestStaleEntryPointReseat_MmapReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: Euclidean, Dim: 4, M: 16})
	require.NoError(t, err)
	idx := NewHNSWIndex(s)

	// Two isolated level-0 nodes. Drive the store so the EP (A) has empty
	// neighbor lists and is the entry point, with B as the only other live node.
	vecA := []float32{1, 0, 0, 0}
	vecB := []float32{0, 1, 0, 0}
	require.NoError(t, s.txnBegin())
	idA, err := s.NextNodeId()
	require.NoError(t, err)
	require.NoError(t, s.PutNode(idA, 0, vecA, 100))
	require.NoError(t, s.SetNeighbors(idA, 0, nil))
	idB, err := s.NextNodeId()
	require.NoError(t, err)
	require.NoError(t, s.PutNode(idB, 0, vecB, 200))
	require.NoError(t, s.SetNeighbors(idB, 0, nil))
	require.NoError(t, s.SetEntryPoint(idA, 0))
	require.NoError(t, s.txnCommit())

	// Delete the EP (A); its neighbor list is empty -> reseat to B.
	require.NoError(t, idx.Delete(100))

	ep, _, err := s.GetEntryPoint()
	require.NoError(t, err)
	require.Equal(t, idB, ep)

	res, err := idx.Search(vecB, 5)
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Equal(t, int64(200), res[0].DocID)
	require.NoError(t, s.Close())

	// Reopen: reseated EP must be durable, and Search on the reopened index
	// must return the survivor.
	s2, err := OpenMmapStore(dir, MmapStoreOptions{Metric: Euclidean, Dim: 4, M: 16})
	require.NoError(t, err)
	defer s2.Close()
	ep2, _, err := s2.GetEntryPoint()
	require.NoError(t, err)
	require.Equal(t, idB, ep2, "reseated EP must survive reopen")

	idx2 := NewHNSWIndex(s2)
	res2, err := idx2.Search(vecB, 5)
	require.NoError(t, err)
	require.Len(t, res2, 1)
	require.Equal(t, int64(200), res2[0].DocID)
}

// (5) Direct unit coverage for the new store methods on empty stores and the
// Mem clear path (proves the cross-store consistency: Mem keys off hasEntry,
// not a ^uint64(0) sentinel, so SetEntryPoint(^uint64(0)) alone would NOT make
// GetEntryPoint error — ClearEntryPoint must).
func TestHighestLiveNodeExcluding_EmptyStores(t *testing.T) {
	mem := NewMemNodeStore(Euclidean)
	_, _, ok, err := mem.HighestLiveNodeExcluding(0)
	require.NoError(t, err)
	require.False(t, ok)

	dir := t.TempDir()
	s, err := OpenMmapStore(dir, MmapStoreOptions{Metric: Euclidean, Dim: 4, M: 16})
	require.NoError(t, err)
	defer s.Close()
	_, _, ok, err = s.HighestLiveNodeExcluding(0)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestClearEntryPoint_MemStore(t *testing.T) {
	mem := NewMemNodeStore(Euclidean)
	require.NoError(t, mem.SetEntryPoint(7, 3))
	ep, _, err := mem.GetEntryPoint()
	require.NoError(t, err)
	require.Equal(t, uint64(7), ep)

	require.NoError(t, mem.ClearEntryPoint())
	_, _, err = mem.GetEntryPoint()
	require.Error(t, err, "ClearEntryPoint must make GetEntryPoint report no entry")
}

// Reseat tie-break: among equal-(highest-)level survivors, the lowest node id
// wins — a documented, deterministic contract on both stores. Inverting the
// tie-break (id < bestID -> id > bestID) leaves the package green without this.
func TestStaleEntryPointReseat_TieBreakLowestId(t *testing.T) {
	// MemStore: delete the EP; B and C are equal-level survivors -> lowest id.
	store := NewMemNodeStore(Euclidean)
	idx := NewHNSWIndex(store)
	idA := buildIsolatedMemNode(t, store, 0, []float32{1, 0, 0, 0}, 100) // A (EP)
	idB := buildIsolatedMemNode(t, store, 0, []float32{0, 1, 0, 0}, 200)
	idC := buildIsolatedMemNode(t, store, 0, []float32{0, 0, 1, 0}, 300)
	require.Less(t, idB, idC) // ascending allocation
	require.NoError(t, store.SetEntryPoint(idA, 0))

	require.NoError(t, idx.Delete(100))
	ep, _, err := store.GetEntryPoint()
	require.NoError(t, err)
	require.Equal(t, idB, ep, "tie among equal-level survivors must pick the lowest id")

	// MmapStore: HighestLiveNodeExcluding directly — equal level, lowest id wins.
	dir := t.TempDir()
	ms, err := OpenMmapStore(dir, MmapStoreOptions{Metric: Euclidean, Dim: 4, M: 16})
	require.NoError(t, err)
	defer ms.Close()
	require.NoError(t, ms.txnBegin())
	require.NoError(t, ms.PutNode(5, 1, []float32{1, 0, 0, 0}, 1))
	require.NoError(t, ms.PutNode(9, 1, []float32{0, 1, 0, 0}, 2)) // same level, higher id
	require.NoError(t, ms.txnCommit())
	id, lvl, ok, err := ms.HighestLiveNodeExcluding(^uint64(0)) // exclude nothing
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 1, lvl)
	require.Equal(t, uint64(5), id, "MmapStore tie-break must pick the lowest id at the max level")
}
