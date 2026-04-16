package vectorindex

import (
	"math/rand"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/stretchr/testify/assert"
)

// --- helpers ---

// poisonBatch creates a PebbleNodeStore with an active indexed batch that has
// been closed.  A closed indexed batch returns errors from Get (reader path)
// but accepts Set/Delete silently.  This exercises the read-side error paths
// in methods like DeleteNode and DeleteNodeMapping that read through the batch.
func poisonBatch(t *testing.T) (*pebble.DB, *PebbleNodeStore) {
	t.Helper()
	dir := t.TempDir()
	db, err := pebble.Open(filepath.Join(dir, "pb.db"), &pebble.Options{})
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}

	store := NewPebbleNodeStore(db, 1)

	// Seed data so that delete operations have something to read.
	requireNoError(t, store.PutNode(1, 1, []float32{1.0, 2.0}))
	requireNoError(t, store.SetNeighbors(1, 0, []uint64{2}))
	requireNoError(t, store.SetNeighbors(1, 1, []uint64{3}))
	requireNoError(t, store.SetNodeMapping("doc-1", 1))

	// Create a real indexed batch, then close it.  activeBatch() will return
	// the closed batch whose Get() returns an error (not ErrNotFound).
	store.BeginBatch()
	batch := store.pendingBatch
	if err := batch.Close(); err != nil {
		t.Fatalf("close batch: %v", err)
	}

	return db, store
}

func cleanupPoison(t *testing.T, db *pebble.DB, store *PebbleNodeStore) {
	t.Helper()
	store.batchMu.Lock()
	store.pendingBatch = nil
	store.batchDepth = 0
	store.batchMu.Unlock()
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
}

// readOnlyStore creates a PebbleNodeStore backed by a read-only Pebble DB.
// The DB is pre-seeded with one node so that delete operations have data.
// On a read-only DB:
//   - db.Set() returns "pebble: read-only"
//   - batch.Commit() returns "pebble: read-only"
//   - db.Get() works normally
//
// This exercises every Commit / db.Set error path in the non-batch code path.
func readOnlyStore(t *testing.T) (*pebble.DB, *PebbleNodeStore) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ro.db")

	// Phase 1: seed data in a writable DB.
	db, err := pebble.Open(dbPath, &pebble.Options{})
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	store := NewPebbleNodeStore(db, 1)
	requireNoError(t, store.PutNode(1, 1, []float32{1.0, 2.0}))
	requireNoError(t, store.SetNeighbors(1, 0, []uint64{2}))
	requireNoError(t, store.SetNeighbors(1, 1, []uint64{3}))
	requireNoError(t, store.SetNodeMapping("doc-1", 1))
	requireNoError(t, db.Close())

	// Phase 2: reopen read-only.
	roDB, err := pebble.Open(dbPath, &pebble.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("open read-only pebble: %v", err)
	}
	roStore := NewPebbleNodeStore(roDB, 1)
	return roDB, roStore
}

// =====================================================================
// PutNode error paths
// =====================================================================

// Batch path: ab.Get(countKey) fails on poisoned batch.
func TestPutNodeBatchGetCountError(t *testing.T) {
	db, store := poisonBatch(t)
	defer cleanupPoison(t, db, store)

	err := store.PutNode(99, 0, []float32{1.0})
	assert.Error(t, err, "PutNode should fail when batch Get for count errors")
}

// Non-batch path: batch.Commit fails on read-only DB.
func TestPutNodeCommitErrorOnReadOnlyDB(t *testing.T) {
	db, store := readOnlyStore(t)
	defer db.Close()

	err := store.PutNode(99, 0, []float32{1.0})
	assert.Error(t, err, "PutNode should fail on read-only DB (commit error)")
}

// =====================================================================
// DeleteNode error paths
// =====================================================================

// Batch path: reader().Get on poisoned batch fails for level read.
func TestDeleteNodeReaderError(t *testing.T) {
	db, store := poisonBatch(t)
	defer cleanupPoison(t, db, store)

	err := store.DeleteNode(1)
	assert.Error(t, err, "DeleteNode should fail on poisoned batch reader")
}

// Non-batch path: batch.Commit fails on read-only DB.
func TestDeleteNodeCommitErrorOnReadOnlyDB(t *testing.T) {
	db, store := readOnlyStore(t)
	defer db.Close()

	err := store.DeleteNode(1)
	assert.Error(t, err, "DeleteNode should fail on read-only DB (commit error)")
}

// =====================================================================
// SetNeighbors error paths
// =====================================================================

// Non-batch path: s.db.Set fails on read-only DB.
func TestSetNeighborsErrorOnReadOnlyDB(t *testing.T) {
	db, store := readOnlyStore(t)
	defer db.Close()

	err := store.SetNeighbors(1, 0, []uint64{2, 3})
	assert.Error(t, err, "SetNeighbors should fail on read-only DB")
}

// =====================================================================
// SetNorm error paths
// =====================================================================

// Non-batch path: s.db.Set fails on read-only DB.
func TestSetNormErrorOnReadOnlyDB(t *testing.T) {
	db, store := readOnlyStore(t)
	defer db.Close()

	err := store.SetNorm(1, 3.14)
	assert.Error(t, err, "SetNorm should fail on read-only DB")
}

// =====================================================================
// SetNodeMapping error paths
// =====================================================================

// Non-batch path: batch.Commit fails on read-only DB.
func TestSetNodeMappingCommitErrorOnReadOnlyDB(t *testing.T) {
	db, store := readOnlyStore(t)
	defer db.Close()

	err := store.SetNodeMapping("doc-new", 99)
	assert.Error(t, err, "SetNodeMapping should fail on read-only DB (commit error)")
}

// =====================================================================
// DeleteNodeMapping error paths
// =====================================================================

// Batch path: reader().Get on poisoned batch fails.
func TestDeleteNodeMappingReaderError(t *testing.T) {
	db, store := poisonBatch(t)
	defer cleanupPoison(t, db, store)

	err := store.DeleteNodeMapping("doc-1")
	assert.Error(t, err, "DeleteNodeMapping should fail on poisoned batch reader")
}

// Non-batch path: batch.Commit fails on read-only DB.
func TestDeleteNodeMappingCommitErrorOnReadOnlyDB(t *testing.T) {
	db, store := readOnlyStore(t)
	defer db.Close()

	err := store.DeleteNodeMapping("doc-1")
	assert.Error(t, err, "DeleteNodeMapping should fail on read-only DB (commit error)")
}

// =====================================================================
// randomLevel
// =====================================================================

func TestRandomLevelDistribution(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance, WithM(16), WithRand(rand.New(rand.NewSource(12345))))

	const iterations = 10_000
	counts := make(map[int]int)
	for i := 0; i < iterations; i++ {
		l := idx.randomLevel()
		assert.GreaterOrEqual(t, l, 0, "level must be non-negative")
		counts[l]++
	}

	// Level 0 should be the most common (exponential distribution).
	assert.Greater(t, counts[0], iterations/2, "level 0 should appear > 50%% of the time")

	nonZero := 0
	for l, c := range counts {
		if l > 0 {
			nonZero += c
		}
	}
	assert.Greater(t, nonZero, 0, "should produce some levels > 0")
}

// zeroSource is a rand.Source that always returns 0, making Float64() == 0.
type zeroSource struct{}

func (zeroSource) Int63() int64 { return 0 }
func (zeroSource) Seed(int64)   {}

// Test the r == 0 guard in randomLevel by injecting a source that returns 0.
func TestRandomLevelZeroGuard(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance, WithM(16), WithRand(rand.New(zeroSource{})))

	level := idx.randomLevel()
	assert.GreaterOrEqual(t, level, 0, "level from r=0 must be non-negative")
	assert.Less(t, level, 200, "level from r=0 (guarded to 1e-18) must be reasonable")
}

// =====================================================================
// nodeDistance
// =====================================================================

func TestNodeDistanceGetVectorRefError(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance)

	// Node 999 does not exist — GetVectorRef returns an error.
	_, err := idx.nodeDistance(999, []float32{1.0, 2.0})
	assert.Error(t, err, "nodeDistance should propagate GetVectorRef error")
}

func TestNodeDistanceSuccess(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, CosineDistance)

	requireNoError(t, store.PutNode(1, 0, []float32{1.0, 0.0}))

	dist, err := idx.nodeDistance(1, []float32{1.0, 0.0})
	requireNoError(t, err)
	assert.InDelta(t, 0.0, dist, 1e-6, "distance to self should be ~0")
}

// =====================================================================
// Closed-DB error paths
//
// Pebble panics (not returns error) when calling Get/Set/Delete on a
// closed DB. We verify the panic is triggered by running the operation
// in a goroutine, recovering the panic, and asserting it occurred.
// =====================================================================

// closedDBStore opens a Pebble DB, seeds one node+mapping, closes the DB,
// and returns the store. Every subsequent DB operation panics.
func closedDBStore(t *testing.T) *PebbleNodeStore {
	t.Helper()
	dir := t.TempDir()
	db, err := pebble.Open(filepath.Join(dir, "closed.db"), &pebble.Options{})
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	store := NewPebbleNodeStore(db, 1)

	// Seed data so delete paths have something to look up.
	requireNoError(t, store.PutNode(1, 1, []float32{1.0, 2.0}))
	requireNoError(t, store.SetNeighbors(1, 0, []uint64{2}))
	requireNoError(t, store.SetNeighbors(1, 1, []uint64{3}))
	requireNoError(t, store.SetNodeMapping("doc-1", 1))

	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	return store
}

// assertPanics runs fn in a goroutine and verifies it panics.
func assertPanics(t *testing.T, name string, fn func()) {
	t.Helper()
	done := make(chan bool, 1)
	go func() {
		defer func() {
			r := recover()
			done <- (r != nil)
		}()
		fn()
	}()
	panicked := <-done
	assert.True(t, panicked, "%s should panic on closed DB", name)
}

func TestPutNodeClosedDB(t *testing.T) {
	store := closedDBStore(t)
	// Evict from cache so PutNode hits the closed DB for count read.
	store.cacheEvict(99)
	assertPanics(t, "PutNode", func() {
		store.PutNode(99, 0, []float32{1.0})
	})
}

func TestDeleteNodeClosedDB(t *testing.T) {
	store := closedDBStore(t)
	// Evict node from cache so DeleteNode hits the closed DB for level read.
	store.cacheEvict(1)
	assertPanics(t, "DeleteNode", func() {
		store.DeleteNode(1)
	})
}

func TestSetNeighborsClosedDB(t *testing.T) {
	store := closedDBStore(t)
	assertPanics(t, "SetNeighbors", func() {
		store.SetNeighbors(1, 0, []uint64{2, 3})
	})
}

func TestSetNodeMappingClosedDB(t *testing.T) {
	store := closedDBStore(t)
	assertPanics(t, "SetNodeMapping", func() {
		store.SetNodeMapping("doc-new", 99)
	})
}

func TestDeleteNodeMappingClosedDB(t *testing.T) {
	store := closedDBStore(t)
	assertPanics(t, "DeleteNodeMapping", func() {
		store.DeleteNodeMapping("doc-1")
	})
}

func TestSetNormClosedDB(t *testing.T) {
	store := closedDBStore(t)
	assertPanics(t, "SetNorm", func() {
		store.SetNorm(1, 3.14)
	})
}

// =====================================================================
// Additional read-side error paths (poisoned batch)
// =====================================================================

// NextNodeId batch path: reader().Get on poisoned batch fails.
func TestNextNodeIdBatchReaderError(t *testing.T) {
	db, store := poisonBatch(t)
	defer cleanupPoison(t, db, store)

	_, err := store.NextNodeId()
	assert.Error(t, err, "NextNodeId should fail on poisoned batch reader")
}

// NextNodeId non-batch path: batch.Commit fails on read-only DB.
func TestNextNodeIdCommitErrorOnReadOnlyDB(t *testing.T) {
	db, store := readOnlyStore(t)
	defer db.Close()

	_, err := store.NextNodeId()
	assert.Error(t, err, "NextNodeId should fail on read-only DB (commit error)")
}

// SetEntryPoint non-batch path: s.db.Set fails on read-only DB.
func TestSetEntryPointErrorOnReadOnlyDB(t *testing.T) {
	db, store := readOnlyStore(t)
	defer db.Close()

	err := store.SetEntryPoint(99, 5)
	assert.Error(t, err, "SetEntryPoint should fail on read-only DB")
}

// GetVector on non-existent node (not in cache) triggers read error.
func TestGetVectorReadError(t *testing.T) {
	db, store := poisonBatch(t)
	defer cleanupPoison(t, db, store)

	// Node 999 not in cache, poisoned batch Get returns error.
	_, err := store.GetVector(999)
	assert.Error(t, err, "GetVector should fail on poisoned batch reader")
}

// GetVectorRef on non-existent node (not in cache) triggers read error.
func TestGetVectorRefReadError(t *testing.T) {
	db, store := poisonBatch(t)
	defer cleanupPoison(t, db, store)

	_, err := store.GetVectorRef(999)
	assert.Error(t, err, "GetVectorRef should fail on poisoned batch reader")
}

// GetNeighbors read error on poisoned batch.
func TestGetNeighborsReadError(t *testing.T) {
	db, store := poisonBatch(t)
	defer cleanupPoison(t, db, store)

	_, err := store.GetNeighbors(1, 0)
	assert.Error(t, err, "GetNeighbors should fail on poisoned batch reader")
}

// GetEntryPoint read error on poisoned batch.
func TestGetEntryPointReadError(t *testing.T) {
	db, store := poisonBatch(t)
	defer cleanupPoison(t, db, store)

	_, _, err := store.GetEntryPoint()
	assert.Error(t, err, "GetEntryPoint should fail on poisoned batch reader")
}

// GetNodeLevel read error on poisoned batch.
func TestGetNodeLevelReadError(t *testing.T) {
	db, store := poisonBatch(t)
	defer cleanupPoison(t, db, store)

	_, err := store.GetNodeLevel(1)
	assert.Error(t, err, "GetNodeLevel should fail on poisoned batch reader")
}

// GetNodeId read error on poisoned batch.
func TestGetNodeIdReadError(t *testing.T) {
	db, store := poisonBatch(t)
	defer cleanupPoison(t, db, store)

	_, _, err := store.GetNodeId("doc-1")
	assert.Error(t, err, "GetNodeId should fail on poisoned batch reader")
}

// GetNorm read error on poisoned batch.
func TestGetNormReadError(t *testing.T) {
	db, store := poisonBatch(t)
	defer cleanupPoison(t, db, store)

	_, err := store.GetNorm(1)
	assert.Error(t, err, "GetNorm should fail on poisoned batch reader")
}

// =====================================================================
// Batch happy-path coverage
//
// Exercise the batch code paths (ab != nil) for PutNode, DeleteNode,
// SetNodeMapping, DeleteNodeMapping, SetNeighbors, SetNorm,
// SetEntryPoint, and NextNodeId. The poisoned-batch tests hit
// the first error in these paths; these tests cover the successful
// lines that the poisoned-batch approach skips.
// =====================================================================

func TestPutNodeBatchPath(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := NewPebbleNodeStore(db, 1)

	store.BeginBatch()
	requireNoError(t, store.PutNode(1, 2, []float32{1.0, 2.0, 3.0}))
	requireNoError(t, store.CommitBatch(true))

	vec, err := store.GetVector(1)
	requireNoError(t, err)
	assert.Equal(t, []float32{1.0, 2.0, 3.0}, vec)

	level, err := store.GetNodeLevel(1)
	requireNoError(t, err)
	assert.Equal(t, 2, level)
}

func TestDeleteNodeBatchPath(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := NewPebbleNodeStore(db, 1)

	// Seed data outside batch.
	requireNoError(t, store.PutNode(1, 1, []float32{1.0, 2.0}))
	requireNoError(t, store.SetNeighbors(1, 0, []uint64{2}))
	requireNoError(t, store.SetNeighbors(1, 1, []uint64{3}))
	requireNoError(t, store.SetNodeMapping("doc-1", 1))

	// Delete inside batch.
	store.BeginBatch()
	requireNoError(t, store.DeleteNode(1))
	requireNoError(t, store.CommitBatch(true))

	// Verify deletion.
	_, err := store.GetVector(1)
	assert.Error(t, err)
	_, found, err := store.GetNodeId("doc-1")
	requireNoError(t, err)
	assert.False(t, found)
}

// DeleteNode with no doc mapping — covers the ErrNotFound branch for
// the node→doc lookup in both batch and non-batch paths.
func TestDeleteNodeNoDocMappingBatchPath(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := NewPebbleNodeStore(db, 1)

	// PutNode without SetNodeMapping, so node→doc lookup returns ErrNotFound.
	requireNoError(t, store.PutNode(1, 0, []float32{1.0}))

	store.BeginBatch()
	requireNoError(t, store.DeleteNode(1))
	requireNoError(t, store.CommitBatch(true))

	_, err := store.GetVector(1)
	assert.Error(t, err)
}

func TestDeleteNodeNoDocMappingNonBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := NewPebbleNodeStore(db, 1)

	// PutNode without SetNodeMapping.
	requireNoError(t, store.PutNode(1, 0, []float32{1.0}))

	requireNoError(t, store.DeleteNode(1))

	_, err := store.GetVector(1)
	assert.Error(t, err)
}

func TestSetNodeMappingBatchPath(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := NewPebbleNodeStore(db, 1)

	store.BeginBatch()
	requireNoError(t, store.SetNodeMapping("doc-batch", 42))
	requireNoError(t, store.CommitBatch(true))

	nodeId, found, err := store.GetNodeId("doc-batch")
	requireNoError(t, err)
	assert.True(t, found)
	assert.Equal(t, uint64(42), nodeId)
}

func TestDeleteNodeMappingBatchPath(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := NewPebbleNodeStore(db, 1)

	requireNoError(t, store.SetNodeMapping("doc-del", 7))

	store.BeginBatch()
	requireNoError(t, store.DeleteNodeMapping("doc-del"))
	requireNoError(t, store.CommitBatch(true))

	_, found, err := store.GetNodeId("doc-del")
	requireNoError(t, err)
	assert.False(t, found)
}

func TestSetNeighborsBatchPath(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := NewPebbleNodeStore(db, 1)

	requireNoError(t, store.PutNode(1, 0, []float32{1.0}))

	store.BeginBatch()
	requireNoError(t, store.SetNeighbors(1, 0, []uint64{2, 3}))
	requireNoError(t, store.CommitBatch(true))

	nb, err := store.GetNeighbors(1, 0)
	requireNoError(t, err)
	assert.Equal(t, []uint64{2, 3}, nb)
}

func TestSetNormBatchPath(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := NewPebbleNodeStore(db, 1)

	store.BeginBatch()
	requireNoError(t, store.SetNorm(1, 3.14))
	requireNoError(t, store.CommitBatch(true))

	norm, err := store.GetNorm(1)
	requireNoError(t, err)
	assert.InDelta(t, 3.14, float64(norm), 1e-6)
}

func TestSetEntryPointBatchPath(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := NewPebbleNodeStore(db, 1)

	store.BeginBatch()
	requireNoError(t, store.SetEntryPoint(10, 3))
	requireNoError(t, store.CommitBatch(true))

	epId, maxLayer, err := store.GetEntryPoint()
	requireNoError(t, err)
	assert.Equal(t, uint64(10), epId)
	assert.Equal(t, 3, maxLayer)
}

func TestNextNodeIdBatchPath(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := NewPebbleNodeStore(db, 1)

	store.BeginBatch()
	id, err := store.NextNodeId()
	requireNoError(t, err)
	assert.Equal(t, uint64(1), id)
	requireNoError(t, store.CommitBatch(true))
}

// =====================================================================
// randomLevel — additional maxLevel parameter coverage
// =====================================================================

func TestRandomLevelDifferentM(t *testing.T) {
	for _, m := range []int{4, 8, 32, 64} {
		store := NewMemNodeStore()
		idx := NewHNSWIndex(store, CosineDistance, WithM(m),
			WithRand(rand.New(rand.NewSource(42))))

		for i := 0; i < 500; i++ {
			level := idx.randomLevel()
			assert.GreaterOrEqual(t, level, 0,
				"M=%d: level must be non-negative", m)
		}
	}
}

// =====================================================================
// nodeDistance — non-existent node via GetVectorRef returning error
// =====================================================================

func TestNodeDistanceNonExistentPebble(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := NewPebbleNodeStore(db, 1)
	idx := NewHNSWIndex(store, CosineDistance)

	// Node 999 does not exist — GetVectorRef returns an error.
	_, err := idx.nodeDistance(999, []float32{1.0, 2.0})
	assert.Error(t, err, "nodeDistance should propagate GetVectorRef error for non-existent node")
}
