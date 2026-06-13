package vectorindex

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/viterin/vek/vek32"
)

// requireLen fails the test if the slice doesn't have the expected length.
func requireLen(t *testing.T, results []SearchResult, expected int, msgAndArgs ...interface{}) {
	t.Helper()
	if len(results) != expected {
		if len(msgAndArgs) > 0 {
			t.Fatalf("expected %d results, got %d: %v", expected, len(results), fmt.Sprint(msgAndArgs...))
		}
		t.Fatalf("expected %d results, got %d", expected, len(results))
	}
}

// requireNotEmpty fails the test if the slice is empty.
func requireNotEmpty(t *testing.T, results []SearchResult, msgAndArgs ...interface{}) {
	t.Helper()
	if len(results) == 0 {
		if len(msgAndArgs) > 0 {
			t.Fatalf("expected non-empty results: %v", fmt.Sprint(msgAndArgs...))
		}
		t.Fatalf("expected non-empty results")
	}
}

// --- Distance function tests ---

func TestCosineDistance(t *testing.T) {
	t.Run("identical", func(t *testing.T) {
		a := []float32{1, 2, 3}
		d := CosineDistance(a, a)
		assert.InDelta(t, 0.0, d, 1e-6)
	})
	t.Run("orthogonal", func(t *testing.T) {
		a := []float32{1, 0}
		b := []float32{0, 1}
		d := CosineDistance(a, b)
		assert.InDelta(t, 1.0, d, 1e-6)
	})
	t.Run("opposite", func(t *testing.T) {
		a := []float32{1, 0}
		b := []float32{-1, 0}
		d := CosineDistance(a, b)
		assert.InDelta(t, 2.0, d, 1e-6)
	})
	t.Run("zero_vector", func(t *testing.T) {
		a := []float32{0, 0, 0}
		b := []float32{1, 2, 3}
		d := CosineDistance(a, b)
		assert.InDelta(t, 1.0, d, 1e-6)
	})
}

func TestCosineDistanceWithNormsEquivalence(t *testing.T) {
	t.Run("random_vectors", func(t *testing.T) {
		rng := rand.New(rand.NewSource(42))
		dims := []int{2, 8, 128, 384}
		for _, dim := range dims {
			for trial := 0; trial < 50; trial++ {
				a := make([]float32, dim)
				b := make([]float32, dim)
				for i := range a {
					a[i] = rng.Float32()*2 - 1
					b[i] = rng.Float32()*2 - 1
				}
				expected := CosineDistance(a, b)
				normA := vek32.Norm(a)
				normB := vek32.Norm(b)
				got := CosineDistanceWithNorms(a, b, normA, normB)
				assert.InDelta(t, float64(expected), float64(got), 1e-6,
					"dim=%d trial=%d: CosineDistanceWithNorms != CosineDistance", dim, trial)
			}
		}
	})

	t.Run("identical_vectors", func(t *testing.T) {
		a := []float32{1, 2, 3, 4}
		normA := vek32.Norm(a)
		expected := CosineDistance(a, a)
		got := CosineDistanceWithNorms(a, a, normA, normA)
		assert.InDelta(t, float64(expected), float64(got), 1e-6)
	})

	t.Run("zero_vector", func(t *testing.T) {
		a := []float32{1, 2, 3}
		zero := []float32{0, 0, 0}
		expected := CosineDistance(a, zero)
		normA := vek32.Norm(a)
		normZ := vek32.Norm(zero)
		got := CosineDistanceWithNorms(a, zero, normA, normZ)
		assert.InDelta(t, float64(expected), float64(got), 1e-6)
	})
}

func TestEuclideanDistance(t *testing.T) {
	t.Run("identical", func(t *testing.T) {
		a := []float32{1, 2, 3}
		d := EuclideanDistance(a, a)
		assert.InDelta(t, 0.0, d, 1e-6)
	})
	t.Run("known", func(t *testing.T) {
		a := []float32{0, 0}
		b := []float32{3, 4}
		d := EuclideanDistance(a, b)
		assert.InDelta(t, 5.0, d, 1e-6)
	})
	t.Run("unit", func(t *testing.T) {
		a := []float32{0}
		b := []float32{1}
		d := EuclideanDistance(a, b)
		assert.InDelta(t, 1.0, d, 1e-6)
	})
}

func TestDotProductDistance(t *testing.T) {
	t.Run("identical_normalized", func(t *testing.T) {
		a := []float32{1, 0}
		d := DotProductDistance(a, a)
		assert.InDelta(t, 0.0, d, 1e-6)
	})
	t.Run("orthogonal", func(t *testing.T) {
		a := []float32{1, 0}
		b := []float32{0, 1}
		d := DotProductDistance(a, b)
		assert.InDelta(t, 1.0, d, 1e-6)
	})
	t.Run("opposite", func(t *testing.T) {
		a := []float32{1, 0}
		b := []float32{-1, 0}
		d := DotProductDistance(a, b)
		assert.InDelta(t, 2.0, d, 1e-6)
	})
}

// --- MemNodeStore tests ---

func TestMemStoreBasicOperations(t *testing.T) {
	store := NewMemNodeStore()
	defer store.Close()

	// NextNodeId
	id1, err := store.NextNodeId()
	requireNoError(t, err)
	assert.Equal(t, uint64(1), id1)

	id2, err := store.NextNodeId()
	requireNoError(t, err)
	assert.Equal(t, uint64(2), id2)

	// PutNode + GetVector
	vec := []float32{1.0, 2.0, 3.0}
	requireNoError(t, store.PutNode(id1, 2, vec))

	got, err := store.GetVector(id1)
	requireNoError(t, err)
	assert.Equal(t, vec, got)

	// GetNodeLevel
	level, err := store.GetNodeLevel(id1)
	requireNoError(t, err)
	assert.Equal(t, 2, level)

	// Non-existent node
	_, err = store.GetVector(999)
	assert.Error(t, err)

	_, err = store.GetNodeLevel(999)
	assert.Error(t, err)
}

func TestMemStoreNeighbors(t *testing.T) {
	store := NewMemNodeStore()

	requireNoError(t, store.PutNode(1, 2, []float32{0.1}))
	requireNoError(t, store.SetNeighbors(1, 0, []uint64{2, 3, 4}))
	requireNoError(t, store.SetNeighbors(1, 1, []uint64{3}))

	nb0, err := store.GetNeighbors(1, 0)
	requireNoError(t, err)
	assert.Equal(t, []uint64{2, 3, 4}, nb0)

	nb1, err := store.GetNeighbors(1, 1)
	requireNoError(t, err)
	assert.Equal(t, []uint64{3}, nb1)

	// Non-existent
	nb, err := store.GetNeighbors(999, 0)
	requireNoError(t, err)
	assert.Nil(t, nb)
}

func TestMemStoreEntryPoint(t *testing.T) {
	store := NewMemNodeStore()

	// No entry point initially
	_, _, err := store.GetEntryPoint()
	assert.Error(t, err)

	requireNoError(t, store.SetEntryPoint(42, 5))
	epId, maxLayer, err := store.GetEntryPoint()
	requireNoError(t, err)
	assert.Equal(t, uint64(42), epId)
	assert.Equal(t, 5, maxLayer)
}

func TestMemStoreMapping(t *testing.T) {
	store := NewMemNodeStore()

	requireNoError(t, store.SetNodeMapping("doc-1", 1))

	nodeId, found, err := store.GetNodeId("doc-1")
	requireNoError(t, err)
	assert.True(t, found)
	assert.Equal(t, uint64(1), nodeId)

	_, found, err = store.GetNodeId("nonexistent")
	requireNoError(t, err)
	assert.False(t, found)

	requireNoError(t, store.DeleteNodeMapping("doc-1"))
	_, found, err = store.GetNodeId("doc-1")
	requireNoError(t, err)
	assert.False(t, found)
}

func TestMemStoreDeleteNode(t *testing.T) {
	store := NewMemNodeStore()

	requireNoError(t, store.PutNode(1, 1, []float32{1.0}))
	requireNoError(t, store.SetNeighbors(1, 0, []uint64{2}))
	requireNoError(t, store.SetNodeMapping("doc-1", 1))

	requireNoError(t, store.DeleteNode(1))

	_, err := store.GetVector(1)
	assert.Error(t, err)

	_, found, _ := store.GetNodeId("doc-1")
	assert.False(t, found)

	// Delete non-existent is a no-op
	requireNoError(t, store.DeleteNode(999))
}

// --- HNSW algorithm tests ---

func randomVectors(rng *rand.Rand, n, dim int) [][]float32 {
	vecs := make([][]float32, n)
	for i := range vecs {
		v := make([]float32, dim)
		for j := range v {
			v[j] = rng.Float32()*2 - 1
		}
		vecs[i] = v
	}
	return vecs
}

func bruteForceKNN(query []float32, vecs [][]float32, k int, dist DistanceFunc) []int {
	type item struct {
		idx  int
		dist float32
	}
	items := make([]item, len(vecs))
	for i, v := range vecs {
		items[i] = item{idx: i, dist: dist(query, v)}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].dist < items[j].dist })
	if k > len(items) {
		k = len(items)
	}
	result := make([]int, k)
	for i := 0; i < k; i++ {
		result[i] = items[i].idx
	}
	return result
}

func TestHNSWEmptyIndex(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithCosineDistance())

	results, err := idx.Search([]float32{1, 2, 3}, 10)
	requireNoError(t, err)
	assert.Empty(t, results)
}

func TestHNSWSingleVector(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithCosineDistance())

	requireNoError(t, idx.Insert("doc-1", []float32{1, 0, 0}))

	results, err := idx.Search([]float32{1, 0, 0}, 1)
	requireNoError(t, err)
	requireLen(t, results, 1)
	assert.Equal(t, uint64(1), results[0].ID)
	assert.InDelta(t, 0.0, results[0].Distance, 1e-6)
}

func TestHNSWRecallSmall(t *testing.T) {
	// 100 vectors, 384 dimensions — small enough for perfect recall.
	rng := rand.New(rand.NewSource(12345))
	dim := 384
	n := 100
	k := 10
	numQueries := 20

	vecs := randomVectors(rng, n, dim)
	queries := randomVectors(rng, numQueries, dim)

	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithCosineDistance(), WithRand(rand.New(rand.NewSource(42))))

	for i, v := range vecs {
		requireNoError(t, idx.Insert(fmt.Sprintf("doc-%d", i), v))
	}

	totalRecall := 0.0
	for _, q := range queries {
		results, err := idx.Search(q, k)
		requireNoError(t, err)
		requireLen(t, results, k)

		bfResults := bruteForceKNN(q, vecs, k, CosineDistance)
		bfSet := make(map[uint64]bool)
		for _, bfIdx := range bfResults {
			bfSet[uint64(bfIdx+1)] = true // node IDs are 1-indexed
		}

		hits := 0
		for _, r := range results {
			if bfSet[r.ID] {
				hits++
			}
		}
		totalRecall += float64(hits) / float64(k)
	}
	avgRecall := totalRecall / float64(numQueries)
	t.Logf("Recall@%d on %d vectors: %.4f", k, n, avgRecall)
	assert.Equal(t, 1.0, avgRecall, "Expected perfect recall for 100 vectors")
}

// TestVisitedSet exercises the version-stamped visited set used by searchLayer,
// including the array-grow path and the epoch-overflow wraparound that zeroes
// the backing array (which normal search workloads never reach).
func TestVisitedSet(t *testing.T) {
	v := &visitedSet{}

	// First epoch: an id is unseen until marked.
	v.begin()
	assert.False(t, v.seen(3), "id should be unseen before marking")
	v.mark(3)
	assert.True(t, v.seen(3), "id should be seen after marking")
	assert.False(t, v.seen(1000), "unmarked out-of-range id should be unseen")

	// A new epoch logically clears all prior marks in O(1).
	v.begin()
	assert.False(t, v.seen(3), "mark from previous epoch should be cleared")

	// mark grows the backing array to fit a large out-of-range id.
	v.mark(5000)
	assert.True(t, v.seen(5000), "id should be seen after grow+mark")

	// Epoch overflow: force the wraparound branch in begin(), which must zero
	// the backing array so a stale stamp equal to the pre-wrap epoch cannot
	// alias the reset epoch.
	v.mark(7)
	v.epoch = math.MaxUint32
	v.versions[7] = math.MaxUint32 // stale stamp matching the pre-wrap epoch
	v.begin()                      // wraps: zero array, epoch -> 0 -> 1
	assert.Equal(t, uint32(1), v.epoch, "epoch should reset to 1 after overflow")
	assert.False(t, v.seen(7), "stale stamp must not survive the wraparound")
	v.mark(7)
	assert.True(t, v.seen(7), "id should be seen after re-marking post-wraparound")
}

func TestHNSWRecallLarger(t *testing.T) {
	// 1000 vectors, 128 dimensions.
	rng := rand.New(rand.NewSource(54321))
	dim := 128
	n := 1000
	k := 10
	numQueries := 20

	vecs := randomVectors(rng, n, dim)
	queries := randomVectors(rng, numQueries, dim)

	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithCosineDistance(), WithRand(rand.New(rand.NewSource(99))))

	for i, v := range vecs {
		requireNoError(t, idx.Insert(fmt.Sprintf("doc-%d", i), v))
	}

	totalRecall := 0.0
	for _, q := range queries {
		results, err := idx.Search(q, k)
		requireNoError(t, err)
		requireLen(t, results, k)

		bfResults := bruteForceKNN(q, vecs, k, CosineDistance)
		bfSet := make(map[uint64]bool)
		for _, bfIdx := range bfResults {
			bfSet[uint64(bfIdx+1)] = true
		}

		hits := 0
		for _, r := range results {
			if bfSet[r.ID] {
				hits++
			}
		}
		totalRecall += float64(hits) / float64(k)
	}
	avgRecall := totalRecall / float64(numQueries)
	t.Logf("Recall@%d on %d vectors: %.4f", k, n, avgRecall)
	assert.GreaterOrEqual(t, avgRecall, 0.95, "Expected recall >= 0.95 for 1000 vectors")
}

func TestHNSWNeighborLimits(t *testing.T) {
	rng := rand.New(rand.NewSource(77))
	dim := 32
	n := 200

	vecs := randomVectors(rng, n, dim)

	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithCosineDistance(), WithRand(rand.New(rand.NewSource(88))))

	for i, v := range vecs {
		requireNoError(t, idx.Insert(fmt.Sprintf("doc-%d", i), v))
	}

	for nodeId := uint64(1); nodeId <= uint64(n); nodeId++ {
		level, err := store.GetNodeLevel(nodeId)
		requireNoError(t, err)

		// Layer 0: <= Mmax0 (32)
		nb0, err := store.GetNeighbors(nodeId, 0)
		requireNoError(t, err)
		assert.LessOrEqual(t, len(nb0), idx.Mmax0,
			"node %d layer 0 has %d neighbors (max %d)", nodeId, len(nb0), idx.Mmax0)

		// Higher layers: <= M (16)
		for l := 1; l <= level; l++ {
			nb, err := store.GetNeighbors(nodeId, l)
			requireNoError(t, err)
			assert.LessOrEqual(t, len(nb), idx.M,
				"node %d layer %d has %d neighbors (max %d)", nodeId, l, len(nb), idx.M)
		}
	}
}

func TestHNSWEntryPointUpdate(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithCosineDistance(), WithRand(rand.New(rand.NewSource(1))))

	// Insert many vectors; track the max level seen.
	maxLevelSeen := 0
	maxLevelNode := uint64(0)
	for i := 0; i < 100; i++ {
		v := make([]float32, 8)
		for j := range v {
			v[j] = float32(i*8 + j)
		}
		requireNoError(t, idx.Insert(fmt.Sprintf("doc-%d", i), v))

		epId, epLevel, err := store.GetEntryPoint()
		requireNoError(t, err)

		nodeLevel, err := store.GetNodeLevel(epId)
		requireNoError(t, err)

		// Entry point must always have level >= epLevel.
		assert.GreaterOrEqual(t, nodeLevel, epLevel)

		if epLevel > maxLevelSeen {
			maxLevelSeen = epLevel
			maxLevelNode = epId
		}
	}

	// The entry point should be a node at the highest level.
	epId, _, err := store.GetEntryPoint()
	requireNoError(t, err)
	epLevel, err := store.GetNodeLevel(epId)
	requireNoError(t, err)
	assert.GreaterOrEqual(t, epLevel, maxLevelSeen,
		"entry point level should be >= highest level seen")
	_ = maxLevelNode
}

func TestHNSWDelete(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	dim := 32
	n := 50
	nDelete := 10

	vecs := randomVectors(rng, n, dim)
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithCosineDistance(), WithRand(rand.New(rand.NewSource(100))))

	for i, v := range vecs {
		requireNoError(t, idx.Insert(fmt.Sprintf("doc-%d", i), v))
	}

	// Delete the first nDelete vectors.
	deletedIds := make(map[uint64]bool)
	for i := 0; i < nDelete; i++ {
		requireNoError(t, idx.Delete(fmt.Sprintf("doc-%d", i)))
		deletedIds[uint64(i+1)] = true
	}

	// Search should not return deleted vectors.
	for qi := 0; qi < 10; qi++ {
		q := randomVectors(rng, 1, dim)[0]
		results, err := idx.Search(q, 10)
		requireNoError(t, err)
		for _, r := range results {
			assert.False(t, deletedIds[r.ID],
				"search returned deleted node %d", r.ID)
		}
	}

	// Remaining vectors should still be searchable.
	for i := nDelete; i < n; i++ {
		results, err := idx.Search(vecs[i], 1)
		requireNoError(t, err)
		requireNotEmpty(t, results, fmt.Sprintf("vector %d not found after deletions", i))
		assert.Equal(t, uint64(i+1), results[0].ID)
	}
}

func TestHNSWWithEuclideanDistance(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithEuclideanDistance(), WithRand(rand.New(rand.NewSource(42))))

	requireNoError(t, idx.Insert("a", []float32{0, 0}))
	requireNoError(t, idx.Insert("b", []float32{1, 0}))
	requireNoError(t, idx.Insert("c", []float32{10, 10}))

	results, err := idx.Search([]float32{0, 0}, 2)
	requireNoError(t, err)
	requireLen(t, results, 2)

	// Closest should be "a" (id=1), then "b" (id=2).
	assert.Equal(t, uint64(1), results[0].ID)
	assert.Equal(t, uint64(2), results[1].ID)
}

func TestHNSWWithDotProductDistance(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithDotProductDistance(), WithRand(rand.New(rand.NewSource(42))))

	// Normalized vectors.
	sqrt2 := float32(1.0 / math.Sqrt(2))
	requireNoError(t, idx.Insert("a", []float32{1, 0}))
	requireNoError(t, idx.Insert("b", []float32{sqrt2, sqrt2}))
	requireNoError(t, idx.Insert("c", []float32{-1, 0}))

	results, err := idx.Search([]float32{1, 0}, 3)
	requireNoError(t, err)
	requireLen(t, results, 3)

	// "a" is identical (dist=0), "b" is close, "c" is opposite (dist=2).
	assert.Equal(t, uint64(1), results[0].ID)
	assert.InDelta(t, 0.0, results[0].Distance, 1e-6)
	assert.Equal(t, uint64(2), results[1].ID)
}

func TestHNSWDeleteEntryPoint(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithCosineDistance(), WithRand(rand.New(rand.NewSource(42))))

	requireNoError(t, idx.Insert("doc-0", []float32{1, 0}))
	requireNoError(t, idx.Insert("doc-1", []float32{0, 1}))
	requireNoError(t, idx.Insert("doc-2", []float32{1, 1}))

	// Find and delete entry point.
	epId, _, err := store.GetEntryPoint()
	requireNoError(t, err)

	for i := 0; i < 3; i++ {
		nid, found, nerr := store.GetNodeId(fmt.Sprintf("doc-%d", i))
		requireNoError(t, nerr)
		if found && nid == epId {
			requireNoError(t, idx.Delete(fmt.Sprintf("doc-%d", i)))
			break
		}
	}

	// Index should still be searchable.
	results, err := idx.Search([]float32{1, 0}, 2)
	requireNoError(t, err)
	assert.NotEmpty(t, results)
}

func TestHNSWDeleteAllVectors(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithCosineDistance(), WithRand(rand.New(rand.NewSource(42))))

	requireNoError(t, idx.Insert("doc-0", []float32{1, 0}))
	requireNoError(t, idx.Insert("doc-1", []float32{0, 1}))

	requireNoError(t, idx.Delete("doc-0"))
	requireNoError(t, idx.Delete("doc-1"))

	// Search on empty index after deletions.
	results, err := idx.Search([]float32{1, 0}, 1)
	requireNoError(t, err)
	assert.Empty(t, results)
}

func TestHNSWSearchKGreaterThanN(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store, WithCosineDistance(), WithRand(rand.New(rand.NewSource(42))))

	requireNoError(t, idx.Insert("doc-0", []float32{1, 0, 0}))
	requireNoError(t, idx.Insert("doc-1", []float32{0, 1, 0}))
	requireNoError(t, idx.Insert("doc-2", []float32{0, 0, 1}))

	results, err := idx.Search([]float32{1, 0, 0}, 10)
	requireNoError(t, err)
	assert.Len(t, results, 3, "should return all 3 vectors when k > n")
}

func TestHNSWInsertUpsert(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store)

	// Insert a vector
	err := idx.Insert("doc1", []float32{1, 0, 0})
	assert.NoError(t, err)

	// Insert same docId with different vector (upsert)
	err = idx.Insert("doc1", []float32{0, 1, 0})
	assert.NoError(t, err)

	// Search for the updated vector — should find it with near-zero distance
	results, err := idx.Search([]float32{0, 1, 0}, 1)
	assert.NoError(t, err)
	assert.Len(t, results, 1)
	assert.InDelta(t, 0.0, results[0].Distance, 0.01)

	// Search broadly — should only have 1 node total (no orphan from old insert)
	results2, err := idx.Search([]float32{1, 0, 0}, 10)
	assert.NoError(t, err)
	assert.Len(t, results2, 1, "should have exactly 1 node after upsert, no orphans")
}

func TestHNSWInsertUpsertMultiple(t *testing.T) {
	store := NewMemNodeStore()
	idx := NewHNSWIndex(store)

	// Insert 5 vectors
	for i := 0; i < 5; i++ {
		v := make([]float32, 3)
		v[i%3] = 1.0
		err := idx.Insert(fmt.Sprintf("doc%d", i), v)
		assert.NoError(t, err)
	}

	// Update doc0 three times
	for j := 0; j < 3; j++ {
		v := make([]float32, 3)
		v[j] = float32(j + 1)
		err := idx.Insert("doc0", v)
		assert.NoError(t, err)
	}

	// Search for all — should get exactly 5 results, no orphans from repeated upserts
	results, err := idx.Search([]float32{1, 1, 1}, 10)
	assert.NoError(t, err)
	assert.Equal(t, 5, len(results), "should have exactly 5 nodes, no orphans from upsert")
}
